//go:build integration

// Run against a disposable cluster:
//
//	ST8KS_IT_KUBECONFIG=~/.kube/st8ks-dev.yaml go test -count=1 -tags integration -run TestIntegrationMCP -v ./internal/mcpserver/
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const itSecret = "it-secret-value-0123456789"

// seed creates a namespace of its own with a Secret, a ConfigMap, a Service,
// a pod and a Role for one service account.
func seed(t *testing.T, cs kubernetes.Interface, ns string) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
	must(err)
	t.Cleanup(func() {
		_ = cs.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	_, err = cs.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db"},
		Data: map[string][]byte{"password": []byte(itSecret)}}, metav1.CreateOptions{})
	must(err)
	_, err = cs.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app"},
		Data: map[string]string{"mode": "a"}}, metav1.CreateOptions{})
	must(err)
	_, err = cs.CoreV1().Services(ns).Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}, Ports: []corev1.ServicePort{{Port: 80}}}}, metav1.CreateOptions{})
	must(err)
	_, err = cs.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web", Labels: map[string]string{"app": "web"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox:1.36", ImagePullPolicy: corev1.PullIfNotPresent,
			Command:      []string{"sh", "-c", "echo mcp-it-line; sleep 3600"},
			VolumeMounts: []corev1.VolumeMount{{Name: "db", MountPath: "/db"}}}},
			Volumes: []corev1.Volume{{Name: "db", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db"}}}}}},
		metav1.CreateOptions{})
	must(err)
	_, err = cs.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "reader"}}, metav1.CreateOptions{})
	must(err)
	_, err = cs.RbacV1().Roles(ns).Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "pod-reader"},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list"}}}}, metav1.CreateOptions{})
	must(err)
	_, err = cs.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "reader"},
		RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "pod-reader"},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "reader", Namespace: ns}}}, metav1.CreateOptions{})
	must(err)
}

func TestIntegrationMCP(t *testing.T) {
	kc := os.Getenv("ST8KS_IT_KUBECONFIG")
	if kc == "" {
		t.Skip("set ST8KS_IT_KUBECONFIG")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	kcs := kubernetes.NewForConfigOrDie(cfg)
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	ns := "st8ks-mcp-it-" + hex.EncodeToString(b)
	seed(t, kcs, ns)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- Serve(ctx, Options{Kubeconfigs: []string{kc}, Context: "kind-st8ks-dev", Version: "it"}, sr, sw)
	}()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "it", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("handshake after %v", time.Since(start).Round(time.Millisecond))
	if ins := cs.InitializeResult().Instructions; !strings.Contains(ins, "kind-st8ks-dev") {
		t.Errorf("instructions = %q", ins)
	}

	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		if res.IsError {
			t.Fatalf("%s %v failed: %s", name, args, sb.String())
		}
		return sb.String()
	}
	expect := func(name, out string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: no %q in:\n%s", name, w, clipText(out))
			}
		}
	}

	out := call("cluster_overview", nil)
	t.Logf("first answer after %v", time.Since(start).Round(time.Millisecond))
	expect("cluster_overview", out, "Context: kind-st8ks-dev", "Nodes: 3")

	var issues IssueList
	if err := json.Unmarshal([]byte(call("list_issues", nil)), &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues.Issues) == 0 {
		t.Fatal("list_issues found no issues. The seed workloads of hack/dev-cluster fail on purpose")
	}
	t.Logf("%d issues, first %s", len(issues.Issues), issues.Issues[0].ID)
	expect("explain_issue", call("explain_issue", map[string]any{"id": issues.Issues[0].ID}), "Findings from the rule engine", "$ kubectl")

	var tv struct {
		Kind    string     `json:"kind"`
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(call("list_resources", map[string]any{"kind": "secret", "namespace": ns})), &tv); err != nil {
		t.Fatal(err)
	}
	if tv.Kind != "Secrets" || len(tv.Rows) != 1 || tv.Rows[0][0] != "db" || !strings.Contains(strings.Join(tv.Columns, ","), "Type") {
		t.Errorf("list_resources secret = %+v", tv)
	}
	expect("list_resources deploy", call("list_resources", map[string]any{"kind": "deployments.apps", "namespace": "prod"}), `"Ready"`, "checkout")
	expect("list_resources po", call("list_resources", map[string]any{"kind": "po", "filter": "app=web", "namespace": ns}), `"web"`)

	sec := call("get_resource", map[string]any{"kind": "Secrets", "namespace": ns, "name": "db"})
	if strings.Contains(sec, itSecret) || strings.Contains(sec, "aXQtc2VjcmV0") {
		t.Fatalf("get_resource returned the Secret value:\n%s", sec)
	}
	expect("get_resource secret", sec, "password: '<redacted: 26 bytes>'", "name: db")
	expect("get_resource pod", call("get_resource", map[string]any{"kind": "pod", "namespace": ns, "name": "web"}), "kind: Pod", "--- Events")

	var pods struct {
		Rows [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(call("list_resources", map[string]any{"kind": "pods", "namespace": "kube-system", "filter": "coredns"})), &pods); err != nil || len(pods.Rows) == 0 {
		t.Fatalf("no coredns pod: %v", err)
	}
	logs := call("get_logs", map[string]any{"namespace": "kube-system", "pod": pods.Rows[0][0], "tail": 3})
	expect("get_logs", logs, "Last 3 lines", "container coredns")

	var rb RbacResult
	_ = json.Unmarshal([]byte(call("rbac_check", map[string]any{"subject_kind": "ServiceAccount", "subject_name": "reader", "subject_namespace": ns,
		"verb": "list", "resource": "pods", "namespace": ns})), &rb)
	if !rb.Allowed || !strings.Contains(rb.Why, "pod-reader") || !strings.Contains(rb.APIServer, "agrees") {
		t.Errorf("rbac_check list pods = %+v", rb)
	}
	rb = RbacResult{}
	_ = json.Unmarshal([]byte(call("rbac_check", map[string]any{"subject_kind": "sa", "subject_name": "system:serviceaccount:" + ns + ":reader",
		"verb": "delete", "resource": "deploy", "namespace": ns})), &rb)
	if rb.Allowed || !strings.Contains(rb.Why, "deployments") || !strings.Contains(rb.APIServer, "agrees") {
		t.Errorf("rbac_check delete deployments = %+v", rb)
	}

	expect("topology", call("topology", map[string]any{"namespace": ns}), "Service web, ClusterIP :80", "Service web routes to Pod web")
	expect("topology prod", call("topology", map[string]any{"namespace": "prod"}), "Ingress shop", "is owned by Deployment checkout")

	expect("helm_releases", call("helm_releases", map[string]any{"namespace": "prod"}), `"name":"web"`)

	cm, err := kcs.CoreV1().ConfigMaps(ns).Get(ctx, "app", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	draft := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: " + ns + "\ndata:\n  mode: b\n"
	expect("dry_run_apply", call("dry_run_apply", map[string]any{"yaml": draft}), "configmap/app configured (server dry run)")
	after, err := kcs.CoreV1().ConfigMaps(ns).Get(ctx, "app", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Data["mode"] != "a" || after.ResourceVersion != cm.ResourceVersion {
		t.Errorf("dry_run_apply changed the ConfigMap: %v %s -> %s", after.Data, cm.ResourceVersion, after.ResourceVersion)
	}

	_ = cs.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("Serve did not return after the client closed the connection")
	}
}

func clipText(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return s
}
