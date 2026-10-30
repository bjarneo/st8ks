package mcpserver

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"st8ks/internal/kube"
)

// fakeCluster answers with fixed data. Its Secret holds values that must
// never reach a client.
type fakeCluster struct {
	applied []bool
}

const secretValue = "hunter2-very-secret"

var secretYAML = `apiVersion: v1
kind: Secret
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"data":{"password":"` + base64.StdEncoding.EncodeToString([]byte(secretValue)) + `"}}'
  name: db
  namespace: shop
type: Opaque
data:
  password: ` + base64.StdEncoding.EncodeToString([]byte(secretValue)) + `
  token: ` + base64.StdEncoding.EncodeToString([]byte("abc")) + `
stringData:
  plain: ` + secretValue + `
`

func (f *fakeCluster) ClusterContext() string { return "Context: kind-test\nOpen issues: none\n" }
func (f *fakeCluster) Issues() []kube.Issue {
	return []kube.Issue{{ID: "crash/shop/web", Sev: "er", Title: "web is crash looping", Kind: "Pods", NS: "shop", Obj: "web-1",
		Reason: "CrashLoopBackOff", Summary: "The container exits with code 1.", Cmds: []string{"kubectl describe pod web-1 -n shop"}}}
}
func (f *fakeCluster) IssueContext(id string) (string, []string, error) {
	if id != "crash/shop/web" {
		return "", nil, errors.New("the issue is resolved or no longer exists")
	}
	return "Issue: web is crash looping\n", []string{"kubectl logs web-1 -n shop --previous --tail 80"}, nil
}
func (f *fakeCluster) ResolveKind(name string) (kube.KindInfo, bool) {
	switch strings.ToLower(name) {
	case "secret", "secrets":
		return kube.KindInfo{Name: "Secrets", Kind: "Secret", Resource: "secrets", Namespaced: true}, true
	case "pod", "pods":
		return kube.KindInfo{Name: "Pods", Kind: "Pod", Resource: "pods", Namespaced: true}, true
	case "deploy":
		return kube.KindInfo{Name: "Deployments", Kind: "Deployment", Group: "apps", Resource: "deployments", Namespaced: true}, true
	}
	return kube.KindInfo{}, false
}
func (f *fakeCluster) TableText(_ context.Context, kind, ns, filter string, limit int) (kube.TableView, error) {
	return kube.TableView{Kind: kind, Columns: []string{"Name", "Namespace"}, Rows: [][]string{{"web-1", "shop"}}, Total: 1}, nil
}
func (f *fakeCluster) GetObject(ref kube.Ref) (*kube.ObjectDoc, error) {
	if ref.Kind == "Secrets" {
		return &kube.ObjectDoc{Kind: "Secrets", NS: ref.NS, Name: ref.Name, API: "v1", YAML: secretYAML,
			Events: []kube.EventItem{{Type: "Normal", Reason: "Synced", Msg: "ok", Count: 1, Last: 1}}}, nil
	}
	return &kube.ObjectDoc{Kind: ref.Kind, NS: ref.NS, Name: ref.Name, API: "v1", YAML: "apiVersion: v1\nkind: Pod\nmetadata:\n  name: " + ref.Name + "\n"}, nil
}
func (f *fakeCluster) TailLogs(ns, pod, container string, previous bool, lines int64) (string, string, error) {
	return "app", strings.Repeat("line\n", int(lines)), nil
}
func (f *fakeCluster) RbacExplain(sub kube.Subject, ns, res, verb string) kube.RbacAnswer {
	return kube.RbacAnswer{Allowed: true, Why: sub.Kind + " " + sub.NS + "/" + sub.Name + " " + verb + " " + res + " in " + ns}
}
func (f *fakeCluster) Topology(ns string) kube.Topology {
	return kube.Topology{NS: ns, Cols: []string{"Ingress", "Service", "Pod"},
		Nodes: []kube.TopoNode{{ID: "svc/web", Type: "Service", Name: "web", Col: 1}, {ID: "pod/web-1", Type: "Pod", Name: "web-1", Sub: "Error", Tone: "e", Col: 2}},
		Edges: []kube.TopoEdge{{A: "svc/web", B: "pod/web-1", T: "r"}}}
}
func (f *fakeCluster) HelmReleases() ([]kube.HelmRelease, error) {
	return []kube.HelmRelease{{Name: "web", NS: "shop", Chart: "web-1.0.0", App: "1.0", Rev: 3, Status: "failed", Updated: 1700000000}}, nil
}
func (f *fakeCluster) Apply(yaml string, dry bool) (string, error) {
	f.applied = append(f.applied, dry)
	return "deployment.apps/web configured (server dry run)", nil
}

