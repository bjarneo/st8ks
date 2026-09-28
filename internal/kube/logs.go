package kube

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LogOpts selects one container log.
type LogOpts struct {
	NS        string `json:"ns"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Previous  bool   `json:"previous"`
	Tail      int64  `json:"tail"`
}

// LogChunk carries new log lines to the frontend.
type LogChunk struct {
	ID      string   `json:"id"`
	Lines   []string `json:"lines,omitempty"`
	End     bool     `json:"end,omitempty"`
	Waiting bool     `json:"waiting,omitempty"`
	Err     string   `json:"err,omitempty"`
}

const maxLine = 64 << 10

func (o LogOpts) podLogOptions(follow bool) *corev1.PodLogOptions {
	opts := &corev1.PodLogOptions{Container: o.Container, Previous: o.Previous, Follow: follow && !o.Previous, Timestamps: true}
	if o.Tail > 0 {
		t := o.Tail
		opts.TailLines = &t
	}
	return opts
}

// StreamLogs follows a container log until ctx ends. It sends lines in
// batches every 50 ms so a fast log does not flood the frontend.
func (c *Cluster) StreamLogs(ctx context.Context, id string, o LogOpts) {
	var mu sync.Mutex
	var buf []string
	// emitMu keeps the batches and the status chunks in order.
	var emitMu sync.Mutex
	flush := func() {
		emitMu.Lock()
		defer emitMu.Unlock()
		mu.Lock()
		lines := buf
		buf = nil
		mu.Unlock()
		if len(lines) > 0 {
			c.emitFn("log", LogChunk{ID: id, Lines: lines})
		}
	}
	send := func(ch LogChunk) {
		flush()
		emitMu.Lock()
		c.emitFn("log", ch)
		emitMu.Unlock()
	}
	done := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		tk := time.NewTicker(50 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				flush()
			}
		}
	}()
	defer func() {
		close(done)
		<-flushed
	}()

	var lastTS time.Time
	for {
		opts := o.podLogOptions(true)
		if !lastTS.IsZero() {
			// Resume after a restart or a dropped stream without replaying
			// lines that were already sent.
			opts.TailLines = nil
			opts.SinceTime = &metav1.Time{Time: lastTS}
		}
		rc, err := c.cs.CoreV1().Pods(o.NS).GetLogs(o.Pod, opts).Stream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !o.Previous && (apierrors.IsBadRequest(err) || strings.Contains(err.Error(), "waiting to start")) {
				send(LogChunk{ID: id, Waiting: true, Err: errString(err)})
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}
			send(LogChunk{ID: id, End: true, Err: errString(err)})
			return
		}
		r := bufio.NewReaderSize(rc, 64<<10)
		resumeAfter := lastTS
		for {
			line, err := r.ReadString('\n')
			if len(line) > 0 {
				line = strings.TrimRight(line, "\r\n")
				if len(line) > maxLine {
					line = line[:maxLine] + " …"
				}
				if sp := strings.IndexByte(line, ' '); sp > 0 {
					if ts, err := time.Parse(time.RFC3339Nano, line[:sp]); err == nil {
						// sinceTime has second precision, so skip the lines
						// of that second that were already sent.
						if !resumeAfter.IsZero() && !ts.After(resumeAfter) {
							continue
						}
						lastTS = ts
					}
				}
				mu.Lock()
				buf = append(buf, line)
				mu.Unlock()
			}
			if err != nil {
				break
			}
		}
		rc.Close()
		if ctx.Err() != nil || o.Previous || !c.canRestart(o.NS, o.Pod, o.Container) {
			send(LogChunk{ID: id, End: true})
			return
		}
		// The container stopped. Wait for a restart and follow again.
		send(LogChunk{ID: id, Waiting: true})
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// canRestart reports if a stopped container can start again, so that
// following its log makes sense.
func (c *Cluster) canRestart(ns, pod, container string) bool {
	o := c.getObj("Pods", ns, pod)
	if o == nil {
		// The pod is gone, or the pod table is not ready yet.
		return c.indexer("Pods") == nil
	}
	p := o.(*corev1.Pod)
	if p.DeletionTimestamp != nil || podTerminal(p) {
		return false
	}
	if p.Spec.RestartPolicy == corev1.RestartPolicyNever {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == container && cs.State.Terminated != nil {
				return false
			}
		}
	}
	return true
}

// SaveLogs writes the complete log of a container to a file.
func (c *Cluster) SaveLogs(o LogOpts, path string) (int64, error) {
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Minute)
	defer cancel()
	opts := o.podLogOptions(false)
	opts.TailLines = nil
	rc, err := c.cs.CoreV1().Pods(o.NS).GetLogs(o.Pod, opts).Stream(ctx)
	if err != nil {
		return 0, errors.New(errString(err))
	}
	defer rc.Close()
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, rc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}
