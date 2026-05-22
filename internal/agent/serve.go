package agent

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/masakasuno1/macker/internal/netutil"
)

// logRequests is a minimal access log middleware.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s (%s)", r.Method, r.URL.Path, sw.status, r.RemoteAddr, time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

// Serve runs the agent until ctx is cancelled, then shuts down gracefully.
// When the configured listen address has no host (":port"), the agent binds
// only to loopback and the node's Tailscale IPs rather than all interfaces, so
// it is not exposed to the wider LAN. An explicit host:port is honored as-is.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	addrs := netutil.BindAddrs(ctx, s.ts, s.cfg.Listen)
	return netutil.Serve(ctx, srv, addrs, "agent", Version)
}