func connect(t *testing.T, fc *fakeCluster) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := New(func(context.Context) (Cluster, string, error) { return fc, "", nil }, Info{Version: "test", Context: "kind-test"})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestToolList(t *testing.T) {
	cs := connect(t, &fakeCluster{})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cluster_overview", "dry_run_apply", "explain_issue", "get_logs", "get_resource", "helm_releases",
		"list_issues", "list_resources", "rbac_check", "topology"}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if tool.Description == "" || tool.Title == "" {
			t.Errorf("%s has no description or title", tool.Name)
		}
		a := tool.Annotations
		if a == nil || !a.ReadOnlyHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s is not marked read-only and closed: %+v", tool.Name, a)
		}
		if schema, ok := tool.InputSchema.(map[string]any); !ok || schema["type"] != "object" {
			t.Errorf("%s input schema = %v", tool.Name, tool.InputSchema)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for _, tool := range res.Tools {
		if tool.Name != "get_resource" {
			continue
		}
		req, _ := tool.InputSchema.(map[string]any)["required"].([]any)
		if !slices.Contains(req, any("kind")) || !slices.Contains(req, any("name")) || slices.Contains(req, any("namespace")) {
			t.Errorf("get_resource required = %v", req)
		}
	}
}

func TestSecretRedaction(t *testing.T) {
	cs := connect(t, &fakeCluster{})
	out, isErr := callText(t, cs, "get_resource", map[string]any{"kind": "secret", "namespace": "shop", "name": "db"})
	if isErr {
		t.Fatalf("get_resource failed: %s", out)
	}
	enc := base64.StdEncoding.EncodeToString([]byte(secretValue))
	if strings.Contains(out, secretValue) || strings.Contains(out, enc) {
		t.Fatalf("the result holds a Secret value:\n%s", out)
	}
	for _, want := range []string{"password: '<redacted: 19 bytes>'", "token: '<redacted: 3 bytes>'", "plain: '<redacted: 19 bytes>'", "name: db", "type: Opaque", "Synced"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result has no %q:\n%s", want, out)
		}
	}
}

func TestRedactSecretsList(t *testing.T) {
	in := "apiVersion: v1\nkind: List\nitems:\n- kind: Secret\n  data:\n    k: " + base64.StdEncoding.EncodeToString([]byte(secretValue)) + "\n- kind: ConfigMap\n  data:\n    k: visible\n"
	out, err := RedactSecrets(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, base64.StdEncoding.EncodeToString([]byte(secretValue))) || !strings.Contains(out, "visible") {
		t.Fatalf("redacted list:\n%s", out)
	}
	pod := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: x\n"
	if got, _ := RedactSecrets(pod); got != pod {
		t.Fatalf("a Pod changed:\n%s", got)
	}
	if _, err := RedactSecrets("kind: Secret\ndata: [unclosed"); err == nil {
		t.Fatal("YAML that does not parse passed")
	}
}

func TestTools(t *testing.T) {
	fc := &fakeCluster{}
	cs := connect(t, fc)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"cluster_overview", nil, "Context: kind-test"},
		{"list_issues", nil, `"severity":"error"`},
		{"explain_issue", map[string]any{"id": "crash/shop/web"}, "$ kubectl logs web-1 -n shop --previous --tail 80"},
		{"list_resources", map[string]any{"kind": "pod"}, `"kind":"Pods"`},
		{"get_logs", map[string]any{"namespace": "shop", "pod": "web-1", "tail": 9000}, "Last 500 lines"},
		{"rbac_check", map[string]any{"subject_kind": "user", "subject_name": "system:serviceaccount:shop:web", "verb": "get", "resource": "deploy"}, "ServiceAccount shop/web get deployments.apps in default"},
		{"topology", map[string]any{"namespace": "shop"}, "Service web routes to Pod web-1"},
		{"helm_releases", nil, `"updated":"2023-11-14T22:13:20Z"`},
		{"dry_run_apply", map[string]any{"yaml": "kind: Deployment"}, "server dry run"},
	}
	for _, tc := range cases {
		out, isErr := callText(t, cs, tc.name, tc.args)
		if isErr || !strings.Contains(out, tc.want) {
			t.Errorf("%s: error=%t, want %q in:\n%s", tc.name, isErr, tc.want, out)
		}
	}
	if !slices.Equal(fc.applied, []bool{true}) {
		t.Errorf("Apply calls = %v, want one dry run", fc.applied)
	}
	if out, isErr := callText(t, cs, "explain_issue", map[string]any{"id": "gone"}); !isErr || !strings.Contains(out, "list_issues") {
		t.Errorf("explain_issue of an unknown id: error=%t %s", isErr, out)
	}
	if out, isErr := callText(t, cs, "list_resources", map[string]any{"kind": "nope"}); !isErr || !strings.Contains(out, "unknown kind") {
		t.Errorf("list_resources of an unknown kind: error=%t %s", isErr, out)
	}
	if out, isErr := callText(t, cs, "get_resource", map[string]any{"kind": "pod", "name": "x"}); !isErr || !strings.Contains(out, "namespace") {
		t.Errorf("get_resource without a namespace: error=%t %s", isErr, out)
	}
}

func TestSourceError(t *testing.T) {
	srv := New(func(context.Context) (Cluster, string, error) { return nil, "", errors.New("context x not found") }, Info{Version: "test"})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if out, isErr := callText(t, cs, "cluster_overview", nil); !isErr || !strings.Contains(out, "context x not found") {
		t.Fatalf("error=%t %s", isErr, out)
	}
}
