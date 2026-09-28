//go:build integration

// Run against a disposable cluster:
//
//	ST8KS_IT_KUBECONFIG=~/.cache/st8ks-dev/kind.yaml go test -tags integration -run TestIntegration -v ./internal/kube/
package kube

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

type recorder struct {
	mu     sync.Mutex
	counts map[string]int
	logs   map[string][]string
	exec   map[string]string
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[name]++
	switch v := data.(type) {
	case LogChunk:
		r.logs[v.ID] = append(r.logs[v.ID], v.Lines...)
	case ExecChunk:
		if v.Data != "" {
			b, _ := base64.StdEncoding.DecodeString(v.Data)
			r.exec[v.ID] += string(b)
		}
	}
}

func waitFor(t *testing.T, what string, d time.Duration, fn func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func findIssue(c *Cluster, prefix string) *Issue {
	for _, i := range c.Issues() {
		if strings.HasPrefix(i.ID, prefix) {
			ii := i
			return &ii
		}
	}
	return nil
}

func TestIntegration(t *testing.T) {
	kc := os.Getenv("ST8KS_IT_KUBECONFIG")
	if kc == "" {
		t.Skip("set ST8KS_IT_KUBECONFIG")
	}
	rec := &recorder{counts: map[string]int{}, logs: map[string][]string{}, exec: map[string]string{}}
	m := NewManager(rec.emit, func() LoadOptions { return LoadOptions{Explicit: []string{kc}} })
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	ctxs := m.Contexts()
	if len(ctxs) != 1 || ctxs[0].Name != "kind-st8ks-dev" {
		t.Fatalf("contexts = %+v", ctxs)
	}
	start := time.Now()
	st, err := m.Connect("kind-st8ks-dev")
	if err != nil || st.Status != StatusConnected {
		t.Fatalf("connect: %v %+v", err, st)
	}
	c := m.Current()
	defer m.Shutdown()
	waitFor(t, "pods synced", 30*time.Second, func() bool {
		tb := c.table("Pods")
		if tb == nil {
			return false
		}
		s := tb.snapshot()
		return s.Synced && len(s.Rows) > 5
	})
	t.Logf("connected and pods synced in %s", time.Since(start).Round(time.Millisecond))
	waitFor(t, "node metadata", 10*time.Second, func() bool { return c.State().Dist == "kind" })

	t.Run("tree and counts", func(t *testing.T) {
		var sections []string
		for _, s := range c.Tree() {
			sections = append(sections, s.ID)
		}
		if strings.Join(sections, ",") != "cluster,workloads,network,config,storage,access,crd,helm,views" {
			t.Errorf("sections = %v", sections)
		}
		counts := c.Counts()
		if counts["Deployments"]["prod"][0] < 4 {
			t.Errorf("prod deployments = %v", counts["Deployments"]["prod"])
		}
		if counts["ConfigMaps"]["prod"][0] == 0 {
			t.Errorf("lazy ConfigMaps count is missing: %v", counts["ConfigMaps"])
		}
	})

	t.Run("rows", func(t *testing.T) {
		snap := c.Snapshot("Deployments")
		var found *Row
		for _, r := range snap.Rows {
			if r.M == "checkout" && r.N == "prod" {
				found = r
			}
		}
		if found == nil || len(found.C) != len(c.kind("Deployments").Cols) {
			t.Fatalf("checkout row = %+v", found)
		}
		// The lazy Secrets table fills with full rows after Watch.
		c.Watch([]string{"Secrets"})
		waitFor(t, "secret rows", 15*time.Second, func() bool {
			for _, r := range c.Snapshot("Secrets").Rows {
				if r.M == "db-credentials" && r.C[2] == "Opaque" {
					return true
				}
			}
			return false
		})
		for _, o := range c.list("Secrets") {
			for k, v := range o.(*corev1.Secret).Data {
				if len(v) > 0 {
					t.Errorf("secret value %s stays in the informer cache", k)
				}
			}
		}
	})

	t.Run("issues", func(t *testing.T) {
		waitFor(t, "crash, image, scheduling and job issues", 150*time.Second, func() bool {
			return findIssue(c, "crash:prod/Deployments/checkout") != nil && findIssue(c, "image:staging/Deployments/image-resizer") != nil &&
				findIssue(c, "sched:data/Deployments/analytics") != nil && findIssue(c, "job:jobs/backfill-orders") != nil &&
				findIssue(c, "image:prod/Deployments/web") != nil
		})
		crash := findIssue(c, "crash:prod/Deployments/checkout")
		if crash.Action == nil || crash.Action.Type != "diff" || !strings.Contains(crash.Reason, "OOMKilled") {
			t.Errorf("crash issue = %+v", crash)
		}
		sched := findIssue(c, "sched:data/Deployments/analytics")
		if sched.Action == nil || sched.Action.Type != "uncordon" || sched.Action.Node != "st8ks-dev-worker2" {
			t.Errorf("scheduling issue = %+v", sched)
		}
		web := findIssue(c, "image:prod/Deployments/web")
		if web.Action == nil || web.Action.Type != "helm-rollback" || web.Action.Name != "web" {
			t.Errorf("helm image issue = %+v", web)
		}
		for _, i := range c.Issues() {
			t.Logf("issue %s: %s · %s · %s", i.Sev, i.Title, i.Reason, i.Meta)
		}

		p, err := c.ProposeMemoryFix(*crash.Action)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.Draft, "memory: 320Mi") || p.Live == p.Draft {
			t.Errorf("draft does not raise the limit:\n%s", p.Draft)
		}
		msg, err := c.Apply(p.Draft, true)
		if err != nil || !strings.Contains(msg, "configured (server dry run)") {
			t.Errorf("dry-run apply: %q %v", msg, err)
		}
		ctxText, cmds, err := c.IssueContext(crash.ID)
		if err != nil || !strings.Contains(ctxText, "OOMKilled") || len(cmds) < 2 {
			t.Errorf("issue context: %v %v\n%s", err, cmds, ctxText[:min(len(ctxText), 400)])
		}
	})

	var gwPod string
	for _, o := range c.byIndex("Pods", "namespace", "prod") {
		if m := objMeta(o); m != nil && strings.HasPrefix(m.GetName(), "api-gateway-") {
			gwPod = m.GetName()
		}
	}
	waitFor(t, "api-gateway running", 90*time.Second, func() bool {
		o := c.getObj("Pods", "prod", gwPod)
		return o != nil && podStatusOf(o.(*corev1.Pod)).reason == "Running"
	})

	t.Run("object", func(t *testing.T) {
		doc, err := c.GetObject(Ref{Kind: "Pods", NS: "prod", Name: gwPod})
		if err != nil {
			t.Fatal(err)
		}
		if doc.Pod == nil || len(doc.Pod.Containers) != 1 || doc.Pod.Containers[0].Ports[0].Port != 80 || doc.Managed == nil || doc.Managed.Name != "api-gateway" {
			t.Errorf("pod doc = %+v", doc)
		}
		rels := map[string]bool{}
		for _, r := range doc.Related {
			rels[r.Rel+" "+r.Kind] = true
		}
		for _, want := range []string{"Controlled by ReplicaSets", "Managed by Deployments", "Selected by Services", "Scheduled on Nodes", "In namespace Namespaces"} {
			if !rels[want] {
				t.Errorf("related misses %q: %v", want, rels)
			}
		}
		if strings.Contains(doc.YAML, "managedFields") {
			t.Error("YAML must not include managedFields")
		}
	})

	t.Run("logs", func(t *testing.T) {
		id, err := m.StartLogs(LogOpts{NS: "prod", Pod: gwPod, Container: "gateway", Tail: 50})
		if err != nil {
			t.Fatal(err)
		}
		defer m.StopLogs(id)
		waitFor(t, "log lines", 20*time.Second, func() bool {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			return len(rec.logs[id]) > 0
		})
		rec.mu.Lock()
		first := rec.logs[id][0]
		rec.mu.Unlock()
		if !strings.Contains(first, "Z ") {
			t.Errorf("log lines need timestamps: %q", first)
		}
	})

	t.Run("exec", func(t *testing.T) {
		id, err := m.StartExec(ExecOpts{NS: "prod", Pod: gwPod, Container: "gateway", Cols: 100, Rows: 30})
		if err != nil {
			t.Fatal(err)
		}
		defer m.StopExec(id)
		time.Sleep(time.Second)
		if err := m.ExecInput(id, base64.StdEncoding.EncodeToString([]byte("echo st8ks-$((40+2))\n"))); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "exec output", 15*time.Second, func() bool {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			return strings.Contains(rec.exec[id], "st8ks-42")
		})
	})

	t.Run("port-forward", func(t *testing.T) {
		f, err := m.StartForward("prod", gwPod, 80)
		if err != nil {
			t.Fatal(err)
		}
		defer m.StopAllForwards()
		res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", f.Local))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("status = %d", res.StatusCode)
		}
	})

	t.Run("writes dry-run", func(t *testing.T) {
		ref := Ref{Kind: "Deployments", NS: "prod", Name: "api-gateway"}
		for name, fn := range map[string]func() (string, error){
			"restart": func() (string, error) { return c.Restart([]Ref{ref}, true) },
			"scale":   func() (string, error) { return c.Scale(ref, 3, true) },
			"cordon":  func() (string, error) { return c.Cordon("st8ks-dev-worker2", false, true) },
			"delete":  func() (string, error) { return c.Delete([]Ref{{Kind: "Pods", NS: "prod", Name: gwPod}}, true) },
			"create":  func() (string, error) { return c.Create(c.NewYAML("ConfigMaps", "prod"), "prod", true) },
			"trigger": func() (string, error) { return c.TriggerCronJob("prod", "cleanup-sessions", true) },
		} {
			msg, err := fn()
			if err != nil || !strings.Contains(msg, "dry") {
				t.Errorf("%s: %q %v", name, msg, err)
			}
		}
	})

	t.Run("helm", func(t *testing.T) {
		rels, err := c.HelmReleases()
		if err != nil {
			t.Fatal(err)
		}
		var web *HelmRelease
		for i := range rels {
			if rels[i].Name == "web" {
				web = &rels[i]
			}
		}
		if web == nil || web.Status != "failed" || web.Rev != 2 || web.Chart != "acme-service-1.8.0" {
			t.Fatalf("releases = %+v", rels)
		}
		d, err := c.HelmDetail("prod", "web")
		if err != nil || len(d.Revisions) != 2 || !strings.Contains(d.Values, "1.99-missing") {
			t.Fatalf("detail = %+v %v", d, err)
		}
		cur, target, err := c.HelmRollbackTarget("prod", "web")
		if err != nil || cur != 2 || target != 1 {
			t.Errorf("rollback target = %d %d %v", cur, target, err)
		}
		if msg, err := c.HelmRollback("prod", "web", 1, true); err != nil {
			t.Errorf("rollback dry-run: %q %v", msg, err)
		}
		if msg, err := c.HelmUpgradeValues("prod", "web", "image:\n  tag: 1.27-alpine\n", true); err != nil || !strings.Contains(msg, "renders") {
			t.Errorf("upgrade dry-run: %q %v", msg, err)
		}
	})

	t.Run("rbac", func(t *testing.T) {
		var group Subject
		for _, s := range c.RbacSubjects() {
			if s.Kind == "Group" && s.Name == "payments-devs" {
				group = s
			}
		}
		if group.Name == "" {
			t.Fatal("payments-devs group not found")
		}
		mx := c.RbacMatrix(group, "prod")
		cell := func(res, verb string) RbacCell {
			for i, r := range mx.Resources {
				if r.Name == res {
					for j, v := range mx.Verbs {
						if v == verb {
							return mx.Cells[i][j]
						}
					}
				}
			}
			t.Fatalf("no cell %s %s", res, verb)
			return RbacCell{}
		}
		if !cell("pods", "delete").A || cell("roles", "create").A || cell("nodes", "get").A {
			t.Errorf("unexpected permissions for payments-devs in prod")
		}
		ans := c.RbacExplain(group, "data", "secrets", "get")
		if ans.Allowed || !strings.Contains(ans.Verified, "agrees") {
			t.Errorf("secrets in data: %+v", ans)
		}
		sa := Subject{Kind: "ServiceAccount", Name: "checkout", NS: "prod"}
		if a := c.RbacExplain(sa, "prod", "configmaps", "list"); !a.Allowed || !strings.Contains(a.Verified, "agrees") {
			t.Errorf("checkout SA: %+v", a)
		}
	})

	t.Run("topology", func(t *testing.T) {
		topo := c.Topology("prod")
		ids := map[string]bool{}
		for _, n := range topo.Nodes {
			ids[n.ID] = true
		}
		for _, want := range []string{"ing/shop", "svc/frontend", "svc/api-gateway", "deploy/checkout", "cm/checkout-config", "sec/db-credentials"} {
			if !ids[want] {
				t.Errorf("topology misses %s", want)
			}
		}
		if len(topo.Edges) < 8 {
			t.Errorf("edges = %d", len(topo.Edges))
		}
	})

	t.Run("metrics", func(t *testing.T) {
		waitFor(t, "metrics-server data", 150*time.Second, func() bool {
			c.pollMetrics()
			return c.State().Metrics && len(c.met.nodesSnapshot()) == 3
		})
		ov := c.computeOverview()
		if ov.CPU.Used <= 0 || ov.Nodes != 3 || ov.Cordoned != 1 {
			t.Errorf("overview = %+v", ov)
		}
	})
	rec.mu.Lock()
	t.Logf("events emitted: %v", rec.counts)
	rec.mu.Unlock()
}
