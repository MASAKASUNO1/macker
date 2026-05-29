package netutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/masakasuno1/macker/internal/tailnet"
)

// fakeStatus is a StatusProvider whose Status result can change over time, to
// simulate tailscaled becoming ready after the agent has already started.
type fakeStatus struct {
	available bool
	mu        sync.Mutex
	ips       []string
	err       error
}

func (f *fakeStatus) Available() bool { return f.available }

func (f *fakeStatus) set(ips []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ips, f.err = ips, err
}

func (f *fakeStatus) Status(context.Context) ([]tailnet.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return []tailnet.Node{{Self: true, IPs: f.ips}}, nil
}

// freePort returns a TCP port that is free at call time on loopback.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

func TestBindAddrs(t *testing.T) {
	// Unavailable provider: loopback only.
	got := BindAddrs(context.Background(), &fakeStatus{available: false}, ":4477")
	want := []string{"127.0.0.1:4477", "[::1]:4477"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unavailable: got %v want %v", got, want)
	}

	// Status error: loopback only (Tailscale IPs are added later by reconcile).
	fs := &fakeStatus{available: true}
	fs.set(nil, errors.New("tailscaled not ready"))
	got = BindAddrs(context.Background(), fs, ":4477")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("status error: got %v want %v", got, want)
	}

	// Status ok: loopback + the node's Tailscale IP.
	fs.set([]string{"100.64.0.1"}, nil)
	got = BindAddrs(context.Background(), fs, ":4477")
	want = []string{"127.0.0.1:4477", "[::1]:4477", "100.64.0.1:4477"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("status ok: got %v want %v", got, want)
	}

	// Explicit host is honored verbatim.
	got = BindAddrs(context.Background(), &fakeStatus{available: true}, "0.0.0.0:4477")
	if fmt.Sprint(got) != fmt.Sprint([]string{"0.0.0.0:4477"}) {
		t.Fatalf("explicit host: got %v", got)
	}
}

// TestReconcileBindsLateAddr is the regression test for the boot-time race that
// left an agent bound to loopback only: the tailnet status is unavailable when
// the reconcile loop starts, then becomes ready, and the address must get bound
// without a restart. The "tailnet IP" here is 127.0.0.1 on a port that was not
// part of the initial bind set, so the test is portable (no loopback aliasing).
func TestReconcileBindsLateAddr(t *testing.T) {
	orig := reconcileInterval
	reconcileInterval = 15 * time.Millisecond
	defer func() { reconcileInterval = orig }()

	port := freePort(t)
	addr := net.JoinHostPort("127.0.0.1", port)

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	b := &binder{srv: srv, label: "test", version: "v0", errCh: make(chan error, 16), bound: map[string]net.Listener{}}

	fs := &fakeStatus{available: true}
	fs.set(nil, errors.New("tailscaled not ready")) // unavailable at startup

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.reconcile(ctx, fs, ":"+port)

	waitDial(t, addr, false) // nothing bound while status is unavailable
	fs.set([]string{"127.0.0.1"}, nil)
	waitDial(t, addr, true) // reconcile bound it once status became ready

	cancel()
	shutCtx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	_ = srv.Shutdown(shutCtx)
}

// TestServeLoopbackAndShutdown is a smoke test that Serve binds loopback for a
// hostless listen, serves requests, and shuts down cleanly on ctx cancel.
func TestServeLoopbackAndShutdown(t *testing.T) {
	port := freePort(t)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, &fakeStatus{available: false}, ":"+port, "test", "v0") }()

	addr := net.JoinHostPort("127.0.0.1", port)
	waitDial(t, addr, true)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not shut down")
	}
}

// waitDial polls addr until it is (want=true) or is not (want=false) accepting
// connections, failing the test if that state is not reached in time.
func waitDial(t *testing.T, addr string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		got := err == nil
		if c != nil {
			c.Close()
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: reachable=%v, wanted %v", addr, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
