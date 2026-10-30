//go:build integration

// Run against the local cluster after hack/dev-cluster/gitops.sh:
//
//	ST8KS_IT_KUBECONFIG=~/.kube/st8ks-dev.yaml go test -tags integration -run TestIntegrationArgoFlux -v ./internal/kube/
package kube

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const itObjects = `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: it-sync
  namespace: gitops
spec:
  project: default
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps.git
    path: guestbook
    targetRevision: main
  destination:
    server: https://kubernetes.default.svc
    namespace: it-sync
  syncPolicy:
    syncOptions: [CreateNamespace=true]
    retry: {limit: 2}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: it-ks
  namespace: gitops
spec:
  interval: 10m
  prune: true
  sourceRef: {kind: GitRepository, name: podinfo}
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: it-hr
  namespace: gitops
spec:
  interval: 10m
  chart:
    spec:
      chart: podinfo
      sourceRef: {kind: HelmRepository, name: podinfo}
`

func TestIntegrationArgoFlux(t *testing.T) {
	kc := os.Getenv("ST8KS_IT_KUBECONFIG")
	if kc == "" {
		t.Skip("set ST8KS_IT_KUBECONFIG")
	}
	m := NewManager(func(string, any) {}, func() LoadOptions { return LoadOptions{Explicit: []string{kc}} })
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if st, err := m.Connect("kind-st8ks-dev"); err != nil || st.Status != StatusConnected {
		t.Fatalf("connect: %v %+v", err, st)
	}
	c := m.Current()
	defer m.Shutdown()

	end := time.Now().Add(20 * time.Second)
	for c.kind(argoApps) == nil || c.kind(fluxOrder[len(fluxOrder)-1]) == nil {
		if time.Now().After(end) {
			t.Skip("the Argo CD and Flux CRDs are missing. Run hack/dev-cluster/gitops.sh first")
		}
		time.Sleep(300 * time.Millisecond)
	}
	// The GitOps kinds are watched without a call to Watch, because the
	// issue loop reads them.
	waitFor(t, "early GitOps watches", 30*time.Second, func() bool {
		for _, k := range gitopsKinds {
			if c.indexer(k) == nil || !c.Snapshot(k).Synced {
				return false
			}
		}
		return true
	})

	t.Run("rows", func(t *testing.T) {
		row := func(kind, name string) *Row {
			for _, r := range c.Snapshot(kind).Rows {
				if r.N == "gitops" && r.M == name {
					return r
				}
			}
			t.Fatalf("no row %s %s", kind, name)
			return nil
		}
		if r := row(argoApps, "payments"); r.C[2] != "Degraded" || r.C[3] != "Synced" || r.K[2] != tErr {
			t.Errorf("payments row = %q %q", r.C, r.K)
		}
		if r := row(argoApps, "orders"); r.C[4] != "Failed" {
			t.Errorf("orders row = %q", r.C)
		}
		ks := fluxTable("Kustomization")
		if r := row(ks, "platform"); r.C[2] != "False" || r.C[3] != "ArtifactFailed" || r.C[5] != "GitRepository/platform" {
			t.Errorf("platform row = %q", r.C)
		}
		if r := row(ks, "monitoring"); r.C[4] != "True" || r.K[4] != tWarn {
			t.Errorf("monitoring row = %q %q", r.C, r.K)
		}
		doc, err := c.GetObject(Ref{Kind: ks, NS: "gitops", Name: "monitoring"})
		if err != nil || !doc.Suspended || doc.Status != "True" {
			t.Errorf("monitoring doc: %v %+v", err, doc)
		}
	})

	t.Run("issues", func(t *testing.T) {
		want := map[string]struct{ reason, action string }{
			"argocd:gitops/payments":             {"Degraded", "argocd-sync"},
			"argocd:gitops/billing":              {"Missing", "argocd-sync"},
			"argocd:gitops/catalog":              {"OutOfSync", "argocd-sync"},
			"argocd:gitops/orders":               {"OperationFailed", "argocd-sync"},
			"argocd:gitops/search":               {"ComparisonError", "argocd-refresh"},
			"flux:gitops/GitRepository/platform": {"GitOperationFailed", "flux-reconcile"},
			"flux:gitops/OCIRepository/policies": {"Stalled · InvalidProviderConfiguration", "flux-reconcile"},
			"flux:gitops/HelmRepository/bitnami": {"Unknown · Progressing", "flux-reconcile"},
			"flux:gitops/HelmChart/gitops-redis": {"ChartPullError", "flux-reconcile"},
			"flux:gitops/Bucket/backups":         {"Suspended · BucketOperationFailed", "flux-resume"},
			"flux:gitops/Kustomization/platform": {"ArtifactFailed", "flux-reconcile"},
			"flux:gitops/Kustomization/infra":    {"BuildFailed", "flux-reconcile"},
			"flux:gitops/HelmRelease/redis":      {"ArtifactFailed", "flux-reconcile"},
			"flux:gitops/HelmRelease/nginx":      {"Stalled · RetriesExceeded", "flux-reconcile"},
		}
		// OutOfSync shows 2 minutes after the creation of a new application.
		waitFor(t, "GitOps issues", 180*time.Second, func() bool {
			for id := range want {
				if c.FindIssue(id) == nil {
					return false
				}
			}
			return true
		})
		for id, w := range want {
			i := c.FindIssue(id)
			if !strings.HasPrefix(i.Reason, w.reason) || i.Action == nil || i.Action.Type != w.action {
				t.Errorf("%s: reason %q action %+v, want %q %s", id, i.Reason, i.Action, w.reason, w.action)
			}
		}
		for _, i := range c.Issues() {
			if i.NS != "gitops" {
				continue
			}
			if _, ok := want[i.ID]; !ok && !strings.Contains(i.ID, "/it-") {
				t.Errorf("unexpected issue %s: %s · %s", i.ID, i.Title, i.Reason)
			}
			t.Logf("issue %s: %s · %s · %s", i.Sev, i.Title, i.Reason, i.Meta)
		}
		if i := c.FindIssue("flux:gitops/Kustomization/platform"); !has(i.Evidence, "Source GitRepository gitops/platform is not ready") {
			t.Errorf("platform evidence = %q", i.Evidence)
		}
		if i := c.FindIssue("flux:gitops/HelmRelease/redis"); !has(i.Evidence, "Source HelmChart gitops/gitops-redis is not ready") ||
			!has(i.Evidence, "Source HelmRepository gitops/bitnami is not ready") {
			t.Errorf("redis evidence = %q", i.Evidence)
		}
		if i := c.FindIssue("argocd:gitops/payments"); !has(i.Evidence, "Deployment payments/payments-api: Deployment payments-api exceeded its progress deadline") {
			t.Errorf("payments evidence = %q", i.Evidence)
		}
		text, cmds, err := c.IssueContext("flux:gitops/HelmRelease/redis")
		if err != nil || !strings.Contains(text, "gitops-redis") || len(cmds) == 0 {
			t.Errorf("issue context: %v %v", err, cmds)
		}
	})

	t.Run("actions", func(t *testing.T) {
		if _, err := c.Create(itObjects, "gitops", false); err != nil {
			t.Fatal(err)
		}
		app := Ref{Kind: argoApps, NS: "gitops", Name: "it-sync"}
		ks := Ref{Kind: fluxTable("Kustomization"), NS: "gitops", Name: "it-ks"}
		hr := Ref{Kind: fluxTable("HelmRelease"), NS: "gitops", Name: "it-hr"}
		defer func() {
			if _, err := c.Delete([]Ref{app, ks, hr}, false); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}()
		get := func(r Ref) *unstructured.Unstructured {
			t.Helper()
			u, err := c.ri(c.kind(r.Kind), r.NS).Get(context.Background(), r.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return u
		}
		// check runs an action as a dry run and for real. The dry run must
		// leave the object unchanged, and the real run must change it.
		check := func(name string, r Ref, run func(dry bool) (string, error), changed func(u *unstructured.Unstructured) bool) {
			t.Helper()
			before := get(r)
			msg, err := run(true)
			if err != nil || !strings.Contains(msg, "dry run") {
				t.Fatalf("%s dry run: %q %v", name, msg, err)
			}
			if after := get(r); after.GetResourceVersion() != before.GetResourceVersion() || changed(after) {
				t.Errorf("%s: the dry run changed the object", name)
			}
			if msg, err = run(false); err != nil {
				t.Fatalf("%s: %q %v", name, msg, err)
			}
			if after := get(r); after.GetResourceVersion() == before.GetResourceVersion() || !changed(after) {
				t.Errorf("%s: the object did not change: %v", name, after.Object)
			}
			t.Logf("%s: %s", name, msg)
		}

		check("sync", app, func(dry bool) (string, error) { return c.ArgoSync(app, dry) }, func(u *unstructured.Unstructured) bool {
			o := u.Object
			return nstr(o, "operation", "sync", "revision") == "main" && nstr(o, "operation", "initiatedBy", "username") == "st8ks" &&
				nint(o, "operation", "retry", "limit") == 2 && len(nslice(o, "operation", "sync", "syncOptions")) == 1
		})
		if _, err := c.ArgoSync(app, true); err == nil || !strings.Contains(err.Error(), "another operation runs") {
			t.Errorf("a second sync must fail: %v", err)
		}
		check("refresh", app, func(dry bool) (string, error) { return c.ArgoRefresh(app, false, dry) }, func(u *unstructured.Unstructured) bool {
			return u.GetAnnotations()["argocd.argoproj.io/refresh"] == "normal"
		})
		check("hard refresh", app, func(dry bool) (string, error) { return c.ArgoRefresh(app, true, dry) }, func(u *unstructured.Unstructured) bool {
			return u.GetAnnotations()["argocd.argoproj.io/refresh"] == "hard"
		})

		check("reconcile", ks, func(dry bool) (string, error) { return c.FluxReconcile(ks, false, dry) }, func(u *unstructured.Unstructured) bool {
			_, err := time.Parse(time.RFC3339Nano, u.GetAnnotations()["reconcile.fluxcd.io/requestedAt"])
			return err == nil
		})
		check("force reconcile", hr, func(dry bool) (string, error) { return c.FluxReconcile(hr, true, dry) }, func(u *unstructured.Unstructured) bool {
			a := u.GetAnnotations()
			return a["reconcile.fluxcd.io/forceAt"] != "" && a["reconcile.fluxcd.io/forceAt"] == a["reconcile.fluxcd.io/requestedAt"]
		})
		if _, err := c.FluxReconcile(ks, true, true); err == nil {
			t.Error("a Kustomization must not accept force")
		}
		check("suspend", ks, func(dry bool) (string, error) { return c.FluxSuspend(ks, true, dry) }, func(u *unstructured.Unstructured) bool {
			return nbool(u.Object, "spec", "suspend")
		})
		if _, err := c.FluxReconcile(ks, false, false); err == nil || !strings.Contains(err.Error(), "suspended") {
			t.Errorf("a suspended object must not reconcile: %v", err)
		}
		check("resume", ks, func(dry bool) (string, error) { return c.FluxSuspend(ks, false, dry) }, func(u *unstructured.Unstructured) bool {
			v, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
			return ok && !v
		})

		if _, err := c.ArgoSync(Ref{Kind: "Deployments", NS: "prod", Name: "api-gateway"}, true); err == nil {
			t.Error("sync must refuse a Deployment")
		}
		if _, err := c.FluxSuspend(Ref{Kind: argoApps, NS: "gitops", Name: "it-sync"}, true, true); err == nil {
			t.Error("suspend must refuse an Application")
		}
	})
}
