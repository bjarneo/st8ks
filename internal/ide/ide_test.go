package ide

import (
	"strings"
	"testing"
)

var repo = map[string]string{
	"apps/api-gateway/kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - hpa.yaml`,
	"apps/api-gateway/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api-gateway
spec:
  replicas: 2
  selector:
    matchLabels:
      app: api-gateway
  template:
    metadata:
      labels:
        app: api-gateway
    spec:
      containers:
        - name: api-gateway
          image: registry.acme.io/api-gateway:3.2.0
          ports:
            - containerPort: 8080
          resources:
            requests:
              cpu: 250m
              memory: 256Mi
          readinessProbe:
            httpGet:
              path: /readyz
              port: 8080`,
	"apps/api-gateway/hpa.yaml": `apiVersion: autoscaling/v2beta2
kind: HorizontalPodAutoscaler
metadata:
  name: api-gateway
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: api-gateway
  minReplicas: 2
  maxReplicas: 10`,
	"apps/checkout/kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml`,
	"apps/checkout/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout
spec:
  replicas: 2
  selector:
    matchLabels:
      app: checkout
  template:
    metadata:
      labels:
        app: checkout
    spec:
      containers:
        - name: checkout
          image: registry.acme.io/checkout:2.14.1
          env:
            - name: CATALOG_CACHE_MAX
              value: 250000
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 256Mi`,
	"apps/image-resizer/kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml`,
	"apps/image-resizer/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: image-resizer
spec:
  selector:
    matchLabels:
      app: image-resizer
  template:
    metadata:
      labels:
        app: resizer
    spec:
      containers:
        - name: image-resizer
          image: registry.acme.io/image-resizer:latest`,
	"overlays/prod/kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: prod
resources:
  - ../../apps/checkout
  - ../../apps/api-gateway
  - ../../apps/missing`,
}

func index(t *testing.T) *Index {
	t.Helper()
	files := make([]string, 0, len(repo))
	parsed := map[string]*Parsed{}
	for p, text := range repo {
		files = append(files, p)
		parsed[p] = &Parsed{Path: p, Text: text, Lines: strings.Split(text, "\n"), Docs: ParseDocs(text)}
	}
	return BuildIndex(files, parsed, nil)
}

func run(t *testing.T, ix *Index, p string, cl Cluster) []Diag {
	t.Helper()
	f := ix.Files[p]
	e := &lintEnv{path: p, text: f.Text, lines: f.Lines, docs: f.Docs, ix: ix, cl: cl}
	ds := lintStatic(e)
	if cl != nil {
		ds = dedupe(append(ds, lintLive(e, ds)...))
	}
	return ds
}

func find(ds []Diag, code string) *Diag {
	for i := range ds {
		if ds[i].Code == code {
			return &ds[i]
		}
	}
	return nil
}

func fix(t *testing.T, ix *Index, p string, d *Diag) string {
	t.Helper()
	if d == nil || d.run == nil {
		t.Fatalf("no fix")
	}
	return strings.Join(d.run(append([]string{}, ix.Files[p].Lines...)), "\n")
}

func TestParseDocs(t *testing.T) {
	docs := ParseDocs("# head\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\napiVersion: v1\nkind: Service\nmetadata: {name: b}\n---\nkey: [unclosed\n")
	if len(docs) != 3 {
		t.Fatalf("got %d docs", len(docs))
	}
	if docs[0].Kind != "ConfigMap" || docs[0].Name != "a" || docs[1].Name != "b" {
		t.Fatalf("wrong identity: %+v %+v", docs[0], docs[1])
	}
	if docs[1].Start != 6 {
		t.Fatalf("second doc starts at %d", docs[1].Start)
	}
	if docs[2].Err == "" || docs[2].ErrLine < 10 {
		t.Fatalf("want an error in the third doc, got %q at %d", docs[2].Err, docs[2].ErrLine)
	}
	f := docs[0].FieldAt(4)
	if f == nil || PathString(f.Path) != "metadata.name" {
		t.Fatalf("field at line 4: %+v", f)
	}
	d := ParseDocs(repo["apps/checkout/deployment.yaml"])[0]
	f = d.FieldAt(15)
	if f == nil || PathString(f.Path) != "spec.template.spec.containers[0].name" {
		t.Fatalf("field at line 15: %v", f)
	}
}

