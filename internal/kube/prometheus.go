package kube

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

// The metrics history comes from Prometheus or VictoriaMetrics. Queries go
// through the service proxy of the API server, so they use the credentials
// of the kubeconfig and need no port-forward. Nothing polls in the
// background. The frontend asks while the Metrics tab shows.

const (
	promTimeout   = 10 * time.Second
	promMaxPoints = 240
	promMaxParts  = 7
	// promRetry is how long a failed discovery stays cached.
	promRetry = 30 * time.Second
	promBody  = 32 << 20
	vmPrefix  = "/select/0/prometheus"
)

// Metrics statuses.
const (
	MetricsOK        = "ok"
	MetricsNone      = "none"
	MetricsForbidden = "forbidden"
	MetricsNoData    = "nodata"
	MetricsError     = "error"
)

// metricRanges are the ranges of the Metrics tab.
var metricRanges = map[string]time.Duration{
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// promSearched tells the user what the discovery looks for.
var promSearched = []string{
	"Services prometheus-operated, *-kube-prometheus-prometheus and prometheus-k8s of kube-prometheus-stack and kube-prometheus",
	"Service prometheus-server of the prometheus-community chart",
	"Services vmsingle-*, vmselect-* and victoria-metrics-single-server of VictoriaMetrics",
	"Services with the label app.kubernetes.io/name set to prometheus, vmsingle or vmselect",
}

// promNamespaces are the namespaces that discovery lists when the
// credentials cannot list Services in all namespaces.
var promNamespaces = []string{"monitoring", "prometheus", "observability", "victoria-metrics", "vm", "kube-prometheus", "openshift-monitoring"}

var promHTTP = &http.Client{Timeout: promTimeout}

// promTarget is a Prometheus API: a Service behind the API server proxy, or
// a direct URL.
type promTarget struct {
	NS, Name, Port, Path string
	URL                  string
	Flavor               string
	scheme               string
}

func (t *promTarget) String() string {
	if t.URL != "" {
		return t.URL
	}
	s := t.NS + "/" + t.Name
	if t.Port != "" {
		s += ":" + t.Port
	}
	return s + t.Path
}

type promState struct {
	mu      sync.Mutex
	setting func() string
	key     string
	done    bool
	target  *promTarget
	src     PromSource
	at      time.Time
}

// SetMetricsSource sets the function that returns the metrics source
// setting. An empty setting finds Prometheus in the cluster.
func (m *Manager) SetMetricsSource(fn func() string) {
	m.mu.Lock()
	m.promSetting = fn
	cur := m.cur
	m.mu.Unlock()
	if cur != nil {
		cur.prom.mu.Lock()
		cur.prom.setting = fn
		cur.prom.mu.Unlock()
	}
}

// PromSource describes the Prometheus that st8ks uses for a cluster.
type PromSource struct {
	Status string `json:"status"`
	// Mode is auto, service or url.
	Mode   string `json:"mode"`
	Flavor string `json:"flavor,omitempty"`
	Target string `json:"target,omitempty"`
	// Via is proxy for the API server service proxy, or direct.
	Via      string   `json:"via,omitempty"`
	Message  string   `json:"message,omitempty"`
	Missing  string   `json:"missing,omitempty"`
	Searched []string `json:"searched,omitempty"`
}

// MetricLine is a horizontal line on a chart, such as a request or a limit.
type MetricLine struct {
	Kind  string  `json:"kind"`
	Label string  `json:"label"`
	V     float64 `json:"v"`
}

// MetricValues holds one value per step. A missing value is NaN and
// encodes as null.
type MetricValues []float64

// MarshalJSON writes the values with 6 significant digits.
func (v MetricValues) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, len(v)*8+2)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			b = append(b, "null"...)
			continue
		}
		b = strconv.AppendFloat(b, f, 'g', 6, 64)
	}
	return append(b, ']'), nil
}

// MetricSeries is one line of a chart.
type MetricSeries struct {
	Name  string       `json:"name"`
	V     MetricValues `json:"v"`
	Lines []MetricLine `json:"lines,omitempty"`
}

// MetricPanel is the CPU chart or the memory chart.
type MetricPanel struct {
	Total *MetricSeries  `json:"total,omitempty"`
	Parts []MetricSeries `json:"parts"`
	// More counts the parts that were left out. Total includes them.
	More int `json:"more"`
}

