package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"st8ks/internal/ide"
	"st8ks/internal/kube"
	"st8ks/internal/settings"
)

// initIDE creates the IDE backend. The IDE targets the connected context.
func (a *App) initIDE() {
	a.ide = ide.NewService(a.emit, func() ide.Cluster {
		c := a.m.Current()
		if c == nil || c.State().Status != kube.StatusConnected {
			return nil
		}
		return c.IdeView()
	})
	a.terms = ide.NewTerminals(a.emit)
	if dir, err := os.UserCacheDir(); err == nil {
		a.termCfg = filepath.Join(dir, "st8ks", "terminal-"+strconv.Itoa(os.Getpid())+".kubeconfig")
		removeStaleTermConfigs(filepath.Dir(a.termCfg))
	}
}

// removeStaleTermConfigs deletes the terminal kubeconfig files of st8ks
// processes that ended without a clean shutdown. The files hold credentials.
func removeStaleTermConfigs(dir string) {
	files, _ := filepath.Glob(filepath.Join(dir, "terminal-*.kubeconfig"))
	for _, f := range files {
		pid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "terminal-"), ".kubeconfig"))
		if err != nil || pid == os.Getpid() {
			continue
		}
		if p, err := os.FindProcess(pid); err != nil || p.Signal(syscall.Signal(0)) != nil {
			_ = os.Remove(f)
		}
	}
}

func (a *App) shutdownIDE() {
	a.ide.Shutdown()
	a.terms.StopAll()
	if a.termCfg != "" {
		_ = os.Remove(a.termCfg)
	}
}

// IdeInit is what the IDE needs at start.
type IdeInit struct {
	State  *ide.State            `json:"state"`
	Diags  map[string][]ide.Diag `json:"diags"`
	Recent []string              `json:"recent"`
	Err    string                `json:"err,omitempty"`
}

// IdeStart opens the last workspace, if there is one.
func (a *App) IdeStart() IdeInit {
	s := a.st.Get()
	out := IdeInit{Recent: s.IdeRecent, Diags: map[string][]ide.Diag{}}
	if st := a.ide.State(); st.Root != "" {
		out.State = &st
		out.Diags = a.ide.Diags()
		return out
	}
	if s.IdeWorkspace == "" {
		return out
	}
	st, err := a.ide.Open(s.IdeWorkspace)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.State = &st
	return out
}

// IdeOpen opens a folder as the workspace. An empty path shows a folder
// dialog.
func (a *App) IdeOpen(path string) (*ide.State, error) {
	if path == "" {
		home, _ := os.UserHomeDir()
		p, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
			Title: "Open a folder with Kubernetes manifests", DefaultDirectory: home, ShowHiddenFiles: false,
		})
		if err != nil || p == "" {
			return nil, err
		}
		path = p
	}
	st, err := a.ide.Open(path)
	if err != nil {
		return nil, err
	}
	_ = a.st.Update(func(s *settings.Settings) {
		s.IdeWorkspace = st.Root
		s.IdeRecent = append([]string{st.Root}, slices.DeleteFunc(s.IdeRecent, func(p string) bool { return p == st.Root })...)
		if len(s.IdeRecent) > 8 {
			s.IdeRecent = s.IdeRecent[:8]
		}
	})
	return &st, nil
}

// IdeClose closes the workspace.
func (a *App) IdeClose() {
	a.ide.Close()
	_ = a.st.Update(func(s *settings.Settings) { s.IdeWorkspace = "" })
}

// IdeForget removes a folder from the recent list.
func (a *App) IdeForget(path string) []string {
	_ = a.st.Update(func(s *settings.Settings) {
		s.IdeRecent = slices.DeleteFunc(s.IdeRecent, func(p string) bool { return p == path })
	})
	return a.st.Get().IdeRecent
}

// IdeRefresh reads the file list and the Git status again.
func (a *App) IdeRefresh() ide.State { return a.ide.Refresh() }

