package ide

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits of the workspace.
const (
	maxFiles    = 50_000
	maxOpenSize = 4 << 20
)

// Workspace is a folder that the IDE edits, usually a Git repository.
type Workspace struct {
	Root   string
	Name   string
	git    bool
	prefix string // the path of Root inside the repository, with a trailing slash

	mu   sync.Mutex
	crlf map[string]bool
}

// Change is one file that differs from HEAD.
type Change struct {
	Path string `json:"path"`
	St   string `json:"st"` // M, A, D, R or U for untracked
}

// State describes the workspace for the frontend.
type State struct {
	Root      string   `json:"root"`
	Name      string   `json:"name"`
	Git       bool     `json:"git"`
	Branch    string   `json:"branch"`
	Upstream  string   `json:"upstream"`
	Ahead     int      `json:"ahead"`
	Behind    int      `json:"behind"`
	Files     []string `json:"files"`
	Changes   []Change `json:"changes"`
	Truncated bool     `json:"truncated"`
	Err       string   `json:"err,omitempty"`
}

// OpenWorkspace opens a folder.
func OpenWorkspace(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", abs)
	}
	w := &Workspace{Root: abs, Name: filepath.Base(abs), crlf: map[string]bool{}}
	if _, err := exec.LookPath("git"); err == nil {
		if out, err := w.gitOut(context.Background(), "rev-parse", "--show-prefix"); err == nil {
			w.git = true
			w.prefix = strings.TrimSpace(out)
			if slug := remoteSlug(w); slug != "" {
				w.Name = slug
				if w.prefix != "" {
					w.Name += "/" + strings.TrimSuffix(w.prefix, "/")
				}
			}
		}
	}
	return w, nil
}

// remoteSlug returns owner/repo from the origin URL.
func remoteSlug(w *Workspace) string {
	out, err := w.gitOut(context.Background(), "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	u := strings.TrimSuffix(strings.TrimSpace(out), ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if j := strings.Index(u, "/"); j >= 0 {
			u = u[j+1:]
		}
	} else if i := strings.Index(u, ":"); i >= 0 {
		u = u[i+1:]
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ""
}

func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
}

func (w *Workspace) gitRun(ctx context.Context, stdin []byte, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", w.Root}, args...)...)
	cmd.Env = gitEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	return out.String(), errb.String(), err
}

func (w *Workspace) gitOut(ctx context.Context, args ...string) (string, error) {
	out, errs, err := w.gitRun(ctx, nil, args...)
	if err != nil {
		msg := strings.TrimSpace(errs)
		if msg == "" {
			msg = err.Error()
		}
		return out, errors.New(msg)
	}
	return out, nil
}

// clean checks a workspace path and returns the absolute file path.
func (w *Workspace) clean(p string) (string, error) {
	c := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if c == "." || strings.HasPrefix(c, "../") || c == ".." || path.IsAbs(c) {
		return "", fmt.Errorf("%s is outside the workspace", p)
	}
	return filepath.Join(w.Root, filepath.FromSlash(c)), nil
}

// Read returns the text of a file. Windows line endings become \n, and
// Write puts them back.
func (w *Workspace) Read(p string) (string, error) {
	abs, err := w.clean(p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a folder", p)
	}
	if st.Size() > maxOpenSize {
		return "", fmt.Errorf("%s is %d MB. The editor opens files up to 4 MB", p, st.Size()>>20)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return "", fmt.Errorf("%s is a binary file", p)
	}
	crlf := bytes.Contains(b, []byte("\r\n"))
	w.mu.Lock()
	w.crlf[p] = crlf
	w.mu.Unlock()
	if crlf {
		b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	}
	return string(b), nil
}

// Write saves a file and keeps its permissions and line endings.
func (w *Workspace) Write(p, text string) error {
	abs, err := w.clean(p)
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(abs); err == nil {
		mode = st.Mode().Perm()
	}
	w.mu.Lock()
	crlf := w.crlf[p]
	w.mu.Unlock()
	if crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	return os.WriteFile(abs, []byte(text), mode)
}

