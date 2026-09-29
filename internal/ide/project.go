package ide

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

// lineEdit replaces the lines from..to, inclusive, with new lines.
type lineEdit struct {
	from, to int
	lines    []string
}

// projector builds the live side of a file diff. It keeps the layout of the
// file and changes only the values that differ from the live object.
type projector struct {
	lines []string
	d     *Doc
	edits []lineEdit
}

// Project returns the file text as the cluster has it: each value that the
// file sets is replaced by the live value, and fields and documents that the
// cluster does not have are removed. Fields that only the live object has
// are not shown. live holds one object per document, or nil.
func Project(lines []string, docs []*Doc, live []map[string]any) (string, int) {
	var edits []lineEdit
	for i, d := range docs {
		if !d.IsObject() {
			continue
		}
		obj := live[i]
		if obj == nil {
			edits = append(edits, lineEdit{from: d.Start, to: d.End - 1})
			continue
		}
		p := &projector{lines: lines, d: d}
		p.mapping(d.Root, obj, nil)
		edits = append(edits, p.edits...)
	}
	return applyEdits(lines, edits)
}

func applyEdits(lines []string, edits []lineEdit) (string, int) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].from < edits[j].from })
	var out []string
	changed := 0
	at := 0
	for _, e := range edits {
		if e.from < at || e.to >= len(lines) {
			continue
		}
		out = append(out, lines[at:e.from]...)
		out = append(out, e.lines...)
		changed += e.to - e.from + 1 + len(e.lines)
		at = e.to + 1
	}
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n"), changed
}

// skipField lists fields that the comparison ignores: identity, status and
// fields that the API server sets.
func skipField(path []string, key string) bool {
	switch len(path) {
	case 0:
		return key == "status" || key == "apiVersion" || key == "kind"
	case 1:
		if path[0] == "metadata" {
			switch key {
			case "name", "namespace", "resourceVersion", "uid", "generation", "creationTimestamp", "managedFields", "selfLink":
				return true
			}
		}
	}
	return false
}

func (p *projector) del(from, to int) {
	p.edits = append(p.edits, lineEdit{from: from, to: to})
}

func (p *projector) mapping(n *yaml.Node, live map[string]any, path []string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if skipField(path, k.Value) {
			continue
		}
		lv, ok := live[k.Value]
		if !ok || (lv == nil && v.Tag != "!!null") {
			p.del(k.Line, p.d.EndOf(v))
			continue
		}
		p.value(k, v, lv, append(append([]string{}, path...), k.Value))
	}
}

// value compares one value. k is nil for a list item.
func (p *projector) value(k, v *yaml.Node, lv any, path []string) {
	switch v.Kind {
	case yaml.MappingNode:
		if m, ok := lv.(map[string]any); ok && v.Style&yaml.FlowStyle == 0 {
			p.mapping(v, m, path)
			return
		}
		if m, ok := lv.(map[string]any); ok && equalDecoded(v, m) {
			return
		}
	case yaml.SequenceNode:
		if a, ok := lv.([]any); ok && v.Style&yaml.FlowStyle == 0 {
			p.sequence(v, a, path)
			return
		}
		if a, ok := lv.([]any); ok && equalDecoded(v, a) {
			return
		}
	case yaml.ScalarNode:
		if equalScalar(v, lv, path) {
			return
		}
		if v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 && p.d.EndOf(v) == v.Line {
			if _, isMap := lv.(map[string]any); !isMap {
				if _, isList := lv.([]any); !isList {
					l := p.lines[v.Line]
					end := spanEnd(l, v.Column)
					p.edits = append(p.edits, lineEdit{from: v.Line, to: v.Line, lines: []string{l[:v.Column] + formatScalar(lv, v) + l[end:]}})
					return
				}
			}
		}
	default:
		return
	}
	p.replace(k, v, lv)
}

// replace renders the live value in place of a key and its value, or of a
// list item.
func (p *projector) replace(k, v *yaml.Node, lv any) {
	start := v.Line
	col := v.Column
	var r []string
	if k != nil {
		start, col = k.Line, k.Column
		r = render(map[string]any{k.Value: lv})
	} else {
		r = render(lv)
	}
	if len(r) == 0 || start >= len(p.lines) || col > len(p.lines[start]) {
		return
	}
	prefix := p.lines[start][:col]
	pad := strings.Repeat(" ", col)
	out := make([]string, len(r))
	for i, l := range r {
		if i == 0 {
			out[i] = prefix + l
		} else {
			out[i] = pad + l
		}
	}
	p.edits = append(p.edits, lineEdit{from: start, to: p.d.EndOf(v), lines: out})
}