func TestIndexTargets(t *testing.T) {
	ix := index(t)
	ts := ix.Targets["apps/checkout/deployment.yaml"]
	if len(ts) != 1 || ts[0].NS != "prod" || ts[0].Root != "overlays/prod/kustomization.yaml" {
		t.Fatalf("targets: %+v", ts)
	}
	if got := ix.Builds("apps/api-gateway/hpa.yaml"); len(got) != 1 {
		t.Fatalf("builds: %v", got)
	}
	ds := run(t, ix, "overlays/prod/kustomization.yaml", nil)
	if d := find(ds, "missing-path"); d == nil || d.Line != 6 || !strings.Contains(d.Msg, "../../apps/missing") {
		t.Fatalf("missing path: %+v", ds)
	}
}

func TestStaticChecks(t *testing.T) {
	ix := index(t)

	ds := run(t, ix, "apps/api-gateway/hpa.yaml", nil)
	d := find(ds, "api-removed")
	if d == nil || d.Sev != SevWarning {
		t.Fatalf("api-removed without a cluster: %+v", ds)
	}
	if got := fix(t, ix, "apps/api-gateway/hpa.yaml", d); !strings.HasPrefix(got, "apiVersion: autoscaling/v2\n") {
		t.Fatalf("migrate fix: %s", got)
	}

	ds = run(t, ix, "apps/api-gateway/deployment.yaml", nil)
	d = find(ds, "hpa-managed")
	if d == nil || d.Line != 5 {
		t.Fatalf("hpa-managed: %+v", ds)
	}
	if got := fix(t, ix, "apps/api-gateway/deployment.yaml", d); strings.Contains(got, "replicas: 2") {
		t.Fatalf("remove replicas: %s", got)
	}
	if find(ds, "no-readiness") != nil || find(ds, "image-tag") != nil {
		t.Fatalf("unexpected: %+v", ds)
	}

	ds = run(t, ix, "apps/image-resizer/deployment.yaml", nil)
	if d := find(ds, "selector-mismatch"); d == nil || d.Line != 11 {
		t.Fatalf("selector: %+v", ds)
	}
	if d := find(ds, "image-tag"); d == nil || d.Line != 15 {
		t.Fatalf("image tag: %+v", ds)
	}
	d = find(ds, "no-resources")
	if d == nil {
		t.Fatalf("no resources: %+v", ds)
	}
	got := fix(t, ix, "apps/image-resizer/deployment.yaml", d)
	if !strings.Contains(got, "latest\n          resources:\n            requests:\n              cpu: 100m") {
		t.Fatalf("resources fix:\n%s", got)
	}

	ds = run(t, ix, "apps/checkout/deployment.yaml", nil)
	d = find(ds, "type")
	if d == nil || d.Line != 19 {
		t.Fatalf("env type: %+v", ds)
	}
	if got := fix(t, ix, "apps/checkout/deployment.yaml", d); !strings.Contains(got, `value: "250000"`) {
		t.Fatalf("quote fix: %s", got)
	}
}

func TestTabsAndSyntax(t *testing.T) {
	text := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n\tname: x\n"
	ix := BuildIndex([]string{"a.yaml"}, map[string]*Parsed{"a.yaml": {Path: "a.yaml", Text: text, Lines: strings.Split(text, "\n"), Docs: ParseDocs(text)}}, nil)
	ds := run(t, ix, "a.yaml", nil)
	if len(ds) != 1 || ds[0].Code != "yaml-tab" || ds[0].Line != 3 {
		t.Fatalf("tabs: %+v", ds)
	}
	if got := fix(t, ix, "a.yaml", &ds[0]); !strings.Contains(got, "\n  name: x") {
		t.Fatalf("tab fix: %q", got)
	}
}

// fake is a cluster for the tests.
type fake struct {
	minor int
	work  map[string]*Workload
	live  map[string]map[string]any
}