// Head returns the text of a file in the last commit.
func (w *Workspace) Head(p string) (string, bool) {
	if !w.git {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := w.gitOut(ctx, "show", "HEAD:./"+p)
	if err != nil {
		return "", false
	}
	return strings.ReplaceAll(out, "\r\n", "\n"), true
}

var skipWalk = map[string]bool{".git": true, "node_modules": true, ".terraform": true, ".venv": true, ".cache": true, ".idea": true}

// State lists the files and the Git status.
func (w *Workspace) State() State {
	st := State{Root: w.Root, Name: w.Name, Git: w.git, Files: []string{}, Changes: []Change{}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if w.git {
		out, err := w.gitOut(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		if err != nil {
			st.Err = err.Error()
			return st
		}
		deleted := map[string]bool{}
		if d, err := w.gitOut(ctx, "ls-files", "-z", "--deleted"); err == nil {
			for _, f := range strings.Split(d, "\x00") {
				deleted[f] = true
			}
		}
		seen := map[string]bool{}
		for _, f := range strings.Split(out, "\x00") {
			if f == "" || deleted[f] || seen[f] {
				continue
			}
			seen[f] = true
			if len(st.Files) >= maxFiles {
				st.Truncated = true
				break
			}
			st.Files = append(st.Files, f)
		}
		w.status(ctx, &st)
	} else {
		_ = filepath.WalkDir(w.Root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != w.Root && skipWalk[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if len(st.Files) >= maxFiles {
				st.Truncated = true
				return filepath.SkipAll
			}
			if rel, err := filepath.Rel(w.Root, p); err == nil {
				st.Files = append(st.Files, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	sort.Strings(st.Files)
	return st
}

// status reads git status --porcelain=v2.
func (w *Workspace) status(ctx context.Context, st *State) {
	out, err := w.gitOut(ctx, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all", "--", ".")
	if err != nil {
		st.Err = err.Error()
		return
	}
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		switch {
		case strings.HasPrefix(r, "# branch.head "):
			st.Branch = strings.TrimPrefix(r, "# branch.head ")
		case strings.HasPrefix(r, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(r, "# branch.upstream ")
		case strings.HasPrefix(r, "# branch.ab "):
			f := strings.Fields(strings.TrimPrefix(r, "# branch.ab "))
			if len(f) == 2 {
				st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(r, "1 ") || strings.HasPrefix(r, "u "):
			f := strings.SplitN(r, " ", 9)
			if strings.HasPrefix(r, "u ") {
				f = strings.SplitN(r, " ", 11)
			}
			w.addChange(st, f[len(f)-1], f[1])
		case strings.HasPrefix(r, "2 "):
			f := strings.SplitN(r, " ", 10)
			w.addChange(st, f[len(f)-1], "R")
			i++ // the original path follows
		case strings.HasPrefix(r, "? "):
			w.addChange(st, strings.TrimPrefix(r, "? "), "??")
		}
	}
}

func (w *Workspace) addChange(st *State, repoPath, xy string) {
	p := strings.TrimPrefix(repoPath, w.prefix)
	if w.prefix != "" && p == repoPath {
		return
	}
	s := "M"
	switch {
	case xy == "??":
		s = "U"
	case xy == "R" || strings.Contains(xy, "R"):
		s = "R"
	case strings.Contains(xy, "D"):
		s = "D"
	case strings.Contains(xy, "A"):
		s = "A"
	}
	st.Changes = append(st.Changes, Change{Path: p, St: s})
}

// Commit stages and commits the given paths.
func (w *Workspace) Commit(msg string, paths []string) (string, error) {
	if !w.git {
		return "", errors.New("the workspace is not a Git repository")
	}
	if len(paths) == 0 {
		return "", errors.New("there are no changes to commit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	list := []byte(strings.Join(paths, "\x00"))
	if _, errs, err := w.gitRun(ctx, list, "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
		return "", errors.New(strings.TrimSpace(errs))
	}
	out, errs, err := w.gitRun(ctx, list, "commit", "-m", msg, "--pathspec-from-file=-", "--pathspec-file-nul")
	text := strings.TrimSpace(out + "\n" + errs)
	if err != nil {
		return text, errors.New(firstLine(strings.TrimSpace(errs + "\n" + out)))
	}
	return text, nil
}

// Push pushes the branch. A branch without an upstream goes to origin.
func (w *Workspace) Push(upstream string) (string, error) {
	if !w.git {
		return "", errors.New("the workspace is not a Git repository")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"push"}
	if upstream == "" {
		args = append(args, "--set-upstream", "origin", "HEAD")
	}
	out, errs, err := w.gitRun(ctx, nil, args...)
	text := strings.TrimSpace(out + "\n" + errs)
	if err != nil {
		if ctx.Err() != nil {
			return text, errors.New("git push did not finish within 2 minutes")
		}
		return text, errors.New("git push failed")
	}
	return text, nil
}
