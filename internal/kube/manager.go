package kube

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrNotConnected is returned when no context is active.
var ErrNotConnected = errors.New("not connected to a cluster")

// Manager owns the kubeconfig, the active cluster and all sessions.
type Manager struct {
	emit Emitter
	opts func() LoadOptions

	mu       sync.Mutex
	entries  map[string]*ctxEntry
	sources  []Source
	current  string
	watch    []string
	dirs     []string
	sig      string
	contexts map[string]*ContextInfo
	cur      *Cluster
	connGen  int

	logs     map[string]context.CancelFunc
	execs    map[string]*ExecSession
	forwards map[string]*forwardHandle
}

// NewManager creates a manager. opts returns the kubeconfig files to read.
func NewManager(emit Emitter, opts func() LoadOptions) *Manager {
	return &Manager{emit: emit, opts: opts, contexts: map[string]*ContextInfo{}, entries: map[string]*ctxEntry{},
		logs: map[string]context.CancelFunc{}, execs: map[string]*ExecSession{}, forwards: map[string]*forwardHandle{}}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Load reads the kubeconfig files. It keeps the status of known contexts.
func (m *Manager) Load() error {
	res := loadKubeconfigs(m.opts())
	sig := fileSignature(res.watch, res.dirs)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = res.entries
	m.sources = res.sources
	m.current = res.current
	m.watch = res.watch
	m.dirs = res.dirs
	m.sig = sig
	next := map[string]*ContextInfo{}
	for name, e := range res.entries {
		info := m.contexts[name]
		if info == nil {
			info = &ContextInfo{Name: name, Status: StatusUnknown}
		}
		ctx := e.raw.Contexts[e.orig]
		info.Cluster = ctx.Cluster
		info.User = ctx.AuthInfo
		info.Namespace = ctx.Namespace
		info.Source = e.source
		info.Server = ""
		if cl := e.raw.Clusters[ctx.Cluster]; cl != nil {
			info.Server = cl.Server
		}
		next[name] = info
	}
	m.contexts = next
	return nil
}

// Sources lists the kubeconfig files and folders that were read.
func (m *Manager) Sources() []Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Source{}, m.sources...)
}

// LoadError describes why no context was found, if so.
func (m *Manager) LoadError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) > 0 {
		return ""
	}
	var errs []string
	for _, s := range m.sources {
		if s.Err != "" {
			errs = append(errs, s.Path+": "+s.Err)
		}
	}
	return strings.Join(errs, "\n")
}

// DefaultContext returns the current-context of the primary kubeconfig.
func (m *Manager) DefaultContext() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// WatchFiles reloads the contexts when a kubeconfig file changes, for
// example after aws eks update-kubeconfig adds a context.
func (m *Manager) WatchFiles(stop <-chan struct{}) {
	tk := time.NewTicker(3 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
		}
		m.mu.Lock()
		watch, dirs, sig := m.watch, m.dirs, m.sig
		m.mu.Unlock()
		if fileSignature(watch, dirs) == sig {
			continue
		}
		_ = m.Load()
		m.emitContexts()
		m.emit("sources", m.Sources())
	}
}

func fileSignature(files, dirs []string) string {
	var b strings.Builder
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", f, st.ModTime().UnixNano(), st.Size())
		} else {
			fmt.Fprintf(&b, "%s:-;", f)
		}
	}
	for _, d := range dirs {
		for _, f := range filesIn(d, 1) {
			b.WriteString(f)
			b.WriteByte(';')
		}
	}
	return b.String()
}

// Contexts lists all contexts sorted by name.
func (m *Manager) Contexts() []ContextInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ContextInfo, 0, len(m.contexts))
	for _, c := range m.contexts {
		ci := *c
		ci.Current = m.cur != nil && m.cur.Name == c.Name
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) emitContexts() { m.emit("contexts", m.Contexts()) }

// ActiveSource returns the kubeconfig file of the connected context, or "".
func (m *Manager) ActiveSource() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur == nil || m.entries[m.cur.Name] == nil {
		return ""
	}
	return m.entries[m.cur.Name].source
}

// DeleteContext removes a context from the kubeconfig file that defines it,
// as kubectl config delete-context does. The cluster and user entries stay,
// because other contexts can use them. It returns the file it changed.
func (m *Manager) DeleteContext(name string) (string, error) {
	m.mu.Lock()
	e := m.entries[name]
	active := m.cur != nil && m.cur.Name == name
	m.mu.Unlock()
	switch {
	case e == nil:
		return "", fmt.Errorf("context %s not found", name)
	case active:
		return "", errors.New("st8ks is connected to this context. Switch to another context first")
	case e.source == "":
		return "", fmt.Errorf("the kubeconfig file of context %s is not known", name)
	}
	// LoadFromFile keeps relative certificate paths as they are, so the
	// file keeps working after the write.
	cfg, err := clientcmd.LoadFromFile(e.source)
	if err != nil {
		return "", err
	}
	if _, ok := cfg.Contexts[e.orig]; !ok {
		return "", fmt.Errorf("%s has no context %s", e.source, e.orig)
	}
	delete(cfg.Contexts, e.orig)
	if cfg.CurrentContext == e.orig {
		cfg.CurrentContext = ""
	}
	if err := clientcmd.WriteToFile(*cfg, e.source); err != nil {
		return "", err
	}
	m.mu.Lock()
	delete(m.contexts, name)
	m.mu.Unlock()
	return e.source, m.Load()
}