// IdeSetActive turns the workspace polling on while the IDE is visible.
func (a *App) IdeSetActive(on bool) { a.ide.SetActive(on) }

// IdeRead loads a file.
func (a *App) IdeRead(path string) (ide.FileData, error) { return a.ide.Read(path) }

// IdeWrite saves a file.
func (a *App) IdeWrite(path, text string) (ide.State, error) { return a.ide.Write(path, text) }

// IdeBuffer sends an unsaved buffer to the checks.
func (a *App) IdeBuffer(path, text string, dirty bool) { a.ide.SetBuffer(path, text, dirty) }

// IdeDiags returns the diagnostics of every file.
func (a *App) IdeDiags() map[string][]ide.Diag { return a.ide.Diags() }

// IdeInspect describes the object and the field at a line.
func (a *App) IdeInspect(path, text string, line int) ide.Inspect {
	return a.ide.Inspect(path, text, line)
}

// IdeLiveText returns the file as the cluster has it.
func (a *App) IdeLiveText(path, text string) ide.LiveDiff { return a.ide.LiveText(path, text) }

// IdeQuickFix applies a quick fix and returns the new text.
func (a *App) IdeQuickFix(path, text string, line int, code string) (string, error) {
	return a.ide.QuickFix(path, text, line, code)
}

// IdePlan runs a server dry run and returns the changes of an apply.
func (a *App) IdePlan(path, text string) (*ide.Plan, error) { return a.ide.Plan(path, text) }

// IdeDryRun runs a server dry run.
func (a *App) IdeDryRun(path, text string) (ide.Run, error) { return a.ide.DryRun(path, text) }

// IdeApply applies a file with server-side apply.
func (a *App) IdeApply(path, text string, force bool) (ide.Run, error) {
	return a.ide.Apply(path, text, force)
}

// IdeCommit commits every change in the workspace.
func (a *App) IdeCommit(msg string) (ide.Run, error) { return a.ide.Commit(msg) }

// IdePush pushes the branch.
func (a *App) IdePush() (ide.Run, error) { return a.ide.Push() }

// IdeFindSource finds the workspace file of a live object.
func (a *App) IdeFindSource(kind, ns, name string) (ide.Source, error) {
	return a.ide.FindSource(kind, ns, name)
}

// writeTermConfig points the terminal kubeconfig at the connected context.
func (a *App) writeTermConfig() (string, error) {
	c := a.m.Current()
	if c == nil || a.termCfg == "" {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(a.termCfg), 0o700); err != nil {
		return "", err
	}
	if err := a.m.WriteKubeconfig(c.Name, a.termCfg); err != nil {
		return "", err
	}
	return c.Name, nil
}

// IdeTermStart opens a shell in the workspace folder. kubectl in the shell
// uses the connected context, also after a switch.
func (a *App) IdeTermStart(cols, rows int) (string, error) {
	st := a.ide.State()
	if st.Root == "" {
		return "", errors.New("open a workspace first")
	}
	var env []string
	if name, err := a.writeTermConfig(); err == nil && name != "" {
		env = append(env, "KUBECONFIG="+a.termCfg, "ST8KS_CONTEXT="+name)
	}
	if cols < 0 || cols > 1000 || rows < 0 || rows > 1000 {
		cols, rows = 100, 24
	}
	return a.terms.Start(st.Root, env, uint16(cols), uint16(rows))
}

// IdeTermInput sends base64 input to the IDE terminal.
func (a *App) IdeTermInput(id, data string) error { return a.terms.Input(id, data) }

// IdeTermResize resizes the IDE terminal.
func (a *App) IdeTermResize(id string, cols, rows int) {
	if cols > 0 && rows > 0 && cols < 65536 && rows < 65536 {
		a.terms.Resize(id, uint16(cols), uint16(rows))
	}
}

// IdeTermStop closes the IDE terminal.
func (a *App) IdeTermStop(id string) { a.terms.Stop(id) }