// mergeKeys are the keys that identify items of the common lists, as
// strategic merge patch does.
var mergeKeys = []string{"name", "mountPath", "containerPort", "devicePath", "ip", "port"}

func mergeKey(n *yaml.Node) string {
	if len(n.Content) == 0 {
		return ""
	}
	for _, key := range mergeKeys {
		ok := true
		for _, it := range n.Content {
			if v := get(it, key); v == nil || v.Kind != yaml.ScalarNode {
				ok = false
				break
			}
		}
		if ok {
			return key
		}
	}
	return ""
}

func (p *projector) sequence(n *yaml.Node, live []any, path []string) {
	key := mergeKey(n)
	for i, it := range n.Content {
		var lv any
		found := false
		if key != "" {
			want := scalar(it, key)
			for _, x := range live {
				if m, ok := x.(map[string]any); ok && m[key] != nil && fmt.Sprint(m[key]) == want {
					lv, found = m, true
					break
				}
			}
		} else if i < len(live) {
			lv, found = live[i], true
		}
		if !found {
			p.del(it.Line, p.d.EndOf(it))
			continue
		}
		p.value(nil, it, lv, append(append([]string{}, path...), "["+strconv.Itoa(i)+"]"))
	}
}

func render(v any) []string {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
}

func formatScalar(v any, orig *yaml.Node) string {
	if s, ok := v.(string); ok {
		switch {
		case orig.Style&yaml.DoubleQuotedStyle != 0:
			return strconv.Quote(s)
		case orig.Style&yaml.SingleQuotedStyle != 0:
			return "'" + strings.ReplaceAll(s, "'", "''") + "'"
		}
	}
	r := render(v)
	if len(r) == 1 {
		return r[0]
	}
	return fmt.Sprint(v)
}

// quantityPath reports if a path holds resource quantities.
func quantityPath(path []string) bool {
	for _, s := range path {
		switch s {
		case "resources", "limits", "requests", "hard", "capacity", "overhead":
			return true
		}
	}
	return false
}

// equalScalar compares a file scalar with a live value.
func equalScalar(n *yaml.Node, lv any, path []string) bool {
	var fv any
	if err := n.Decode(&fv); err != nil {
		return false
	}
	switch x := lv.(type) {
	case nil:
		return fv == nil
	case string:
		if s, ok := fv.(string); ok && s == x {
			return true
		}
	case bool:
		if b, ok := fv.(bool); ok {
			return b == x
		}
		return n.Style == 0 && boolWords[n.Value] && yaml11True(n.Value) == x
	case int64, int, float64:
		if f, ok := toFloat(fv); ok {
			if g, ok := toFloat(x); ok && f == g {
				return true
			}
		}
	}
	if quantityPath(path) {
		a, errA := resource.ParseQuantity(fmt.Sprint(fv))
		b, errB := resource.ParseQuantity(fmt.Sprint(lv))
		if errA == nil && errB == nil {
			return a.Cmp(b) == 0
		}
	}
	return fmt.Sprint(fv) == fmt.Sprint(lv) && isStringy(fv) == isStringy(lv)
}

// yaml11True reads a YAML 1.1 boolean word.
func yaml11True(s string) bool {
	switch strings.ToLower(s) {
	case "y", "yes", "on", "true":
		return true
	}
	return false
}

func isStringy(v any) bool {
	_, ok := v.(string)
	return ok
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// equalDecoded compares a flow collection with a live value.
func equalDecoded(n *yaml.Node, lv any) bool {
	var fv any
	if err := n.Decode(&fv); err != nil {
		return false
	}
	return fmt.Sprint(normalize(fv)) == fmt.Sprint(normalize(lv))
}

// normalize makes decoded YAML and JSON values comparable.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return v
}

// liveAt returns the live value at a document path, following the same
// list matching as the projection.
func liveAt(d *Doc, live map[string]any, path []string) (any, bool) {
	var cur any = live
	n := d.Root
	for _, seg := range path {
		if strings.HasPrefix(seg, "[") {
			i, _ := strconv.Atoi(seg[1 : len(seg)-1])
			a, ok := cur.([]any)
			if !ok || n == nil || n.Kind != yaml.SequenceNode || i >= len(n.Content) {
				return nil, false
			}
			item := n.Content[i]
			if key := mergeKey(n); key != "" {
				want := scalar(item, key)
				found := false
				for _, x := range a {
					if m, ok := x.(map[string]any); ok && fmt.Sprint(m[key]) == want {
						cur, found = m, true
						break
					}
				}
				if !found {
					return nil, false
				}
			} else if i < len(a) {
				cur = a[i]
			} else {
				return nil, false
			}
			n = item
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
		n = get(n, seg)
	}
	return cur, true
}
