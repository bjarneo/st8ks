// Package shellenv loads the environment of the user's login shell.
//
// A desktop app that starts from the Dock, Finder or an app launcher does not
// get the PATH and variables from the shell profile. Kubeconfig exec plugins
// such as aws, gke-gcloud-auth-plugin and kubelogin then fail. This package
// copies the login shell environment into the process.
package shellenv

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const marker = "__ST8KS_ENV__"

// Needed reports if the process probably did not start from a terminal.
func Needed() bool {
	if os.Getenv("ST8KS_NO_SHELL_ENV") != "" || runtime.GOOS == "windows" {
		return false
	}
	return runtime.GOOS == "darwin" || os.Getenv("TERM") == ""
}

// Load runs the login shell and copies its environment. Variables that are
// already set keep their value, except PATH, which gets the shell entries
// first.
func Load() {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", "printf '%s\\n' "+marker+"; env -0")
	cmd.Stdin = nil
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return
	}
	i := bytes.Index(out, []byte(marker+"\n"))
	if i < 0 {
		return
	}
	for _, kv := range bytes.Split(out[i+len(marker)+1:], []byte{0}) {
		k, v, ok := strings.Cut(string(kv), "=")
		if !ok || k == "" || strings.HasPrefix(k, "_") || k == "SHLVL" || k == "PWD" || k == "OLDPWD" {
			continue
		}
		if k == "PATH" {
			os.Setenv("PATH", mergePath(v, os.Getenv("PATH")))
			continue
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}

func mergePath(first, second string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range append(strings.Split(first, ":"), strings.Split(second, ":")...) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, ":")
}
