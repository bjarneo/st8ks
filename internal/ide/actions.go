package ide

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// Line is one line of output. The tone is ok, er, wa, fa or tx, and empty
// for muted text.
type Line struct {
	T string `json:"t"`
	C string `json:"c,omitempty"`
}

// Run is the output of an action.
type Run struct {
	OK    bool   `json:"ok"`
	Lines []Line `json:"lines"`
}

// ---- Live cache ----

type cached[T any] struct {
	at  time.Time
	val T
}

// liveCache keeps live lookups for a short time, because the inspector
// asks on every cursor move.
type liveCache struct {
	mu      sync.Mutex
	objs    map[Ref]cached[map[string]any]
	status  map[Ref]cached[*LiveStatus]
	renders map[string]cached[renderResult]
}

type renderResult struct {
	key  uint64
	list []Rendered
	err  string
}

func (c *liveCache) init() {
	c.objs = map[Ref]cached[map[string]any]{}
	c.status = map[Ref]cached[*LiveStatus]{}
	c.renders = map[string]cached[renderResult]{}
}

func (c *liveCache) reset() {
	c.mu.Lock()
	c.init()
	c.mu.Unlock()
}

const liveTTL = 2 * time.Second

func (c *liveCache) obj(cl Cluster, r Ref) map[string]any {
	c.mu.Lock()
	if v, ok := c.objs[r]; ok && time.Since(v.at) < liveTTL {
		c.mu.Unlock()
		return v.val
	}
	c.mu.Unlock()
	o, err := cl.Live(r)
	if err != nil {
		o = nil
	}
	c.mu.Lock()
	c.objs[r] = cached[map[string]any]{at: time.Now(), val: o}
	c.mu.Unlock()
	return o
}

func (c *liveCache) stat(cl Cluster, r Ref) *LiveStatus {
	c.mu.Lock()
	if v, ok := c.status[r]; ok && time.Since(v.at) < liveTTL {
		c.mu.Unlock()
		return v.val
	}
	c.mu.Unlock()
	st := cl.Status(r)
	c.mu.Lock()
	c.status[r] = cached[*LiveStatus]{at: time.Now(), val: st}
	c.mu.Unlock()
	return st
}

// forget drops cached live data after an apply.
func (c *liveCache) forget() {
	c.mu.Lock()
	c.objs = map[Ref]cached[map[string]any]{}
	c.status = map[Ref]cached[*LiveStatus]{}
	c.mu.Unlock()
}

// findLive returns the first live object of a document.
func (s *Service) findLive(cl Cluster, ix *Index, p string, d *Doc) (Ref, map[string]any) {
	for _, r := range refs(ix, cl, p, d) {
		if o := s.cache.obj(cl, r); o != nil {
			return r, o
		}
	}
	return Ref{}, nil
}

func (s *Service) index() *Index {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ix == nil {
		return BuildIndex(nil, map[string]*Parsed{}, nil)
	}
	return s.ix
}

// ---- Inspect ----

// FieldInfo describes the field at the cursor.
type FieldInfo struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Doc     string `json:"doc"`
	Value   string `json:"value"`
	Live    string `json:"live"`
	HasLive bool   `json:"hasLive"`
	Same    bool   `json:"same"`
}

// Inspect is what the inspector shows for the cursor position.
type Inspect struct {
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	APIVersion string      `json:"apiVersion"`
	Where      string      `json:"where"`
	Doc        int         `json:"doc"`
	Docs       int         `json:"docs"`
	Crumbs     []string    `json:"crumbs"`
	Kust       bool        `json:"kust"`
	Renders    []Rendered  `json:"renders"`
	RenderErr  string      `json:"renderErr"`
	Ref        *Ref        `json:"ref,omitempty"`
	Live       *LiveStatus `json:"live,omitempty"`
	LiveMsg    string      `json:"liveMsg"`
	Drift      int         `json:"drift"`
	Field      *FieldInfo  `json:"field,omitempty"`
	Schema     string      `json:"schema"`
}

