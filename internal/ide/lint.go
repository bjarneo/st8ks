package ide

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Severities of a diagnostic.
const (
	SevError   = "error"
	SevWarning = "warning"
	SevInfo    = "info"
)

// Diag is one problem in a file. Lines and columns are 0-based. End is the
// column after the marked text, or -1 for the end of the line.
type Diag struct {
	Line int    `json:"line"`
	Col  int    `json:"col"`
	End  int    `json:"end"`
	Sev  string `json:"sev"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Live bool   `json:"live,omitempty"`
	Fix  string `json:"fix,omitempty"`

	run func(lines []string) []string
}

// lintEnv is the input of the checks for one file.
type lintEnv struct {
	path   string
	text   string
	lines  []string
	docs   []*Doc
	ix     *Index
	cl     Cluster                            // nil when no cluster is connected
	schema func(apiVersion string) *SchemaDoc // returns nil when the schema is not loaded
	build  string                             // the kustomize build error of this file
}

// lintStatic runs the checks that depend on the text, the workspace and
// the schema.
func lintStatic(e *lintEnv) []Diag {
	if e.ix.skip(e.path, e.text) {
		return nil
	}
	out := tabs(e.lines)
	hasTabs := len(out) > 0
	kust := e.ix.Kusts[e.path]
	for _, d := range e.docs {
		if d.Err != "" {
			if !hasTabs {
				out = append(out, Diag{Line: d.ErrLine, Col: 0, End: -1, Sev: SevError, Code: "yaml-syntax", Msg: d.Err})
			}
			continue
		}
		if kust != nil && isKustDoc(d) {
			out = append(out, e.kustomization(d, kust)...)
			continue
		}
		if !d.IsObject() {
			continue
		}
		out = append(out, e.object(d)...)
	}
	return dedupe(out)
}

// lintLive runs the checks that read live objects. It can add quick fixes
// to the static diagnostics.
func lintLive(e *lintEnv, static []Diag) []Diag {
	if e.cl == nil || e.ix.skip(e.path, e.text) {
		return nil
	}
	var out []Diag
	kust := e.ix.Kusts[e.path]
	for _, d := range e.docs {
		switch {
		case d.Err != "":
		case kust != nil && isKustDoc(d):
			out = append(out, e.liveImages(kust)...)
		case d.IsObject():
			out = append(out, e.live(d, static)...)
		}
	}
	return out
}

func isKustDoc(d *Doc) bool {
	return d.Kind == "Kustomization" || d.Kind == "Component" || d.APIVersion == ""
}

// dedupe keeps one diagnostic per line and code, and sorts by line.
func dedupe(ds []Diag) []Diag {
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Line < ds[j].Line })
	out := ds[:0]
	seen := map[string]bool{}
	for _, d := range ds {
		k := strconv.Itoa(d.Line) + d.Code
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	return out
}

func tabs(lines []string) []Diag {
	var out []Diag
	for i, l := range lines {
		ind := indentOf(l)
		if strings.Contains(ind, "\t") {
			out = append(out, Diag{Line: i, Col: 0, End: len(ind), Sev: SevError, Code: "yaml-tab",
				Msg: "Tabs are not allowed in YAML indentation", Fix: "Convert tabs to spaces", run: func(ls []string) []string {
					for j, x := range ls {
						in := indentOf(x)
						if strings.Contains(in, "\t") {
							ls[j] = strings.ReplaceAll(in, "\t", "  ") + x[len(in):]
						}
					}
					return ls
				}})
		}
	}
	return out
}

// valueDiag marks the value of a node.
func (e *lintEnv) valueDiag(n *yaml.Node, sev, code, msg string) Diag {
	col, end := 0, -1
	if n != nil && n.Line >= 0 && n.Line < len(e.lines) {
		col = n.Column
		end = spanEnd(e.lines[n.Line], col)
		if n.Kind != yaml.ScalarNode || n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			end = -1
		}
	}
	line := 0
	if n != nil {
		line = n.Line
	}
	return Diag{Line: line, Col: col, End: end, Sev: sev, Code: code, Msg: msg}
}

// keyDiag marks a key.
func keyDiag(k *yaml.Node, sev, code, msg string) Diag {
	return Diag{Line: k.Line, Col: k.Column, End: k.Column + len(k.Value), Sev: sev, Code: code, Msg: msg}
}

// replaceValue returns a fix that sets the scalar at a node to text.
func replaceValue(n *yaml.Node, text string) func([]string) []string {
	line, col := n.Line, n.Column
	return func(ls []string) []string {
		if line >= len(ls) || col > len(ls[line]) {
			return ls
		}
		l := ls[line]
		end := spanEnd(l, col)
		ls[line] = l[:col] + text + l[end:]
		return ls
	}
}

// deleteLines returns a fix that removes the lines from a to b.
func deleteLines(a, b int) func([]string) []string {
	return func(ls []string) []string {
		if a < 0 || b >= len(ls) || a > b {
			return ls
		}
		return append(ls[:a:a], ls[b+1:]...)
	}
}

// ---- Objects ----

func (e *lintEnv) object(d *Doc) []Diag {
	var out []Diag
	out = append(out, e.apiVersion(d)...)
	var sd *SchemaDoc
	if e.schema != nil {
		sd = e.schema(d.APIVersion)
	}
	if sd != nil {
		if root := sd.Kind(d.Kind); root != nil {
			v := &validator{e: e, sd: sd, patch: e.ix.Patches[e.path]}
			v.node(d.Root, root, nil, d.Start)
			out = append(out, v.out...)
		}
	}
	out = append(out, e.workload(d, sd != nil)...)
	return out
}

type removal struct {
	to    string
	minor int
	fix   bool
}

// removedAPIs lists API versions that Kubernetes no longer serves. A kind of
// "*" matches every kind of the group version.
var removedAPIs = map[string]removal{
	"extensions/v1beta1 Deployment":                         {"apps/v1", 16, true},
	"extensions/v1beta1 DaemonSet":                          {"apps/v1", 16, true},
	"extensions/v1beta1 ReplicaSet":                         {"apps/v1", 16, true},
	"extensions/v1beta1 NetworkPolicy":                      {"networking.k8s.io/v1", 16, true},
	"extensions/v1beta1 Ingress":                            {"networking.k8s.io/v1", 22, false},
	"extensions/v1beta1 PodSecurityPolicy":                  {"", 25, false},
	"apps/v1beta1 *":                                        {"apps/v1", 16, true},
	"apps/v1beta2 *":                                        {"apps/v1", 16, true},
	"networking.k8s.io/v1beta1 Ingress":                     {"networking.k8s.io/v1", 22, false},
	"networking.k8s.io/v1beta1 IngressClass":                {"networking.k8s.io/v1", 22, true},
	"batch/v1beta1 CronJob":                                 {"batch/v1", 25, true},
	"policy/v1beta1 PodDisruptionBudget":                    {"policy/v1", 25, true},
	"policy/v1beta1 PodSecurityPolicy":                      {"", 25, false},
	"autoscaling/v2beta1 HorizontalPodAutoscaler":           {"autoscaling/v2", 25, false},
	"autoscaling/v2beta2 HorizontalPodAutoscaler":           {"autoscaling/v2", 26, true},
	"discovery.k8s.io/v1beta1 EndpointSlice":                {"discovery.k8s.io/v1", 25, false},
	"events.k8s.io/v1beta1 Event":                           {"events.k8s.io/v1", 25, false},
	"node.k8s.io/v1beta1 RuntimeClass":                      {"node.k8s.io/v1", 25, true},
	"rbac.authorization.k8s.io/v1beta1 *":                   {"rbac.authorization.k8s.io/v1", 22, true},
	"admissionregistration.k8s.io/v1beta1 *":                {"admissionregistration.k8s.io/v1", 22, false},
	"apiextensions.k8s.io/v1beta1 CustomResourceDefinition": {"apiextensions.k8s.io/v1", 22, false},
	"apiregistration.k8s.io/v1beta1 APIService":             {"apiregistration.k8s.io/v1", 22, true},
	"certificates.k8s.io/v1beta1 CertificateSigningRequest": {"certificates.k8s.io/v1", 22, false},
	"coordination.k8s.io/v1beta1 Lease":                     {"coordination.k8s.io/v1", 22, true},
	"scheduling.k8s.io/v1beta1 PriorityClass":               {"scheduling.k8s.io/v1", 22, true},
	"storage.k8s.io/v1beta1 CSIDriver":                      {"storage.k8s.io/v1", 22, true},
	"storage.k8s.io/v1beta1 CSINode":                        {"storage.k8s.io/v1", 22, true},
	"storage.k8s.io/v1beta1 StorageClass":                   {"storage.k8s.io/v1", 22, true},
	"storage.k8s.io/v1beta1 VolumeAttachment":               {"storage.k8s.io/v1", 22, true},
	"storage.k8s.io/v1beta1 CSIStorageCapacity":             {"storage.k8s.io/v1", 27, true},
	"flowcontrol.apiserver.k8s.io/v1beta1 *":                {"flowcontrol.apiserver.k8s.io/v1", 26, false},
	"flowcontrol.apiserver.k8s.io/v1beta2 *":                {"flowcontrol.apiserver.k8s.io/v1", 29, false},
	"flowcontrol.apiserver.k8s.io/v1beta3 *":                {"flowcontrol.apiserver.k8s.io/v1", 32, false},
}

func removedAPI(apiVersion, kind string) (removal, bool) {
	if r, ok := removedAPIs[apiVersion+" "+kind]; ok {
		return r, true
	}
	r, ok := removedAPIs[apiVersion+" *"]
	return r, ok
}

func (e *lintEnv) apiVersion(d *Doc) []Diag {
	k, v := pair(d.Root, "apiVersion")
	if k == nil {
		return nil
	}
	if r, ok := removedAPI(d.APIVersion, d.Kind); ok {
		sev, msg := SevWarning, fmt.Sprintf("%s %s was removed in Kubernetes 1.%d", d.APIVersion, d.Kind, r.minor)
		if e.cl != nil && e.cl.Minor() > 0 {
			msg += fmt.Sprintf(". %s runs 1.%d", e.cl.Name(), e.cl.Minor())
			switch {
			case e.cl.Minor() >= r.minor:
				sev = SevError
			default:
				sev = SevInfo
				msg = fmt.Sprintf("%s %s is removed in Kubernetes 1.%d. %s runs 1.%d, so migrate before you upgrade", d.APIVersion, d.Kind, r.minor, e.cl.Name(), e.cl.Minor())
			}
		}
		dg := e.valueDiag(v, sev, "api-removed", msg)
		if r.fix && r.to != "" {
			dg.Fix = "Migrate to " + r.to
			dg.run = replaceValue(v, r.to)
		}
		return []Diag{dg}
	}
	if e.cl == nil || strings.HasPrefix(d.APIVersion, "kustomize.config.k8s.io/") {
		return nil
	}
	if _, ok := e.cl.Resource(d.APIVersion, d.Kind); ok {
		return nil
	}
	group, _ := splitAPIVersion(d.APIVersion)
	if e.ix.CRDs[group+"/"+d.Kind] {
		return nil
	}
	return []Diag{e.valueDiag(v, SevWarning, "not-served",
		fmt.Sprintf("%s does not serve %s %s. Install the CRD first, or check the apiVersion", e.cl.Name(), d.APIVersion, d.Kind))}
}

// ---- Schema ----

type validator struct {
	e     *lintEnv
	sd    *SchemaDoc
	patch bool
	out   []Diag
}

// boolWords are plain scalars that Kubernetes reads as booleans, because
// its YAML decoder follows YAML 1.1.
var boolWords = map[string]bool{
	"y": true, "Y": true, "yes": true, "Yes": true, "YES": true, "n": true, "N": true, "no": true, "No": true, "NO": true,
	"on": true, "On": true, "ON": true, "off": true, "Off": true, "OFF": true,
	"true": true, "True": true, "TRUE": true, "false": true, "False": true, "FALSE": true,
}

func (v *validator) node(n *yaml.Node, s *Schema, path []string, keyLine int) {
	r := v.sd.Resolve(s)
	if r.Schema == nil || r.PreserveUnknown || r.Ref == "Quantity" || r.IntOrString || len(r.OneOf) > 0 || len(r.AnyOf) > 0 {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		if r.Type != "" && r.Type != "object" {
			v.typeErr(n, path, r.Type)
			return
		}
		if !v.patch && len(path) > 0 {
			for _, req := range r.Required {
				if get(n, req) == nil {
					d := Diag{Line: keyLine, Col: 0, End: -1, Sev: SevWarning, Code: "required",
						Msg: "Missing required field " + PathString(append(append([]string{}, path...), req))}
					if keyLine < len(v.e.lines) {
						d.Col = len(indentOf(v.e.lines[keyLine]))
					}
					v.out = append(v.out, d)
				}
			}
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			if strings.HasPrefix(k.Value, "$") || k.Kind != yaml.ScalarNode {
				continue
			}
			p := append(append([]string{}, path...), k.Value)
			prop := r.Properties[k.Value]
			if prop == nil {
				if r.Additional != nil {
					if r.Additional.Schema != nil {
						v.node(val, r.Additional.Schema, p, k.Line)
					}
					continue
				}
				if len(r.Properties) == 0 || (len(path) == 0 && k.Value == "status") {
					continue
				}
				v.unknown(k, r, p)
				continue
			}
			if len(path) == 0 && k.Value == "status" {
				continue
			}
			v.node(val, prop, p, k.Line)
		}
	case yaml.SequenceNode:
		if r.Type != "" && r.Type != "array" {
			v.typeErr(n, path, r.Type)
			return
		}
		for i, it := range n.Content {
			v.node(it, r.Items, append(append([]string{}, path...), "["+strconv.Itoa(i)+"]"), it.Line)
		}
	case yaml.ScalarNode:
		v.scalar(n, r, path)
	}
}

func (v *validator) unknown(k *yaml.Node, r Resolved, path []string) {
	names := make([]string, 0, len(r.Properties))
	for n := range r.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	msg := "Unknown field " + PathString(path)
	d := keyDiag(k, SevError, "unknown-field", "")
	if s := suggest(k.Value, names); s != "" {
		msg += ". Did you mean " + s + "?"
		d.Fix = "Rename to " + s
		line, col, old := k.Line, k.Column, k.Value
		d.run = func(ls []string) []string {
			if line < len(ls) && strings.HasPrefix(ls[line][col:], old) {
				ls[line] = ls[line][:col] + s + ls[line][col+len(old):]
			}
			return ls
		}
	}
	d.Msg = msg
	v.out = append(v.out, d)
}

func (v *validator) typeErr(n *yaml.Node, path []string, want string) {
	name := PathString(path)
	msg := name + " must be "
	switch want {
	case "object":
		msg += "a map"
	case "array":
		msg += "a list"
	case "integer":
		msg += "an integer"
	case "boolean":
		msg += "true or false"
	default:
		msg += "a " + want
	}
	v.out = append(v.out, v.e.valueDiag(n, SevError, "type", msg))
}

func (v *validator) scalar(n *yaml.Node, r Resolved, path []string) {
	if n.Tag == "!!null" {
		return
	}
	plain := n.Style == 0
	switch r.Type {
	case "string":
		if n.Tag == "!!int" || n.Tag == "!!float" || n.Tag == "!!bool" || (plain && boolWords[n.Value]) {
			what := "a number"
			if n.Tag == "!!bool" || boolWords[n.Value] {
				what = "a boolean"
			}
			d := v.e.valueDiag(n, SevError, "type", fmt.Sprintf("%s must be a string. Kubernetes reads %s as %s", PathString(path), n.Value, what))
			d.Fix = "Quote the value"
			d.run = replaceValue(n, strconv.Quote(n.Value))
			v.out = append(v.out, d)
		}
	case "integer":
		switch {
		case n.Tag == "!!int":
		case n.Tag == "!!str" && !plain && isInt(n.Value):
			d := v.e.valueDiag(n, SevError, "type", PathString(path)+" must be an integer, not a string")
			d.Fix = "Remove the quotes"
			d.run = replaceValue(n, n.Value)
			v.out = append(v.out, d)
		default:
			v.typeErr(n, path, "integer")
		}
	case "number":
		if n.Tag != "!!int" && n.Tag != "!!float" {
			v.typeErr(n, path, "number")
		}
	case "boolean":
		if n.Tag != "!!bool" && !(plain && boolWords[n.Value]) {
			v.typeErr(n, path, "boolean")
		}
	case "object":
		v.typeErr(n, path, "object")
	case "array":
		v.typeErr(n, path, "array")
	}
}

func isInt(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

// suggest returns the property name closest to a misspelled key.
func suggest(key string, names []string) string {
	best, bestD := "", 3
	lk := strings.ToLower(key)
	for _, n := range names {
		ln := strings.ToLower(n)
		if ln == lk {
			return n
		}
		d := editDistance(lk, ln)
		if (strings.HasPrefix(ln, lk) || strings.HasPrefix(lk, ln)) && abs(len(ln)-len(lk)) <= 3 {
			d = min(d, 1)
		}
		if d < bestD && d <= max(1, len(lk)/3) {
			best, bestD = n, d
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// ---- Workloads ----

// podSpec returns the pod spec of a workload document.
func podSpec(d *Doc) *yaml.Node {
	switch d.Kind {
	case "Pod":
		return get(d.Root, "spec")
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "ReplicationController":
		return dig(d.Root, "spec", "template", "spec")
	case "CronJob":
		return dig(d.Root, "spec", "jobTemplate", "spec", "template", "spec")
	}
	return nil
}

// container is one container mapping of a pod spec.
type container struct {
	node *yaml.Node
	name string
	init bool
}

func containers(ps *yaml.Node) []container {
	var out []container
	for _, key := range []string{"initContainers", "containers"} {
		for _, c := range items(get(ps, key)) {
			if c.Kind == yaml.MappingNode {
				out = append(out, container{node: c, name: scalar(c, "name"), init: key == "initContainers"})
			}
		}
	}
	return out
}

// imageTag returns the tag of an image reference, "" when it has none.
func imageTag(img string) string {
	if strings.Contains(img, "@") {
		return "@"
	}
	slash := strings.LastIndex(img, "/")
	if i := strings.LastIndex(img, ":"); i > slash {
		return img[i+1:]
	}
	return ""
}

// imageName returns an image reference without its tag and digest.
func imageName(img string) string {
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	slash := strings.LastIndex(img, "/")
	if i := strings.LastIndex(img, ":"); i > slash {
		img = img[:i]
	}
	return img
}

func (e *lintEnv) workload(d *Doc, hasSchema bool) []Diag {
	var out []Diag
	switch d.Kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		out = append(out, e.selector(d)...)
	}
	ps := podSpec(d)
	if ps == nil {
		return out
	}
	served := d.Kind != "Job" && d.Kind != "CronJob"
	for _, c := range containers(ps) {
		at := c.node
		if k, _ := pair(c.node, "name"); k != nil {
			at = k
		}
		if img := get(c.node, "image"); img != nil && img.Kind == yaml.ScalarNode && !e.ix.imageSet(e.path, imageName(img.Value)) {
			switch tag := imageTag(img.Value); tag {
			case "", "latest":
				msg := "Mutable tag latest: rollouts are not reproducible, and a rollback cannot restore the old image"
				if tag == "" {
					msg = "No image tag, so the image resolves to latest. Pin a tag or a digest"
				}
				out = append(out, e.valueDiag(img, SevWarning, "image-tag", msg))
			}
		}
		res := get(c.node, "resources")
		if res == nil || (get(res, "requests") == nil && get(res, "limits") == nil) {
			dg := keyDiag(at, SevWarning, "no-resources",
				"No CPU or memory requests. The scheduler cannot account for this container, and it is evicted first")
			if res == nil {
				dg.Fix = "Add a resources block"
				dg.run = e.addResources(d, c.node)
			}
			out = append(out, dg)
		}
		for _, r := range []string{"cpu", "memory"} {
			rq, lm := dig(res, "requests", r), dig(res, "limits", r)
			if rq == nil || lm == nil || rq.Kind != yaml.ScalarNode || lm.Kind != yaml.ScalarNode {
				continue
			}
			a, errA := resource.ParseQuantity(rq.Value)
			b, errB := resource.ParseQuantity(lm.Value)
			if errA == nil && errB == nil && a.Cmp(b) > 0 {
				name := "Memory"
				if r == "cpu" {
					name = "CPU"
				}
				out = append(out, e.valueDiag(rq, SevError, "request-gt-limit",
					fmt.Sprintf("%s request %s is above the limit %s. The API server rejects this", name, rq.Value, lm.Value)))
			}
		}
		if served && !c.init && get(c.node, "ports") != nil && get(c.node, "readinessProbe") == nil {
			out = append(out, keyDiag(at, SevInfo, "no-readiness", "No readinessProbe. The Service sends traffic as soon as the container starts"))
		}
		if !hasSchema {
			for _, ev := range items(get(c.node, "env")) {
				if val := get(ev, "value"); val != nil && val.Kind == yaml.ScalarNode && val.Tag != "!!null" &&
					(val.Tag == "!!int" || val.Tag == "!!float" || val.Tag == "!!bool" || (val.Style == 0 && boolWords[val.Value])) {
					dg := e.valueDiag(val, SevError, "type", "env value must be a string. Kubernetes reads "+val.Value+" as a "+map[bool]string{true: "boolean", false: "number"}[val.Tag == "!!bool" || boolWords[val.Value]])
					dg.Fix = "Quote the value"
					dg.run = replaceValue(val, strconv.Quote(val.Value))
					out = append(out, dg)
				}
			}
		}
	}
	if k, v := pair(d.Root, "spec"); k != nil {
		if rk, rv := pair(v, "replicas"); rk != nil {
			if hpa := e.ix.HPAs[d.Kind+"/"+d.Name]; hpa != "" {
				dg := keyDiag(rk, SevInfo, "hpa-managed", fmt.Sprintf("The HPA in %s scales this workload. An apply resets replicas to %s until the next scale event", baseName(hpa), rv.Value))
				dg.Fix = "Remove replicas"
				dg.run = deleteLines(rk.Line, d.EndOf(rv))
				out = append(out, dg)
			}
		}
	}
	return out
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// addResources inserts a resources block after the image of a container.
func (e *lintEnv) addResources(d *Doc, c *yaml.Node) func([]string) []string {
	after := c.Line
	col := c.Column
	for i := 0; i+1 < len(c.Content); i += 2 {
		k, v := c.Content[i], c.Content[i+1]
		col = k.Column
		if k.Value == "image" || k.Value == "name" {
			after = max(after, d.EndOf(v))
		}
	}
	return func(ls []string) []string {
		pad := strings.Repeat(" ", col)
		block := []string{pad + "resources:", pad + "  requests:", pad + "    cpu: 100m", pad + "    memory: 128Mi", pad + "  limits:", pad + "    memory: 256Mi"}
		if after+1 > len(ls) {
			return ls
		}
		out := append([]string{}, ls[:after+1]...)
		out = append(out, block...)
		return append(out, ls[after+1:]...)
	}
}

func (e *lintEnv) selector(d *Doc) []Diag {
	sel := dig(d.Root, "spec", "selector", "matchLabels")
	tk, tpl := pair(dig(d.Root, "spec", "template"), "metadata")
	if sel == nil || sel.Kind != yaml.MappingNode {
		return nil
	}
	labels := get(tpl, "labels")
	var out []Diag
	for i := 0; i+1 < len(sel.Content); i += 2 {
		key, want := sel.Content[i].Value, sel.Content[i+1].Value
		lk, lv := pair(labels, key)
		switch {
		case lk == nil:
			at := tk
			if lbk, _ := pair(tpl, "labels"); lbk != nil {
				at = lbk
			}
			if at == nil {
				at = sel.Content[i]
			}
			out = append(out, keyDiag(at, SevError, "selector-mismatch",
				fmt.Sprintf("The template has no label %s=%s, which the selector requires. The API server rejects this", key, want)))
		case lv.Value != want:
			dg := e.valueDiag(lv, SevError, "selector-mismatch",
				fmt.Sprintf("Template label %s=%s does not match the selector %s=%s. The API server rejects this", key, lv.Value, key, want))
			dg.Fix = "Set the label to " + want
			dg.run = replaceValue(lv, want)
			out = append(out, dg)
		}
	}
	return out
}

// ---- Kustomizations ----

func (e *lintEnv) kustomization(d *Doc, k *Kust) []Diag {
	var out []Diag
	for _, r := range k.Refs {
		if r.Remote || r.Exists {
			continue
		}
		msg := r.Raw + " does not exist"
		if r.NoKust {
			msg = r.Raw + " has no kustomization.yaml"
		}
		out = append(out, Diag{Line: r.Line, Col: r.Col, End: r.Col + len(r.Raw), Sev: SevError, Code: "missing-path", Msg: msg})
	}
	deprecated := map[string]string{
		"bases":                 "bases is deprecated. Move the entries to resources",
		"commonLabels":          "commonLabels is deprecated. Use labels with includeSelectors: true",
		"patchesStrategicMerge": "patchesStrategicMerge is deprecated. Use patches",
		"patchesJson6902":       "patchesJson6902 is deprecated. Use patches",
		"vars":                  "vars is deprecated. Use replacements",
	}
	if d.Root != nil && d.Root.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(d.Root.Content); i += 2 {
			if msg, ok := deprecated[d.Root.Content[i].Value]; ok {
				out = append(out, keyDiag(d.Root.Content[i], SevInfo, "deprecated", msg))
			}
		}
	}
	if e.build != "" && len(out) == 0 {
		line := d.Start
		if rk, _ := pair(d.Root, "resources"); rk != nil {
			line = rk.Line
		}
		out = append(out, Diag{Line: line, Col: 0, End: -1, Sev: SevError, Code: "kustomize", Msg: "kustomize build fails: " + kustSummary(e.build)})
	}
	return out
}
