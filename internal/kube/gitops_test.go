package kube

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

var gitopsNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// at returns a time relative to gitopsNow in RFC 3339.
func at(d time.Duration) string { return gitopsNow.Add(-d).Format(time.RFC3339) }

func fixture(t *testing.T, text string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("fixture: %v\n%s", err, text)
	}
	return &unstructured.Unstructured{Object: m}
}

func app(t *testing.T, status string) *unstructured.Unstructured {
	return fixture(t, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: shop, namespace: argocd, uid: u1, creationTimestamp: "`+at(48*time.Hour)+`"}
spec:
  project: default
  source: {repoURL: "https://example.com/shop.git", path: deploy, targetRevision: main}
  destination: {server: "https://kubernetes.default.svc", namespace: shop}
`+status)
}

func has(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestArgoRules(t *testing.T) {
	cases := []struct {
		name, status   string
		since          time.Duration // out of sync since, before now. 0 means not out of sync.
		sev, reason    string
		title, action  string
		evidence, wake string
	}{
		{name: "healthy", status: `status:
  health: {status: Healthy}
  sync: {status: Synced, revision: 0123456789abcdef0123456789abcdef01234567}
  operationState: {phase: Succeeded, startedAt: "` + at(time.Hour) + `", finishedAt: "` + at(time.Hour) + `", operation: {sync: {revision: main}}}
`},
		{name: "degraded", status: `status:
  health: {status: Degraded, lastTransitionTime: "` + at(4*time.Minute) + `"}
  sync: {status: Synced}
  resources:
  - {kind: Deployment, namespace: shop, name: api, status: Synced, health: {status: Degraded, message: "Deployment \"api\" exceeded its progress deadline"}}
  - {kind: Service, namespace: shop, name: api, status: Synced, health: {status: Healthy}}
`, sev: "er", reason: "Degraded", title: "shop is degraded", action: "argocd-sync", evidence: `Deployment shop/api: Deployment "api" exceeded its progress deadline`},
		{name: "missing", status: `status:
  health: {status: Missing}
  sync: {status: Synced}
  resources:
  - {kind: ConfigMap, namespace: shop, name: settings, status: Synced, health: {status: Missing}}
`, sev: "wa", reason: "Missing", title: "shop has missing resources", action: "argocd-sync", evidence: "ConfigMap shop/settings"},
		{name: "out of sync in grace time", status: `status:
  health: {status: Healthy}
  sync: {status: OutOfSync}
`, since: 30 * time.Second, wake: "1m30s"},
		{name: "out of sync", status: `status:
  health: {status: Healthy}
  sync: {status: OutOfSync}
  resources:
  - {kind: Deployment, namespace: shop, name: api, status: OutOfSync, health: {status: Healthy}}
  - {kind: ConfigMap, namespace: shop, name: old, status: OutOfSync, requiresPruning: true}
`, since: 5 * time.Minute, sev: "wa", reason: "OutOfSync", title: "shop is out of sync", action: "argocd-sync", evidence: "ConfigMap shop/old requires pruning"},
		{name: "out of sync while a sync runs", status: `operation: {sync: {revision: main}}
status:
  health: {status: Progressing}
  sync: {status: OutOfSync}
  operationState: {phase: Running, startedAt: "` + at(10*time.Minute) + `", operation: {sync: {revision: main}}}
`},
		{name: "operation failed", status: `status:
  health: {status: Healthy}
  sync: {status: Synced}
  operationState:
    phase: Failed
    message: "one or more objects failed to apply"
    retryCount: 3
    startedAt: "` + at(10*time.Minute) + `"
    finishedAt: "` + at(9*time.Minute) + `"
    operation: {sync: {revision: main}}
    syncResult:
      revision: abc
      resources:
      - {group: apps, version: v1, kind: Deployment, namespace: shop, name: api, status: SyncFailed, message: "spec.template.spec.containers[0].image: Required value"}
      - {group: "", version: v1, kind: Service, namespace: shop, name: api, status: Synced}
`, sev: "er", reason: "OperationFailed", title: "shop sync failed", action: "argocd-sync", evidence: "Deployment shop/api: spec.template.spec.containers[0].image: Required value"},
		{name: "operation error", status: `status:
  health: {status: Healthy}
  sync: {status: Synced}
  operationState: {phase: Error, message: "rpc error", startedAt: "` + at(time.Minute) + `", operation: {sync: {revision: main}}}
`, sev: "er", reason: "OperationError", title: "shop sync failed", action: "argocd-sync", evidence: "Sync phase Error: rpc error"},
		{name: "comparison error", status: `status:
  health: {status: Healthy}
  sync: {status: Unknown}
  conditions:
  - {type: ComparisonError, message: "Unable to resolve 'feature/x' to a commit SHA", lastTransitionTime: "` + at(time.Minute) + `"}
`, sev: "er", reason: "ComparisonError", title: "shop has a ComparisonError", action: "argocd-refresh", evidence: "ComparisonError: Unable to resolve"},
		{name: "invalid spec error", status: `status:
  conditions:
  - {type: InvalidSpecError, message: "application destination server is not permitted in project"}
`, sev: "er", reason: "InvalidSpecError", action: "argocd-refresh", evidence: "InvalidSpecError: application destination"},
		{name: "sync error", status: `status:
  conditions:
  - {type: SyncError, message: "Failed to sync: one or more synchronization tasks are not valid"}
`, sev: "er", reason: "SyncError", action: "argocd-sync"},
		{name: "warning condition", status: `status:
  health: {status: Healthy}
  sync: {status: Synced}
  conditions:
  - {type: SharedResourceWarning, message: "ConfigMap is part of applications shop and cart"}
`},
		{name: "several problems", status: `status:
  health: {status: Degraded}
  sync: {status: OutOfSync}
  operationState: {phase: Failed, message: "hook failed", startedAt: "` + at(time.Hour) + `", finishedAt: "` + at(time.Hour) + `", operation: {sync: {revision: main}}}
`, since: time.Hour, sev: "er", reason: "OperationFailed · Degraded · OutOfSync", title: "shop sync failed", action: "argocd-sync", evidence: "Out of sync for 1h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := app(t, tc.status)
			a := readArgo(u)
			var since time.Time
			if tc.since > 0 {
				since = gitopsNow.Add(-tc.since)
			}
			i, wake := argoIssue(u, a, gitopsNow, since)
			if tc.wake != "" {
				if got := wake.Sub(gitopsNow).String(); got != tc.wake {
					t.Errorf("wake in %s, want %s", got, tc.wake)
				}
			}
			if tc.sev == "" {
				if i != nil {
					t.Fatalf("want no issue, got %+v", i)
				}
				return
			}
			if i == nil {
				t.Fatal("want an issue")
			}
			if i.ID != "argocd:argocd/shop" || i.Kind != argoApps || i.NS != "argocd" || i.Obj != "shop" {
				t.Errorf("issue ref = %s %s %s/%s", i.ID, i.Kind, i.NS, i.Obj)
			}
			if i.Sev != tc.sev || i.Reason != tc.reason {
				t.Errorf("sev %s reason %q, want %s %q", i.Sev, i.Reason, tc.sev, tc.reason)
			}
			if tc.title != "" && i.Title != tc.title {
				t.Errorf("title %q, want %q", i.Title, tc.title)
			}
			if i.Action == nil || i.Action.Type != tc.action || i.Action.Name != "shop" || i.Action.NS != "argocd" {
				t.Errorf("action = %+v, want %s", i.Action, tc.action)
			}
			if tc.evidence != "" && !has(i.Evidence, tc.evidence) {
				t.Errorf("evidence misses %q: %q", tc.evidence, i.Evidence)
			}
			if !has(i.Cmds, "argocd app get argocd/shop") || !has(i.Cmds, "kubectl describe application.argoproj.io shop -n argocd") {
				t.Errorf("cmds = %q", i.Cmds)
			}
			if i.Summary == "" || i.Fix == "" || !strings.Contains(i.Fix, "argocd app") {
				t.Errorf("summary %q fix %q", i.Summary, i.Fix)
			}
		})
	}
}

func TestOutOfSyncSince(t *testing.T) {
	var g gitopsState
	now := gitopsNow
	old := now.Add(-time.Hour)
	// An application that st8ks sees for the first time starts at the reference.
	if got := g.since("a", true, old, now); !got.Equal(old) {
		t.Errorf("first sight = %v, want %v", got, old)
	}
	if got := g.since("b", false, old, now); !got.IsZero() {
		t.Errorf("in sync = %v", got)
	}
	g.commit()
	// The time stays while the application stays out of sync.
	if got := g.since("a", true, now, now.Add(time.Minute)); !got.Equal(old) {
		t.Errorf("second run = %v, want %v", got, old)
	}
	// An application that st8ks saw in sync starts now.
	later := now.Add(time.Minute)
	if got := g.since("b", true, old, later); !got.Equal(later) {
		t.Errorf("transition = %v, want %v", got, later)
	}
	g.commit()
	// An application that leaves the cache is forgotten.
	g.since("a", true, old, later)
	g.commit()
	if _, ok := g.oos["b"]; ok || g.seen["b"] {
		t.Error("b is not forgotten")
	}
}

func flux(t *testing.T, kind, name, spec, status string) *unstructured.Unstructured {
	t.Helper()
	fk := fluxKinds[fluxTable(kind)]
	group := strings.SplitN(fk.name, ".", 2)[1]
	return fixture(t, `apiVersion: `+group+`/v1
kind: `+kind+`
metadata: {name: `+name+`, namespace: flux-system, uid: `+name+`, creationTimestamp: "`+at(24*time.Hour)+`"}
spec:
`+spec+`
status:
`+status)
}

func ready(status, reason, msg string, ago time.Duration) string {
	b, _ := json.Marshal(msg)
	return `  conditions:
  - {type: Ready, status: "` + status + `", reason: ` + reason + `, message: ` + string(b) + `, lastTransitionTime: "` + at(ago) + `"}
`
}

func TestFluxRules(t *testing.T) {
	ksSpec := "  interval: 10m\n  prune: true\n  path: ./apps\n  sourceRef: {kind: GitRepository, name: podinfo}\n"
	gitSpec := "  interval: 1m\n  url: https://example.com/podinfo.git\n"
	cases := []struct {
		name               string
		obj                *unstructured.Unstructured
		sev, reason, title string
		action, evidence   string
		wake               time.Duration
	}{
		{name: "ready", obj: flux(t, "GitRepository", "podinfo", gitSpec, ready("True", "Succeeded", "stored artifact for revision 'main@sha1:abc'", time.Hour))},
		{name: "ready false", obj: flux(t, "GitRepository", "podinfo", gitSpec, ready("False", "GitOperationFailed", "failed to checkout and determine revision: authentication required", time.Hour)),
			sev: "er", reason: "GitOperationFailed", title: "podinfo GitRepository is not ready", action: "flux-reconcile", evidence: "Ready False, reason GitOperationFailed: failed to checkout"},
		{name: "stalled", obj: flux(t, "OCIRepository", "manifests", "  interval: 5m\n  url: example.com/manifests\n", ready("False", "URLInvalid", "URL must start with oci://", time.Hour)+
			"  - {type: Stalled, status: \"True\", reason: URLInvalid, message: \"URL must start with oci://\", lastTransitionTime: \""+at(time.Hour)+"\"}\n"),
			sev: "er", reason: "Stalled · URLInvalid", title: "manifests OCIRepository is stalled", action: "flux-reconcile", evidence: "Stalled True, reason URLInvalid"},
		{name: "unknown in grace time", obj: flux(t, "HelmRepository", "charts", "  url: https://charts.example.com\n", ready("Unknown", "Progressing", "building artifact", 4*time.Minute)), wake: 6 * time.Minute},
		{name: "unknown", obj: flux(t, "HelmRepository", "charts", "  url: https://charts.example.com\n", ready("Unknown", "Progressing", "building artifact: fetching index", 15*time.Minute)),
			sev: "wa", reason: "Unknown · Progressing", title: "charts HelmRepository does not become ready", action: "flux-reconcile", evidence: "Ready Unknown, reason Progressing"},
		{name: "no ready condition", obj: flux(t, "Bucket", "assets", "  interval: 5m\n  endpoint: minio.example.com\n  bucketName: assets\n", "  observedGeneration: 1\n"),
			sev: "wa", reason: "Unknown", action: "flux-reconcile", evidence: "No controller reported a Ready condition for 24h"},
		{name: "suspended and ready", obj: flux(t, "Kustomization", "apps", ksSpec+"  suspend: true\n", ready("True", "ReconciliationSucceeded", "Applied revision: main@sha1:abc", time.Hour))},
		{name: "suspended and failing", obj: flux(t, "Kustomization", "apps", ksSpec+"  suspend: true\n", ready("False", "BuildFailed", "kustomize build failed", time.Hour)),
			sev: "wa", reason: "Suspended · BuildFailed", title: "apps Kustomization is suspended and not ready", action: "flux-resume", evidence: "kustomize build failed"},
		{name: "build failed", obj: flux(t, "Kustomization", "apps", ksSpec, ready("False", "BuildFailed", "kustomize build failed: accumulating resources", time.Hour)),
			sev: "er", reason: "BuildFailed", title: "apps Kustomization is not ready", action: "flux-reconcile", evidence: "kustomize build failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.obj.GetKind()
			fk := fluxKinds[fluxTable(kind)]
			i, wake := fluxIssue(tc.obj, fk, gitopsNow, nil)
			if tc.wake > 0 && wake.Sub(gitopsNow) != tc.wake {
				t.Errorf("wake in %s, want %s", wake.Sub(gitopsNow), tc.wake)
			}
			if tc.sev == "" {
				if i != nil {
					t.Fatalf("want no issue, got %+v", i)
				}
				return
			}
			if i == nil {
				t.Fatal("want an issue")
			}
			if i.ID != "flux:flux-system/"+kind+"/"+tc.obj.GetName() || i.Kind != fk.name {
				t.Errorf("id %s kind %s", i.ID, i.Kind)
			}
			if i.Sev != tc.sev || i.Reason != tc.reason {
				t.Errorf("sev %s reason %q, want %s %q", i.Sev, i.Reason, tc.sev, tc.reason)
			}
			if tc.title != "" && i.Title != tc.title {
				t.Errorf("title %q, want %q", i.Title, tc.title)
			}
			if i.Action == nil || i.Action.Type != tc.action || i.Action.Kind != fk.name {
				t.Errorf("action = %+v, want %s", i.Action, tc.action)
			}
			if !has(i.Evidence, tc.evidence) {
				t.Errorf("evidence misses %q: %q", tc.evidence, i.Evidence)
			}
			if !has(i.Cmds, "flux get "+fk.get+" "+tc.obj.GetName()+" -n flux-system") || !has(i.Cmds, "flux events --for "+kind+"/") || !has(i.Cmds, "kubectl describe "+strings.ToLower(kind)+".") {
				t.Errorf("cmds = %q", i.Cmds)
			}
			if i.Summary == "" || !strings.Contains(i.Fix, "flux ") {
				t.Errorf("summary %q fix %q", i.Summary, i.Fix)
			}
		})
	}
}

func TestFluxSourceEvidence(t *testing.T) {
	ks := flux(t, "Kustomization", "apps", "  interval: 10m\n  prune: true\n  sourceRef: {kind: GitRepository, name: podinfo}\n",
		ready("False", "ArtifactFailed", "Source artifact not found, retrying in 30s", time.Hour))
	hr := flux(t, "HelmRelease", "redis", "  interval: 10m\n  chart: {spec: {chart: redis, version: 99.0.0, sourceRef: {kind: HelmRepository, name: bitnami}}}\n",
		ready("False", "ArtifactFailed", "HelmChart 'flux-system/flux-system-redis' is not ready", time.Hour)+"  helmChart: flux-system/flux-system-redis\n")
	gitFail := flux(t, "GitRepository", "podinfo", "  interval: 1m\n  url: https://example.com/podinfo.git\n", ready("False", "GitOperationFailed", "authentication required", time.Hour))
	gitOK := flux(t, "GitRepository", "podinfo", "  interval: 1m\n  url: https://example.com/podinfo.git\n", ready("True", "Succeeded", "stored artifact", time.Hour))
	chart := flux(t, "HelmChart", "flux-system-redis", "  interval: 10m\n  chart: redis\n  sourceRef: {kind: HelmRepository, name: bitnami}\n", ready("False", "ChartPullError", "no chart version found for redis-99.0.0", time.Hour))
	repo := flux(t, "HelmRepository", "bitnami", "  url: https://charts.example.com\n", ready("Unknown", "Progressing", "fetching index", time.Hour))

	cache := func(objs ...*unstructured.Unstructured) sourceLookup {
		return func(kind, ns, name string) (*unstructured.Unstructured, bool) {
			if fluxTable(kind) == "" {
				return nil, false
			}
			for _, o := range objs {
				if o.GetKind() == kind && o.GetNamespace() == ns && o.GetName() == name {
					return o, true
				}
			}
			return nil, true
		}
	}
	ksKind := fluxKinds[fluxTable("Kustomization")]
	hrKind := fluxKinds[fluxTable("HelmRelease")]

	i, _ := fluxIssue(ks, ksKind, gitopsNow, cache(gitFail))
	if i.Title != "apps Kustomization fails because its source fails" || !has(i.Evidence, "Source GitRepository flux-system/podinfo is not ready: Ready False, reason GitOperationFailed: authentication required") ||
		!strings.Contains(i.Fix, "Fix source GitRepository flux-system/podinfo first") || !strings.Contains(i.Fix, "--with-source") {
		t.Errorf("failing source: %q %q %q", i.Title, i.Evidence, i.Fix)
	}
	i, _ = fluxIssue(ks, ksKind, gitopsNow, cache(gitOK))
	if has(i.Evidence, "Source GitRepository") || strings.Contains(i.Title, "source") {
		t.Errorf("ready source is named: %q %q", i.Title, i.Evidence)
	}
	i, _ = fluxIssue(ks, ksKind, gitopsNow, cache())
	if !has(i.Evidence, "st8ks cannot find source GitRepository flux-system/podinfo.") {
		t.Errorf("missing source: %q", i.Evidence)
	}
	// The chain goes from the HelmRelease to its HelmChart and to the
	// HelmRepository of the chart.
	i, _ = fluxIssue(hr, hrKind, gitopsNow, cache(chart, repo))
	if !has(i.Evidence, "Source HelmChart flux-system/flux-system-redis is not ready: Ready False, reason ChartPullError") ||
		!has(i.Evidence, "Source HelmRepository flux-system/bitnami is not ready: Ready Unknown") || !strings.Contains(i.Fix, "HelmChart flux-system/flux-system-redis") {
		t.Errorf("helm chain: %q %q", i.Evidence, i.Fix)
	}
	// A kind that st8ks does not watch adds no evidence.
	ea := flux(t, "Kustomization", "ext", "  interval: 10m\n  prune: true\n  sourceRef: {kind: ExternalArtifact, name: x}\n", ready("False", "ArtifactFailed", "x", time.Hour))
	if i, _ = fluxIssue(ea, ksKind, gitopsNow, cache()); has(i.Evidence, "source") {
		t.Errorf("unknown source kind: %q", i.Evidence)
	}
	// Without spec.chart.spec in the status, the chart name comes from the release.
	hr2 := flux(t, "HelmRelease", "web", "  interval: 10m\n  chart: {spec: {chart: web, sourceRef: {kind: HelmRepository, name: bitnami, namespace: charts}}}\n", ready("True", "InstallSucceeded", "ok", time.Hour))
	if r := fluxSource(hrKind, hr2); r == nil || r.String() != "HelmChart charts/flux-system-web" {
		t.Errorf("derived chart = %v", r)
	}
}

func TestArgoSyncPatch(t *testing.T) {
	read := func(b []byte) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	// The text continues the spec of the fixture.
	u := app(t, `  syncPolicy:
    syncOptions: [CreateNamespace=true]
    retry: {limit: 2}
`)
	u.SetResourceVersion("42")
	b, rev, err := argoSyncPatch(u)
	if err != nil || rev != "main" {
		t.Fatalf("patch: %q %v", rev, err)
	}
	m := read(b)
	if nstr(m, "metadata", "resourceVersion") != "42" || nstr(m, "operation", "sync", "revision") != "main" ||
		nstr(m, "operation", "initiatedBy", "username") != "st8ks" || nmap(m, "operation", "sync", "syncStrategy", "hook") == nil ||
		nstr(m, "operation", "sync", "source", "repoURL") != "https://example.com/shop.git" || nint(m, "operation", "retry", "limit") != 2 ||
		len(nslice(m, "operation", "sync", "syncOptions")) != 1 || nbool(m, "operation", "sync", "prune") {
		t.Errorf("single source patch = %s", b)
	}
	if st, ok := m["status"].(map[string]any); !ok || st["operationState"] != nil {
		t.Errorf("the patch must remove status.operationState: %s", b)
	} else if _, ok := st["operationState"]; !ok {
		t.Errorf("the patch must set status.operationState to null: %s", b)
	}

	multi := fixture(t, `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: multi, namespace: argocd}