func docAt(docs []*Doc, line int) (int, *Doc) {
	idx := -1
	for i, d := range docs {
		if d.Start <= line {
			idx = i
		}
	}
	if idx < 0 && len(docs) > 0 {
		idx = 0
	}
	if idx < 0 {
		return -1, nil
	}
	return idx, docs[idx]
}

func whereText(ix *Index, p string) string {
	if ix.InChart(p) {
		return p + " · Helm chart source. st8ks does not render charts"
	}
	var parts []string
	for _, t := range ix.Targets[p] {
		if t.Root == p {
			continue
		}
		s := dirOf(t.Root)
		if s == "" {
			s = t.Root
		}
		if t.NS != "" {
			s += " (namespace " + t.NS + ")"
		}
		if !contains(parts, s) {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return p
	}
	return p + " · built by " + strings.Join(parts, ", ")
}

func describe(v any) string {
	switch x := v.(type) {
	case map[string]any:
		return "(map)"
	case []any:
		return "(list)"
	case nil:
		return "null"
	case string:
		return x
	}
	r := render(v)
	if len(r) == 1 {
		return r[0]
	}
	return fmt.Sprint(v)
}

// Inspect describes the document and the field at a line.
func (s *Service) Inspect(p, text string, line int) Inspect {
	docs := ParseDocs(text)
	out := Inspect{Docs: len(docs), Crumbs: []string{}}
	i, d := docAt(docs, line)
	if d == nil {
		return out
	}
	out.Doc = i
	ix := s.index()
	cl := s.cluster()
	if cl != nil {
		out.Schema = "No schema"
		if sd := s.schemaFor(cl, d.APIVersion); sd != nil {
			out.Schema = "Schema of " + cl.Name()
		}
	}
	f := d.FieldAt(line)
	if f != nil {
		out.Crumbs = f.Path
	}
	kust := IsKustomization(p) && isKustDoc(d)
	out.Kust = kust
	var live map[string]any
	switch {
	case kust:
		out.Kind, out.Name = "Kustomization", dirOf(p)
		if out.Name == "" {
			out.Name = "workspace root"
		}
		out.Where = whereText(ix, p)
		out.Renders, out.RenderErr = s.renders(p, text)
		out.LiveMsg = "A kustomization is an input of kustomize build. The list above shows the objects that it builds."
	case d.IsObject():
		out.Kind, out.Name, out.APIVersion = d.Kind, d.Name, d.APIVersion
		out.Where = whereText(ix, p)
		switch {
		case ix.skip(p, text):
			out.LiveMsg = "st8ks does not read templates."
		case cl == nil:
			out.LiveMsg = "Connect to a cluster to see the live object."
		default:
			var ref Ref
			ref, live = s.findLive(cl, ix, p, d)
			if live != nil {
				out.Ref = &ref
				out.Live = s.cache.stat(cl, ref)
				_, out.Drift = Project(strings.Split(text, "\n"), []*Doc{d}, []map[string]any{live})
			} else {
				rs := refs(ix, cl, p, d)
				msg := fmt.Sprintf("%s %s is not on %s", d.Kind, d.Name, cl.Name())
				if len(rs) > 0 && rs[0].NS != "" {
					msg = fmt.Sprintf("%s %s is not in namespace %s on %s", d.Kind, rs[0].Name, rs[0].NS, cl.Name())
				}
				out.LiveMsg = msg + ". An apply creates it."
			}
		}
	default:
		out.Kind = "YAML"
		out.Name = path.Base(p)
		out.Where = p
	}
	if f != nil && (kust || d.IsObject()) {
		out.Field = s.field(cl, d, f, kust, live)
	}
	return out
}

func (s *Service) field(cl Cluster, d *Doc, f *Field, kust bool, live map[string]any) *FieldInfo {
	fi := &FieldInfo{Path: PathString(f.Path)}
	key := ""
	for i := len(f.Path) - 1; i >= 0; i-- {
		if !strings.HasPrefix(f.Path[i], "[") {
			key = f.Path[i]
			break
		}
	}
	if !kust && cl != nil {
		if sd := s.schemaFor(cl, d.APIVersion); sd != nil {
			if root := sd.Kind(d.Kind); root != nil {
				if fs := sd.At(root, f.Path); fs != nil {
					r := sd.Resolve(fs)
					fi.Type = sd.TypeName(fs)
					fi.Doc = firstSentences(r.Desc, 360)
					// A map value such as limits.memory has only the text of
					// its type, which says little about the field.
					if doc, ok := builtinDoc(false, key); ok && !r.FieldDesc {
						fi.Doc = doc.doc
					}
				}
			}
		}
	}
	if fi.Type == "" {
		if doc, ok := builtinDoc(kust, key); ok {
			fi.Type, fi.Doc = doc.typ, doc.doc
		}
	}
	if f.Val != nil {
		switch f.Val.Kind {
		case yaml.ScalarNode:
			fi.Value = f.Val.Value
		case yaml.MappingNode:
			fi.Value = "(map)"
		case yaml.SequenceNode:
			fi.Value = "(list)"
		}
	}
	if live != nil && f.Val != nil {
		fi.HasLive = true
		if lv, ok := liveAt(d, live, f.Path); ok {
			fi.Live = describe(lv)
			fi.Same = f.Val.Kind != yaml.ScalarNode || equalScalar(f.Val, lv, f.Path)
		} else {
			fi.Live = "not set"
		}
	}
	return fi
}

// renders lists the objects that a kustomization builds.
func (s *Service) renders(p, text string) ([]Rendered, string) {
	w, err := s.workspace()
	if err != nil {
		return nil, err.Error()
	}
	s.mu.Lock()
	ov := make(map[string]string, len(s.overlays)+1)
	for k, v := range s.overlays {
		ov[k] = v
	}
	key := hashString(text) ^ s.ixKey
	s.mu.Unlock()
	ov[p] = text
	s.cache.mu.Lock()
	if c, ok := s.cache.renders[p]; ok && c.val.key == key && time.Since(c.at) < 30*time.Second {
		s.cache.mu.Unlock()
		return c.val.list, c.val.err
	}
	s.cache.mu.Unlock()
	_, list, err := build(w.Root, p, ov, true)
	r := renderResult{key: key, list: list}
	if err != nil {
		r.err = err.Error()
	}
	s.cache.mu.Lock()
	s.cache.renders[p] = cached[renderResult]{at: time.Now(), val: r}
	s.cache.mu.Unlock()
	return r.list, r.err
}

// ---- Live diff ----

// LiveDiff is the live side of the diff of a file.
type LiveDiff struct {
	Text    string `json:"text"`
	Found   int    `json:"found"`
	Objects int    `json:"objects"`
	Msg     string `json:"msg"`
}

// LiveText returns the file as the cluster has it.
func (s *Service) LiveText(p, text string) LiveDiff {
	cl := s.cluster()
	if cl == nil {
		return LiveDiff{Msg: "Connect to a cluster to compare the file with the live objects."}
	}
	ix := s.index()
	if ix.skip(p, text) {
		return LiveDiff{Msg: "st8ks does not compare templates with the cluster."}
	}
	lines := strings.Split(text, "\n")
	docs := ParseDocs(text)
	live := make([]map[string]any, len(docs))
	out := LiveDiff{}
	for i, d := range docs {
		if !d.IsObject() {
			continue
		}
		out.Objects++
		if _, o := s.findLive(cl, ix, p, d); o != nil {
			if d.Kind == "Secret" {
				o = withoutSecretData(o)
			}
			live[i] = o
			out.Found++
		}
	}
	if out.Objects == 0 {
		out.Msg = "The file has no Kubernetes objects."
		return out
	}
	out.Text, _ = Project(lines, docs, live)
	return out
}

// withoutSecretData hides Secret values: the live side shows the file
// values, so the diff never shows a secret.
func withoutSecretData(o map[string]any) map[string]any {
	c := make(map[string]any, len(o))
	for k, v := range o {
		if k != "data" && k != "stringData" {
			c[k] = v
		}
	}
	return c
}

// ---- Apply ----

// Blocked is a check error that stops a dry run or an apply.
type Blocked struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Msg  string `json:"msg"`
}