// MetricsData is the history of one object.
type MetricsData struct {
	Status  string     `json:"status"`
	Message string     `json:"message,omitempty"`
	Source  PromSource `json:"source"`
	Range   string     `json:"range"`
	Start   int64      `json:"start"`
	Step    int64      `json:"step"`
	N       int        `json:"n"`
	// Parts is container or pod: what the parts of a panel are.
	Parts string      `json:"parts,omitempty"`
	CPU   MetricPanel `json:"cpu"`
	Mem   MetricPanel `json:"mem"`
}

// ---- Settings and discovery ----

// parsePromSetting reads the metrics source setting. An empty setting
// returns nil, which means auto discovery.
func parsePromSetting(s string) (*promTarget, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "auto") {
		return nil, nil
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("the metrics source %q is not a valid URL", s)
		}
		u.RawQuery, u.Fragment = "", ""
		return &promTarget{URL: strings.TrimRight(u.String(), "/"), Flavor: "Prometheus"}, nil
	}
	bad := fmt.Errorf("the metrics source %q is not a URL or a service reference such as monitoring/prometheus-server:80", s)
	ns, rest, ok := strings.Cut(s, "/")
	if !ok {
		return nil, bad
	}
	path := ""
	if i := strings.Index(rest, "/"); i >= 0 {
		rest, path = rest[:i], strings.TrimRight(rest[i:], "/")
	}
	name, port, _ := strings.Cut(rest, ":")
	if len(validation.IsDNS1123Label(ns)) > 0 || len(validation.IsDNS1123Label(name)) > 0 {
		return nil, bad
	}
	if port != "" && len(validation.IsValidPortName(port)) > 0 {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, bad
		}
	}
	t := &promTarget{NS: ns, Name: name, Port: port, Path: path, Flavor: "Prometheus"}
	if path == vmPrefix || strings.HasPrefix(name, "vm") {
		t.Flavor = "VictoriaMetrics"
	}
	if strings.HasPrefix(port, "https") {
		t.scheme = "https"
	}
	return t, nil
}

var promExclude = []string{"alertmanager", "node-exporter", "pushgateway", "operator", "kube-state-metrics", "blackbox", "adapter", "grafana", "thanos", "vmagent", "vminsert", "vmalert", "vmauth"}

// promScore rates a Service as a Prometheus API. 0 means no match.
func promScore(s *corev1.Service) (score int, flavor, path string) {
	if s.Spec.Type == corev1.ServiceTypeExternalName || len(s.Spec.Ports) == 0 {
		return 0, "", ""
	}
	name := s.Name
	lbl := s.Labels["app.kubernetes.io/name"]
	app := s.Labels["app"]
	comp := s.Labels["app.kubernetes.io/component"]
	for _, x := range promExclude {
		if strings.Contains(name, x) || strings.Contains(lbl, x) || strings.Contains(app, x) {
			return 0, "", ""
		}
	}
	switch {
	case strings.HasSuffix(name, "-kube-prometheus-prometheus") || app == "kube-prometheus-stack-prometheus":
		return 100, "Prometheus", ""
	case name == "prometheus-k8s":
		return 95, "Prometheus", ""
	case name == "prometheus-server" || strings.HasSuffix(name, "-prometheus-server") || (lbl == "prometheus" && comp == "server"):
		return 95, "Prometheus", ""
	case name == "prometheus-operated":
		return 90, "Prometheus", ""
	case strings.HasPrefix(name, "vmsingle-") || lbl == "vmsingle" ||
		strings.HasSuffix(name, "victoria-metrics-single-server") || lbl == "victoria-metrics-single":
		return 90, "VictoriaMetrics", ""
	case strings.HasPrefix(name, "vmselect-") || strings.HasSuffix(name, "-vmselect") || lbl == "vmselect" || app == "vmselect":
		return 85, "VictoriaMetrics", vmPrefix
	case lbl == "prometheus" || app == "prometheus" || name == "prometheus":
		return 60, "Prometheus", ""
	}
	return 0, "", ""
}

// promPort picks the API port of a Service: a known name first, then a
// known number, then the only port.
func promPort(s *corev1.Service) (port, scheme string) {
	ref := func(p corev1.ServicePort) (string, string) {
		sc := ""
		if strings.HasPrefix(p.Name, "https") {
			sc = "https"
		}
		if p.Name != "" {
			return p.Name, sc
		}
		return strconv.Itoa(int(p.Port)), sc
	}
	for _, n := range []string{"web", "http-web", "http", "https-web", "https"} {
		for _, p := range s.Spec.Ports {
			if p.Name == n {
				return ref(p)
			}
		}
	}
	for _, n := range []int32{9090, 8428, 8481, 80} {
		for _, p := range s.Spec.Ports {
			if p.Port == n || p.TargetPort.IntValue() == int(n) {
				return ref(p)
			}
		}
	}
	if len(s.Spec.Ports) == 1 {
		return ref(s.Spec.Ports[0])
	}
	return "", ""
}

