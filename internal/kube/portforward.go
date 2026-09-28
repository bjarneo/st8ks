package kube

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/streaming/pkg/httpstream"
)

// Forward describes one running port-forward.
type Forward struct {
	ID      string `json:"id"`
	Context string `json:"context"`
	NS      string `json:"ns"`
	Pod     string `json:"pod"`
	Remote  int    `json:"remote"`
	Local   int    `json:"local"`
	Status  string `json:"status"`
	Err     string `json:"err,omitempty"`
}

// forwardHandle controls one running port-forward.
type forwardHandle struct {
	info  Forward
	stop  chan struct{}
	once  sync.Once
	ended bool // guarded by Manager.mu
}

// Close stops the forward.
func (f *forwardHandle) Close() { f.once.Do(func() { close(f.stop) }) }

func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// StartForward forwards a local port to a pod port. It uses the same local
// port when it is free and a random port otherwise. onEnd runs when the
// forward stops by itself.
func (c *Cluster) StartForward(id, ns, pod string, remote int, onEnd func(f *forwardHandle, err string)) (*forwardHandle, error) {
	req := c.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward")
	transport, upgrader, err := spdy.RoundTripperFor(c.cfg)
	if err != nil {
		return nil, err
	}
	// The WebSocket tunnel returns its upgrade errors as k8s.io/streaming
	// types, so the fallback to SPDY checks those types.
	var dialer httpstream.Dialer = spdy.NewDialerForStreaming(upgrader, &http.Client{Transport: transport}, "POST", req.URL())
	if ws, err := portforward.NewSPDYOverWebsocketDialerForStreaming(req.URL(), c.cfg); err == nil {
		dialer = portforward.NewFallbackDialerForStreaming(ws, dialer, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	}
	local := remote
	if local < 1024 || !portFree(local) {
		local = 0
	}
	f := &forwardHandle{info: Forward{ID: id, Context: c.Name, NS: ns, Pod: pod, Remote: remote, Status: "Starting"}, stop: make(chan struct{})}
	ready := make(chan struct{})
	errOut := &syncBuffer{}
	fw, err := portforward.NewOnAddressesForStreaming(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("%d:%d", local, remote)}, f.stop, ready, io.Discard, errOut)
	if err != nil {
		return nil, err
	}
	failed := make(chan error, 1)
	go func() {
		err := fw.ForwardPorts()
		select {
		case <-ready:
			msg := ""
			if err != nil {
				msg = errString(err)
			}
			onEnd(f, msg)
		default:
			if err == nil {
				err = errors.New("port-forward stopped before it was ready")
			}
			failed <- err
		}
	}()
	select {
	case <-ready:
	case err := <-failed:
		return nil, errors.New(strings.TrimSpace(errString(err) + " " + errOut.String()))
	case <-time.After(20 * time.Second):
		f.Close()
		return nil, errors.New("port-forward did not start within 20 seconds")
	}
	if ports, err := fw.GetPorts(); err == nil && len(ports) > 0 {
		f.info.Local = int(ports[0].Local)
	}
	f.info.Status = "Active"
	return f, nil
}
