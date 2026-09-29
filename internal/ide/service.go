// Package ide is the backend of the IDE mode: a workspace of Kubernetes
// manifests, checks against the schema and the live state of the target
// cluster, kustomize builds, server-side apply, Git and a local terminal.
package ide

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limits of the checks.
const (
	maxLintFiles = 5000
	maxLintSize  = 1 << 20
)

type diskFile struct {
	size int64
	mod  time.Time
	text string
}

type staticCache struct {
	key   uint64
	diags []Diag
}

type buildCache struct {
	err string
}

// Service is the IDE backend. It is safe for concurrent use.
type Service struct {
	emit    func(string, any)
	cluster func() Cluster
	wake    chan struct{}
	stop    chan struct{}

	mu       sync.Mutex
	ws       *Workspace
	gen      int
	state    State
	stateSig uint64
	active   bool
	overlays map[string]string
	parsed   map[string]*Parsed
	ix       *Index
	ixKey    uint64
	builds   map[string]buildCache
	diags    map[string][]Diag
	diagSig  map[string]uint64

	// Only the loop goroutine uses these.
	disk      map[string]diskFile
	static    map[string]staticCache
	buildKeys map[string]uint64
	clRef     Cluster
	loopGen   int

	schemas schemaCache
	cache   liveCache
}

// NewService starts the IDE backend. cluster returns the connected cluster
// or nil.
func NewService(emit func(string, any), cluster func() Cluster) *Service {
	s := &Service{emit: emit, cluster: cluster, wake: make(chan struct{}, 1), stop: make(chan struct{}),
		overlays: map[string]string{}, parsed: map[string]*Parsed{}, builds: map[string]buildCache{},
		diags: map[string][]Diag{}, diagSig: map[string]uint64{}, disk: map[string]diskFile{}, static: map[string]staticCache{},
		buildKeys: map[string]uint64{}}
	s.schemas.init()
	s.cache.init()
	go s.loop()
	return s
}

// Shutdown stops the loop.
func (s *Service) Shutdown() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func hashString(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// ---- Workspace ----

// Open opens a folder as the workspace.
func (s *Service) Open(root string) (State, error) {
	w, err := OpenWorkspace(root)
	if err != nil {
		return State{}, err
	}
	st := w.State()
	if st.Err != "" && len(st.Files) == 0 {
		return State{}, errors.New(st.Err)
	}
	s.mu.Lock()
	s.ws = w
	s.gen++
	s.state = st
	s.stateSig = stateSig(st)
	s.overlays = map[string]string{}
	s.parsed = map[string]*Parsed{}
	s.ix = nil
	s.builds = map[string]buildCache{}
	s.diags = map[string][]Diag{}
	s.diagSig = map[string]uint64{}
	s.mu.Unlock()
	s.poke()
	return st, nil
}

// Close closes the workspace.
func (s *Service) Close() {
	s.mu.Lock()
	s.ws = nil
	s.gen++
	s.state = State{}
	s.overlays = map[string]string{}
	s.parsed = map[string]*Parsed{}
	s.ix = nil
	s.diags = map[string][]Diag{}
	s.diagSig = map[string]uint64{}
	s.mu.Unlock()
}

func (s *Service) workspace() (*Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ws == nil {
		return nil, errors.New("no workspace is open")
	}
	return s.ws, nil
}

// State returns the last known state of the workspace.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Refresh reads the file list and the Git status again.
func (s *Service) Refresh() State {
	s.mu.Lock()
	w, gen := s.ws, s.gen
	s.mu.Unlock()
	if w == nil {
		return State{}
	}
	st := w.State()
	s.setState(gen, st)
	s.poke()
	return st
}

func stateSig(st State) uint64 {
	b, _ := json.Marshal(st)
	return hashString(string(b))
}

func (s *Service) setState(gen int, st State) {
	sig := stateSig(st)
	s.mu.Lock()
	if gen != s.gen || sig == s.stateSig {
		s.mu.Unlock()
		return
	}
	s.state = st
	s.stateSig = sig
	s.mu.Unlock()
	s.emit("ide:ws", st)
}

// SetActive turns the polling of the workspace on or off.
func (s *Service) SetActive(on bool) {
	s.mu.Lock()
	s.active = on
	s.mu.Unlock()
	if on {
		s.poke()
	}
}

// FileData is a file for the editor.
type FileData struct {
	Text    string `json:"text"`
	Head    string `json:"head"`
	HasHead bool   `json:"hasHead"`
}

// Read loads a file and its text in the last commit.
func (s *Service) Read(p string) (FileData, error) {
	w, err := s.workspace()
	if err != nil {
		return FileData{}, err
	}
	t, err := w.Read(p)
	if err != nil {
		return FileData{}, err
	}
	h, ok := w.Head(p)
	return FileData{Text: t, Head: h, HasHead: ok}, nil
}

// Write saves a file.
func (s *Service) Write(p, text string) (State, error) {
	w, err := s.workspace()
	if err != nil {
		return State{}, err
	}
	if err := w.Write(p, text); err != nil {
		return State{}, err
	}
	s.mu.Lock()
	delete(s.overlays, p)
	s.mu.Unlock()
	return s.Refresh(), nil
}

// SetBuffer tells the checks about an unsaved buffer. dirty false removes
// it, so the checks read the file on disk.
func (s *Service) SetBuffer(p, text string, dirty bool) {
	s.mu.Lock()
	if dirty {
		s.overlays[p] = text
	} else {
		delete(s.overlays, p)
	}
	s.mu.Unlock()
	s.poke()
}

// Diags returns the diagnostics of every file.
func (s *Service) Diags() map[string][]Diag {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]Diag, len(s.diags))
	for k, v := range s.diags {
		out[k] = v
	}
	return out
}

