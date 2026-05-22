package agent

import (
	"context"
	"net/http"

	"github.com/masakasuno1/macker/internal/authz"
)

// ctxKey carries the resolved peer through the request context.
type ctxKey struct{}

func peerFrom(ctx context.Context) (authz.Peer, bool) {
	p, ok := ctx.Value(ctxKey{}).(authz.Peer)
	return p, ok
}

// authResolve resolves the caller's identity and capability using the shared
// authorization model.
func (s *Server) authResolve(r *http.Request) authz.Peer {
	return authz.Resolve(s.ts, s.cfg.Policy, s.localToken, s.selfLogin, r)
}

// actorString identifies the peer for audit logs.
func actorString(p authz.Peer) string { return p.Actor() }