spec:
  sources:
  - {repoURL: "https://example.com/a.git", targetRevision: v1.2.0}
  - {repoURL: "https://charts.example.com", chart: redis, targetRevision: ""}
`)
	b, rev, err = argoSyncPatch(multi)
	m = read(b)
	revs := nslice(m, "operation", "sync", "revisions")
	if err != nil || rev != "v1.2.0, HEAD" || len(revs) != 2 || revs[1] != "HEAD" || len(nslice(m, "operation", "sync", "sources")) != 2 || nmap(m, "operation", "sync", "source") != nil {
		t.Errorf("multi source patch = %s %q %v", b, rev, err)
	}

	busy := app(t, "operation: {sync: {revision: main}}\n")
	if _, _, err := argoSyncPatch(busy); err == nil || !strings.Contains(err.Error(), "another operation runs") {
		t.Errorf("busy application: %v", err)
	}
}

func TestGitopsRows(t *testing.T) {
	ak := &Kind{Name: argoApps, Namespaced: true, Custom: true}
	gitopsColumns(ak)
	if len(ak.Cols) != 8 || ak.Cols[2].L != "Health" || ak.Cols[3].L != "Sync" || ak.Actions[0] != "argocd-sync" {
		t.Errorf("argo columns = %+v %v", ak.Cols, ak.Actions)
	}
	u := app(t, `status:
  health: {status: Degraded}
  sync: {status: OutOfSync, revision: 0123456789abcdef0123456789abcdef01234567}
  operationState: {phase: Failed, startedAt: "`+at(time.Hour)+`", operation: {sync: {revision: main}}}