// ---- Loop ----

func (s *Service) loop() {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
			t := time.NewTimer(120 * time.Millisecond)
		wait:
			for {
				select {
				case <-s.wake:
					t.Reset(120 * time.Millisecond)
				case <-t.C:
					break wait
				case <-s.stop:
					t.Stop()
					return
				}
			}
			s.cycle(false)
		case <-tick.C:
			s.cycle(true)
		}
	}
}

func isYAML(p string) bool {
	return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml") || baseName(p) == "Kustomization"
}

func (s *Service) cycle(poll bool) {
	s.mu.Lock()
	w, gen, active := s.ws, s.gen, s.active
	s.mu.Unlock()
	if w == nil || (poll && !active) {
		return
	}
	if poll {
		s.setState(gen, w.State())
	}
	s.relint(w, gen)
}

// relint reads changed files, rebuilds the index and runs the checks.
func (s *Service) relint(w *Workspace, gen int) {
	if gen != s.loopGen {
		s.loopGen = gen
		s.disk = map[string]diskFile{}
		s.static = map[string]staticCache{}
		s.buildKeys = map[string]uint64{}
	}
	s.mu.Lock()
	files := s.state.Files
	overlays := make(map[string]string, len(s.overlays))
	for k, v := range s.overlays {
		overlays[k] = v
	}
	old := s.parsed
	s.mu.Unlock()

	parsed := map[string]*Parsed{}
	changed := false
	n := 0
	for _, p := range files {
		if !isYAML(p) {
			continue
		}
		if n++; n > maxLintFiles {
			break
		}
		text, ok := overlays[p]
		if !ok {
			text, ok = s.readDisk(w, p)
			if !ok {
				continue
			}
		}
		h := hashString(text)
		if o := old[p]; o != nil && o.hash == h {
			parsed[p] = o
			continue
		}
		parsed[p] = &Parsed{Path: p, Text: text, Lines: strings.Split(text, "\n"), Docs: ParseDocs(text), hash: h}
		changed = true
	}
	if len(parsed) != len(old) {
		changed = true
	}
	for p := range s.disk {
		if parsed[p] == nil {
			delete(s.disk, p)
		}
	}

	s.mu.Lock()
	if gen != s.gen {
		s.mu.Unlock()
		return
	}
	ix := s.ix
	s.mu.Unlock()
	if changed || ix == nil {
		ix = BuildIndex(files, parsed, func(p string) (bool, bool) {
			st, err := os.Stat(filepath.Join(w.Root, filepath.FromSlash(p)))
			if err != nil {
				return false, false
			}
			return st.IsDir(), true
		})
	}
	ixKey := indexKey(ix)

	cl := s.cluster()
	if cl != s.clRef {
		s.clRef = cl
		s.schemas.reset()
		s.cache.reset()
	}
	clName := ""
	if cl != nil {
		clName = cl.Name()
	}

	builds := s.runBuilds(w, ix, overlays)

	diags := map[string][]Diag{}
	for p, f := range parsed {
		e := &lintEnv{path: p, text: f.Text, lines: f.Lines, docs: f.Docs, ix: ix, cl: cl, build: builds[p]}
		if cl != nil {
			e.schema = func(av string) *SchemaDoc { return s.schemaFor(cl, av) }
		}
		key := hashString(f.Text, clName, e.build) ^ ixKey*31 ^ uint64(s.schemas.version())*131
		sc, ok := s.static[p]
		if !ok || sc.key != key {
			sc = staticCache{key: key, diags: lintStatic(e)}
			s.static[p] = sc
		}
		ds := sc.diags
		if cl != nil {
			cp := append([]Diag{}, ds...)
			if live := lintLive(e, cp); len(live) > 0 || !sameFixes(cp, ds) {
				ds = dedupe(append(cp, live...))
			}
		}
		if len(ds) > 0 {
			diags[p] = ds
		}
	}
	for p := range s.static {
		if parsed[p] == nil {
			delete(s.static, p)
		}
	}

	s.mu.Lock()
	if gen != s.gen {
		s.mu.Unlock()
		return
	}
	s.parsed = parsed
	s.ix = ix
	s.ixKey = ixKey
	s.builds = map[string]buildCache{}
	for p, e := range builds {
		s.builds[p] = buildCache{err: e}
	}
	upd := map[string][]Diag{}
	for p, ds := range diags {
		b, _ := json.Marshal(ds)
		sig := hashString(string(b))
		if s.diagSig[p] != sig {
			upd[p] = ds
			s.diagSig[p] = sig
		}
	}
	for p := range s.diags {
		if diags[p] == nil {
			upd[p] = nil
			delete(s.diagSig, p)
		}
	}
	s.diags = diags
	s.mu.Unlock()
	if len(upd) > 0 {
		s.emit("ide:diags", upd)
	}
}

