package agent

import (
	"sync"
	"time"

	"github.com/masakasuno1/macker/internal/api"
)

// lease records that a client is (or was) attached to a session. The entry is
// kept after expiry so the agent can distinguish an *orphaned* ephemeral
// session (holder vanished without a clean exit) from a cleanly detached one
// (DESIGN.md §4). Entries are removed on a clean unlease or when the session
// no longer exists.
type lease struct {
	clientID  string
	expiry    time.Time
	ephemeral bool
}

// leaseRegistry tracks leases per session name. Safe for concurrent use.
type leaseRegistry struct {
	mu sync.Mutex
	m  map[string]lease
}

func newLeaseRegistry() *leaseRegistry { return &leaseRegistry{m: map[string]lease{}} }

// Renew registers or refreshes a lease.
func (r *leaseRegistry) Renew(name, clientID string, ttl time.Duration, ephemeral bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[name] = lease{clientID: clientID, expiry: time.Now().Add(ttl), ephemeral: ephemeral}
}

// Release removes a lease (a clean detach). It only removes the entry if the
// clientID matches, so a stale client cannot drop a newer holder's lease.
func (r *leaseRegistry) Release(name, clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.m[name]; ok && (clientID == "" || l.clientID == clientID) {
		delete(r.m, name)
	}
}

// Drop removes a lease unconditionally (e.g. the session was killed).
func (r *leaseRegistry) Drop(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, name)
}

// stateInfo is the derived lease view for one session.
type stateInfo struct {
	state  api.SessionState
	client string
}

// view returns the lease-derived state for a session, given whether a tmux
// client is currently attached. It does not prune; call Prune separately.
func (r *leaseRegistry) view(name string, tmuxAttached bool) stateInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.m[name]
	now := time.Now()
	switch {
	case ok && l.expiry.After(now):
		return stateInfo{state: api.StateAttached, client: l.clientID}
	case ok && l.ephemeral:
		// Lease expired without a clean release on an ephemeral session.
		return stateInfo{state: api.StateOrphaned, client: l.clientID}
	case ok:
		return stateInfo{state: api.StateDetached, client: l.clientID}
	case tmuxAttached:
		// No macker lease, but a tmux client is attached (plain ssh/tmux user).
		return stateInfo{state: api.StateAttached}
	default:
		return stateInfo{state: api.StateDetached}
	}
}

// Prune drops lease entries whose session name is not in alive.
func (r *leaseRegistry) Prune(alive map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.m {
		if !alive[name] {
			delete(r.m, name)
		}
	}
}