func (m *Manager) restConfig(name string) (*rest.Config, *ctxEntry, string, error) {
	m.mu.Lock()
	e := m.entries[name]
	m.mu.Unlock()
	if e == nil {
		return nil, nil, "", fmt.Errorf("context %s not found", name)
	}
	cc := clientcmd.NewNonInteractiveClientConfig(*e.raw, e.orig, &clientcmd.ConfigOverrides{}, nil)
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, e, "", err
	}
	return cfg, e, e.raw.Contexts[e.orig].Namespace, nil
}

// Current returns the active cluster or nil.
func (m *Manager) Current() *Cluster {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur
}

// Cluster returns the active cluster or ErrNotConnected.
func (m *Manager) Cluster() (*Cluster, error) {
	if c := m.Current(); c != nil {
		return c, nil
	}
	return nil, ErrNotConnected
}

// Connect switches to a context. It stops the watches of the previous
// context. Port-forwards keep running.
func (m *Manager) Connect(name string) (ClusterState, error) {
	m.mu.Lock()
	info := m.contexts[name]
	old := m.cur
	m.cur = nil
	m.connGen++
	gen := m.connGen
	m.mu.Unlock()
	if old != nil {
		m.rememberCounts(old)
		old.Stop()
		m.stopSessions(false)
	}
	if info == nil {
		return ClusterState{Context: name, Status: StatusUnreachable, Error: "context " + name + " not found"}, errors.New("context not found")
	}
	m.setInfo(name, func(i *ContextInfo) { i.Status = StatusConnecting; i.Error = "" })
	cfg, entry, ns, err := m.restConfig(name)
	if err != nil {
		st := ClusterState{Context: name, Status: StatusUnreachable, Error: err.Error()}
		m.setInfo(name, func(i *ContextInfo) { i.Status = StatusUnreachable; i.Error = err.Error() })
		return st, nil
	}
	var c *Cluster
	emit := func(ev string, data any) {
		if ev == "cluster" {
			if s, ok := data.(ClusterState); ok {
				m.setInfo(name, func(i *ContextInfo) {
					i.Status, i.Error, i.Dist, i.Version, i.Region = s.Status, s.Error, s.Dist, s.Version, s.Region
				})
			}
		}
		m.emit(ev, data)
	}
	c, err = newCluster(name, entry.orig, *entry.raw, cfg, ns, emit)
	if err != nil {
		st := ClusterState{Context: name, Status: StatusUnreachable, Error: err.Error()}
		m.setInfo(name, func(i *ContextInfo) { i.Status = StatusUnreachable; i.Error = err.Error() })
		return st, nil
	}
	m.mu.Lock()
	if m.connGen != gen {
		m.mu.Unlock()
		c.Stop()
		return ClusterState{Context: name, Status: StatusUnknown}, errors.New("another context was selected")
	}
	m.cur = c
	m.mu.Unlock()
	m.emitContexts()
	_ = c.Start()
	return c.State(), nil
}

func (m *Manager) rememberCounts(c *Cluster) {
	counts := c.Counts()
	sum := func(k string) int {
		n := 0
		for _, v := range counts[k] {
			n += v[0]
		}
		return n
	}
	m.setInfo(c.Name, func(i *ContextInfo) {
		i.Nodes = sum("Nodes")
		i.Pods = sum("Pods")
	})
}

func (m *Manager) setInfo(name string, fn func(i *ContextInfo)) {
	m.mu.Lock()
	info := m.contexts[name]
	if info != nil {
		fn(info)
	}
	m.mu.Unlock()
	if info != nil {
		m.emitContexts()
	}
}

// ProbeAll checks every inactive context with a short timeout.
func (m *Manager) ProbeAll() {
	names := []string{}
	for _, c := range m.Contexts() {
		if !c.Current {
			names = append(names, c.Name)
		}
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m.probe(name)
		}(name)
	}
	wg.Wait()
}

func (m *Manager) probe(name string) {
	cfg, _, _, err := m.restConfig(name)
	if err != nil {
		m.setInfo(name, func(i *ContextInfo) { i.Status = StatusUnreachable; i.Error = err.Error() })
		return
	}
	cfg = rest.CopyConfig(cfg)
	cfg.Timeout = 5 * time.Second
	cfg.WarningHandler = rest.NoWarnings{}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		m.setInfo(name, func(i *ContextInfo) { i.Status = StatusUnreachable; i.Error = err.Error() })
		return
	}
	v, err := cs.Discovery().ServerVersion()
	if cur := m.Current(); cur != nil && cur.Name == name {
		return
	}
	if err != nil {
		msg := friendlyErr(err)
		m.setInfo(name, func(i *ContextInfo) { i.Status = StatusUnreachable; i.Error = msg })
		return
	}
	m.setInfo(name, func(i *ContextInfo) {
		i.Status = StatusConnected
		i.Error = ""
		i.Version = v.GitVersion
		if i.Dist == "" || i.Dist == "Kubernetes" {
			i.Dist = distFromVersion(v.GitVersion)
		}
	})
}