// pickPrometheus returns the best Prometheus API among the Services.
func pickPrometheus(svcs []*corev1.Service) *promTarget {
	type cand struct {
		t     *promTarget
		score int
	}
	var cs []cand
	for _, s := range svcs {
		score, flavor, path := promScore(s)
		if score == 0 {
			continue
		}
		port, scheme := promPort(s)
		if port == "" {
			continue
		}
		cs = append(cs, cand{&promTarget{NS: s.Namespace, Name: s.Name, Port: port, Path: path, Flavor: flavor, scheme: scheme}, score})
	}
	if len(cs) == 0 {
		return nil
	}
	sort.Slice(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if (a.t.NS == "monitoring") != (b.t.NS == "monitoring") {
			return a.t.NS == "monitoring"
		}
		if a.t.NS != b.t.NS {
			return a.t.NS < b.t.NS
		}
		return a.t.Name < b.t.Name
	})
	return cs[0].t
}

// discoveryServices returns the Services to search. It uses the watch
// cache when it holds all namespaces.
func (c *Cluster) discoveryServices(ctx context.Context) ([]*corev1.Service, bool) {
	c.mu.RLock()
	s := c.sources["Services"]
	c.mu.RUnlock()
	if s != nil && !s.meta && s.ns == "" && s.inf.HasSynced() {
		var out []*corev1.Service
		for _, o := range s.inf.GetStore().List() {
			if svc, ok := o.(*corev1.Service); ok {
				out = append(out, svc)
			}
		}
		return out, true
	}
	l, err := c.cs.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err == nil {
		out := make([]*corev1.Service, len(l.Items))
		for i := range l.Items {
			out[i] = &l.Items[i]
		}
		return out, true
	}
	var out []*corev1.Service
	for _, ns := range promNamespaces {
		l, err := c.cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	}
	return out, false
}

// prometheus returns the Prometheus API of the cluster. The result is
// cached until the setting changes. A failed discovery is cached for
// promRetry.
func (c *Cluster) prometheus(ctx context.Context) (*promTarget, PromSource) {
	p := &c.prom
	p.mu.Lock()
	key := ""
	if p.setting != nil {
		key = strings.TrimSpace(p.setting())
	}
	if p.done && p.key == key && (p.target != nil || time.Since(p.at) < promRetry) {
		t, src := p.target, p.src
		p.mu.Unlock()
		return t, src
	}
	p.mu.Unlock()

	t, src := c.resolvePrometheus(ctx, key)
	if ctx.Err() != nil && t == nil {
		// A timeout says nothing about the cluster, so it is not cached.
		return t, src
	}
	p.mu.Lock()
	p.done, p.key, p.target, p.src, p.at = true, key, t, src, time.Now()
	p.mu.Unlock()
	return t, src
}

func (c *Cluster) resolvePrometheus(ctx context.Context, key string) (*promTarget, PromSource) {
	t, err := parsePromSetting(key)
	if err != nil {
		return nil, PromSource{Status: MetricsError, Mode: "service", Message: err.Error() + ". Change it in Settings > Metrics."}
	}
	if t != nil {
		src := PromSource{Status: MetricsOK, Mode: "service", Flavor: t.Flavor, Target: t.String(), Via: "proxy"}
		if t.URL != "" {
			src.Mode, src.Via = "url", "direct"
			return t, src
		}
		if t.Port == "" {
			svc, err := c.cs.CoreV1().Services(t.NS).Get(ctx, t.Name, metav1.GetOptions{})
			if err != nil {
				src.Status, src.Message = MetricsError, "st8ks cannot read Service "+t.NS+"/"+t.Name+": "+friendlyErr(err)
				return nil, src
			}
			if t.Port, t.scheme = promPort(svc); t.Port == "" {
				src.Status, src.Message = MetricsError, "Service "+t.NS+"/"+t.Name+" has more than one port. Add the port to the metrics source, for example "+t.NS+"/"+t.Name+":9090."
				return nil, src
			}
			src.Target = t.String()
		}
		return t, src
	}
	svcs, all := c.discoveryServices(ctx)
	if t := pickPrometheus(svcs); t != nil {
		return t, PromSource{Status: MetricsOK, Mode: "auto", Flavor: t.Flavor, Target: t.String(), Via: "proxy"}
	}
	src := PromSource{Status: MetricsNone, Mode: "auto", Searched: promSearched,
		Message: "st8ks found no Prometheus or VictoriaMetrics in the cluster."}
	if !all {
		src.Message = "Your credentials cannot list Services in all namespaces. st8ks searched only the namespaces " +
			strings.Join(promNamespaces, ", ") + ", and found no Prometheus there."
	}
	return nil, src
}

