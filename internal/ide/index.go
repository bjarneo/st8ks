package ide

import (
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Parsed is one parsed file of the workspace.
type Parsed struct {
	Path  string
	Text  string
	Lines []string
	Docs  []*Doc
	hash  uint64
}

// Kust is a kustomization file and the paths it references.
type Kust struct {
	Path       string
	Dir        string
	Namespace  string
	NamePrefix string
	NameSuffix string
	Refs       []KustRef
	Images     []KustImage
}

// KustRef is one path in a kustomization.
type KustRef struct {
	Field     string // resources, components, patches and so on
	Raw       string
	Path      string // the workspace path
	Line, Col int
	Dir       bool
	Exists    bool
	NoKust    bool // a directory without a kustomization file
	Remote    bool
}

// KustImage is one entry of the images list.
type KustImage struct {
	Name, NewName, NewTag, Digest string
	Tag                           *yaml.Node // the newTag value
}

// Target is how a kustomization build changes the objects of a file.
type Target struct {
	Root   string // the kustomization that builds it
	NS     string
	Prefix string
	Suffix string
}

// Index holds the facts that the checks need from other files.
type Index struct {
	Files   map[string]*Parsed
	Kusts   map[string]*Kust
	Targets map[string][]Target // file to the builds that include it
	Images  map[string][]string // file to the image names that builds rewrite
	Patches map[string]bool
	HPAs    map[string]string // Kind/name to the file of the HPA
	CRDs    map[string]bool   // group/Kind of CRDs in the workspace
	Charts  []string          // Helm chart directories
	all     map[string]bool
	dirs    map[string]bool
}

var kustNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// IsKustomization reports if a file name is a kustomization file.
func IsKustomization(p string) bool {
	b := baseName(p)
	for _, n := range kustNames {
		if b == n {
			return true
		}
	}
	return false
}

func isRemote(raw string) bool {
	return strings.Contains(raw, "://") || strings.HasPrefix(raw, "github.com/") || strings.HasPrefix(raw, "git@") ||
		strings.Contains(raw, "?ref=") || strings.Contains(raw, ".git//")
}

// BuildIndex indexes the parsed files. files lists every file of the
// workspace. stat answers for paths outside the workspace.
func BuildIndex(files []string, parsed map[string]*Parsed, stat func(p string) (isDir, ok bool)) *Index {
	ix := &Index{
		Files: parsed, Kusts: map[string]*Kust{}, Targets: map[string][]Target{}, Images: map[string][]string{},
		Patches: map[string]bool{}, HPAs: map[string]string{}, CRDs: map[string]bool{},
		all: make(map[string]bool, len(files)), dirs: map[string]bool{},
	}
	for _, f := range files {
		ix.all[f] = true
		for d := path.Dir(f); d != "." && d != "/" && !ix.dirs[d]; d = path.Dir(d) {
			ix.dirs[d] = true
		}
		if baseName(f) == "Chart.yaml" {
			ix.Charts = append(ix.Charts, path.Dir(f))
		}
	}
	for p, f := range parsed {
		if IsKustomization(p) {
			for _, d := range f.Docs {
				if d.Root != nil && d.Root.Kind == yaml.MappingNode {
					ix.Kusts[p] = ix.parseKust(p, d, stat)
					break
				}
			}
			continue
		}
		for _, d := range f.Docs {
			switch d.Kind {
			case "HorizontalPodAutoscaler":
				if t := dig(d.Root, "spec", "scaleTargetRef"); t != nil {
					ix.HPAs[scalar(t, "kind")+"/"+scalar(t, "name")] = p
				}
			case "CustomResourceDefinition":
				ix.CRDs[scalar(dig(d.Root, "spec"), "group")+"/"+scalar(dig(d.Root, "spec", "names"), "kind")] = true
			}
		}
	}
	ix.walkBuilds()
	return ix
}

func (ix *Index) kustIn(dir string) string {
	for _, n := range kustNames {
		if p := path.Join(dir, n); ix.all[p] {
			return p
		}
	}
	return ""
}

func (ix *Index) parseKust(p string, d *Doc, stat func(string) (bool, bool)) *Kust {
	k := &Kust{Path: p, Dir: path.Dir(p), Namespace: scalar(d.Root, "namespace"),
		NamePrefix: scalar(d.Root, "namePrefix"), NameSuffix: scalar(d.Root, "nameSuffix")}
	add := func(field string, n *yaml.Node) {
		if n == nil || n.Kind != yaml.ScalarNode || n.Value == "" || strings.Contains(n.Value, "\n") {
			return
		}
		r := KustRef{Field: field, Raw: n.Value, Line: n.Line, Col: n.Column}
		if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 {
			r.Col++
		}
		if isRemote(n.Value) {
			r.Remote = true
			k.Refs = append(k.Refs, r)
			return
		}
		r.Path = path.Clean(path.Join(k.Dir, n.Value))
		switch {
		case ix.all[r.Path]:
			r.Exists = true
		case ix.dirs[r.Path]:
			r.Exists, r.Dir = true, true
		case strings.HasPrefix(r.Path, "../") || r.Path == "..":
			if stat != nil {
				r.Dir, r.Exists = stat(r.Path)
			}
			if r.Exists && r.Dir {
				r.Remote = true // outside the workspace: exists, but not indexed
			}
		}
		if r.Dir && !r.Remote && (field == "resources" || field == "bases" || field == "components") && ix.kustIn(r.Path) == "" {
			r.NoKust = true
			r.Exists = false
		}
		k.Refs = append(k.Refs, r)
	}
	for _, f := range []string{"resources", "bases", "components", "crds"} {
		for _, n := range stringItems(get(d.Root, f)) {
			add(f, n)
		}
	}
	for _, it := range items(get(d.Root, "patches")) {
		add("patches", get(it, "path"))
	}
	for _, it := range items(get(d.Root, "patchesJson6902")) {
		add("patchesJson6902", get(it, "path"))
	}
	for _, n := range stringItems(get(d.Root, "patchesStrategicMerge")) {
		if !strings.Contains(n.Value, ":") || strings.HasSuffix(n.Value, ".yaml") || strings.HasSuffix(n.Value, ".yml") {
			add("patchesStrategicMerge", n)
		}
	}
	for _, gen := range []string{"configMapGenerator", "secretGenerator"} {
		for _, g := range items(get(d.Root, gen)) {
			for _, n := range stringItems(get(g, "files")) {
				v := n.Value
				if i := strings.Index(v, "="); i >= 0 {
					c := *n
					c.Value = v[i+1:]
					c.Column += i + 1
					add(gen, &c)
					continue
				}
				add(gen, n)
			}
			for _, n := range stringItems(get(g, "envs")) {
				add(gen, n)
			}
			add(gen, get(g, "env"))
		}
	}
	for _, it := range items(get(d.Root, "images")) {
		k.Images = append(k.Images, KustImage{Name: scalar(it, "name"), NewName: scalar(it, "newName"),
			NewTag: scalar(it, "newTag"), Digest: scalar(it, "digest"), Tag: get(it, "newTag")})
	}
	return k
}

// walkBuilds follows every top kustomization down to the files it builds.
func (ix *Index) walkBuilds() {
	included := map[string]bool{}
	for _, k := range ix.Kusts {
		for _, r := range k.Refs {
			if r.Dir && !r.Remote {
				if p := ix.kustIn(r.Path); p != "" {
					included[p] = true
				}
			}
		}
	}
	roots := make([]string, 0, len(ix.Kusts))
	for p := range ix.Kusts {
		if !included[p] {
			roots = append(roots, p)
		}
	}
	sort.Strings(roots)
	for _, root := range roots {
		ix.walk(ix.Kusts[root], Target{Root: root}, nil, map[string]bool{})
	}
	for f, ts := range ix.Targets {
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].Root < ts[j].Root })
		ix.Targets[f] = ts
	}
}

