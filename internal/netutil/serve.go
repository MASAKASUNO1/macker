// Package netutil holds shared HTTP serving helpers so the agent and collector
// bind and shut down identically.
package netutil

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/masakasuno1/macker/internal/tailnet"
)

// BindAddrs computes the addresses to bind for listen. When listen has no host
// (":port"), it returns loopback (v4+v6) plus the node's Tailscale IPs, so the
// service is reachable on the tailnet without being exposed on every LAN
// interface. An explicit host:port is honored verbatim.
func BindAddrs(ctx context.Context, ts *tailnet.Client, listen string) []string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host != "" {
		return []string{listen}
	}
	addrs := []string{net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("::1", port)}
	if ts != nil && ts.Available() {
		if nodes, err := ts.Status(ctx); err == nil {
			for _, n := range nodes {
				if !n.Self {
					continue
				}
				for _, ip := range n.IPs {
					addrs = append(addrs, net.JoinHostPort(ip, port))
				}
			}
		} else {
			log.Printf("macker: tailnet status unavailable, binding loopback only: %v", err)
		}
	}
	return dedup(addrs)
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

// Serve runs srv on every address in addrs until ctx is cancelled, then shuts
// down gracefully. label is used in log lines (e.g. "agent", "collector").
func Serve(ctx context.Context, srv *http.Server, addrs []string, label, version string) error {
	listeners := make([]net.Listener, 0, len(addrs))
	for _, a := range addrs {
		l, err := net.Listen("tcp", a)
		if err != nil {
			log.Printf("macker %s: cannot listen on %s: %v", label, a, err)
			continue
		}
		listeners = append(listeners, l)
	}
	if len(listeners) == 0 {
		return fmt.Errorf("%s: failed to bind any of %v", label, addrs)
	}

	errCh := make(chan error, len(listeners))
	for _, l := range listeners {
		go func(l net.Listener) { errCh <- srv.Serve(l) }(l)
		log.Printf("macker %s %s listening on %s", label, version, l.Addr())
	}

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return err
	}
}