// Plan is the preview of an apply: a server dry run of every object.
type Plan struct {
	Title   string        `json:"title"`
	Cmd     string        `json:"cmd"`
	Ctx     string        `json:"ctx"`
	Items   []ApplyResult `json:"items"`
	Notes   []string      `json:"notes"`
	Blocked []Blocked     `json:"blocked"`
	NS      []string      `json:"ns"`
}

func (s *Service) cmdLine(p, ctx string, dry bool) string {
	c := "kubectl apply --server-side --field-manager=st8ks"
	if dry {
		c += " --dry-run=server"
	}
	if IsKustomization(p) {
		d := dirOf(p)
		if d == "" {
			d = "."
		}
		c += " -k " + d
	} else {
		c += " -f " + p
	}
	return "$ " + c + " --context " + ctx
}

// blocking returns the check errors of a file and, for a kustomization, of
// the files it builds.
func (s *Service) blocking(p, text string) []Blocked {
	e := s.env(p, text)
	ds := lintStatic(e)
	if e.cl != nil {
		ds = append(ds, lintLive(e, ds)...)
	}
	var out []Blocked
	for _, d := range ds {
		if d.Sev == SevError {
			out = append(out, Blocked{Path: p, Line: d.Line, Msg: d.Msg})
		}
	}
	if k := e.ix.Kusts[p]; k != nil {
		var ms []string
		members(e.ix, k, map[string]bool{}, &ms)
		s.mu.Lock()
		for _, m := range ms {
			if m == p {
				continue
			}
			for _, d := range s.diags[m] {
				if d.Sev == SevError {
					out = append(out, Blocked{Path: m, Line: d.Line, Msg: d.Msg})
				}
			}
		}
		s.mu.Unlock()
	}
	return out
}

