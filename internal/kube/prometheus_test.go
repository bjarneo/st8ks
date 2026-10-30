package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestPromStep(t *testing.T) {
	cases := map[string]struct {
		step time.Duration
		n    int
	}{
		"1h":  {15 * time.Second, 240},
		"6h":  {90 * time.Second, 240},
		"24h": {6 * time.Minute, 240},
		"7d":  {42 * time.Minute, 240},
	}
	for rng, want := range cases {
		step, n := promStep(metricRanges[rng])
		if step != want.step || n != want.n {
			t.Errorf("%s: step %s, %d points; want %s, %d", rng, step, n, want.step, want.n)
		}
		if n > promMaxPoints || step%(15*time.Second) != 0 {
			t.Errorf("%s: %d points with step %s", rng, n, step)
		}
	}
	if step, n := promStep(10 * time.Minute); step != 15*time.Second || n != 40 {
		t.Errorf("10m: step %s, %d points", step, n)
	}
	if r, m := promWindows(15 * time.Second); r != 5*time.Minute || m != 0 {
		t.Errorf("windows for 15s: %s %s", r, m)
	}
	if r, m := promWindows(42 * time.Minute); r != 42*time.Minute || m != 42*time.Minute {
		t.Errorf("windows for 42m: %s %s", r, m)
	}
}

func TestControllerPodRegex(t *testing.T) {
	long := strings.Repeat("a", 50)
	cases := []struct {
		kind, name string
		match      []string
		miss       []string
	}{
		{"Deployments", "checkout",
			[]string{"checkout-7d9f8b6c4-x2k9p", "checkout-5-bcdfg", "checkout-6657759866-xpg26"},
			[]string{"checkout-x2k9p", "checkout-api-7d9f8b6c4-x2k9p", "checkout-7d9f8b6c4-x2k9", "other-7d9f8b6c4-x2k9p", "checkout-7d9f8b6c4-xak9p"}},
		{"Deployments", "api.v2",
			[]string{"api.v2-7d9f8b6c4-x2k9p"},
			[]string{"apixv2-7d9f8b6c4-x2k9p"}},
		// 50 + 1 leaves 7 characters before the 58 character cut: a short
		// hash stays whole, a long hash is cut to 7 characters.
		{"Deployments", long,
			[]string{long + "-7d9f8-x2k9p", long + "-7d9f8b6x2k9p"},
			[]string{long + "-7d9f8b6c4-x2k9p"}},
		{"StatefulSets", "redis",
			[]string{"redis-0", "redis-12"},
			[]string{"redis-headless-0", "redis-", "redis-a"}},
		{"DaemonSets", "kindnet",
			[]string{"kindnet-x2k9p"},
			[]string{"kindnet-7d9f8b6c4-x2k9p", "kindnet-x2k9", "kindnet-proxy-x2k9p"}},
		{"DaemonSets", strings.Repeat("b", 60),
			[]string{strings.Repeat("b", 58) + "x2k9p"},
			[]string{strings.Repeat("b", 60) + "-x2k9p"}},
	}
	for _, c := range cases {
		// PromQL anchors a regular expression at both ends.
		re := regexp.MustCompile("^(?:" + controllerPodRegex(c.kind, c.name) + ")$")
		for _, m := range c.match {
			if !re.MatchString(m) {
				t.Errorf("%s %s: %s does not match %s", c.kind, c.name, re, m)
			}
		}
		for _, m := range c.miss {
			if re.MatchString(m) {
				t.Errorf("%s %s: %s matches %s", c.kind, c.name, re, m)
			}
		}
	}
	if got := promRe("pod", controllerPodRegex("StatefulSets", "a.b")); got != `pod=~"a\\.b-[0-9]+"` {
		t.Errorf("escaped selector = %s", got)
	}
}