// forget clears a discovered target after the Service stopped answering,
// so the next request searches again.
func (c *Cluster) forgetPrometheus(t *promTarget) {
	p := &c.prom
	p.mu.Lock()
	if p.target == t && p.src.Mode == "auto" {
		p.done = false
	}
	p.mu.Unlock()
}

// ---- Queries ----

type promPoint struct {
	T float64
	V float64
}

func (p *promPoint) UnmarshalJSON(b []byte) error {
	var raw [2]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[0], &p.T); err != nil {
		return err
	}
	var s string
	if err := json.Unmarshal(raw[1], &s); err != nil {
		return err
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	p.V = v
	return nil
}

type promResult struct {
	Metric map[string]string `json:"metric"`
	Values []promPoint       `json:"values"`
	Value  *promPoint        `json:"value"`
}

type promResponse struct {
	// Kind is Status when the API server answers instead of Prometheus.
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string       `json:"resultType"`
		Result     []promResult `json:"result"`
	} `json:"data"`
}

// parsePromResponse reads a Prometheus API answer. reqErr is the error of
// the request. A Prometheus error in the body wins over it, because it
// explains more.
func parsePromResponse(body []byte, reqErr error) ([]promResult, error) {
	var r promResponse
	if len(body) == 0 || json.Unmarshal(body, &r) != nil || r.Status == "" {
		if reqErr != nil {
			return nil, reqErr
		}
		return nil, errors.New("the answer is not a Prometheus API response")
	}
	if r.Kind == "Status" {
		// client-go keeps the text of an error only for text bodies, so
		// the Status of the API server is decoded here.
		var st metav1.Status
		if json.Unmarshal(body, &st) == nil && st.Status == metav1.StatusFailure {
			return nil, &apierrors.StatusError{ErrStatus: st}
		}
		return nil, cmp.Or(reqErr, errors.New("the API server answered instead of Prometheus"))
	}
	if r.Status != "success" {
		msg := cmp.Or(r.Error, "unknown error")
		if r.ErrorType != "" {
			msg = r.ErrorType + ": " + msg
		}
		return nil, errors.New("the query failed: " + msg)
	}
	return r.Data.Result, nil
}

func (c *Cluster) promGet(ctx context.Context, t *promTarget, api string, params map[string]string) ([]byte, error) {
	if t.URL == "" {
		w := c.cs.CoreV1().Services(t.NS).ProxyGet(t.scheme, t.Name, t.Port, t.Path+api, params)
		if r, ok := w.(*rest.Request); ok {
			// The client asks for protobuf, which Prometheus does not speak.
			r.SetHeader("Accept", "application/json")
		}
		return w.DoRaw(ctx)
	}
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL+api+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := promHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, promBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return body, fmt.Errorf("%s answered %s", t.URL, resp.Status)
	}
	return body, nil
}

func (c *Cluster) promQuery(ctx context.Context, t *promTarget, q string) ([]promResult, error) {
	return parsePromResponse(c.promGet(ctx, t, "/api/v1/query", map[string]string{"query": q}))
}

func (c *Cluster) promRange(ctx context.Context, t *promTarget, q string, start, end, step int64) ([]promResult, error) {
	return parsePromResponse(c.promGet(ctx, t, "/api/v1/query_range", map[string]string{
		"query": q, "start": strconv.FormatInt(start, 10), "end": strconv.FormatInt(end, 10), "step": strconv.FormatInt(step, 10),
	}))
}