// applyNS picks the namespace for a document without one.
func (s *Service) applyNS(cl Cluster, ix *Index, p string, d *Doc) string {
	var nss []string
	for _, t := range ix.Targets[p] {
		if t.NS != "" && !contains(nss, t.NS) {
			nss = append(nss, t.NS)
		}
	}
	for _, ns := range nss {
		if s.cache.obj(cl, Ref{APIVersion: d.APIVersion, Kind: d.Kind, NS: ns, Name: d.Name}) != nil {
			return ns
		}
	}
	if len(nss) == 1 {
		return nss[0]
	}
	if n := cl.DefaultNamespace(); n != "" {
		return n
	}
	return "default"
}

// objects returns the objects that an apply of a file sends.
func (s *Service) objects(cl Cluster, p, text string) ([]map[string]any, error) {
	if IsKustomization(p) {
		w, err := s.workspace()
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		ov := make(map[string]string, len(s.overlays)+1)
		for k, v := range s.overlays {
			ov[k] = v
		}
		s.mu.Unlock()
		ov[p] = text
		objs, _, err := build(w.Root, p, ov, false)
		if err != nil {
			return nil, fmt.Errorf("kustomize build fails: %w", err)
		}
		out := make([]map[string]any, 0, len(objs))
		for _, o := range objs {
			out = append(out, o.Object)
		}
		return out, nil
	}
	ix := s.index()
	lines := strings.Split(text, "\n")
	var out []map[string]any
	for _, d := range ParseDocs(text) {
		if d.Err != "" {
			return nil, fmt.Errorf("line %d: %s", d.ErrLine+1, d.Err)
		}
		if !d.IsObject() {
			continue
		}
		j, err := sigsyaml.YAMLToJSON([]byte(strings.Join(lines[d.Start:d.End], "\n")))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", d.Start+1, err)
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(j); err != nil {
			return nil, fmt.Errorf("line %d: %w", d.Start+1, err)
		}
		if namespaced(cl, d.APIVersion, d.Kind) && u.GetNamespace() == "" {
			u.SetNamespace(s.applyNS(cl, ix, p, d))
		}
		out = append(out, u.Object)
	}
	if len(out) == 0 {
		return nil, errors.New("the file has no Kubernetes objects")
	}
	return out, nil
}