func sameFixes(a, b []Diag) bool {
	for i := range a {
		if a[i].Fix != b[i].Fix {
			return false
		}
	}
	return true
}

func (s *Service) readDisk(w *Workspace, p string) (string, bool) {
	abs := filepath.Join(w.Root, filepath.FromSlash(p))
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() || st.Size() > maxLintSize {
		return "", false
	}
	if d, ok := s.disk[p]; ok && d.size == st.Size() && d.mod.Equal(st.ModTime()) {
		return d.text, true
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", false
	}
	t := strings.ReplaceAll(string(b), "\r\n", "\n")
	s.disk[p] = diskFile{size: st.Size(), mod: st.ModTime(), text: t}
	return t, true
}

// indexKey hashes the facts of the index that the checks read, so a change
// to one file re-runs the checks of other files only when these change.
func indexKey(ix *Index) uint64 {
	var b strings.Builder
	keys := func(m map[string]bool) {
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		b.WriteString(strings.Join(ks, ","))
		b.WriteByte('|')
	}
	keys(ix.Patches)
	keys(ix.CRDs)
	hp := make([]string, 0, len(ix.HPAs))
	for k, v := range ix.HPAs {
		hp = append(hp, k+"="+v)
	}
	sort.Strings(hp)
	b.WriteString(strings.Join(hp, ","))
	tp := make([]string, 0, len(ix.Targets))
	for f, ts := range ix.Targets {
		for _, t := range ts {
			tp = append(tp, f+">"+t.Root+"/"+t.NS+"/"+t.Prefix+"/"+t.Suffix)
		}
	}
	sort.Strings(tp)
	b.WriteString(strings.Join(tp, ","))
	im := make([]string, 0, len(ix.Images))
	for f, ns := range ix.Images {
		im = append(im, f+">"+strings.Join(ns, ","))
	}
	sort.Strings(im)
	b.WriteString(strings.Join(im, ","))
	kp := make([]string, 0, len(ix.Kusts))
	for p, k := range ix.Kusts {
		for _, r := range k.Refs {
			kp = append(kp, p+">"+r.Raw+"/"+boolStr(r.Exists)+boolStr(r.NoKust))
		}
	}
	sort.Strings(kp)
	b.WriteString(strings.Join(kp, ","))
	b.WriteString(strings.Join(ix.Charts, ","))
	return hashString(b.String())
}

