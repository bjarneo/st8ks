package kube

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/streaming/pkg/httpstream"
)

// ExecOpts selects the container for a terminal session.
type ExecOpts struct {
	NS        string `json:"ns"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Attach    bool   `json:"attach"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

// ExecChunk carries terminal output to the frontend.
type ExecChunk struct {
	ID   string `json:"id"`
	Data string `json:"data,omitempty"`
	End  bool   `json:"end,omitempty"`
	Err  string `json:"err,omitempty"`
}

// ExecSession is one running terminal.
type ExecSession struct {
	stdin  *io.PipeWriter
	sizes  chan remotecommand.TerminalSize
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

// Next implements remotecommand.TerminalSizeQueue.
func (s *ExecSession) Next() *remotecommand.TerminalSize {
	select {
	case sz := <-s.sizes:
		return &sz
	case <-s.done:
		return nil
	}
}

// Write sends keyboard input to the container.
func (s *ExecSession) Write(data []byte) error {
	_, err := s.stdin.Write(data)
	return err
}

// Resize changes the terminal size.
func (s *ExecSession) Resize(cols, rows uint16) {
	select {
	case <-s.done:
	case s.sizes <- remotecommand.TerminalSize{Width: cols, Height: rows}:
	default:
	}
}

// Close ends the session.
func (s *ExecSession) Close() {
	s.once.Do(func() {
		s.cancel()
		s.stdin.Close()
	})
}

type termWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *termWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	w.mu.Unlock()
	return len(p), nil
}

func (w *termWriter) take() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buf
	w.buf = nil
	return b
}

const shellCmd = `export TERM=xterm-256color; if command -v bash >/dev/null 2>&1; then exec bash; elif command -v ash >/dev/null 2>&1; then exec ash; else exec sh; fi`

// StartExec opens an interactive shell, or attaches to a debug container.
// onEnd runs when the session ends.
func (c *Cluster) StartExec(id string, o ExecOpts, onEnd func()) (*ExecSession, error) {
	req := c.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(o.NS).Name(o.Pod)
	if o.Attach {
		req = req.SubResource("attach").VersionedParams(&corev1.PodAttachOptions{
			Container: o.Container, Stdin: true, Stdout: true, TTY: true,
		}, scheme.ParameterCodec)
	} else {
		req = req.SubResource("exec").VersionedParams(&corev1.PodExecOptions{
			Container: o.Container, Command: []string{"/bin/sh", "-c", shellCmd},
			Stdin: true, Stdout: true, TTY: true,
		}, scheme.ParameterCodec)
	}
	spdyExec, err := remotecommand.NewSPDYExecutor(c.cfg, "POST", req.URL())
	if err != nil {
		return nil, err
	}
	wsExec, err := remotecommand.NewWebSocketExecutor(c.cfg, "GET", req.URL().String())
	if err != nil {
		return nil, err
	}
	executor, err := remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(c.ctx)
	s := &ExecSession{stdin: pw, sizes: make(chan remotecommand.TerminalSize, 4), done: make(chan struct{}), cancel: cancel}
	if o.Cols > 0 && o.Rows > 0 {
		s.sizes <- remotecommand.TerminalSize{Width: o.Cols, Height: o.Rows}
	}
	out := &termWriter{}
	done := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		tk := time.NewTicker(16 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				if b := out.take(); len(b) > 0 {
					c.emitFn("exec", ExecChunk{ID: id, Data: base64.StdEncoding.EncodeToString(b)})
				}
				return
			case <-tk.C:
				if b := out.take(); len(b) > 0 {
					c.emitFn("exec", ExecChunk{ID: id, Data: base64.StdEncoding.EncodeToString(b)})
				}
			}
		}
	}()
	go func() {
		err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin: pr, Stdout: out, Tty: true, TerminalSizeQueue: s,
		})
		close(done)
		// The last output goes out before the end marker.
		<-flushed
		close(s.done)
		msg := ""
		if err != nil && ctx.Err() == nil {
			msg = errString(err)
		}
		c.emitFn("exec", ExecChunk{ID: id, End: true, Err: msg})
		s.Close()
		if onEnd != nil {
			onEnd()
		}
	}()
	return s, nil
}

// StartDebugContainer adds an ephemeral container that shares the process
// namespace of target, like kubectl debug -it --target.
func (c *Cluster) StartDebugContainer(ns, pod, target, image string) (string, error) {
	if image == "" {
		image = "busybox:1.36"
	}
	ctx, cancel := context.WithTimeout(c.ctx, 90*time.Second)
	defer cancel()
	p, err := c.cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return "", wrapf(err, "cannot read pod %s", pod)
	}
	buf := make([]byte, 3)
	_, _ = rand.Read(buf)
	name := "debugger-" + hex.EncodeToString(buf)[:5]
	p2 := p.DeepCopy()
	p2.Spec.EphemeralContainers = append(p2.Spec.EphemeralContainers, corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name: name, Image: image, Stdin: true, TTY: true,
			ImagePullPolicy:          corev1.PullIfNotPresent,
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		},
		TargetContainerName: target,
	})
	if _, err := c.cs.CoreV1().Pods(ns).UpdateEphemeralContainers(ctx, pod, p2, metav1.UpdateOptions{FieldManager: "st8ks"}); err != nil {
		return "", wrapf(err, "cannot add a debug container")
	}
	for {
		p, err := c.cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
		if err == nil {
			for _, st := range p.Status.EphemeralContainerStatuses {
				if st.Name != name {
					continue
				}
				if st.State.Running != nil {
					return name, nil
				}
				if st.State.Terminated != nil {
					return "", fmt.Errorf("debug container stopped: %s", st.State.Terminated.Reason)
				}
				if w := st.State.Waiting; w != nil && (w.Reason == "ErrImagePull" || w.Reason == "ImagePullBackOff") {
					return "", fmt.Errorf("debug image %s cannot be pulled: %s", image, w.Message)
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", errors.New("the debug container did not start within 90 seconds")
		case <-time.After(time.Second):
		}
	}
}