func TestParsePromResponse(t *testing.T) {
	matrix := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"container":"app"},"values":[[1700000000,"0.5"],[1700000015.000,"NaN"],[1700000030,"1e3"]]}]}}`
	res, err := parsePromResponse([]byte(matrix), nil)
	if err != nil || len(res) != 1 || len(res[0].Values) != 3 {
		t.Fatalf("matrix: %v %+v", err, res)
	}
	if p := res[0].Values[2]; p.T != 1700000030 || p.V != 1000 {
		t.Errorf("point = %+v", p)
	}
	if !math.IsNaN(res[0].Values[1].V) {
		t.Errorf("NaN point = %+v", res[0].Values[1])
	}

	vector := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000.5,"66899148.8"]}]}}`
	res, err = parsePromResponse([]byte(vector), nil)
	if err != nil || len(res) != 1 || res[0].Value == nil || res[0].Value.V != 66899148.8 {
		t.Fatalf("vector: %v %+v", err, res)
	}

	bad := `{"status":"error","errorType":"bad_data","error":"parse error at char 5"}`
	reqErr := fmt.Errorf("the server rejected our request")
	if _, err := parsePromResponse([]byte(bad), reqErr); err == nil || err.Error() != "the query failed: bad_data: parse error at char 5" {
		t.Errorf("error body: %v", err)
	}
	denied := `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"services \"prometheus-server:http\" is forbidden: User \"u\" cannot get resource \"services/proxy\" in API group \"\" in the namespace \"monitoring\"","reason":"Forbidden","details":{"name":"prometheus-server:http","kind":"services"},"code":403}`
	if _, err := parsePromResponse([]byte(denied), reqErr); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), `"services/proxy"`) {
		t.Errorf("API server status: %v", err)
	}
	if _, err := parsePromResponse([]byte("<html>no</html>"), reqErr); err != reqErr {
		t.Errorf("request error: %v", err)
	}
	if _, err := parsePromResponse([]byte("<html>no</html>"), nil); err == nil {
		t.Error("an HTML page is not an answer")
	}
}

func TestParsePromSetting(t *testing.T) {
	cases := map[string]string{
		"":                                "",
		" auto ":                          "",
		"monitoring/prometheus-server:80": "monitoring/prometheus-server:80",
		"monitoring/prometheus-server":    "monitoring/prometheus-server",
		"vm/vmselect-main:8481/select/0/prometheus/":         "vm/vmselect-main:8481/select/0/prometheus",
		"monitoring/kps-kube-prometheus-prometheus:http-web": "monitoring/kps-kube-prometheus-prometheus:http-web",
		"http://localhost:9090/":                             "http://localhost:9090",
		"https://prom.example.test/prometheus?x=1":           "https://prom.example.test/prometheus",
	}
	for in, want := range cases {
		tg, err := parsePromSetting(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		got := ""
		if tg != nil {
			got = tg.String()
		}
		if got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
	if tg, _ := parsePromSetting("vm/vmselect-main:8481/select/0/prometheus"); tg.Flavor != "VictoriaMetrics" || tg.Path != vmPrefix {
		t.Errorf("vmselect target = %+v", tg)
	}
	for _, in := range []string{"prometheus", "Monitoring/prom:80", "monitoring/prom:0", "monitoring/:80", "http://", "monitoring/prom:-x"} {
		if _, err := parsePromSetting(in); err == nil {
			t.Errorf("%q is accepted", in)
		}
	}
}

func svc(ns, name string, labels map[string]string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels}, Spec: corev1.ServiceSpec{Ports: ports}}
}

func port(name string, n int32) corev1.ServicePort {
	return corev1.ServicePort{Name: name, Port: n, TargetPort: intstr.FromInt32(n)}
}

