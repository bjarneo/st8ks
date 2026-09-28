package kube

import (
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
)

// TopoNode is one box in the topology view.
type TopoNode struct {
	ID   string  `json:"id"`
	Type string  `json:"type"`
	Kind string  `json:"kind"`
	NS   string  `json:"ns"`
	Name string  `json:"name"`
	Sub  string  `json:"sub"`
	Tone string  `json:"tone"`
	Col  int     `json:"col"`
	Y    float64 `json:"y"`
}

// TopoEdge connects two nodes. T is o (owns), r (routes or selects) or u
// (uses or mounts).
type TopoEdge struct {
	A string `json:"a"`
	B string `json:"b"`
	T string `json:"t"`
}

// Topology is the object graph of one namespace.
type Topology struct {
	NS        string     `json:"ns"`
	Cols      []string   `json:"cols"`
	Nodes     []TopoNode `json:"nodes"`
	Edges     []TopoEdge `json:"edges"`
	Height    float64    `json:"height"`
	Truncated bool       `json:"truncated"`
}

const (
	topoRow    = 56.0
	topoTop    = 70.0
	topoMaxPod = 300
)

// Topology builds the graph from ingresses to workloads in a namespace.
func (c *Cluster) Topology(ns string) Topology {
	t := Topology{NS: ns, Cols: []string{"Ingress", "Service", "Pod", "Controller", "Workload", "Config & storage"}}
	nodes := map[string]*TopoNode{}
	var order []string
	edges := map[TopoEdge]bool{}
	node := func(id, typ, kind, name, sub string, tone byte, col int) *TopoNode {
		if n := nodes[id]; n != nil {
			return n
		}
		n := &TopoNode{ID: id, Type: typ, Kind: kind, NS: ns, Name: name, Sub: sub, Tone: string(tone), Col: col, Y: -1}
		nodes[id] = n
		order = append(order, id)
		return n
	}
	edge := func(a, b, typ string) {
		if nodes[a] != nil && nodes[b] != nil && a != b {
			edges[TopoEdge{A: a, B: b, T: typ}] = true
		}
	}

	var pods []*corev1.Pod
	for _, o := range c.byIndex("Pods", "namespace", ns) {
		p := o.(*corev1.Pod)
		if !podTerminal(p) {
			pods = append(pods, p)
		}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	if len(pods) > topoMaxPod {
		pods = pods[:topoMaxPod]
		t.Truncated = true
	}
	for _, p := range pods {
		st := podStatusOf(p)
		tone := statusTone(st.reason)
		if st.reason == "Running" {
			tone = tOK
		}
		node("pod/"+p.Name, "Pod", "Pods", p.Name, st.reason, tone, 2)
	}

	var svcs []*corev1.Service
	for _, o := range c.byIndex("Services", "namespace", ns) {
		svcs = append(svcs, o.(*corev1.Service))
	}
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
	for _, s := range svcs {
		sub := string(s.Spec.Type)
		if len(s.Spec.Ports) > 0 {
			sub += " :" + itoa(s.Spec.Ports[0].Port)
		}
		node("svc/"+s.Name, "Service", "Services", s.Name, sub, tNone, 1)
		if len(s.Spec.Selector) == 0 {
			continue
		}
		sel := labels.SelectorFromSet(s.Spec.Selector)
		for _, p := range pods {
			if sel.Matches(labels.Set(p.Labels)) {
				edge("svc/"+s.Name, "pod/"+p.Name, "r")
			}
		}
	}

	for _, o := range c.byIndex("Ingresses", "namespace", ns) {
		in := o.(*networkingv1.Ingress)
		host := "*"
		if len(in.Spec.Rules) > 0 && in.Spec.Rules[0].Host != "" {
			host = in.Spec.Rules[0].Host
		}
		node("ing/"+in.Name, "Ingress", "Ingresses", in.Name, host, tNone, 0)
		for _, s := range ingressServices(in) {
			edge("ing/"+in.Name, "svc/"+s, "r")
		}
	}

	// Controllers and workloads through owner references.
	for _, p := range pods {
		ref := ptrController(p.OwnerReferences)
		if ref == nil {
			continue
		}
		switch ref.Kind {
		case "ReplicaSet":
			if o := c.getObj("ReplicaSets", ns, ref.Name); o != nil {
				rs := o.(*appsv1.ReplicaSet)
				want := int64(ptr.Deref(rs.Spec.Replicas, 1))
				node("rs/"+rs.Name, "ReplicaSet", "ReplicaSets", rs.Name, ratio(int64(rs.Status.ReadyReplicas), want), readyToneOK(int64(rs.Status.ReadyReplicas), want), 3)
				edge("pod/"+p.Name, "rs/"+rs.Name, "o")
				if dr := ptrController(rs.OwnerReferences); dr != nil && dr.Kind == "Deployment" {
					if o := c.getObj("Deployments", ns, dr.Name); o != nil {
						d := o.(*appsv1.Deployment)
						dw := int64(ptr.Deref(d.Spec.Replicas, 1))
						node("deploy/"+d.Name, "Deployment", "Deployments", d.Name, ratio(int64(d.Status.ReadyReplicas), dw), readyToneOK(int64(d.Status.ReadyReplicas), dw), 4)
						edge("rs/"+rs.Name, "deploy/"+d.Name, "o")
					}
				}
			}
		case "StatefulSet":
			if o := c.getObj("StatefulSets", ns, ref.Name); o != nil {
				s := o.(*appsv1.StatefulSet)
				want := int64(ptr.Deref(s.Spec.Replicas, 1))
				node("sts/"+s.Name, "StatefulSet", "StatefulSets", s.Name, ratio(int64(s.Status.ReadyReplicas), want), readyToneOK(int64(s.Status.ReadyReplicas), want), 4)
				edge("pod/"+p.Name, "sts/"+s.Name, "o")
			}
		case "DaemonSet":
			if o := c.getObj("DaemonSets", ns, ref.Name); o != nil {
				d := o.(*appsv1.DaemonSet)
				node("ds/"+d.Name, "DaemonSet", "DaemonSets", d.Name, ratio(int64(d.Status.NumberReady), int64(d.Status.DesiredNumberScheduled)), readyToneOK(int64(d.Status.NumberReady), int64(d.Status.DesiredNumberScheduled)), 4)
				edge("pod/"+p.Name, "ds/"+d.Name, "o")
			}
		case "Job":
			if o := c.getObj("Jobs", ns, ref.Name); o != nil {
				j := o.(*batchv1.Job)
				st := jobStatus(j)
				node("job/"+j.Name, "Job", "Jobs", j.Name, st, statusTone(st), 3)
				edge("pod/"+p.Name, "job/"+j.Name, "o")
				if cr := ptrController(j.OwnerReferences); cr != nil && cr.Kind == "CronJob" {
					node("cj/"+cr.Name, "CronJob", "CronJobs", cr.Name, "", tNone, 4)
					edge("job/"+j.Name, "cj/"+cr.Name, "o")
				}
			}
		}
		for _, v := range p.Spec.Volumes {
			switch {
			case v.ConfigMap != nil:
				node("cm/"+v.ConfigMap.Name, "ConfigMap", "ConfigMaps", v.ConfigMap.Name, "", tNone, 5)
				edge("pod/"+p.Name, "cm/"+v.ConfigMap.Name, "u")
			case v.Secret != nil:
				node("sec/"+v.Secret.SecretName, "Secret", "Secrets", v.Secret.SecretName, "", tNone, 5)
				edge("pod/"+p.Name, "sec/"+v.Secret.SecretName, "u")
			case v.PersistentVolumeClaim != nil:
				node("pvc/"+v.PersistentVolumeClaim.ClaimName, "PVC", "PersistentVolumeClaims", v.PersistentVolumeClaim.ClaimName, "", tNone, 5)
				edge("pod/"+p.Name, "pvc/"+v.PersistentVolumeClaim.ClaimName, "u")
			}
		}
		for _, ct := range p.Spec.Containers {
			for _, ef := range ct.EnvFrom {
				if ef.ConfigMapRef != nil {
					node("cm/"+ef.ConfigMapRef.Name, "ConfigMap", "ConfigMaps", ef.ConfigMapRef.Name, "", tNone, 5)
					edge("pod/"+p.Name, "cm/"+ef.ConfigMapRef.Name, "u")
				}
				if ef.SecretRef != nil {
					node("sec/"+ef.SecretRef.Name, "Secret", "Secrets", ef.SecretRef.Name, "", tNone, 5)
					edge("pod/"+p.Name, "sec/"+ef.SecretRef.Name, "u")
				}
			}
		}
	}

	t.Edges = make([]TopoEdge, 0, len(edges))
	for e := range edges {
		t.Edges = append(t.Edges, e)
	}
	sort.Slice(t.Edges, func(i, j int) bool {
		if t.Edges[i].A != t.Edges[j].A {
			return t.Edges[i].A < t.Edges[j].A
		}
		return t.Edges[i].B < t.Edges[j].B
	})
	t.Height = layoutTopo(nodes, order, t.Edges)
	t.Nodes = make([]TopoNode, 0, len(order))
	for _, id := range order {
		t.Nodes = append(t.Nodes, *nodes[id])
	}
	return t
}

func ptrController(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

func readyToneOK(a, b int64) byte {
	if t := readyTone(a, b); t != tNone {
		return t
	}
	return tOK
}

// layoutTopo orders the pods by their service, then places every other
// column at the mean height of its neighbors and resolves overlaps.
func layoutTopo(nodes map[string]*TopoNode, order []string, edges []TopoEdge) float64 {
	nbr := map[string][]string{}
	for _, e := range edges {
		nbr[e.A] = append(nbr[e.A], e.B)
		nbr[e.B] = append(nbr[e.B], e.A)
	}
	byCol := map[int][]*TopoNode{}
	for _, id := range order {
		n := nodes[id]
		byCol[n.Col] = append(byCol[n.Col], n)
	}
	// Ingresses keep their order. Services follow ingresses.
	place := func(col int) {
		list := byCol[col]
		for _, n := range list {
			n.Y = mean(nodes, nbr[n.ID], n.Col)
		}
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i].Y, list[j].Y
			if a < 0 {
				a = 1e9
			}
			if b < 0 {
				b = 1e9
			}
			if a != b {
				return a < b
			}
			return list[i].Name < list[j].Name
		})
		y := topoTop
		for _, n := range list {
			if n.Y < y {
				n.Y = y
			}
			y = n.Y + topoRow
		}
	}
	for i, n := range byCol[0] {
		n.Y = topoTop + float64(i)*topoRow
	}
	place(1)
	// Pods are the densest column, so they get even spacing.
	pods := byCol[2]
	for _, n := range pods {
		n.Y = mean(nodes, nbr[n.ID], 2)
	}
	sort.SliceStable(pods, func(i, j int) bool {
		a, b := pods[i].Y, pods[j].Y
		if a < 0 {
			a = 1e9
		}
		if b < 0 {
			b = 1e9
		}
		if a != b {
			return a < b
		}
		return pods[i].Name < pods[j].Name
	})
	for i, n := range pods {
		n.Y = topoTop + float64(i)*topoRow
	}
	place(1)
	place(3)
	place(4)
	place(5)
	h := topoTop
	for _, n := range nodes {
		if n.Y+topoRow > h {
			h = n.Y + topoRow
		}
	}
	return h
}

// mean returns the average height of the neighbors that are placed, or -1.
func mean(nodes map[string]*TopoNode, ids []string, col int) float64 {
	var sum float64
	n := 0
	for _, id := range ids {
		if o := nodes[id]; o != nil && o.Y >= 0 && o.Col != col {
			sum += o.Y
			n++
		}
	}
	if n == 0 {
		return -1
	}
	return sum / float64(n)
}