// promFail turns a query error into a status and a message.
func (c *Cluster) promFail(t *promTarget, err error) (status, msg, missing string) {
	if t.URL == "" && apierrors.IsForbidden(err) && strings.Contains(err.Error(), "services/proxy") {
		missing = "get on services/proxy in namespace " + t.NS
		return MetricsForbidden, "Your credentials cannot open the API server proxy of Service " + t.String() +
			". st8ks needs the permission " + missing + ".", missing
	}
	if t.URL == "" && (apierrors.IsNotFound(err) || apierrors.IsServiceUnavailable(err)) {
		c.forgetPrometheus(t)
		if apierrors.IsNotFound(err) {
			return MetricsError, "Service " + t.NS + "/" + t.Name + " does not exist, or it has no port " + t.Port +
				". Check the metrics source in Settings > Metrics.", ""
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return MetricsError, t.String() + " did not answer within " + promTimeout.String() + ".", ""
	}
	return MetricsError, t.String() + ": " + friendlyErr(err), ""
}

// CheckPrometheus finds the Prometheus of the cluster and sends one test
// query to it.
func (c *Cluster) CheckPrometheus() PromSource {
	ctx, cancel := context.WithTimeout(c.ctx, promTimeout)
	defer cancel()
	t, src := c.prometheus(ctx)
	if t == nil {
		return src
	}
	start := time.Now()
	if _, err := c.promQuery(ctx, t, "vector(1)"); err != nil {
		src.Status, src.Message, src.Missing = c.promFail(t, err)
		return src
	}
	src.Message = fmt.Sprintf("The test query answered in %d ms.", time.Since(start).Milliseconds())
	return src
}

// ---- Series ----

// promStep returns the step and the point count for a range. The step is
// the range divided into promMaxPoints, rounded up to a multiple of 15
// seconds.
func promStep(r time.Duration) (step time.Duration, n int) {
	const q = 15 * time.Second
	step = (r + promMaxPoints - 1) / promMaxPoints
	step = (step + q - 1) / q * q
	if step < q {
		step = q
	}
	return step, int(r / step)
}

// promWindows returns the window of rate for CPU, and the window of
// max_over_time for memory. A memory window of 0 means raw samples.
func promWindows(step time.Duration) (rate, mem time.Duration) {
	rate = max(step, 5*time.Minute)
	if step > time.Minute {
		mem = step
	}
	return rate, mem
}

func promDur(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) + "s" }

func promEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func promSel(parts ...string) string { return "{" + strings.Join(parts, ",") + "}" }
func promEq(k, v string) string      { return k + `="` + promEscape(v) + `"` }
func promRe(k, v string) string      { return k + `=~"` + promEscape(v) + `"` }

// promContainers selects the cAdvisor series of containers. The image
// filter drops the kubelet resource metrics, which have the same names.
const promContainers = `container!="",container!="POD",image!=""`

const nameChars = "[bcdfghjklmnpqrstvwxz2456789]"

// maxGenerated is the longest prefix that Kubernetes keeps from a
// generateName, before it adds 5 random characters.
const maxGenerated = 58

// controllerPodRegex returns a PromQL regular expression for the names of
// the pods of a controller. It also matches pods that are gone.
func controllerPodRegex(kind, name string) string {
	switch kind {
	case "StatefulSets":
		return regexp.QuoteMeta(name) + "-[0-9]+"
	case "DaemonSets":
		return generatedRegex(name + "-")
	case "Deployments":
		// Pods are NAME-HASH-XXXXX. The ReplicaSet hash has 1 to 10 characters.
		base := name + "-"
		if len(base) >= maxGenerated {
			return generatedRegex(base)
		}
		room := maxGenerated - len(base)
		alts := []string{}
		if room >= 2 {
			alts = append(alts, nameChars+"{1,"+strconv.Itoa(min(10, room-1))+"}-")
		}
		if room <= 10 {
			// The prefix is cut inside the hash.
			alts = append(alts, nameChars+"{"+strconv.Itoa(room)+"}")
		}
		mid := alts[0]
		if len(alts) > 1 {
			mid = "(?:" + strings.Join(alts, "|") + ")"
		}
		return regexp.QuoteMeta(base) + mid + nameChars + "{5}"
	}
	return regexp.QuoteMeta(name)
}

// generatedRegex matches a name from generateName base.
func generatedRegex(base string) string {
	if len(base) > maxGenerated {
		base = base[:maxGenerated]
	}
	return regexp.QuoteMeta(base) + nameChars + "{5}"
}

// toValues puts the points of a series on the step grid.
func toValues(pts []promPoint, start, step int64, n int) MetricValues {
	v := make(MetricValues, n)
	for i := range v {
		v[i] = math.NaN()
	}
	for _, p := range pts {
		i := int(math.Round((p.T - float64(start)) / float64(step)))
		if i >= 0 && i < n && !math.IsNaN(p.V) && !math.IsInf(p.V, 0) {
			v[i] = p.V
		}
	}
	return v
}

func peak(v MetricValues) float64 {
	m := math.Inf(-1)
	for _, f := range v {
		if !math.IsNaN(f) && f > m {
			m = f
		}
	}
	return m
}

