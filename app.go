package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"st8ks/internal/assistant"
	"st8ks/internal/ide"
	"st8ks/internal/kube"
	"st8ks/internal/settings"
)

// Flags are the command line options.
type Flags struct {
	Kubeconfigs []string
	Context     string
	Version     bool
}

// parseFlags reads --kubeconfig and --context. It ignores other arguments,
// such as the process serial number that old macOS versions pass.
func parseFlags(args []string) Flags {
	var f Flags
	for i := 0; i < len(args); i++ {
		a := args[i]
		key, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") {
			continue
		}
		if !hasVal && i+1 < len(args) && (key == "kubeconfig" || key == "context") {
			val = args[i+1]
			i++
		}
		switch key {
		case "version", "v":
			f.Version = true
		case "kubeconfig":
			if val != "" {
				f.Kubeconfigs = append(f.Kubeconfigs, val)
			}
		case "context":
			f.Context = val
		}
	}
	return f
}

// App is the API that the frontend calls.
type App struct {
	ctx   context.Context
	stop  chan struct{}
	flags Flags
	st    *settings.Store
	m     *kube.Manager
	ai    *assistant.Assistant

	aiMu  sync.Mutex
	aiCtx map[string]string

	ide     *ide.Service
	terms   *ide.Terminals
	termCfg string // the kubeconfig of the IDE terminal
}

// NewApp creates the app.
func NewApp(flags Flags) *App {
	a := &App{st: settings.Open(), flags: flags, stop: make(chan struct{}), aiCtx: map[string]string{}}
	a.m = kube.NewManager(a.emit, func() kube.LoadOptions {
		s := a.st.Get()
		return kube.LoadOptions{Explicit: a.flags.Kubeconfigs, Extra: s.Kubeconfigs, Hidden: s.HiddenKubeconfigs, Scan: s.ScanKubeDir}
	})
	a.ai = assistant.New(a.emit, func() string { return a.st.Get().AnthropicKey }, func() string { return a.st.Get().AssistantModel })
	a.initIDE()
	return a
}

func (a *App) emit(name string, data any) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, name, data)
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	_ = a.m.Load()
	go a.m.WatchFiles(a.stop)
}

func (a *App) shutdown(context.Context) {
	close(a.stop)
	a.shutdownIDE()
	a.m.Shutdown()
}