// Plan runs a server dry run and returns what an apply changes.
func (s *Service) Plan(p, text string) (*Plan, error) {
	cl := s.cluster()
	if cl == nil {
		return nil, errors.New("connect to a cluster first")
	}
	name := p
	if parts := strings.Split(p, "/"); len(parts) > 2 {
		name = strings.Join(parts[len(parts)-2:], "/")
	}
	plan := &Plan{Title: "Apply " + name + " to " + cl.Name(), Cmd: s.cmdLine(p, cl.Name(), false), Ctx: cl.Name(),
		Items: []ApplyResult{}, Notes: []string{}, Blocked: []Blocked{}, NS: []string{}}
	if b := s.blocking(p, text); len(b) > 0 {
		plan.Blocked = b
		return plan, nil
	}
	objs, err := s.objects(cl, p, text)
	if err != nil {
		return nil, err
	}
	res := cl.Apply(objs, true, false)
	for i, r := range res {
		if r.Conflict {
			f := cl.Apply(objs[i:i+1], true, true)
			if len(f) == 1 && f[0].Err == "" {
				f[0].Conflict = true
				f[0].Err = r.Err
				res[i] = f[0]
			}
		}
	}
	plan.Items = res
	var managers []string
	for _, r := range res {
		if r.NS != "" && !contains(plan.NS, r.NS) {
			plan.NS = append(plan.NS, r.NS)
		}
		if r.Manager != "" && !contains(managers, r.Manager) {
			managers = append(managers, r.Manager)
		}
	}
	ix := s.index()
	if !IsKustomization(p) {
		if bs := ix.Builds(p); len(bs) > 0 {
			var dirs []string
			for _, b := range bs {
				dirs = append(dirs, dirOf(b))
			}
			plan.Notes = append(plan.Notes, fmt.Sprintf("This file is part of the build in %s. An apply of the file alone skips the changes of that build, such as namespaces, image tags and name prefixes. Apply its kustomization.yaml instead.", strings.Join(dirs, ", ")))
		}
	}
	if len(managers) > 0 {
		note := strings.Join(managers, " and ") + " manages these objects. A sync can revert a change that is not in Git."
		if w, err := s.workspace(); err == nil {
			if head, ok := w.Head(p); !ok || head != text {
				note = strings.Join(managers, " and ") + " manages these objects, and this change is not committed. A sync can revert it unless you commit and push."
			}
		}
		plan.Notes = append(plan.Notes, note)
	}
	return plan, nil
}