func (ix *Index) walk(k *Kust, t Target, images []string, seen map[string]bool) {
	if k == nil || seen[k.Path] {
		return
	}
	seen[k.Path] = true
	defer delete(seen, k.Path)
	if t.NS == "" {
		t.NS = k.Namespace
	}
	t.Prefix += k.NamePrefix
	t.Suffix = k.NameSuffix + t.Suffix
	for _, im := range k.Images {
		images = append(images, im.Name)
	}
	ix.addTarget(k.Path, t, images)
	for _, r := range k.Refs {
		if !r.Exists || r.Remote {
			continue
		}
		if r.Dir {
			ix.walk(ix.Kusts[ix.kustIn(r.Path)], t, images, seen)
			continue
		}
		switch r.Field {
		case "patches", "patchesJson6902", "patchesStrategicMerge":
			ix.Patches[r.Path] = true
		case "configMapGenerator", "secretGenerator":
			continue
		}
		ix.addTarget(r.Path, t, images)
	}
}

func (ix *Index) addTarget(f string, t Target, images []string) {
	for _, x := range ix.Targets[f] {
		if x == t {
			return
		}
	}
	ix.Targets[f] = append(ix.Targets[f], t)
	for _, im := range images {
		if !contains(ix.Images[f], im) {
			ix.Images[f] = append(ix.Images[f], im)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// imageSet reports if a build that includes the file rewrites an image.
func (ix *Index) imageSet(file, name string) bool {
	return contains(ix.Images[file], name)
}

// InChart reports if a file belongs to a Helm chart.
func (ix *Index) InChart(p string) bool {
	for _, c := range ix.Charts {
		if c == "." || strings.HasPrefix(p, c+"/") {
			return true
		}
	}
	return false
}

// skip reports if a file is a template that the checks cannot read.
func (ix *Index) skip(p, text string) bool {
	return ix.InChart(p) || strings.Contains(text, "{{")
}

// Builds returns the top kustomizations that build a file.
func (ix *Index) Builds(file string) []string {
	var out []string
	for _, t := range ix.Targets[file] {
		if t.Root != file && !contains(out, t.Root) {
			out = append(out, t.Root)
		}
	}
	return out
}
