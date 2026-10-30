package mcpserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"st8ks/internal/kube"
)

const lastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// RedactSecrets replaces every value of a Secret in the YAML with a note of
// its byte length. The keys stay. It also redacts the Secrets in the items
// of a list. Other objects come back unchanged. YAML that does not parse is
// an error, so that a value never passes by mistake.
func RedactSecrets(text string) (string, error) {
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(text), &obj); err != nil {
		return "", errors.New("st8ks cannot read the YAML of this object, so it does not show it")
	}
	if !redact(obj) {
		return text, nil
	}
	b, err := yaml.Marshal(obj)
	if err != nil {
		return "", errors.New("st8ks cannot write the redacted YAML of this object, so it does not show it")
	}
	return string(b), nil
}

// redact changes obj in place and reports if it held a Secret.
func redact(obj map[string]any) bool {
	found := false
	if items, ok := obj["items"].([]any); ok {
		for _, it := range items {
			if m, ok := it.(map[string]any); ok && redact(m) {
				found = true
			}
		}
	}
	if obj["kind"] != "Secret" {
		return found
	}
	if m, ok := obj["data"].(map[string]any); ok {
		for k, v := range m {
			s, _ := v.(string)
			n := len(s)
			if b, err := base64.StdEncoding.DecodeString(s); err == nil {
				n = len(b)
			}
			m[k] = redacted(n)
		}
	}
	if m, ok := obj["stringData"].(map[string]any); ok {
		for k, v := range m {
			s, _ := v.(string)
			m[k] = redacted(len(s))
		}
	}
	// kubectl apply keeps the whole object, values included, in this
	// annotation.
	if md, ok := obj["metadata"].(map[string]any); ok {
		if a, ok := md["annotations"].(map[string]any); ok {
			if _, ok := a[lastApplied]; ok {
				a[lastApplied] = "<redacted>"
			}
		}
	}
	return true
}

func redacted(n int) string {
	return fmt.Sprintf("<redacted: %d bytes>", n)
}

var edgeVerb = map[string]string{"o": "is owned by", "r": "routes to", "u": "uses"}

// TopologyText writes the topology of a namespace as text: the objects by
// column, then one line for each edge.
func TopologyText(t kube.Topology) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Topology of namespace %s: %d objects, %d edges.\n", t.NS, len(t.Nodes), len(t.Edges))
	if t.Truncated {
		b.WriteString("The namespace has more pods than the graph shows.\n")
	}
	if len(t.Nodes) == 0 {
		b.WriteString("The namespace has no ingresses, services or running pods.\n")
		return b.String()
	}
	byID := map[string]kube.TopoNode{}
	cols := make([][]kube.TopoNode, len(t.Cols))
	for _, n := range t.Nodes {
		byID[n.ID] = n
		if n.Col >= 0 && n.Col < len(cols) {
			cols[n.Col] = append(cols[n.Col], n)
		}
	}
	for i, col := range cols {
		if len(col) == 0 {
			continue
		}
		sort.Slice(col, func(a, b int) bool { return col[a].Type+col[a].Name < col[b].Type+col[b].Name })
		fmt.Fprintf(&b, "\n%s:\n", t.Cols[i])
		for _, n := range col {
			b.WriteString("- " + label(n))
			if n.Sub != "" {
				b.WriteString(", " + n.Sub)
			}
			switch n.Tone {
			case "e":
				b.WriteString(" [error]")
			case "w":
				b.WriteString(" [warning]")
			}
			b.WriteByte('\n')
		}
	}
	if len(t.Edges) > 0 {
		b.WriteString("\nEdges:\n")
		for _, e := range t.Edges {
			a, okA := byID[e.A]
			z, okB := byID[e.B]
			if !okA || !okB {
				continue
			}
			fmt.Fprintf(&b, "- %s %s %s\n", label(a), edgeVerb[e.T], label(z))
		}
	}
	return b.String()
}

func label(n kube.TopoNode) string { return n.Type + " " + n.Name }