// ---- Sessions ----

func (m *Manager) stopSessions(forwards bool) {
	m.mu.Lock()
	logs := m.logs
	execs := m.execs
	m.logs = map[string]context.CancelFunc{}
	m.execs = map[string]*ExecSession{}
	var fws []*forwardHandle
	if forwards {
		for _, f := range m.forwards {
			fws = append(fws, f)
		}
		m.forwards = map[string]*forwardHandle{}
	}
	m.mu.Unlock()
	for _, c := range logs {
		c()
	}
	for _, e := range execs {
		e.Close()
	}
	for _, f := range fws {
		f.Close()
	}
}

// Shutdown stops everything.
func (m *Manager) Shutdown() {
	m.stopSessions(true)
	if c := m.Current(); c != nil {
		c.Stop()
	}
}

// StartLogs starts a log stream and returns its ID.
func (m *Manager) StartLogs(o LogOpts) (string, error) {
	c, err := m.Cluster()
	if err != nil {
		return "", err
	}
	id := newID()
	ctx, cancel := context.WithCancel(c.ctx)
	m.mu.Lock()
	m.logs[id] = cancel
	m.mu.Unlock()
	go func() {
		c.StreamLogs(ctx, id, o)
		m.mu.Lock()
		delete(m.logs, id)
		m.mu.Unlock()
	}()
	return id, nil
}

// StopLogs ends a log stream.
func (m *Manager) StopLogs(id string) {
	m.mu.Lock()
	cancel := m.logs[id]
	delete(m.logs, id)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// StartExec opens a terminal and returns its ID.
func (m *Manager) StartExec(o ExecOpts) (string, error) {
	c, err := m.Cluster()
	if err != nil {
		return "", err
	}
	id := newID()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := c.StartExec(id, o, func() {
		m.mu.Lock()
		delete(m.execs, id)
		m.mu.Unlock()
	})
	if err != nil {
		return "", err
	}
	m.execs[id] = s
	return id, nil
}

// ExecInput sends base64 keyboard input to a terminal.
func (m *Manager) ExecInput(id, data string) error {
	m.mu.Lock()
	s := m.execs[id]
	m.mu.Unlock()
	if s == nil {
		return errors.New("the terminal session ended")
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	return s.Write(b)
}

// ExecResize changes the terminal size.
func (m *Manager) ExecResize(id string, cols, rows uint16) {
	m.mu.Lock()
	s := m.execs[id]
	m.mu.Unlock()
	if s != nil {
		s.Resize(cols, rows)
	}
}

// StopExec closes a terminal.
func (m *Manager) StopExec(id string) {
	m.mu.Lock()
	s := m.execs[id]
	delete(m.execs, id)
	m.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// StartForward starts a port-forward.
func (m *Manager) StartForward(ns, pod string, port int) (*Forward, error) {
	c, err := m.Cluster()
	if err != nil {
		return nil, err
	}
	for _, f := range m.Forwards() {
		if f.Context == c.Name && f.NS == ns && f.Pod == pod && f.Remote == port {
			return nil, errors.New("this port is already forwarded")
		}
	}
	id := newID()
	f, err := c.StartForward(id, ns, pod, port, func(f *forwardHandle, msg string) {
		m.mu.Lock()
		f.ended = true
		delete(m.forwards, id)
		m.mu.Unlock()
		if msg != "" {
			m.emit("toast", "Port-forward to "+pod+" stopped: "+msg)
		}
		m.emit("forwards", m.Forwards())
	})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	// The forward can stop before this point. Then it is not registered.
	if !f.ended {
		m.forwards[id] = f
	}
	m.mu.Unlock()
	m.emit("forwards", m.Forwards())
	info := f.info
	return &info, nil
}

// StopForward stops one port-forward.
func (m *Manager) StopForward(id string) {
	m.mu.Lock()
	f := m.forwards[id]
	delete(m.forwards, id)
	m.mu.Unlock()
	if f != nil {
		f.Close()
	}
	m.emit("forwards", m.Forwards())
}

// StopAllForwards stops every port-forward.
func (m *Manager) StopAllForwards() {
	m.mu.Lock()
	fws := m.forwards
	m.forwards = map[string]*forwardHandle{}
	m.mu.Unlock()
	for _, f := range fws {
		f.Close()
	}
	m.emit("forwards", m.Forwards())
}

// Forwards lists the running port-forwards.
func (m *Manager) Forwards() []Forward {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Forward, 0, len(m.forwards))
	for _, f := range m.forwards {
		out = append(out, f.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pod+out[i].ID < out[j].Pod+out[j].ID })
	return out
}
