// Package netutil holds shared HTTP serving helpers so the agent and collector
// bind and shut down identically.
package netutil

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/masakasuno1/macker/internal/tailnet"
)

// reconcileInterval is how often a hostless listener re-checks the tailnet for
// addresses it has not yet bound. It is a package var so tests can shrink it.
var reconcileInterval = 5 * time.Second

// StatusProvider is the slice of *tailnet.Client that BindAddrs / Serve need.
// Defining it here lets tests inject a fake without shelling out to tailscale.
type StatusProvider interface {
	Available() bool
	Status(ctx context.Context) ([]tailnet.Node, error)
}

// loopbackAddrs returns the v4+v6 loopback bind addresses for port.
func loopbackAddrs(port string) []string {
	return []string{net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("::1", port)}
}

// tailscaleAddrs returns the node's own Tailscale IPs as host:port bind
// addresses. A nil/unavailable provider yields (nil, nil); a status error is
// surfaced so the caller can decide whether to retry.
func tailscaleAddrs(ctx context.Context, ts StatusProvider, port string) ([]string, error) {
	if ts == nil || !ts.Available() {
		return nil, nil
	}
	nodes, err := ts.Status(ctx)
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, n := range nodes {
		if !n.Self {
			continue
		}
		for _, ip := range n.IPs {
			addrs = append(addrs, net.JoinHostPort(ip, port))
		}
	}
	return addrs, nil
}

// BindAddrs computes the addresses to bind for listen. When listen has no host
// (":port"), it returns loopback (v4+v6) plus the node's Tailscale IPs, so the
// service is reachable on the tailnet without being exposed on every LAN
// interface. An explicit host:port is honored verbatim.
//
// When the tailnet status is momentarily unavailable (e.g. tailscaled is not
// ready yet at boot) only loopback is returned here; Serve's reconcile loop
// binds the Tailscale IPs once they appear, so a hostless listener is not
// permanently stuck on loopback.
func BindAddrs(ctx context.Context, ts StatusProvider, listen string) []string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host != "" {
		return []string{listen}
	}
	addrs := loopbackAddrs(port)
	taddrs, err := tailscaleAddrs(ctx, ts, port)
	if err != nil {
		log.Printf("macker: tailnet status unavailable, binding loopback only for now (will retry): %v", err)
	}
	return dedup(append(addrs, taddrs...))
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// binder owns the set of live listeners for one http.Server and lets new
// addresses be bound after Serve has started (used by the reconcile loop).
type binder struct {
	srv     *http.Server
	label   string
	version string
	errCh   chan error

	mu    sync.Mutex
	bound map[string]net.Listener
}

// listen binds addr and starts serving it. It is a no-op (returns false) if
// addr is already bound or the bind fails. ok reports whether addr is now live.
func (b *binder) listen(addr string) (ok bool) {
	b.mu.Lock()
	if _, exists := b.bound[addr]; exists {
		b.mu.Unlock()
		return true
	}
	b.mu.Unlock()

	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("macker %s: cannot listen on %s: %v", b.label, addr, err)
		return false
	}

	b.mu.Lock()
	b.bound[addr] = l
	b.mu.Unlock()

	go func() { b.errCh <- b.srv.Serve(l) }()
	log.Printf("macker %s %s listening on %s", b.label, b.version, l.Addr())
	return true
}

func (b *binder) isBound(addr string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.bound[addr]
	return ok
}

func (b *binder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.bound)
}

// reconcile periodically re-resolves the node's Tailscale addresses and binds
// any that are not yet live, then returns once every desired address is bound.
// While the tailnet status stays unavailable it keeps retrying, so an agent
// that started before tailscaled was ready becomes reachable on the tailnet
// without a restart. listen must be hostless (":port").
func (b *binder) reconcile(ctx context.Context, ts StatusProvider, listen string) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return
	}
	t := time.NewTicker(reconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			want, err := tailscaleAddrs(ctx, ts, port)
			if err != nil {
				// tailscaled still not ready; keep waiting.
				continue
			}
			allBound := len(want) > 0
			for _, addr := range want {
				if b.isBound(addr) {
					continue
				}
				if b.listen(addr) {
					log.Printf("macker %s: bound newly-available tailnet address %s", b.label, addr)
				} else {
					allBound = false
				}
			}
			if allBound {
				return // healthy: every Tailscale IP is now bound.
			}
		}
	}
}

// Serve runs srv on the addresses for listen until ctx is cancelled, then shuts
// down gracefully. label is used in log lines (e.g. "agent", "collector").
//
// For a hostless listen (":port") it binds loopback immediately and then keeps
// trying to bind the node's Tailscale IPs in the background, so a boot-time
// race with tailscaled does not leave the service stuck on loopback only.
func Serve(ctx context.Context, srv *http.Server, ts StatusProvider, listen, label, version string) error {
	b := &binder{
		srv:     srv,
		label:   label,
		version: version,
		errCh:   make(chan error, 16),
		bound:   map[string]net.Listener{},
	}

	addrs := BindAddrs(ctx, ts, listen)
	for _, a := range addrs {
		b.listen(a)
	}
	if b.count() == 0 {
		return fmt.Errorf("%s: failed to bind any of %v", label, addrs)
	}

	// Only a hostless listen gets the tailnet reconcile loop; an explicit
	// host:port is bound verbatim and left alone.
	if host, _, err := net.SplitHostPort(listen); err == nil && host == "" && ts != nil && ts.Available() {
		go b.reconcile(ctx, ts, listen)
	}

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-b.errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return err
	}
}