// DryRun runs a server dry run and returns kubectl-style output.
func (s *Service) DryRun(p, text string) (Run, error) {
	plan, err := s.Plan(p, text)
	if err != nil {
		return Run{}, err
	}
	out := Run{Lines: []Line{{T: s.cmdLine(p, plan.Ctx, true), C: "tx"}}}
	if len(plan.Blocked) > 0 {
		for _, b := range plan.Blocked {
			out.Lines = append(out.Lines, Line{T: fmt.Sprintf("error: %s:%d: %s", b.Path, b.Line+1, b.Msg), C: "er"})
		}
		n := len(plan.Blocked)
		out.Lines = append(out.Lines, Line{T: fmt.Sprintf("Dry-run failed: %d %s", n, plural(n, "error")), C: "er"})
		return out, nil
	}
	out.OK = true
	for _, r := range plan.Items {
		switch {
		case r.Err != "" && !r.Conflict:
			out.OK = false
			out.Lines = append(out.Lines, Line{T: "error: " + r.Resource + ": " + r.Err, C: "er"})
		case r.Conflict:
			out.Lines = append(out.Lines, Line{T: r.Resource + " " + r.Verb + " (server dry run). Conflicts with other field managers: " + firstLine(r.Err), C: "wa"})
		default:
			c := "ok"
			if r.Verb == "unchanged" {
				c = "fa"
			}
			out.Lines = append(out.Lines, Line{T: r.Resource + " " + r.Verb + " (server dry run)", C: c})
		}
	}
	return out, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// Apply applies a file, then follows the rollouts in the output panel.
func (s *Service) Apply(p, text string, force bool) (Run, error) {
	cl := s.cluster()
	if cl == nil {
		return Run{}, errors.New("connect to a cluster first")
	}
	if b := s.blocking(p, text); len(b) > 0 {
		return Run{}, fmt.Errorf("fix %d %s before you apply", len(b), plural(len(b), "error"))
	}
	objs, err := s.objects(cl, p, text)
	if err != nil {
		return Run{}, err
	}
	cmd := s.cmdLine(p, cl.Name(), false)
	if force {
		cmd = strings.Replace(cmd, "--field-manager=st8ks", "--field-manager=st8ks --force-conflicts", 1)
	}
	out := Run{OK: true, Lines: []Line{{T: cmd, C: "tx"}}}
	res := cl.Apply(objs, false, force)
	s.cache.forget()
	for _, r := range res {
		if r.Err != "" {
			out.OK = false
			out.Lines = append(out.Lines, Line{T: "error: " + r.Resource + ": " + r.Err, C: "er"})
			continue
		}
		c := "ok"
		if r.Verb == "unchanged" {
			c = "fa"
		}
		out.Lines = append(out.Lines, Line{T: r.Resource + " " + r.Verb, C: c})
		if r.Verb != "unchanged" {
			switch r.Ref.Kind {
			case "Deployment", "StatefulSet", "DaemonSet":
				ref := r.Ref
				go cl.Rollout(ref, func(t, tone string) { s.emit("ide:log", Line{T: t, C: tone}) })
			}
		}
	}
	s.poke()
	return out, nil
}

// ---- Git ----

// Commit commits every changed file of the workspace. A failed commit
// returns OK false with the Git output.
func (s *Service) Commit(msg string) (Run, error) {
	w, err := s.workspace()
	if err != nil {
		return Run{}, err
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return Run{}, errors.New("write a commit message first")
	}
	st := s.Refresh()
	var paths []string
	for _, c := range st.Changes {
		paths = append(paths, c.Path)
	}
	text, err := w.Commit(msg, paths)
	out := gitRun("$ git commit -m "+fmt.Sprintf("%q", msg), text, err)
	s.Refresh()
	return out, nil
}

// Push pushes the current branch. A failed push returns OK false with the
// Git output.
func (s *Service) Push() (Run, error) {
	w, err := s.workspace()
	if err != nil {
		return Run{}, err
	}
	st := s.State()
	cmd := "$ git push"
	if st.Upstream == "" {
		cmd += " --set-upstream origin HEAD"
	}
	text, err := w.Push(st.Upstream)
	out := gitRun(cmd, text, err)
	s.Refresh()
	return out, nil
}

func gitRun(cmd, text string, err error) Run {
	out := Run{OK: err == nil, Lines: []Line{{T: cmd, C: "tx"}}}
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimRight(l, " \r"); l != "" {
			out.Lines = append(out.Lines, Line{T: l})
		}
	}
	if err != nil {
		out.Lines = append(out.Lines, Line{T: err.Error(), C: "er"})
	}
	return out
}

// ---- Sources ----

// Source is a place in a workspace file.
type Source struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// FindSource finds the file that defines an object.
func (s *Service) FindSource(kind, ns, name string) (Source, error) {
	s.mu.Lock()
	ix := s.ix
	s.mu.Unlock()
	if ix == nil {
		return Source{}, errors.New("no workspace is open")
	}
	best, bestScore := Source{}, -1
	for p, f := range ix.Files {
		for _, d := range f.Docs {
			if d.Kind != kind || !d.IsObject() {
				continue
			}
			score := -1
			if d.Name == name && (d.Namespace == ns || d.Namespace == "") {
				score = 1
			}
			for _, t := range ix.Targets[p] {
				if t.Prefix+d.Name+t.Suffix == name && (t.NS == "" || t.NS == ns) {
					score = 2
				}
			}
			if score > bestScore || (score == bestScore && score >= 0 && p < best.Path) {
				best, bestScore = Source{Path: p, Line: d.Start}, score
			}
		}
	}
	if bestScore < 0 {
		return Source{}, fmt.Errorf("no file in the workspace defines %s %s", kind, name)
	}
	return best, nil
}