func (f *fake) Name() string             { return "prod-us-east" }
func (f *fake) DefaultNamespace() string { return "default" }
func (f *fake) Minor() int               { return f.minor }
func (f *fake) Version() uint64          { return 1 }
func (f *fake) Resource(apiVersion, kind string) (Resource, bool) {
	if apiVersion == "autoscaling/v2beta2" {
		return Resource{}, false
	}
	return Resource{Name: strings.ToLower(kind), Namespaced: true}, true
}
func (f *fake) OpenAPI(string) ([]byte, error) { return nil, nil }
func (f *fake) Live(r Ref) (map[string]any, error) {
	return f.live[r.NS+"/"+r.Name], nil
}
func (f *fake) Workload(r Ref) *Workload                         { return f.work[r.NS+"/"+r.Name] }
func (f *fake) PullFailures() []PullFailure                      { return nil }
func (f *fake) Status(Ref) *LiveStatus                           { return &LiveStatus{Found: true} }
func (f *fake) Apply([]map[string]any, bool, bool) []ApplyResult { return nil }
func (f *fake) Rollout(Ref, func(string, string))                {}

func TestLiveChecks(t *testing.T) {
	ix := index(t)
	cl := &fake{minor: 31, work: map[string]*Workload{
		"prod/checkout": {Found: true, Containers: map[string]*ContainerFacts{
			"checkout": {Image: "registry.acme.io/checkout:2.14.1", Pods: 2, OOMKills: 14, MemLimit: "256Mi", CrashExit: -1},
		}},
	}}
	ds := run(t, ix, "apps/api-gateway/hpa.yaml", cl)
	if d := find(ds, "api-removed"); d == nil || d.Sev != SevError || !strings.Contains(d.Msg, "runs 1.31") {
		t.Fatalf("api-removed on 1.31: %+v", ds)
	}
	ds = run(t, ix, "apps/checkout/deployment.yaml", cl)
	d := find(ds, "live-oom")
	if d == nil || d.Line != 26 || !d.Live || d.Fix != "Set the limit to 512Mi, the request to 256Mi" {
		t.Fatalf("live-oom: %+v", ds)
	}
	got := fix(t, ix, "apps/checkout/deployment.yaml", d)
	if !strings.Contains(got, "memory: 512Mi") || !strings.Contains(got, "memory: 256Mi") || strings.Contains(got, "128Mi") {
		t.Fatalf("oom fix:\n%s", got)
	}
}

func TestSchemaChecks(t *testing.T) {
	sd, err := ParseSchemaDoc([]byte(`{"components":{"schemas":{
"io.k8s.api.apps.v1.Deployment":{"type":"object","x-kubernetes-group-version-kind":[{"group":"apps","version":"v1","kind":"Deployment"}],
 "properties":{"apiVersion":{"type":"string"},"kind":{"type":"string"},"metadata":{"type":"object","properties":{"name":{"type":"string"},"labels":{"type":"object","additionalProperties":{"type":"string"}}}},
 "spec":{"allOf":[{"$ref":"#/components/schemas/io.k8s.api.apps.v1.DeploymentSpec"}],"description":"Spec of the deployment."}}},
"io.k8s.api.apps.v1.DeploymentSpec":{"type":"object","required":["selector"],"properties":{"replicas":{"type":"integer","description":"Number of desired pods."},"selector":{"type":"object"},"paused":{"type":"boolean"}}}
}}}`), "apps/v1")
	if err != nil {
		t.Fatal(err)
	}
	text := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: x\n  labels:\n    version: 1.0\nspec:\n  replica: 2\n  paused: \"no\"\n"
	ix := BuildIndex([]string{"d.yaml"}, map[string]*Parsed{"d.yaml": {Path: "d.yaml", Text: text, Lines: strings.Split(text, "\n"), Docs: ParseDocs(text)}}, nil)
	e := &lintEnv{path: "d.yaml", text: text, lines: strings.Split(text, "\n"), docs: ParseDocs(text), ix: ix,
		cl: &fake{minor: 31}, schema: func(string) *SchemaDoc { return sd }}
	ds := lintStatic(e)
	u := find(ds, "unknown-field")
	if u == nil || u.Line != 7 || u.Fix != "Rename to replicas" {
		t.Fatalf("unknown field: %+v", ds)
	}
	if got := strings.Join(u.run(append([]string{}, e.lines...)), "\n"); !strings.Contains(got, "  replicas: 2") {
		t.Fatalf("rename fix: %s", got)
	}
	var types []int
	for _, d := range ds {
		if d.Code == "type" {
			types = append(types, d.Line)
		}
	}
	if len(types) != 2 || types[0] != 5 || types[1] != 8 {
		t.Fatalf("type errors on lines %v: %+v", types, ds)
	}
	if r := find(ds, "required"); r == nil || r.Line != 6 {
		t.Fatalf("required: %+v", ds)
	}
	d := ParseDocs(text)[0]
	f := d.FieldAt(7)
	fs := sd.At(sd.Kind("Deployment"), []string{"spec"})
	if sd.TypeName(fs) != "DeploymentSpec" || sd.Resolve(fs).Desc != "Spec of the deployment." || f == nil {
		t.Fatalf("type name %q, desc %q", sd.TypeName(fs), sd.Resolve(fs).Desc)
	}
}

