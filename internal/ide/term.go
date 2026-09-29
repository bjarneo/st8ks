package ide

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/creack/pty"
)

// termChunk has the same JSON shape as the pod terminal events, so the
// frontend routes both the same way.
type termChunk struct {
	ID   string `json:"id"`
	Data string `json:"data,omitempty"`
	End  bool   `json:"end,omitempty"`
	Err  string `json:"err,omitempty"`
}

type term struct {
	f    *os.File
	cmd  *exec.Cmd
	once sync.Once
}

func (t *term) close() {
	t.once.Do(func() {
		// Closing the PTY sends SIGHUP to the shell.
		_ = t.f.Close()
	})
}

// Terminals runs local shells for the IDE.
type Terminals struct {
	emit func(string, any)
	mu   sync.Mutex
	s    map[string]*term
}

// NewTerminals creates the terminal manager.
func NewTerminals(emit func(string, any)) *Terminals {
	return &Terminals{emit: emit, s: map[string]*term{}}
}

func shellPath() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	for _, s := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}

// Start opens a shell in dir with extra environment variables.
func (ts *Terminals) Start(dir string, env []string, cols, rows uint16) (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("the IDE terminal needs Linux or macOS")
	}
	sh := shellPath()
	args := []string{}
	if runtime.GOOS == "darwin" {
		args = append(args, "-l")
	}
	cmd := exec.Command(sh, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=st8ks"), env...)
	if cols == 0 || rows == 0 {
		cols, rows = 100, 24
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return "", err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	id := "t-" + hex.EncodeToString(b)
	t := &term{f: f, cmd: cmd}
	ts.mu.Lock()
	ts.s[id] = t
	ts.mu.Unlock()
	go ts.pump(id, t)
	return id, nil
}

// pump sends the output in batches, at most one event per frame.
func (ts *Terminals) pump(id string, t *term) {
	var mu sync.Mutex
	var buf []byte
	done := make(chan struct{})
	go func() {
		tk := time.NewTicker(16 * time.Millisecond)
		defer tk.Stop()
		flush := func() {
			mu.Lock()
			b := buf
			buf = nil
			mu.Unlock()
			if len(b) > 0 {
				ts.emit("exec", termChunk{ID: id, Data: base64.StdEncoding.EncodeToString(b)})
			}
		}
		for {
			select {
			case <-done:
				flush()
				_ = t.cmd.Wait()
				msg := ""
				if st := t.cmd.ProcessState; st != nil && !st.Success() && st.ExitCode() > 0 {
					msg = "exit code " + strconv.Itoa(st.ExitCode())
				}
				ts.emit("exec", termChunk{ID: id, End: true, Err: msg})
				ts.mu.Lock()
				delete(ts.s, id)
				ts.mu.Unlock()
				return
			case <-tk.C:
				flush()
			}
		}
	}()
	p := make([]byte, 32<<10)
	for {
		n, err := t.f.Read(p)
		if n > 0 {
			mu.Lock()
			buf = append(buf, p[:n]...)
			mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	t.close()
	close(done)
}

func (ts *Terminals) get(id string) *term {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.s[id]
}

// Input sends base64 keyboard input.
func (ts *Terminals) Input(id, data string) error {
	t := ts.get(id)
	if t == nil {
		return errors.New("the terminal session ended")
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return err
	}
	_, err = t.f.Write(b)
	return err
}

// Resize changes the terminal size.
func (ts *Terminals) Resize(id string, cols, rows uint16) {
	if t := ts.get(id); t != nil && cols > 0 && rows > 0 {
		_ = pty.Setsize(t.f, &pty.Winsize{Cols: cols, Rows: rows})
	}
}

// Stop closes a terminal.
func (ts *Terminals) Stop(id string) {
	if t := ts.get(id); t != nil {
		t.close()
	}
}

// StopAll closes every terminal.
func (ts *Terminals) StopAll() {
	ts.mu.Lock()
	all := make([]*term, 0, len(ts.s))
	for _, t := range ts.s {
		all = append(all, t)
	}
	ts.mu.Unlock()
	for _, t := range all {
		t.close()
	}
}
