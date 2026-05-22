// Package authz centralizes macker's authorization model so the agent and the
// collector enforce it identically (see DESIGN.md §3): transport trust and
// membership come from Tailscale; this layer only decides capability.
//
//   - loopback callers are the local owner, but only if they present the local
//     token (when one is configured) — this stops a different local user on a
//     multi-user host from driving the daemon;
//   - remote callers are identified via `tailscale whois` (unspoofable, since
//     it resolves the real socket address) and mapped to a capability by policy.
//
// It fails closed: anything it cannot positively authorize gets CapNone.
package authz

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/masakasuno1/macker/internal/config"
	"github.com/masakasuno1/macker/internal/tailnet"
)

// Capability is an ordered authorization level; higher includes lower.
type Capability int

const (
	CapNone   Capability = iota // unauthorized
	CapAttach                   // list/health/attach lifecycle, ship/read per role
	CapExec                     // create sessions, exec, kill, read audit log
)

func (c Capability) String() string {
	switch c {
	case CapExec:
		return "exec"
	case CapAttach:
		return "attach"
	default:
		return "none"
	}
}

// CapabilityFor maps a tailnet login to a capability per the policy.
func CapabilityFor(p config.Policy, login string) Capability {
	if contains(p.Owners, login) || contains(p.ExecAllow, login) {
		return CapExec
	}
	// Empty AttachAllow means "any authenticated tailnet peer"; membership is
	// already enforced by Tailscale.
	if len(p.AttachAllow) == 0 || contains(p.AttachAllow, login) {
		return CapAttach
	}
	return CapNone
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Peer is a resolved caller identity and capability.
type Peer struct {
	Login    string
	NodeName string
	Cap      Capability
}

// Actor returns a stable string identifying the peer for audit logs.
func (p Peer) Actor() string {
	if p.Login != "" {
		return p.Login
	}
	if p.NodeName != "" {
		return p.NodeName
	}
	return "unknown"
}

// IsLoopback reports whether remoteAddr is a loopback address. Reaching
// loopback already requires local access, so loopback peers are the local user.
func IsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// BearerToken extracts a bearer token from the Authorization header.
func BearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// Resolve determines the capability of the caller behind r. localToken, when
// non-empty, is required of loopback callers. ts may be nil (then only loopback
// can be authorized).
func Resolve(ts *tailnet.Client, policy config.Policy, localToken string, r *http.Request) Peer {
	if IsLoopback(r.RemoteAddr) {
		if localToken != "" && subtle.ConstantTimeCompare([]byte(BearerToken(r)), []byte(localToken)) != 1 {
			return Peer{Cap: CapNone}
		}
		return Peer{Login: "local", Cap: CapExec}
	}
	if ts == nil || !ts.Available() {
		return Peer{Cap: CapNone}
	}
	id, err := ts.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil || id == nil {
		return Peer{Cap: CapNone}
	}
	return Peer{Login: id.Login, NodeName: id.NodeName, Cap: CapabilityFor(policy, id.Login)}
}