func TestPickPrometheus(t *testing.T) {
	noise := []*corev1.Service{
		svc("default", "kubernetes", nil, port("https", 443)),
		svc("monitoring", "kps-kube-prometheus-alertmanager", map[string]string{"app": "kube-prometheus-stack-alertmanager"}, port("http-web", 9093)),
		svc("monitoring", "kps-kube-prometheus-operator", map[string]string{"app": "kube-prometheus-stack-operator"}, port("https", 443)),
		svc("monitoring", "prometheus-node-exporter", map[string]string{"app.kubernetes.io/name": "prometheus-node-exporter"}, port("metrics", 9100)),
		svc("monitoring", "prometheus-pushgateway", map[string]string{"app.kubernetes.io/name": "prometheus-pushgateway"}, port("http", 9091)),
	}
	cases := []struct {
		name string
		svcs []*corev1.Service
		want string
	}{
		{"none", noise, ""},
		{"kube-prometheus-stack", append(noise,
			svc("monitoring", "prometheus-operated", nil, port("web", 9090)),
			svc("monitoring", "kps-kube-prometheus-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, port("http-web", 9090), port("reloader-web", 8080))),
			"monitoring/kps-kube-prometheus-prometheus:http-web"},
		{"operator only", append(noise, svc("monitoring", "prometheus-operated", nil, port("web", 9090))), "monitoring/prometheus-operated:web"},
		{"prometheus-community", append(noise,
			svc("monitoring", "prometheus-server", map[string]string{"app.kubernetes.io/name": "prometheus", "app.kubernetes.io/component": "server"}, port("http", 80))),
			"monitoring/prometheus-server:http"},
		{"label only", []*corev1.Service{svc("obs", "metrics-db", map[string]string{"app.kubernetes.io/name": "prometheus", "app.kubernetes.io/component": "server"}, port("", 9090))},
			"obs/metrics-db:9090"},
		{"vmselect", []*corev1.Service{svc("vm", "vmselect-main", map[string]string{"app.kubernetes.io/name": "vmselect"}, port("http", 8481))},
			"vm/vmselect-main:http/select/0/prometheus"},
		{"vmsingle beats a plain prometheus label", []*corev1.Service{
			svc("a", "prometheus", nil, port("", 9090)),
			svc("vm", "vm-victoria-metrics-single-server", map[string]string{"app.kubernetes.io/name": "victoria-metrics-single"}, port("http", 8428))},
			"vm/vm-victoria-metrics-single-server:http"},
		{"monitoring namespace first", []*corev1.Service{
			svc("apps", "prometheus-server", nil, port("http", 80)),
			svc("monitoring", "prometheus-server", nil, port("http", 80))},
			"monitoring/prometheus-server:http"},
		{"port by number", []*corev1.Service{svc("m", "prometheus-k8s", nil, port("metrics", 8080), port("", 9090))}, "m/prometheus-k8s:9090"},
	}
	for _, c := range cases {
		got := ""
		if tg := pickPrometheus(c.svcs); tg != nil {
			got = tg.String()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMetricValuesJSON(t *testing.T) {
	b, err := json.Marshal(MetricValues{0.123456789, math.NaN(), 67108864, 0})
	if err != nil || string(b) != "[0.123457,null,6.71089e+07,0]" {
		t.Errorf("json = %s %v", b, err)
	}
	if s := fmtBytesF(66899148.8); s != "63.8Mi" {
		t.Errorf("fmtBytesF = %s", s)
	}
	if s := fmtBytesF(64 << 20); s != "64Mi" {
		t.Errorf("fmtBytesF = %s", s)
	}
}

func TestBuildPanel(t *testing.T) {
	pts := func(start int64, vals ...float64) []promPoint {
		out := make([]promPoint, len(vals))
		for i, v := range vals {
			out[i] = promPoint{T: float64(start + int64(i)*15), V: v}
		}
		return out
	}
	var res []promResult
	for i := range 9 {
		res = append(res, promResult{Metric: map[string]string{"pod": fmt.Sprintf("web-%d", i)}, Values: pts(1015, float64(i), float64(i))})
	}
	p := buildPanel(res, "pod", nil, 1000, 15, 4)
	if p.More != 2 || len(p.Parts) != promMaxParts {
		t.Fatalf("more %d, parts %d", p.More, len(p.Parts))
	}
	if p.Parts[0].Name != "web-2" || p.Parts[6].Name != "web-8" {
		t.Errorf("kept parts %s .. %s", p.Parts[0].Name, p.Parts[6].Name)
	}
	if !math.IsNaN(p.Total.V[0]) || p.Total.V[1] != 36 || p.Total.V[2] != 36 || !math.IsNaN(p.Total.V[3]) {
		t.Errorf("total = %v", p.Total.V)
	}
	order := buildPanel([]promResult{
		{Metric: map[string]string{"container": "b"}, Values: pts(1000, 1)},
		{Metric: map[string]string{"container": "sidecar"}, Values: pts(1000, 1)},
		{Metric: map[string]string{"container": "a"}, Values: pts(1000, 1)},
	}, "container", []string{"sidecar", "a"}, 1000, 15, 1)
	if order.Parts[0].Name != "sidecar" || order.Parts[1].Name != "a" || order.Parts[2].Name != "b" {
		t.Errorf("order = %s %s %s", order.Parts[0].Name, order.Parts[1].Name, order.Parts[2].Name)
	}
	if hasValue(buildPanel(nil, "pod", nil, 1000, 15, 4)) {
		t.Error("an empty panel has a value")
	}
}

// TestMetricsURL runs the whole path against a fake Prometheus at a direct
// URL.
func TestMetricsURL(t *testing.T) {
	// The CPU and memory queries run at the same time, so the fake server
	// guards its state.
	var mu sync.Mutex
	var queries []string
	mode := "ok"
	setMode := func(m string) {
		mu.Lock()
		mode = m
		mu.Unlock()
	}
	taken := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := queries
		queries = nil
		return out
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		queries = append(queries, q.Get("query"))
		mode := mode
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case mode == "error":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid parameter"}`))
		case mode == "empty":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		case r.URL.Path == "/prom/api/v1/query":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"66899148.8"]}]}}`))
		default:
			start := q.Get("start")
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"container":"app"},"values":[[%s,"0.25"]]}]}}`, start)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Cluster{ctx: ctx}
	c.prom.setting = func() string { return srv.URL + "/prom/" }

	d, err := c.Metrics(Ref{Kind: "Pods", NS: "prod", Name: "checkout-1"}, "1h")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != MetricsOK || d.Source.Mode != "url" || d.N != 240 || d.Step != 15 || d.Parts != "container" {
		t.Fatalf("data = %+v", d)
	}
	if d.CPU.Parts[0].Name != "app" || d.CPU.Parts[0].V[0] != 0.25 || d.CPU.Total.V[0] != 0.25 {
		t.Errorf("cpu = %+v", d.CPU)
	}
	got := taken()
	if len(got) != 2 {
		t.Fatalf("queries = %q", got)
	}
	for _, q := range got {
		if !strings.Contains(q, `namespace="prod",pod="checkout-1",container!="",container!="POD",image!=""`) || !strings.HasPrefix(q, "sum by (container) (") {
			t.Errorf("query = %s", q)
		}
	}

	if _, err := c.Metrics(Ref{Kind: "StatefulSets", NS: "data", Name: "redis"}, "7d"); err != nil {
		t.Fatal(err)
	}
	got = taken()
	for _, q := range got {
		if !strings.Contains(q, `pod=~"redis-[0-9]+"`) || !strings.HasPrefix(q, "sum by (pod) (") {
			t.Errorf("query = %s", q)
		}
	}
	if !strings.Contains(strings.Join(got, "\n"), "max_over_time(container_memory_working_set_bytes") {
		t.Errorf("a 7d memory query uses raw samples: %q", got)
	}

	issue := &Issue{ID: "crash:prod/Deployments/checkout/app", Kind: "Pods", NS: "prod", Obj: "checkout-1", Reason: "CrashLoopBackOff · OOMKilled"}
	if e := c.oomEvidence(issue); e != "Memory working set peaked at 63.8Mi in the last hour" {
		t.Errorf("evidence = %q", e)
	}
	if q := taken(); len(q) != 1 || !strings.Contains(q[0], `pod=~"checkout-1",container="app"`) {
		t.Errorf("evidence query = %q", q)
	}
	issue.Reason = "CrashLoopBackOff · Error"
	if e := c.oomEvidence(issue); e != "" {
		t.Errorf("evidence for a crash without OOM = %q", e)
	}

	setMode("empty")
	if d, _ := c.Metrics(Ref{Kind: "Nodes", Name: "worker"}, "6h"); d.Status != MetricsNoData || !strings.Contains(d.Message, "Node history is not available") {
		t.Errorf("empty = %+v", d)
	}
	setMode("error")
	if d, _ := c.Metrics(Ref{Kind: "Pods", NS: "prod", Name: "x"}, "24h"); d.Status != MetricsError || !strings.Contains(d.Message, "bad_data: invalid parameter") {
		t.Errorf("error = %+v", d)
	}
	if _, err := c.Metrics(Ref{Kind: "Services", NS: "prod", Name: "x"}, "1h"); err == nil {
		t.Error("services have no metrics")
	}
	if _, err := c.Metrics(Ref{Kind: "Pods", NS: "prod", Name: "x"}, "2h"); err == nil {
		t.Error("2h is not a range")
	}

	c.prom.setting = func() string { return "not a source" }
	if d, _ := c.Metrics(Ref{Kind: "Pods", NS: "prod", Name: "x"}, "1h"); d.Status != MetricsError || !strings.Contains(d.Message, "Settings > Metrics") {
		t.Errorf("bad setting = %+v", d)
	}
}
