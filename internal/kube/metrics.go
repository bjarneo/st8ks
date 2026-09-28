package kube

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const trendLen = 36

type metricsState struct {
	mu       sync.RWMutex
	pods     map[string][2]int64
	nodes    map[string][2]int64
	ok       bool
	version  uint64
	usedCPU  int64
	usedMem  int64
	cpuTrend []float64
	memTrend []float64
}

func (m *metricsState) podsSnapshot() map[string][2]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pods
}

func (m *metricsState) nodesSnapshot() map[string][2]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nodes
}

func (m *metricsState) ver() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.version
}

// PodUsage returns the CPU and memory usage of one pod.
func (c *Cluster) PodUsage(uid string) (int64, int64, bool) {
	c.met.mu.RLock()
	defer c.met.mu.RUnlock()
	u, ok := c.met.pods[uid]
	return u[0], u[1], ok
}

// metricsLoop polls metrics-server every 15 seconds, like the design's
// "live · 15s" overview.
func (c *Cluster) metricsLoop() {
	c.pollMetrics()
	tk := time.NewTicker(15 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-tk.C:
			c.pollMetrics()
		}
	}
}

func (c *Cluster) pollMetrics() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	pm, perr := c.mc.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{})
	nm, nerr := c.mc.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if c.ctx.Err() != nil {
		return
	}
	ok := perr == nil && nerr == nil
	pods := map[string][2]int64{}
	nodes := map[string][2]int64{}
	var usedCPU, usedMem int64
	if perr == nil {
		for i := range pm.Items {
			p := &pm.Items[i]
			obj := c.getObj("Pods", p.Namespace, p.Name)
			if obj == nil {
				continue
			}
			var cpu, mem int64
			for _, ct := range p.Containers {
				cpu += ct.Usage.Cpu().MilliValue()
				mem += ct.Usage.Memory().Value()
			}
			pods[string(obj.(*corev1.Pod).UID)] = [2]int64{cpu, mem}
		}
	}
	if nerr == nil {
		for i := range nm.Items {
			n := &nm.Items[i]
			cpu := n.Usage.Cpu().MilliValue()
			mem := n.Usage.Memory().Value()
			usedCPU += cpu
			usedMem += mem
			if obj := c.getObj("Nodes", "", n.Name); obj != nil {
				nodes[string(obj.(*corev1.Node).UID)] = [2]int64{cpu, mem}
			}
		}
	}
	var allocCPU, allocMem int64
	for _, o := range c.list("Nodes") {
		n := o.(*corev1.Node)
		allocCPU += n.Status.Allocatable.Cpu().MilliValue()
		allocMem += n.Status.Allocatable.Memory().Value()
	}

	m := &c.met
	m.mu.Lock()
	m.ok = ok
	m.pods = pods
	m.nodes = nodes
	m.usedCPU = usedCPU
	m.usedMem = usedMem
	if ok && allocCPU > 0 && allocMem > 0 {
		m.cpuTrend = pushTrend(m.cpuTrend, float64(usedCPU)*100/float64(allocCPU))
		m.memTrend = pushTrend(m.memTrend, float64(usedMem)*100/float64(allocMem))
	}
	m.version++
	m.mu.Unlock()

	if c.State().Metrics != ok {
		c.setState(func(s *ClusterState) { s.Metrics = ok })
	}
	c.mu.RLock()
	wp, wn := c.watched["Pods"], c.watched["Nodes"]
	c.mu.RUnlock()
	out := map[string]any{}
	if wp {
		out["pods"] = pods
	}
	if wn {
		out["nodes"] = nodes
	}
	if len(out) > 0 {
		c.emit("metrics", out)
	}
}

func pushTrend(t []float64, v float64) []float64 {
	t = append(t, v)
	if len(t) > trendLen {
		t = t[len(t)-trendLen:]
	}
	return t
}

// Usage holds one resource of the overview cards.
type Usage struct {
	Used  int64     `json:"used"`
	Alloc int64     `json:"alloc"`
	Req   int64     `json:"req"`
	Lim   int64     `json:"lim"`
	Trend []float64 `json:"trend"`
}

// Overview holds the cluster summary numbers.
type Overview struct {
	CPU      Usage `json:"cpu"`
	Mem      Usage `json:"mem"`
	Pods     int   `json:"pods"`
	PodsCap  int64 `json:"podsCap"`
	Nodes    int   `json:"nodes"`
	Ready    int   `json:"ready"`
	Cordoned int   `json:"cordoned"`
	Metrics  bool  `json:"metrics"`
}

// computeOverview sums node capacity and pod requests.
func (c *Cluster) computeOverview() Overview {
	var ov Overview
	for _, o := range c.list("Nodes") {
		n := o.(*corev1.Node)
		ov.Nodes++
		if nodeReady(n) == "Ready" {
			ov.Ready++
		}
		if n.Spec.Unschedulable {
			ov.Cordoned++
		}
		a := n.Status.Allocatable
		ov.CPU.Alloc += a.Cpu().MilliValue()
		ov.Mem.Alloc += a.Memory().Value()
		ov.PodsCap += a.Pods().Value()
	}
	for _, o := range c.list("Pods") {
		p := o.(*corev1.Pod)
		if podTerminal(p) {
			continue
		}
		ov.Pods++
		if p.Spec.NodeName == "" {
			continue
		}
		for _, ct := range p.Spec.Containers {
			r, l := ct.Resources.Requests, ct.Resources.Limits
			ov.CPU.Req += r.Cpu().MilliValue()
			ov.Mem.Req += r.Memory().Value()
			if q, ok := l[corev1.ResourceCPU]; ok {
				ov.CPU.Lim += q.MilliValue()
			}
			if q, ok := l[corev1.ResourceMemory]; ok {
				ov.Mem.Lim += q.Value()
			}
		}
	}
	m := &c.met
	m.mu.RLock()
	ov.Metrics = m.ok
	ov.CPU.Used, ov.Mem.Used = -1, -1
	if m.ok {
		ov.CPU.Used = m.usedCPU
		ov.Mem.Used = m.usedMem
	}
	ov.CPU.Trend = append([]float64(nil), m.cpuTrend...)
	ov.Mem.Trend = append([]float64(nil), m.memTrend...)
	m.mu.RUnlock()
	return ov
}
