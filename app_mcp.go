package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"

	"st8ks/internal/ide"
	"st8ks/internal/kube"
	"st8ks/internal/mcpserver"
	"st8ks/internal/shellenv"
)

const mcpUsage = `Usage: st8ks mcp [--kubeconfig PATH]... [--context NAME]

Serves the st8ks MCP server on stdin and stdout. The tools read one
kubeconfig context. Without --kubeconfig, st8ks reads the same files as
the app. Without --context, it uses the current context.

To add the server to Claude Code:

  claude mcp add st8ks -- st8ks mcp`

// runMCP serves the MCP server on stdin and stdout and returns the exit
// code. Stdout belongs to the protocol, so os.Stdout points at stderr while
// the server runs.
func runMCP(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Fprintln(os.Stderr, mcpUsage)
			return 0
		}
	}
	out := os.Stdout
	os.Stdout = os.Stderr
	if os.Getenv("ST8KS_DEBUG") == "" {
		klog.SetLogger(logr.Discard())
	}
	if shellenv.Needed() {
		shellenv.Load()
	}
	f := parseFlags(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := mcpserver.Serve(ctx, mcpserver.Options{Kubeconfigs: f.Kubeconfigs, Context: f.Context, Version: version}, os.Stdin, out)
	if err != nil {
		mcpserver.Stderr("%v", err)
		return 1
	}
	return 0
}

// ---- Claude Code ----

const claudeInstall = "npm install -g @anthropic-ai/claude-code"

// claudeCode runs Claude Code sessions in terminals. Each session has its
// own kubeconfig and MCP config file, which the session end removes.
type claudeCode struct {
	terms *ide.Terminals
	dir   string
}

func (a *App) initClaude() {
	a.cc = &claudeCode{terms: ide.NewTerminals(a.emit)}
	if dir, err := os.UserCacheDir(); err == nil {
		a.cc.dir = filepath.Join(dir, "st8ks")
		removeStaleClaudeFiles(a.cc.dir)
	}
}

func (a *App) shutdownClaude() {
	a.cc.terms.StopAll()
	if a.cc.dir == "" {
		return
	}
	files, _ := filepath.Glob(filepath.Join(a.cc.dir, "claude-"+strconv.Itoa(os.Getpid())+"-*"))
	for _, f := range files {
		_ = os.Remove(f)
	}
}

// removeStaleClaudeFiles deletes the session files of st8ks processes that
// ended without a clean shutdown. The kubeconfig files hold credentials.
func removeStaleClaudeFiles(dir string) {
	files, _ := filepath.Glob(filepath.Join(dir, "claude-*"))
	for _, f := range files {
		pid, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(filepath.Base(f), "claude-"), "-", 2)[0])
		if err != nil || pid == os.Getpid() {
			continue
		}
		if p, err := os.FindProcess(pid); err != nil || p.Signal(syscall.Signal(0)) != nil {
			_ = os.Remove(f)
		}
	}
}

// findClaude looks for the claude program on the PATH, which includes the
// PATH of the login shell, and in the folders of the native installer.
func findClaude() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".claude", "local", "claude"), filepath.Join(home, ".local", "bin", "claude"),
		"/opt/homebrew/bin/claude", "/usr/local/bin/claude"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return "st8ks"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// shellQuote quotes a word for a POSIX shell when it needs quotes.
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@=,", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ClaudeInfo tells the frontend if Claude Code can start in st8ks, and how
// to add the st8ks MCP server to Claude Code outside the app.
type ClaudeInfo struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Path      string `json:"path"`
	Install   string `json:"install"`
	Exe       string `json:"exe"`
	AddCmd    string `json:"addCmd"`
}

// ClaudeInfo finds the claude program and the command that adds st8ks to
// Claude Code.
func (a *App) ClaudeInfo() ClaudeInfo {
	exe := exePath()
	info := ClaudeInfo{Supported: true, Path: findClaude(), Install: claudeInstall, Exe: exe,
		AddCmd: "claude mcp add st8ks -- " + shellQuote(exe) + " mcp"}
	if goruntime.GOOS == "windows" {
		info.Supported = false
		info.Reason = "Claude Code in st8ks needs Linux or macOS, because the terminal needs a pseudo-terminal. Run claude in a terminal. To add st8ks to it, use the command below."
		info.AddCmd = `claude mcp add st8ks -- "` + exe + `" mcp`
	}
	return info
}