// buildPanel turns query results into a panel. label is the label that
// names a part. order puts the parts in that order, others follow by name.
func buildPanel(res []promResult, label string, order []string, start, step int64, n int) MetricPanel {
	var parts []MetricSeries
	for _, r := range res {
		parts = append(parts, MetricSeries{Name: r.Metric[label], V: toValues(r.Values, start, step, n)})
	}
	p := MetricPanel{Parts: []MetricSeries{}}
	if len(parts) == 0 {
		return p
	}
	total := make(MetricValues, n)
	for i := range total {
		total[i] = math.NaN()
		for _, s := range parts {
			if f := s.V[i]; !math.IsNaN(f) {
				if math.IsNaN(total[i]) {
					total[i] = 0
				}
				total[i] += f
			}
		}
	}
	p.Total = &MetricSeries{Name: "total", V: total}
	if len(parts) > promMaxParts {
		sort.SliceStable(parts, func(i, j int) bool { return peak(parts[i].V) > peak(parts[j].V) })
		p.More = len(parts) - promMaxParts
		parts = parts[:promMaxParts]
	}
	rank := func(name string) int {
		if i := slices.Index(order, name); i >= 0 {
			return i
		}
		return len(order)
	}
	sort.SliceStable(parts, func(i, j int) bool {
		ri, rj := rank(parts[i].Name), rank(parts[j].Name)
		if ri != rj {
			return ri < rj
		}
		return parts[i].Name < parts[j].Name
	})
	p.Parts = parts
	return p
}

func hasValue(p MetricPanel) bool {
	return p.Total != nil && !math.IsInf(peak(p.Total.V), -1)
}

// resLines returns the request and limit lines of one set of containers,
// times a pod count. A limit shows only when every container has one.
// label names a line kind on the chart.
func resLines(cts []corev1.Container, times int64, label func(kind string) string) (cpu, mem []MetricLine) {
	var cr, cl, mr, ml int64
	cAll, mAll := len(cts) > 0, len(cts) > 0
	for _, ct := range cts {
		cr += ct.Resources.Requests.Cpu().MilliValue()
		mr += ct.Resources.Requests.Memory().Value()
		if q, ok := ct.Resources.Limits[corev1.ResourceCPU]; ok {
			cl += q.MilliValue()
		} else {
			cAll = false
		}
		if q, ok := ct.Resources.Limits[corev1.ResourceMemory]; ok {
			ml += q.Value()
		} else {
			mAll = false
		}
	}
	add := func(out []MetricLine, kind string, v float64) []MetricLine {
		if v > 0 {
			out = append(out, MetricLine{Kind: kind, Label: label(kind), V: v * float64(times)})
		}
		return out
	}
	cpu = add(cpu, "request", float64(cr)/1000)
	mem = add(mem, "request", float64(mr))
	if cAll {
		cpu = add(cpu, "limit", float64(cl)/1000)
	}
	if mAll {
		mem = add(mem, "limit", float64(ml))
	}
	return cpu, mem
}

// podContainers returns the containers that run beside each other: the
// app containers and the sidecars.
func podContainers(spec *corev1.PodSpec) []corev1.Container {
	out := append([]corev1.Container(nil), spec.Containers...)
	for _, ct := range spec.InitContainers {
		if ct.RestartPolicy != nil && *ct.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, ct)
		}
	}
	return out
}

// metricsQuery describes the queries of one object.
type metricsQuery struct {
	cpu, mem         string
	label            string
	order            []string
	parts            string
	cpuLines         []MetricLine // lines of the total
	memLines         []MetricLine
	partCPU, partMem map[string][]MetricLine // lines per part; "" for every part
	empty            string
}