// middleware serves bulk data over the asset server. A fetch of a large
// table is faster than an event, because the webview parses the JSON body
// directly instead of evaluating a script.
func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/_st8ks/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		c := a.m.Current()
		if c == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"not connected"}`))
			return
		}
		switch r.URL.Path {
		case "/_st8ks/table":
			snap := c.Snapshot(r.URL.Query().Get("kind"))
			_ = json.NewEncoder(w).Encode(snap)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	})
}

// PublicSettings are the settings without the API key.
type PublicSettings struct {
	settings.Settings
	HasKey bool `json:"hasKey"`
}

func (a *App) publicSettings() PublicSettings {
	s := a.st.Get()
	has := s.AnthropicKey != "" || os.Getenv("ANTHROPIC_API_KEY") != ""
	s.AnthropicKey = ""
	return PublicSettings{Settings: s, HasKey: has}
}

// InitState is what the frontend needs at start.
type InitState struct {
	Settings PublicSettings     `json:"settings"`
	Contexts []kube.ContextInfo `json:"contexts"`
	Sources  []kube.Source      `json:"sources"`
	Explicit bool               `json:"explicit"`
	Initial  string             `json:"initial"`
	Platform string             `json:"platform"`
	LoadErr  string             `json:"loadErr"`
	Version  string             `json:"version"`
}

// Init returns the settings, the contexts, and the context to open first.
func (a *App) Init() InitState {
	ctxs := a.m.Contexts()
	has := func(n string) bool {
		return slices.ContainsFunc(ctxs, func(c kube.ContextInfo) bool { return c.Name == n })
	}
	initial := a.flags.Context
	if !has(initial) {
		initial = a.st.Get().LastContext
	}
	if !has(initial) {
		initial = a.m.DefaultContext()
	}
	if !has(initial) && len(ctxs) > 0 {
		initial = ctxs[0].Name
	}
	if !has(initial) {
		initial = ""
	}
	return InitState{Settings: a.publicSettings(), Contexts: ctxs, Sources: a.m.Sources(), Explicit: len(a.flags.Kubeconfigs) > 0,
		Initial: initial, Platform: goruntime.GOOS, LoadErr: a.m.LoadError(), Version: version}
}

// SaveSettings stores the settings that the frontend owns. The API key, the
// kubeconfig sources, the last context and the IDE folders have their own
// methods, so an old copy in the frontend cannot undo them.
func (a *App) SaveSettings(s settings.Settings) error {
	return a.st.Update(func(cur *settings.Settings) {
		s.AnthropicKey = cur.AnthropicKey
		s.Kubeconfigs = cur.Kubeconfigs
		s.HiddenKubeconfigs = cur.HiddenKubeconfigs
		s.ScanKubeDir = cur.ScanKubeDir
		s.LastContext = cur.LastContext
		s.IdeWorkspace = cur.IdeWorkspace
		s.IdeRecent = cur.IdeRecent
		*cur = s
	})
}

// SetAPIKey stores the Anthropic API key. An empty key removes it.
func (a *App) SetAPIKey(key string) (PublicSettings, error) {
	key = strings.TrimSpace(key)
	if err := assistant.CheckKey(key); err != nil {
		return a.publicSettings(), err
	}
	err := a.st.Update(func(s *settings.Settings) { s.AnthropicKey = key })
	return a.publicSettings(), err
}

// CheckAssistant sends one request to the Anthropic API to show if the key
// and the model work.
func (a *App) CheckAssistant() (string, error) { return a.ai.Check() }

// ---- Contexts ----

// Connect switches to a context.
func (a *App) Connect(name string) (kube.ClusterState, error) {
	a.aiMu.Lock()
	a.aiCtx = map[string]string{}
	a.aiMu.Unlock()
	st, err := a.m.Connect(name)
	if err == nil && st.Status == kube.StatusConnected {
		_ = a.st.Update(func(s *settings.Settings) { s.LastContext = name })
		// A running IDE terminal follows the new context.
		if _, statErr := os.Stat(a.termCfg); a.termCfg != "" && statErr == nil {
			_, _ = a.writeTermConfig()
		}
	}
	return st, err
}

// ClusterState returns the state of the active context.
func (a *App) ClusterState() (kube.ClusterState, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return kube.ClusterState{}, err
	}
	return c.State(), nil
}

// Retry checks an unreachable cluster again.
func (a *App) Retry() (kube.ClusterState, error) {
	c := a.m.Current()
	if c == nil {
		return kube.ClusterState{}, kube.ErrNotConnected
	}
	st := c.State()
	if st.Version == "" {
		return a.m.Connect(c.Name)
	}
	return c.Probe(), nil
}

// Contexts lists the kubeconfig contexts.
func (a *App) Contexts() []kube.ContextInfo { return a.m.Contexts() }

// ProbeContexts checks every inactive context in the background.
func (a *App) ProbeContexts() { go a.m.ProbeAll() }

// Sources lists the kubeconfig files and folders that were read.
func (a *App) Sources() []kube.Source { return a.m.Sources() }

func (a *App) reloadKubeconfigs() {
	_ = a.m.Load()
	a.emit("contexts", a.m.Contexts())
	a.emit("sources", a.m.Sources())
	go a.m.ProbeAll()
}

// addSource stores a file or folder path and reports the new contexts.
func (a *App) addSource(path string) (string, error) {
	before := map[string]bool{}
	for _, c := range a.m.Contexts() {
		before[c.Name] = true
	}
	if err := a.st.Update(func(s *settings.Settings) {
		if !slices.Contains(s.Kubeconfigs, path) {
			s.Kubeconfigs = append(s.Kubeconfigs, path)
		}
		s.HiddenKubeconfigs = slices.DeleteFunc(s.HiddenKubeconfigs, func(p string) bool { return p == path })
	}); err != nil {
		return "", err
	}
	a.reloadKubeconfigs()
	for _, s := range a.m.Sources() {
		if s.Path == path && s.Err != "" {
			return "", fmt.Errorf("%s is not a valid kubeconfig: %s", filepath.Base(path), s.Err)
		}
	}
	n := 0
	for _, c := range a.m.Contexts() {
		if !before[c.Name] {
			n++
		}
	}
	if n == 0 {
		return "No new contexts in " + filepath.Base(path) + ".", nil
	}
	return fmt.Sprintf("Added %d context(s) from %s", n, filepath.Base(path)), nil
}

// ImportKubeconfig adds a kubeconfig file that the user picks.
func (a *App) ImportKubeconfig() (string, error) {
	if len(a.flags.Kubeconfigs) > 0 {
		return "", errors.New("st8ks runs with --kubeconfig, so it reads only those files")
	}
	home, _ := os.UserHomeDir()
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Add kubeconfig file", DefaultDirectory: filepath.Join(home, ".kube"), ShowHiddenFiles: true,
	})
	if err != nil || path == "" {
		return "", err
	}
	return a.addSource(path)
}

// ImportKubeconfigFolder adds a folder. Every kubeconfig file in it is read,
// and files that appear later are picked up.
func (a *App) ImportKubeconfigFolder() (string, error) {
	if len(a.flags.Kubeconfigs) > 0 {
		return "", errors.New("st8ks runs with --kubeconfig, so it reads only those files")
	}
	home, _ := os.UserHomeDir()
	path, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Add kubeconfig folder", DefaultDirectory: home, ShowHiddenFiles: true,
	})
	if err != nil || path == "" {
		return "", err
	}
	return a.addSource(path)
}

// AddKubeconfigPath adds a file or folder by path.
func (a *App) AddKubeconfigPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("enter a path")
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s does not exist", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return a.addSource(abs)
}

// RemoveKubeconfig stops reading a kubeconfig file or folder. An added path
// leaves the list. A file that st8ks found itself, for example in ~/.kube,
// is hidden and can come back with RestoreKubeconfig. No file is deleted.
func (a *App) RemoveKubeconfig(path string) (string, error) {
	if slices.Contains(a.flags.Kubeconfigs, path) {
		return "", errors.New("st8ks runs with --kubeconfig, so it reads the files that the flag names")
	}
	if src := a.m.ActiveSource(); src == path || strings.HasPrefix(src, path+string(filepath.Separator)) {
		return "", errors.New("the connected context comes from this path. Switch to another context first")
	}
	err := a.st.Update(func(s *settings.Settings) {
		n := len(s.Kubeconfigs)
		s.Kubeconfigs = slices.DeleteFunc(s.Kubeconfigs, func(p string) bool {
			abs, _ := filepath.Abs(p)
			return p == path || abs == path
		})
		if len(s.Kubeconfigs) == n && !slices.Contains(s.HiddenKubeconfigs, path) {
			s.HiddenKubeconfigs = append(s.HiddenKubeconfigs, path)
		}
	})
	a.reloadKubeconfigs()
	if err != nil {
		return "", err
	}
	return "st8ks no longer reads " + filepath.Base(path) + ". The file stays on disk.", nil
}

// RestoreKubeconfig reads a removed file again.
func (a *App) RestoreKubeconfig(path string) error {
	err := a.st.Update(func(s *settings.Settings) {
		s.HiddenKubeconfigs = slices.DeleteFunc(s.HiddenKubeconfigs, func(p string) bool { return p == path })
	})
	a.reloadKubeconfigs()
	return err
}

// DeleteContext removes a context from its kubeconfig file.
func (a *App) DeleteContext(name string) (string, error) {
	file, err := a.m.DeleteContext(name)
	if err != nil {
		return "", err
	}
	_ = a.st.Update(func(s *settings.Settings) {
		delete(s.NsByContext, name)
		if s.LastContext == name {
			s.LastContext = ""
		}
	})
	a.reloadKubeconfigs()
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(file, home+string(filepath.Separator)) {
		file = "~" + file[len(home):]
	}
	return "Deleted context " + name + " from " + file, nil
}

// SetScanKubeDir turns the scan of ~/.kube on or off.
func (a *App) SetScanKubeDir(on bool) error {
	err := a.st.Update(func(s *settings.Settings) { s.ScanKubeDir = on })
	a.reloadKubeconfigs()
	return err
}

// ReloadKubeconfigs reads all kubeconfig files again.
func (a *App) ReloadKubeconfigs() []kube.ContextInfo {
	a.reloadKubeconfigs()
	return a.m.Contexts()
}

// ---- Tables ----

// TreeState is the navigation tree and the kind registry.
type TreeState struct {
	Tree  []kube.TreeSection `json:"tree"`
	Kinds []kube.KindInfo    `json:"kinds"`
}

// Tree returns the navigation tree for the active context.
func (a *App) Tree() (TreeState, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return TreeState{}, err
	}
	return TreeState{Tree: c.Tree(), Kinds: c.Kinds()}, nil
}

// Counts returns object and problem counts per kind and namespace.
func (a *App) Counts() (map[string]map[string][2]int, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.Counts(), nil
}

// Watch sets the kinds whose changes the frontend receives.
func (a *App) Watch(kinds []string) {
	if c := a.m.Current(); c != nil {
		c.Watch(kinds)
	}
}

// Overview returns the cluster summary.
func (a *App) Overview() (kube.Overview, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return kube.Overview{}, err
	}
	return c.Overview(), nil
}

// Issues returns the detected problems.
func (a *App) Issues() ([]kube.Issue, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.Issues(), nil
}

// ---- Objects ----

// GetObject loads the detail of one object.
func (a *App) GetObject(ref kube.Ref) (*kube.ObjectDoc, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.GetObject(ref)
}

// Apply updates an object from YAML.
func (a *App) Apply(yaml string, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.Apply(yaml, dry)
}

// Create creates objects from YAML.
func (a *App) Create(yaml, ns string, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.Create(yaml, ns, dry)
}

// NewYAML returns a template for a new object.
func (a *App) NewYAML(kind, ns string) string {
	if c := a.m.Current(); c != nil {
		return c.NewYAML(kind, ns)
	}
	return ""
}

// Delete deletes objects.
func (a *App) Delete(items []kube.Ref, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.Delete(items, dry)
}

// Restart restarts workloads.
func (a *App) Restart(items []kube.Ref, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.Restart(items, dry)
}

// Scale sets the replicas of a workload.
func (a *App) Scale(ref kube.Ref, replicas int64, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.Scale(ref, replicas, dry)
}

// Cordon marks a node unschedulable, or schedulable when on is false.
func (a *App) Cordon(node string, on, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.Cordon(node, on, dry)
}

// TriggerCronJob runs a CronJob now.
func (a *App) TriggerCronJob(ns, name string, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.TriggerCronJob(ns, name, dry)
}

// SuspendCronJob suspends or resumes a CronJob.
func (a *App) SuspendCronJob(ns, name string, suspend, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.SuspendCronJob(ns, name, suspend, dry)
}

// ProposeFix builds the diff for an issue action.
func (a *App) ProposeFix(act kube.IssueAction) (*kube.FixProposal, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.ProposeMemoryFix(act)
}

// ---- Logs, exec, port-forward ----

// StartLogs follows a container log. Lines arrive as "log" events.
func (a *App) StartLogs(o kube.LogOpts) (string, error) {
	if o.Tail <= 0 {
		o.Tail = int64(a.st.Get().LogTail)
	}
	return a.m.StartLogs(o)
}

// StopLogs ends a log stream.
func (a *App) StopLogs(id string) { a.m.StopLogs(id) }

// SaveLogs writes the full log of a container to a file the user picks.
func (a *App) SaveLogs(o kube.LogOpts) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	name := o.Pod
	if o.Container != "" {
		name += "-" + o.Container
	}
	if o.Previous {
		name += "-previous"
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: "Save log", DefaultFilename: name + ".log"})
	if err != nil || path == "" {
		return "", err
	}
	n, err := c.SaveLogs(o, path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved %s (%d KB)", filepath.Base(path), (n+1023)/1024), nil
}

// StartExec opens a terminal. Output arrives as "exec" events.
func (a *App) StartExec(o kube.ExecOpts) (string, error) { return a.m.StartExec(o) }

// ExecInput sends base64 input to a terminal.
func (a *App) ExecInput(id, data string) error { return a.m.ExecInput(id, data) }

// ExecResize resizes a terminal.
func (a *App) ExecResize(id string, cols, rows int) {
	if cols > 0 && rows > 0 && cols < 65536 && rows < 65536 {
		a.m.ExecResize(id, uint16(cols), uint16(rows))
	}
}

// StopExec closes a terminal.
func (a *App) StopExec(id string) { a.m.StopExec(id) }

// StartDebug adds an ephemeral debug container and returns its name.
func (a *App) StartDebug(ns, pod, target, image string) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.StartDebugContainer(ns, pod, target, image)
}

// StartForward forwards a local port to a pod.
func (a *App) StartForward(ns, pod string, port int) (*kube.Forward, error) {
	return a.m.StartForward(ns, pod, port)
}

// StopForward stops one port-forward.
func (a *App) StopForward(id string) { a.m.StopForward(id) }

// StopAllForwards stops all port-forwards.
func (a *App) StopAllForwards() { a.m.StopAllForwards() }

// Forwards lists the port-forwards.
func (a *App) Forwards() []kube.Forward { return a.m.Forwards() }

// OpenURL opens a URL in the default browser.
func (a *App) OpenURL(url string) {
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		runtime.BrowserOpenURL(a.ctx, url)
	}
}

// ---- RBAC ----

// RbacSubjects lists the subjects of all bindings.
func (a *App) RbacSubjects() ([]kube.Subject, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.RbacSubjects(), nil
}

// RbacMatrix returns the effective permissions of a subject.
func (a *App) RbacMatrix(s kube.Subject, ns string) (kube.RbacMatrix, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return kube.RbacMatrix{}, err
	}
	return c.RbacMatrix(s, ns), nil
}

// RbacExplain explains one permission.
func (a *App) RbacExplain(s kube.Subject, ns, res, verb string) (kube.RbacAnswer, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return kube.RbacAnswer{}, err
	}
	return c.RbacExplain(s, ns, res, verb), nil
}

// ---- Helm ----

// HelmReleases lists the releases.
func (a *App) HelmReleases() ([]kube.HelmRelease, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.HelmReleases()
}

// HelmDetail loads one release.
func (a *App) HelmDetail(ns, name string) (*kube.HelmDetail, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	return c.HelmDetail(ns, name)
}

// HelmRollbackTarget returns the current revision and the last good one.
func (a *App) HelmRollbackTarget(ns, name string) ([]int, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return nil, err
	}
	cur, target, err := c.HelmRollbackTarget(ns, name)
	if err != nil {
		return nil, err
	}
	return []int{cur, target}, nil
}

// HelmRollback rolls back a release.
func (a *App) HelmRollback(ns, name string, rev int, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.HelmRollback(ns, name, rev, dry)
}

// HelmUninstall uninstalls a release.
func (a *App) HelmUninstall(ns, name string, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.HelmUninstall(ns, name, dry)
}

// HelmUpgradeValues upgrades a release with new values.
func (a *App) HelmUpgradeValues(ns, name, values string, dry bool) (string, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return "", err
	}
	return c.HelmUpgradeValues(ns, name, values, dry)
}

// ---- Topology ----

// Topology returns the object graph of a namespace.
func (a *App) Topology(ns string) (kube.Topology, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return kube.Topology{}, err
	}
	return c.Topology(ns), nil
}

// ---- Assistant ----

// AskStart tells the frontend which answer stream to follow.
type AskStart struct {
	ID   string   `json:"id"`
	Cmds []string `json:"cmds"`
}

// Ask collects the cluster data for an issue and streams an answer as "ai"
// events. An empty issueID asks about the cluster as a whole.
func (a *App) Ask(reqID, issueID string, history []assistant.Msg, question string) (AskStart, error) {
	c, err := a.m.Cluster()
	if err != nil {
		return AskStart{}, err
	}
	// Issue IDs repeat across clusters, so the cache key holds the context.
	key := c.Name + "\x00" + issueID
	a.aiMu.Lock()
	text, ok := a.aiCtx[key]
	a.aiMu.Unlock()
	var cmds []string
	if !ok {
		if issueID == "" {
			text = c.ClusterContext()
		} else {
			text, cmds, err = c.IssueContext(issueID)
			if err != nil {
				return AskStart{}, err
			}
		}
		if a.m.Current() != c {
			return AskStart{}, errors.New("the context changed while the assistant read the cluster")
		}
		a.aiMu.Lock()
		a.aiCtx[key] = text
		a.aiMu.Unlock()
	}
	if reqID == "" {
		return AskStart{}, errors.New("missing request id")
	}
	a.ai.Ask(reqID, text, history, question)
	return AskStart{ID: reqID, Cmds: cmds}, nil
}

// CancelAsk stops an answer.
func (a *App) CancelAsk(reqID string) { a.ai.Cancel(reqID) }