func boolStr(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// members lists the files that a kustomization reads, itself included.
func members(ix *Index, k *Kust, seen map[string]bool, out *[]string) {
	if k == nil || seen[k.Path] {
		return
	}
	seen[k.Path] = true
	*out = append(*out, k.Path)
	for _, r := range k.Refs {
		if !r.Exists || r.Remote {
			continue
		}
		if r.Dir {
			members(ix, ix.Kusts[ix.kustIn(r.Path)], seen, out)
			continue
		}
		*out = append(*out, r.Path)
	}
}

// runBuilds builds every kustomization whose files changed, and returns
// the build errors.
func (s *Service) runBuilds(w *Workspace, ix *Index, overlays map[string]string) map[string]string {
	s.mu.Lock()
	prev := s.builds
	s.mu.Unlock()
	out := map[string]string{}
	keys := make([]string, 0, len(ix.Kusts))
	for p := range ix.Kusts {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	for _, p := range keys {
		var ms []string
		members(ix, ix.Kusts[p], map[string]bool{}, &ms)
		sort.Strings(ms)
		parts := make([]string, 0, len(ms))
		for _, m := range ms {
			if f := ix.Files[m]; f != nil {
				parts = append(parts, m, f.Text)
			} else {
				parts = append(parts, m)
			}
		}
		key := hashString(parts...)
		if c, ok := s.buildKeys[p]; ok && c == key {
			out[p] = prev[p].err
			continue
		}
		_, _, err := build(w.Root, p, overlays, false)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		out[p] = msg
		s.buildKeys[p] = key
	}
	return out
}

// ---- Schemas ----

type schemaCache struct {
	mu      sync.Mutex
	docs    map[string]*SchemaDoc
	loading map[string]bool
	failed  map[string]time.Time
	ver     int
}

func (c *schemaCache) init() {
	c.docs = map[string]*SchemaDoc{}
	c.loading = map[string]bool{}
	c.failed = map[string]time.Time{}
}

func (c *schemaCache) reset() {
	c.mu.Lock()
	c.init()
	c.ver++
	c.mu.Unlock()
}

func (c *schemaCache) version() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ver
}

// schemaFor returns the schema of an API version. It starts a download in
// the background and returns nil until the schema is loaded.
func (s *Service) schemaFor(cl Cluster, apiVersion string) *SchemaDoc {
	if cl == nil || apiVersion == "" || strings.HasPrefix(apiVersion, "kustomize.config.k8s.io/") {
		return nil
	}
	c := &s.schemas
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.docs[apiVersion]; ok {
		return d
	}
	if c.loading[apiVersion] || time.Since(c.failed[apiVersion]) < time.Minute {
		return nil
	}
	c.loading[apiVersion] = true
	ver := c.ver
	go func() {
		b, err := cl.OpenAPI(apiVersion)
		var d *SchemaDoc
		if err == nil {
			d, err = ParseSchemaDoc(b, apiVersion)
		}
		c.mu.Lock()
		if c.ver != ver {
			c.mu.Unlock()
			return
		}
		delete(c.loading, apiVersion)
		if err != nil {
			c.failed[apiVersion] = time.Now()
		} else {
			c.docs[apiVersion] = d
			c.ver++
		}
		c.mu.Unlock()
		if err == nil {
			s.poke()
		}
	}()
	return nil
}

// env builds the check input for a text that is not saved yet.
func (s *Service) env(p, text string) *lintEnv {
	s.mu.Lock()
	ix := s.ix
	be := s.builds[p].err
	s.mu.Unlock()
	if ix == nil {
		ix = BuildIndex(nil, map[string]*Parsed{}, nil)
	}
	cl := s.cluster()
	e := &lintEnv{path: p, text: text, lines: strings.Split(text, "\n"), docs: ParseDocs(text), ix: ix, cl: cl, build: be}
	if cl != nil {
		e.schema = func(av string) *SchemaDoc { return s.schemaFor(cl, av) }
	}
	return e
}

// QuickFix applies the fix of a diagnostic to a text and returns the new
// text.
func (s *Service) QuickFix(p, text string, line int, code string) (string, error) {
	e := s.env(p, text)
	ds := lintStatic(e)
	if e.cl != nil {
		ds = append(ds, lintLive(e, ds)...)
	}
	for _, d := range ds {
		if d.Line == line && d.Code == code && d.run != nil {
			lines := append([]string{}, e.lines...)
			return strings.Join(d.run(lines), "\n"), nil
		}
	}
	return "", errors.New("the quick fix no longer applies. The file changed")
}

// dirOf returns the folder of a workspace path for messages.
func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}