func (c *Cluster) metricsQuery(ref Ref, rate, mem time.Duration) (*metricsQuery, error) {
	memExpr := func(sel string) string {
		if mem == 0 {
			return "container_memory_working_set_bytes" + sel
		}
		return "max_over_time(container_memory_working_set_bytes" + sel + "[" + promDur(mem) + "])"
	}
	cpuExpr := func(sel string) string {
		return "rate(container_cpu_usage_seconds_total" + sel + "[" + promDur(rate) + "])"
	}
	q := &metricsQuery{partCPU: map[string][]MetricLine{}, partMem: map[string][]MetricLine{}}
	switch ref.Kind {
	case "Pods":
		sel := promSel(promEq("namespace", ref.NS), promEq("pod", ref.Name), promContainers)
		q.cpu = "sum by (container) (" + cpuExpr(sel) + ")"
		q.mem = "sum by (container) (" + memExpr(sel) + ")"
		q.label, q.parts = "container", "container"
		q.empty = "Prometheus has no CPU or memory series for pod " + ref.Name + " in this range."
		if o := c.getObj("Pods", ref.NS, ref.Name); o != nil {
			cts := podContainers(&o.(*corev1.Pod).Spec)
			same := func(kind string) string { return kind }
			for _, ct := range cts {
				q.order = append(q.order, ct.Name)
				q.partCPU[ct.Name], q.partMem[ct.Name] = resLines([]corev1.Container{ct}, 1, same)
			}
			q.cpuLines, q.memLines = resLines(cts, 1, same)
		}
	case "Deployments", "StatefulSets", "DaemonSets":
		sel := promSel(promEq("namespace", ref.NS), promRe("pod", controllerPodRegex(ref.Kind, ref.Name)), promContainers)
		q.cpu = "sum by (pod) (" + cpuExpr(sel) + ")"
		q.mem = "sum by (pod) (" + memExpr(sel) + ")"
		q.label, q.parts = "pod", "pod"
		q.empty = "Prometheus has no CPU or memory series for the pods of " + singular(ref.Kind) + " " + ref.Name + " in this range."
		var spec *corev1.PodSpec
		var replicas int64
		switch o := c.getObj(ref.Kind, ref.NS, ref.Name).(type) {
		case *appsv1.Deployment:
			spec, replicas = &o.Spec.Template.Spec, int64(ptr.Deref(o.Spec.Replicas, 1))
		case *appsv1.StatefulSet:
			spec, replicas = &o.Spec.Template.Spec, int64(ptr.Deref(o.Spec.Replicas, 1))
		case *appsv1.DaemonSet:
			spec, replicas = &o.Spec.Template.Spec, int64(o.Status.DesiredNumberScheduled)
		}
		if spec != nil {
			cts := podContainers(spec)
			q.partCPU[""], q.partMem[""] = resLines(cts, 1, func(kind string) string { return kind + " per pod" })
			if replicas > 0 {
				q.cpuLines, q.memLines = resLines(cts, replicas, func(kind string) string {
					return fmt.Sprintf("%ss of %d pods", kind, replicas)
				})
			}
		}
	case "Nodes":
		// The root cgroup is the whole node. The prometheus-community chart
		// names the node in the instance label. kube-prometheus-stack drops
		// the root cgroup, so the sum of the containers is the last choice.
		alt := func(expr func(string) string) string {
			return "sum(" + expr(promSel(`id="/"`, promEq("node", ref.Name))) + ") or sum(" +
				expr(promSel(`id="/"`, promEq("instance", ref.Name))) + ") or sum(" +
				expr(promSel(promEq("node", ref.Name), promContainers)) + ")"
		}
		q.cpu, q.mem = alt(cpuExpr), alt(memExpr)
		q.empty = "Node history is not available. The cAdvisor series of this Prometheus have no node label for " + ref.Name + "."
		if o := c.getObj("Nodes", "", ref.Name); o != nil {
			n := o.(*corev1.Node)
			var cr, mr int64
			for _, po := range c.byIndex("Pods", "node", ref.Name) {
				p := po.(*corev1.Pod)
				if podTerminal(p) {
					continue
				}
				for _, ct := range podContainers(&p.Spec) {
					cr += ct.Resources.Requests.Cpu().MilliValue()
					mr += ct.Resources.Requests.Memory().Value()
				}
			}
			a := n.Status.Allocatable
			if cr > 0 {
				q.cpuLines = append(q.cpuLines, MetricLine{Kind: "request", Label: "pod requests", V: float64(cr) / 1000})
			}
			if mr > 0 {
				q.memLines = append(q.memLines, MetricLine{Kind: "request", Label: "pod requests", V: float64(mr)})
			}
			q.cpuLines = append(q.cpuLines, MetricLine{Kind: "limit", Label: "allocatable", V: float64(a.Cpu().MilliValue()) / 1000})
			q.memLines = append(q.memLines, MetricLine{Kind: "limit", Label: "allocatable", V: float64(a.Memory().Value())})
		}
	default:
		return nil, fmt.Errorf("st8ks has no metrics history for %s", ref.Kind)
	}
	return q, nil
}

