package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"st8ks/internal/kube"
	"st8ks/internal/settings"
)

// SyncTimeout is the longest time the first tool call waits for the
// watches to list their objects.
const SyncTimeout = 45 * time.Second

// Options configure Serve.
type Options struct {
	// Kubeconfigs are the files of the --kubeconfig flag. When empty, st8ks
	// reads the same files as the app.
	Kubeconfigs []string
	// Context is the context to connect to. When empty, the current context
	// of the kubeconfig is used.
	Context string
	Version string
}

// Serve answers MCP requests on in and out until the client closes in or
// ctx ends. The connection to the cluster starts in the background, so the
// client gets its handshake fast. A connection error goes to the result of
// every tool, where the agent can read it, and to stderr.
func Serve(ctx context.Context, o Options, in io.ReadCloser, out io.WriteCloser) error {
	m := kube.NewManager(func(string, any) {}, func() kube.LoadOptions {
		s := settings.Open().Get()
		return kube.LoadOptions{Explicit: o.Kubeconfigs, Extra: s.Kubeconfigs, Hidden: s.HiddenKubeconfigs, Scan: s.ScanKubeDir}
	})
	defer m.Shutdown()

	var (
		ready = make(chan struct{})
		cl    *kube.Cluster
		note  string
	)
	name, cerr := pickContext(m, o.Context)
	go func() {
		defer close(ready)
		if cerr != nil {
			Stderr("%v", cerr)
			return
		}
		st, err := m.Connect(name)
		if err != nil || st.Status != kube.StatusConnected {
			msg := st.Error
			if msg == "" && err != nil {
				msg = err.Error()
			}
			cerr = fmt.Errorf("cannot connect to context %s: %s", name, msg)
			Stderr("%v", cerr)
			return
		}
		cl = m.Current()
		wctx, cancel := context.WithTimeout(ctx, SyncTimeout)
		defer cancel()
		if p := cl.WaitSynced(wctx); len(p) > 0 {
			note = "These kinds did not finish loading, so the data can be incomplete: " + strings.Join(p, ", ") + "."
			Stderr("%s", note)
		}
	}()

	src := func(ctx context.Context) (Cluster, string, error) {
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
		if cerr != nil {
			return nil, "", cerr
		}
		if st := cl.State(); st.Status == kube.StatusUnreachable {
			return nil, "", fmt.Errorf("the cluster of context %s is unreachable: %s", name, st.Error)
		}
		return cl, note, nil
	}
	srv := New(src, Info{Version: o.Version, Context: name})
	err := srv.Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// pickContext returns the context to connect to: the wanted one, else the
// current context, else the only one.
func pickContext(m *kube.Manager, want string) (string, error) {
	_ = m.Load()
	var names []string
	for _, c := range m.Contexts() {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		msg := "no kubeconfig context found"
		if e := m.LoadError(); e != "" {
			msg += ": " + e
		}
		return want, errors.New(msg)
	}
	list := strings.Join(names, ", ")
	switch {
	case want != "" && !slices.Contains(names, want):
		return want, fmt.Errorf("context %s not found. The contexts are: %s", want, list)
	case want != "":
		return want, nil
	case slices.Contains(names, m.DefaultContext()):
		return m.DefaultContext(), nil
	case len(names) == 1:
		return names[0], nil
	}
	return "", fmt.Errorf("the kubeconfig has no current context. Start st8ks mcp with --context NAME. The contexts are: %s", list)
}

// Stderr writes one line to stderr. Stdout belongs to the protocol.
func Stderr(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "st8ks mcp: "+format+"\n", a...)
}