`)
	r := ak.row(nil, u)
	if strings.Join(r.C[2:7], "|") != "Degraded|OutOfSync|Failed|0123456|default" || r.K[2:5] != "ewe" || !r.B {
		t.Errorf("argo row = %q %q", r.C, r.K)
	}
	before := r.S
	u.Object["status"].(map[string]any)["operationState"].(map[string]any)["message"] = "hook failed"
	if ak.row(nil, u).S == before {
		t.Error("the digest must change with the operation message")
	}

	fk := &Kind{Name: fluxTable("Kustomization"), Namespaced: true, Custom: true}
	gitopsColumns(fk)
	if len(fk.Cols) != 9 || fk.Cols[5].L != "Source" || fk.Actions[1] != "flux-suspend" {
		t.Errorf("flux columns = %+v %v", fk.Cols, fk.Actions)
	}
	ks := flux(t, "Kustomization", "apps", "  interval: 10m\n  prune: true\n  suspend: true\n  sourceRef: {kind: GitRepository, name: podinfo}\n",
		ready("False", "BuildFailed", "kustomize build failed\nline two", time.Hour)+"  lastAttemptedRevision: main@sha1:0123456789abcdef0123456789abcdef01234567\n")
	r = fk.row(nil, ks)
	if strings.Join(r.C[2:8], "|") != "False|BuildFailed|True|GitRepository/podinfo|main@sha1:0123456789ab|kustomize build failed" || r.K[2:5] != "eew" {
		t.Errorf("flux row = %q %q", r.C, r.K)
	}
	before = r.S
	ks.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["message"] = "kustomize build failed\nline three"
	if fk.row(nil, ks).S == before {
		t.Error("the digest must change with the condition message")
	}

	hr := fluxKinds[fluxTable("HelmRelease")]
	h := flux(t, "HelmRelease", "redis", "  interval: 10m\n  chart: {spec: {chart: redis, version: 19.x, sourceRef: {kind: HelmRepository, name: bitnami}}}\n", ready("True", "UpgradeSucceeded", "ok", time.Hour))
	if got := fluxSourceCell(hr, h); got != "redis@19.x" {
		t.Errorf("chart cell = %q", got)
	}
	other := &Kind{Name: "certificates.cert-manager.io", Namespaced: true, Custom: true, Actions: actDefault}
	gitopsColumns(other)
	if other.row != nil || len(other.Actions) != 2 {
		t.Error("other kinds must keep their columns")
	}
}

func TestShortRev(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789abcdef0123456789abcdef01234567":            "0123456",
		"main@sha1:0123456789abcdef0123456789abcdef01234567":  "main@sha1:0123456789ab",
		"sha256:0123456789abcdef0123456789abcdef0123456789ab": "sha256:0123456789ab",
		"6.5.0": "6.5.0",
	} {
		if got := shortRev(in); got != want {
			t.Errorf("shortRev(%q) = %q, want %q", in, got, want)
		}
	}
}