// ClaudeSession describes a Claude Code session that started.
type ClaudeSession struct {
	ID      string `json:"id"`
	Context string `json:"context"`
	Dir     string `json:"dir"`
}

// claudePrompt is the first prompt of a session that opens on an issue.
func claudePrompt(i *kube.Issue, ctx string) string {
	return fmt.Sprintf("Investigate the st8ks issue %s in the Kubernetes context %s: %s. "+
		"Use the st8ks MCP tools. Start with explain_issue for this id, then read more with get_resource, get_logs and list_resources. "+
		"Explain the root cause and propose a fix. Do not change the cluster. Check a proposed YAML change with dry_run_apply.",
		i.ID, ctx, i.Title)
}

// ClaudeStart runs Claude Code in a terminal with the st8ks MCP server for
// the connected context. kubectl in the session uses the same context. A
// non-empty issueID starts the session with a prompt about that issue.
func (a *App) ClaudeStart(cols, rows int, issueID string) (ClaudeSession, error) {
	info := a.ClaudeInfo()
	if !info.Supported {
		return ClaudeSession{}, errors.New(info.Reason)
	}
	if info.Path == "" {
		return ClaudeSession{}, errors.New("claude is not on the PATH of your login shell. To install Claude Code, run " + claudeInstall)
	}
	c := a.m.Current()
	if c == nil || c.State().Status != kube.StatusConnected {
		return ClaudeSession{}, errors.New("connect to a cluster first")
	}
	var prompt string
	if issueID != "" {
		i := c.FindIssue(issueID)
		if i == nil {
			return ClaudeSession{}, errors.New("the issue is resolved or no longer exists")
		}
		prompt = claudePrompt(i, c.Name)
	}
	if a.cc.dir == "" {
		return ClaudeSession{}, errors.New("st8ks has no cache folder for the session files")
	}
	if err := os.MkdirAll(a.cc.dir, 0o700); err != nil {
		return ClaudeSession{}, err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	base := filepath.Join(a.cc.dir, "claude-"+strconv.Itoa(os.Getpid())+"-"+hex.EncodeToString(b))
	kubeconfig, mcpConfig := base+".kubeconfig", base+".mcp.json"
	cleanup := func() {
		_ = os.Remove(kubeconfig)
		_ = os.Remove(mcpConfig)
	}
	if err := a.m.WriteKubeconfig(c.Name, kubeconfig); err != nil {
		cleanup()
		return ClaudeSession{}, err
	}
	// The temporary kubeconfig names the context as its own file does.
	ctxName := c.KubeconfigContext()
	cfg := map[string]any{"mcpServers": map[string]any{"st8ks": map[string]any{
		"type":    "stdio",
		"command": info.Exe,
		"args":    []string{"mcp", "--kubeconfig", kubeconfig, "--context", ctxName},
		// st8ks already loaded the login shell environment, and the
		// server inherits it.
		"env": map[string]string{"ST8KS_NO_SHELL_ENV": "1"},
	}}}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(mcpConfig, raw, 0o600); err != nil {
		cleanup()
		return ClaudeSession{}, err
	}
	// --mcp-config takes more than one value, so -- ends it before the prompt.
	args := []string{"--mcp-config", mcpConfig}
	if prompt != "" {
		args = append(args, "--", prompt)
	}
	dir := a.ide.State().Root
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	env := []string{"KUBECONFIG=" + kubeconfig, "ST8KS_CONTEXT=" + c.Name}
	if cols <= 0 || cols > 1000 || rows <= 0 || rows > 1000 {
		cols, rows = 100, 24
	}
	id, err := a.cc.terms.StartCommand(info.Path, args, dir, env, uint16(cols), uint16(rows), cleanup)
	if err != nil {
		cleanup()
		return ClaudeSession{}, fmt.Errorf("claude did not start: %w", err)
	}
	return ClaudeSession{ID: id, Context: c.Name, Dir: dir}, nil
}

// ClaudeInput sends base64 input to a Claude Code session.
func (a *App) ClaudeInput(id, data string) error { return a.cc.terms.Input(id, data) }

// ClaudeResize resizes a Claude Code session.
func (a *App) ClaudeResize(id string, cols, rows int) {
	if cols > 0 && rows > 0 && cols < 65536 && rows < 65536 {
		a.cc.terms.Resize(id, uint16(cols), uint16(rows))
	}
}

// ClaudeStop ends a Claude Code session.
func (a *App) ClaudeStop(id string) { a.cc.terms.Stop(id) }