func TestProject(t *testing.T) {
	text := repo["apps/checkout/deployment.yaml"]
	lines := strings.Split(text, "\n")
	docs := ParseDocs(text)
	live := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "checkout", "namespace": "prod", "uid": "x"},
		"spec": map[string]any{
			"replicas": int64(2),
			"selector": map[string]any{"matchLabels": map[string]any{"app": "checkout"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "checkout"}},
				"spec": map[string]any{"containers": []any{map[string]any{
					"name": "checkout", "image": "registry.acme.io/checkout:2.14.1",
					"env": []any{map[string]any{"name": "CATALOG_CACHE_MAX", "value": "250000"}},
					"resources": map[string]any{
						"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
						"limits":   map[string]any{"cpu": "500m", "memory": "512Mi"},
					},
				}}},
			},
		},
	}
	got, changed := Project(lines, docs, []map[string]any{live})
	// The file has a number where the live object has a string.
	want := strings.Replace(strings.Replace(text, "memory: 256Mi", "memory: 512Mi", 1), "value: 250000", `value: "250000"`, 1)
	if got != want || changed != 4 {
		t.Fatalf("changed %d, got:\n%s", changed, got)
	}
	delete(live["spec"].(map[string]any), "replicas")
	got, _ = Project(lines, docs, []map[string]any{live})
	if strings.Contains(got, "replicas") {
		t.Fatalf("replicas should be gone:\n%s", got)
	}
	v, ok := liveAt(docs[0], live, []string{"spec", "template", "spec", "containers", "[0]", "resources", "limits", "memory"})
	if !ok || v != "512Mi" {
		t.Fatalf("liveAt: %v %v", v, ok)
	}
	same, _ := Project(lines, docs, []map[string]any{nil})
	if strings.Contains(same, "checkout") {
		t.Fatalf("a missing object should be removed:\n%s", same)
	}
}

func TestQuantityEqual(t *testing.T) {
	d := ParseDocs("resources:\n  limits:\n    cpu: 1000m\n")[0]
	n := dig(d.Root, "resources", "limits", "cpu")
	if !equalScalar(n, "1", []string{"resources", "limits", "cpu"}) {
		t.Fatal("1000m should equal 1")
	}
	if equalScalar(n, "2", []string{"resources", "limits", "cpu"}) {
		t.Fatal("1000m should not equal 2")
	}
}

func TestKustSummary(t *testing.T) {
	msg := "accumulating resources: accumulation err='accumulating resources from '../../apps/frontend/deployment.yaml': security; file 'apps/frontend/deployment.yaml' is not in or below 'overlays/prod'': must build at directory: 'apps/frontend/deployment.yaml': file is not directory"
	want := "file 'apps/frontend/deployment.yaml' is not in or below 'overlays/prod'. Reference a folder with a kustomization.yaml instead"
	if got := kustSummary(msg); got != want {
		t.Fatalf("got %q", got)
	}
	if got := kustSummary("accumulating resources: accumulation err='x': missing: the path ../foo does not exist"); got != "the path ../foo does not exist" {
		t.Fatalf("got %q", got)
	}
}
