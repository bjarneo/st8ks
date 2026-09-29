package ide

import (
	"bytes"
	"errors"
	"io"
	"path"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"
)

// overlayFS reads unsaved editor buffers in place of the files on disk.
type overlayFS struct {
	filesys.FileSystem
	root     string
	overlays map[string]string // workspace path to text
}

func (o overlayFS) text(abs string) (string, bool) {
	rel, err := filepath.Rel(o.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	t, ok := o.overlays[filepath.ToSlash(rel)]
	return t, ok
}

func (o overlayFS) ReadFile(p string) ([]byte, error) {
	if t, ok := o.text(p); ok {
		return []byte(t), nil
	}
	return o.FileSystem.ReadFile(p)
}

// Rendered is one object of a kustomize build.
type Rendered struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	NS   string `json:"ns"`
	Path string `json:"path"` // the workspace file it comes from, or ""
}

// build runs kustomize build for the folder of a kustomization file. With
// origins set, it also reports the source file of each object.
func build(root, kustPath string, overlays map[string]string, origins bool) ([]*unstructured.Unstructured, []Rendered, error) {
	dir := path.Dir(kustPath)
	ov := make(map[string]string, len(overlays)+1)
	for k, v := range overlays {
		ov[k] = v
	}
	if origins {
		text, ok := ov[kustPath]
		if !ok {
			b, err := filesys.MakeFsOnDisk().ReadFile(filepath.Join(root, filepath.FromSlash(kustPath)))
			if err != nil {
				return nil, nil, err
			}
			text = string(b)
		}
		if t, err := withOrigins(text); err == nil {
			ov[kustPath] = t
		}
	}
	fsys := overlayFS{FileSystem: filesys.MakeFsOnDisk(), root: root, overlays: ov}
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	rm, err := k.Run(fsys, filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return nil, nil, errors.New(cleanKustErr(err.Error(), root))
	}
	var list []Rendered
	for _, r := range rm.Resources() {
		it := Rendered{Kind: r.GetKind(), Name: r.GetName(), NS: r.GetNamespace()}
		if o, err := r.GetOrigin(); err == nil && o != nil && o.Repo == "" && o.Path != "" {
			it.Path = path.Clean(path.Join(dir, filepath.ToSlash(o.Path)))
		}
		list = append(list, it)
	}
	if origins {
		_ = rm.RemoveOriginAnnotations()
	}
	b, err := rm.AsYaml()
	if err != nil {
		return nil, nil, err
	}
	objs, err := decodeObjects(b)
	return objs, list, err
}

// withOrigins adds buildMetadata: [originAnnotations] to a kustomization.
func withOrigins(text string) (string, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(text), &m); err != nil || m == nil {
		return text, err
	}
	bm, _ := m["buildMetadata"].([]any)
	for _, x := range bm {
		if x == "originAnnotations" {
			return text, nil
		}
	}
	m["buildMetadata"] = append(bm, "originAnnotations")
	b, err := yaml.Marshal(m)
	return string(b), err
}

func cleanKustErr(msg, root string) string {
	msg = strings.ReplaceAll(msg, root+string(filepath.Separator), "")
	return strings.ReplaceAll(msg, root, ".")
}

// kustSummary picks the cause out of a nested kustomize error.
func kustSummary(msg string) string {
	parts := strings.Split(msg, ": ")
	pick := ""
	for _, p := range parts {
		p = trimQuote(p)
		switch {
		case p == "" || strings.HasPrefix(p, "accumulat") || strings.HasPrefix(p, "recursed") || p == "must build at directory":
			continue
		case strings.HasPrefix(p, "security; "):
			return strings.TrimPrefix(p, "security; ") + ". Reference a folder with a kustomization.yaml instead"
		case strings.Contains(p, "not found") || strings.Contains(p, "no such file") || strings.Contains(p, "invalid") || strings.Contains(p, "unable"):
			if pick == "" {
				pick = p
			}
		}
	}
	if pick == "" {
		pick = trimQuote(parts[len(parts)-1])
	}
	if len(pick) > 240 {
		pick = pick[:240] + "…"
	}
	return pick
}

// trimQuote removes quotes that the nesting of kustomize errors leaves
// without a partner.
func trimQuote(p string) string {
	p = strings.TrimSpace(p)
	for strings.Count(p, "'")%2 == 1 {
		switch {
		case strings.HasSuffix(p, "'"):
			p = p[:len(p)-1]
		case strings.HasPrefix(p, "'"):
			p = p[1:]
		default:
			return p
		}
	}
	for len(p) > 1 && strings.HasPrefix(p, "'") && strings.HasSuffix(p, "'") && strings.Count(p, "'") == 2 {
		p = p[1 : len(p)-1]
	}
	return p
}

// decodeObjects reads YAML documents into objects, with the same YAML
// rules as kubectl.
func decodeObjects(b []byte) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	var out []*unstructured.Unstructured
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if len(obj) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: obj})
	}
	return out, nil
}
