package kube

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/jsonpath"
)

type crdColumn struct {
	col  Col
	path *jsonpath.JSONPath
	typ  string
	name string
}

type crdPrinter struct {
	cols []crdColumn
}

var statusLike = map[string]bool{"ready": true, "status": true, "sync": true, "synced": true, "health": true,
	"phase": true, "state": true, "available": true, "healthy": true, "reconciled": true}

// customKind builds a Kind for one CRD.
func customKind(c *apiextv1.CustomResourceDefinition) *Kind {
	v := crdVersion(c)
	if v == nil {
		return nil
	}
	namespaced := c.Spec.Scope == apiextv1.NamespaceScoped
	k := &Kind{
		Name:       c.Spec.Names.Plural + "." + c.Spec.Group,
		Label:      kindLabel(c.Spec.Names.Kind) + "s",
		Kind:       c.Spec.Names.Kind,
		Group:      c.Spec.Group,
		Version:    v.Name,
		Resource:   c.Spec.Names.Plural,
		Namespaced: namespaced,
		Section:    "crd",
		Actions:    actDefault,
		Custom:     true,
	}
	if strings.HasSuffix(c.Spec.Names.Kind, "s") {
		k.Label = kindLabel(c.Spec.Names.Kind) + "es"
	} else if strings.HasSuffix(c.Spec.Names.Kind, "y") {
		k.Label = kindLabel(strings.TrimSuffix(c.Spec.Names.Kind, "y")) + "ies"
	}
	if len(c.Spec.Names.ShortNames) > 0 {
		k.Short = c.Spec.Names.ShortNames[0]
	}
	p := &crdPrinter{}
	sig := []string{k.Label, k.Version, string(c.Spec.Scope)}
	cols := []Col{cName}
	if namespaced {
		cols = append(cols, cNs)
	}
	for _, pc := range v.AdditionalPrinterColumns {
		if pc.Priority > 0 || strings.EqualFold(pc.Name, "Age") {
			continue
		}
		jp := jsonpath.New(pc.Name).AllowMissingKeys(true)
		if err := jp.Parse("{" + pc.JSONPath + "}"); err != nil {
			continue
		}
		typ := CText
		switch {
		case pc.Type == "date":
			typ = CTime
		case pc.Type == "integer" || pc.Type == "number":
			typ = CNum
		case statusLike[strings.ToLower(pc.Name)]:
			typ = CStatus
		case strings.Contains(pc.JSONPath, "image") || strings.Contains(pc.JSONPath, "url") || strings.Contains(pc.JSONPath, "revision"):
			typ = CMono
		}
		sig = append(sig, pc.Name, pc.Type, pc.JSONPath)
		cc := crdColumn{col: col(pc.Name, typ), path: jp, typ: pc.Type, name: pc.Name}
		p.cols = append(p.cols, cc)
		cols = append(cols, cc.col)
	}
	cols = append(cols, cAge)
	k.Cols = cols
	k.crd = p
	k.sig = strings.Join(sig, "\x00")
	k.row = func(_ *Cluster, obj any) *Row { return p.row(namespaced, obj) }
	return k
}

func (p *crdPrinter) row(namespaced bool, obj any) *Row {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	b := begin(u, len(p.cols)+3)
	b.skip()
	if namespaced {
		b.skip()
	}
	for _, c := range p.cols {
		v := p.value(c, u.Object)
		switch c.col.T {
		case CTime:
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				b.add(strconv.FormatInt(t.Unix(), 10), tNone)
			} else {
				b.add("", tNone)
			}
		case CStatus:
			t := statusTone(v)
			if v == "True" {
				t = tOK
			}
			b.add(orDash(v), t)
		default:
			b.text(v)
		}
	}
	b.skip()
	return b.done(-1)
}

func (p *crdPrinter) value(c crdColumn, obj map[string]any) string {
	res, err := c.path.FindResults(obj)
	if err != nil || len(res) == 0 {
		return ""
	}
	var parts []string
	for _, r := range res {
		for _, v := range r {
			if !v.IsValid() || !v.CanInterface() {
				continue
			}
			parts = append(parts, fmtValue(v.Interface()))
		}
	}
	return strings.Join(parts, ", ")
}

func fmtValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return boolStr(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice {
		return fmt.Sprintf("%v", v)
	}
	return fmt.Sprint(v)
}

// customKinds builds the kinds for all CRDs, sorted by label.
func customKinds(crds []*apiextv1.CustomResourceDefinition) []*Kind {
	var out []*Kind
	seen := map[string]int{}
	for _, c := range crds {
		if k := customKind(c); k != nil {
			out = append(out, k)
			seen[k.Label]++
		}
	}
	for _, k := range out {
		if seen[k.Label] > 1 {
			k.Label += " (" + k.Group + ")"
			k.sig += "\x00" + k.Label
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
