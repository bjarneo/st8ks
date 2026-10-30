//go:build integration

// Run against the local cluster with Prometheus installed:
//
//	hack/dev-cluster/prometheus.sh
//	ST8KS_IT_KUBECONFIG=~/.kube/st8ks-dev.yaml go test -tags integration -run TestIntegrationPrometheus -v ./internal/kube/
package kube

import (
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
)

func lastValue(v MetricValues) float64 {
	for i := len(v) - 1; i >= 0; i-- {
		if !math.IsNaN(v[i]) {
			return v[i]
		}
	}
	return math.NaN()
}

func lineOf(lines []MetricLine, kind string) float64 {
	for _, l := range lines {
		if l.Kind == kind {
			return l.V
		}
	}
	return 0
}

func TestIntegrationPrometheus(t *testing.T) {
	kc := os.Getenv("ST8KS_IT_KUBECONFIG")
	if kc == "" {
		t.Skip("set ST8KS_IT_KUBECONFIG")
	}
	rec := &recorder{counts: map[string]int{}, logs: map[string][]string{}, exec: map[string]string{}}
	m := NewManager(rec.emit, func() LoadOptions { return LoadOptions{Explicit: []string{kc}} })
	setting := ""
	m.SetMetricsSource(func() string { return setting })
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if st, err := m.Connect("kind-st8ks-dev"); err != nil || st.Status != StatusConnected {
		t.Fatalf("connect: %v %+v", err, st)
	}
	c := m.Current()
	defer m.Shutdown()
	waitFor(t, "pods and services synced", 30*time.Second, func() bool {
		p, s := c.table("Pods"), c.table("Services")
		return p != nil && s != nil && p.snapshot().Synced && s.snapshot().Synced
	})

	t.Run("discovery", func(t *testing.T) {
		start := time.Now()
		src := c.CheckPrometheus()
		if src.Status == MetricsNone {
			t.Skip("no Prometheus in the cluster. Run hack/dev-cluster/prometheus.sh")
		}
		if src.Status != MetricsOK || src.Mode != "auto" || src.Target != "monitoring/prometheus-server:http" || src.Via != "proxy" {
			t.Fatalf("source = %+v", src)
		}
		t.Logf("found %s and queried it in %s: %s", src.Target, time.Since(start).Round(time.Millisecond), src.Message)
	})
	if c.prom.target == nil {
		t.Skip("no Prometheus")
	}

	var pod *corev1.Pod
	for _, o := range c.list("Pods") {
		p := o.(*corev1.Pod)
		if p.Namespace == "prod" && strings.HasPrefix(p.Name, "api-gateway-") && p.Status.Phase == corev1.PodRunning {
			pod = p
		}
	}
	if pod == nil {
		t.Fatal("no running api-gateway pod in prod")
	}

	t.Run("pod", func(t *testing.T) {
		var d *MetricsData
		waitFor(t, "pod history", 90*time.Second, func() bool {
			var err error
			d, err = c.Metrics(Ref{Kind: "Pods", NS: pod.Namespace, Name: pod.Name}, "1h")
			if err != nil {
				t.Fatal(err)
			}
			return d.Status == MetricsOK && d.CPU.Total != nil && !math.IsNaN(lastValue(d.CPU.Total.V))
		})
		if d.N != 240 || d.Step != 15 || d.Parts != "container" {
			t.Errorf("grid = %d points of %ds, parts %s", d.N, d.Step, d.Parts)
		}
		if len(d.Mem.Parts) != 1 || d.Mem.Parts[0].Name != "gateway" {
			t.Fatalf("memory parts = %+v", d.Mem.Parts)
		}
		mem := lastValue(d.Mem.Parts[0].V)
		if !(mem > 1<<20 && mem < 128<<20) {
			t.Errorf("gateway working set = %v", mem)
		}
		if r, l := lineOf(d.CPU.Parts[0].Lines, "request"), lineOf(d.CPU.Parts[0].Lines, "limit"); r != 0.02 || l != 0.2 {
			t.Errorf("cpu lines = %+v", d.CPU.Parts[0].Lines)
		}
		if l := lineOf(d.Mem.Total.Lines, "limit"); l != 128<<20 {
			t.Errorf("memory lines = %+v", d.Mem.Total.Lines)
		}
		t.Logf("%s: cpu %.4f cores, memory %s", pod.Name, lastValue(d.CPU.Total.V), fmtBytesF(mem))
	})

	t.Run("workloads", func(t *testing.T) {
		for _, ref := range []Ref{
			{Kind: "Deployments", NS: "prod", Name: "api-gateway"},
			{Kind: "DaemonSets", NS: "kube-system", Name: "kindnet"},
			{Kind: "StatefulSets", NS: "data", Name: "redis"},
		} {
			d, err := c.Metrics(ref, "6h")
			if err != nil {
				t.Fatal(err)
			}
			if ref.Kind == "StatefulSets" && d.Status == MetricsNoData {
				t.Logf("%s %s has no running pods: %s", ref.Kind, ref.Name, d.Message)
				continue
			}
			if d.Status != MetricsOK || d.Parts != "pod" || len(d.CPU.Parts) == 0 || d.Mem.Total == nil {
				t.Errorf("%s %s = %s %s, %d parts", ref.Kind, ref.Name, d.Status, d.Message, len(d.CPU.Parts))
				continue
			}
			re := regexp.MustCompile("^(?:" + controllerPodRegex(ref.Kind, ref.Name) + ")$")
			var names []string
			for _, p := range d.CPU.Parts {
				names = append(names, p.Name)
				if !re.MatchString(p.Name) {
					t.Errorf("%s %s: pod %s", ref.Kind, ref.Name, p.Name)
				}
			}
			t.Logf("%s %s: %d points of %ds, pods %s", ref.Kind, ref.Name, d.N, d.Step, strings.Join(names, ", "))
		}
		d, _ := c.Metrics(Ref{Kind: "Deployments", NS: "prod", Name: "api-gateway"}, "24h")
		if lineOf(d.Mem.Parts[0].Lines, "limit") != 128<<20 || lineOf(d.Mem.Total.Lines, "limit") != 2*128<<20 {
			t.Errorf("deployment lines: per pod %+v, total %+v", d.Mem.Parts[0].Lines, d.Mem.Total.Lines)
		}
	})

	t.Run("node", func(t *testing.T) {
		d, err := c.Metrics(Ref{Kind: "Nodes", Name: "st8ks-dev-worker"}, "1h")
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != MetricsOK || d.Mem.Total == nil || lastValue(d.Mem.Total.V) < 32<<20 {
			t.Fatalf("node = %s %s", d.Status, d.Message)
		}
		if lineOf(d.Mem.Total.Lines, "limit") <= 0 {
			t.Errorf("node lines = %+v", d.Mem.Total.Lines)
		}
		t.Logf("node memory %s, allocatable %s", fmtBytesF(lastValue(d.Mem.Total.V)), fmtBytesF(lineOf(d.Mem.Total.Lines, "limit")))
	})

	t.Run("setting", func(t *testing.T) {
		defer func() { setting = "" }()
		setting = "monitoring/prometheus-server:http"
		if src := c.CheckPrometheus(); src.Status != MetricsOK || src.Mode != "service" {
			t.Errorf("service ref = %+v", src)
		}
		setting = "monitoring/prometheus-server"
		if src := c.CheckPrometheus(); src.Status != MetricsOK || src.Target != "monitoring/prometheus-server:http" {
			t.Errorf("service ref without a port = %+v", src)
		}
		setting = "monitoring/no-such-prometheus:9090"
		if src := c.CheckPrometheus(); src.Status != MetricsError || !strings.Contains(src.Message, "does not exist") {
			t.Errorf("missing service = %+v", src)
		}
		setting = "http://127.0.0.1:1"
		if d, _ := c.Metrics(Ref{Kind: "Pods", NS: pod.Namespace, Name: pod.Name}, "1h"); d.Status != MetricsError || d.Source.Mode != "url" {
			t.Errorf("closed URL = %+v", d)
		}
	})

	t.Run("forbidden", func(t *testing.T) {
		// A user without RBAC rules. The impersonation needs no objects.
		cfg := rest.CopyConfig(c.cfg)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:monitoring:st8ks-it-no-proxy"}
		nc, err := newCluster("no-proxy", c.ctxOrig, c.raw, cfg, "default", func(string, any) {})
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Stop()
		if src := nc.CheckPrometheus(); src.Status != MetricsNone || !strings.Contains(src.Message, "cannot list Services") {
			t.Errorf("auto without list = %+v", src)
		}
		nc.prom.setting = func() string { return "monitoring/prometheus-server:http" }
		d, err := nc.Metrics(Ref{Kind: "Pods", NS: pod.Namespace, Name: pod.Name}, "1h")
		if err != nil {
			t.Fatal(err)
		}
		if d.Status != MetricsForbidden || d.Source.Missing != "get on services/proxy in namespace monitoring" {
			t.Errorf("forbidden = %+v", d)
		}
		t.Logf("forbidden: %s", d.Message)
	})

	t.Run("oom evidence", func(t *testing.T) {
		var id string
		waitFor(t, "checkout OOM issue", 60*time.Second, func() bool {
			i := findIssue(c, "crash:prod/Deployments/checkout/")
			if i != nil && strings.Contains(i.Reason, "OOMKilled") {
				id = i.ID
			}
			return id != ""
		})
		start := time.Now()
		text, _, err := c.IssueContext(id)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(`- Memory working set peaked at [0-9.]+[KMG]i of the [0-9.]+[KMG]i limit in the last hour[^\n]*`)
		line := re.FindString(text)
		if line == "" {
			findings, _, _ := strings.Cut(text, "Rule engine suggestion")
			t.Fatalf("no evidence line in:\n%s", findings)
		}
		t.Logf("%s (context in %s)", line, time.Since(start).Round(time.Millisecond))

		// Without Prometheus, the line is left out and the context still builds.
		setting = "http://127.0.0.1:1"
		defer func() { setting = "" }()
		text, _, err = c.IssueContext(id)
		if err != nil || strings.Contains(text, "Memory working set peaked") {
			t.Errorf("context without Prometheus: %v", err)
		}
	})
}