// Metrics returns the CPU and memory history of a pod, a workload or a
// node. rng is 1h, 6h, 24h or 7d.
func (c *Cluster) Metrics(ref Ref, rng string) (*MetricsData, error) {
	r, ok := metricRanges[rng]
	if !ok {
		return nil, fmt.Errorf("unknown range %q", rng)
	}
	step, n := promStep(r)
	rate, mem := promWindows(step)
	q, err := c.metricsQuery(ref, rate, mem)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(c.ctx, promTimeout)
	defer cancel()
	d := &MetricsData{Range: rng, Parts: q.parts, CPU: MetricPanel{Parts: []MetricSeries{}}, Mem: MetricPanel{Parts: []MetricSeries{}}}
	// The end aligns to 15 seconds, not to the step, so the last point of a
	// 7d range is recent and not up to 42 minutes old.
	st := int64(step / time.Second)
	end := time.Now().Unix() / 15 * 15
	start := end - int64(n-1)*st
	d.Start, d.Step, d.N = start, st, n

	var cpuRes, memRes []promResult
	for try := 0; ; try++ {
		t, src := c.prometheus(ctx)
		d.Source = src
		if t == nil {
			d.Status, d.Message = src.Status, src.Message
			return d, nil
		}
		var cpuErr, memErr error
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			cpuRes, cpuErr = c.promRange(ctx, t, q.cpu, start, end, st)
		}()
		memRes, memErr = c.promRange(ctx, t, q.mem, start, end, st)
		wg.Wait()
		err := cmp.Or(cpuErr, memErr)
		if err == nil {
			break
		}
		d.Status, d.Message, d.Source.Missing = c.promFail(t, err)
		// A discovered Service that is gone is searched again once.
		if try > 0 || src.Mode != "auto" || !apierrors.IsNotFound(err) {
			return d, nil
		}
	}
	d.CPU = buildPanel(cpuRes, q.label, q.order, start, st, n)
	d.Mem = buildPanel(memRes, q.label, q.order, start, st, n)
	if !hasValue(d.CPU) && !hasValue(d.Mem) {
		d.Status, d.Message = MetricsNoData, q.empty
		return d, nil
	}
	d.Status = MetricsOK
	setLines(&d.CPU, q.cpuLines, q.partCPU)
	setLines(&d.Mem, q.memLines, q.partMem)
	return d, nil
}

// setLines adds the request and limit lines to a panel. byPart[""] holds
// the lines of every part.
func setLines(p *MetricPanel, total []MetricLine, byPart map[string][]MetricLine) {
	if p.Total != nil {
		p.Total.Lines = total
	}
	for i := range p.Parts {
		l, ok := byPart[p.Parts[i].Name]
		if !ok {
			l = byPart[""]
		}
		p.Parts[i].Lines = l
	}
}

// ---- Assistant evidence ----

// oomEvidence returns the peak memory working set of the last hour against
// the limit, for a crash loop with OOMKilled. It returns "" when Prometheus
// does not answer within 3 seconds.
func (c *Cluster) oomEvidence(i *Issue) string {
	if !strings.HasPrefix(i.ID, "crash:") || !strings.Contains(i.Reason, "OOMKilled") || i.Kind != "Pods" {
		return ""
	}
	container := i.ID[strings.LastIndex(i.ID, "/")+1:]
	pods := i.Pods
	if len(pods) == 0 {
		pods = []string{i.Obj}
	}
	if len(pods) > 20 {
		pods = pods[:20]
	}
	names := make([]string, len(pods))
	for n, p := range pods {
		names[n] = regexp.QuoteMeta(p)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
	defer cancel()
	t, _ := c.prometheus(ctx)
	if t == nil {
		return ""
	}
	sel := promSel(promEq("namespace", i.NS), promRe("pod", strings.Join(names, "|")), promEq("container", container), `image!=""`)
	res, err := c.promQuery(ctx, t, "max(max_over_time(container_memory_working_set_bytes"+sel+"[1h]))")
	if err != nil || len(res) == 0 || res[0].Value == nil {
		return ""
	}
	v := res[0].Value.V
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return ""
	}
	line := "Memory working set peaked at " + fmtBytesF(v)
	low := false
	if o := c.getObj("Pods", i.NS, i.Obj); o != nil {
		if lim, ok := containerLimit(o.(*corev1.Pod), container, corev1.ResourceMemory); ok && lim.Value() > 0 {
			line += " of the " + fmtBytesF(float64(lim.Value())) + " limit"
			low = v < 0.8*float64(lim.Value())
		}
	}
	line += " in the last hour"
	if low {
		// A container that dies seconds after it starts is often not scraped
		// at its peak.
		line += ". Prometheus samples at an interval, so it can miss a short spike before the kill"
	}
	return line
}

// fmtBytesF formats bytes with binary units and one decimal.
func fmtBytesF(b float64) string {
	const k = 1024.0
	switch {
	case b >= k*k*k*k:
		return trimFloat(b/(k*k*k*k)) + "Ti"
	case b >= k*k*k:
		return trimFloat(b/(k*k*k)) + "Gi"
	case b >= k*k:
		return trimFloat(b/(k*k)) + "Mi"
	case b >= k:
		return trimFloat(b/k) + "Ki"
	}
	return strconv.FormatFloat(b, 'f', 0, 64)
}
