package agent

import (
	"testing"
	"time"

	"github.com/masakasuno1/macker/internal/api"
)

func TestLeaseStateAttached(t *testing.T) {
	r := newLeaseRegistry()
	r.Renew("s", "c1", time.Minute, true)
	if got := r.view("s", false); got.state != api.StateAttached || got.client != "c1" {
		t.Fatalf("got %+v, want attached/c1", got)
	}
}

func TestLeaseStateOrphanedVsDetached(t *testing.T) {
	r := newLeaseRegistry()
	// Expired ephemeral lease => orphaned.
	r.Renew("eph", "c1", -time.Second, true)
	if got := r.view("eph", false); got.state != api.StateOrphaned {
		t.Errorf("ephemeral expired: got %v, want orphaned", got.state)
	}
	// Expired keep lease => detached.
	r.Renew("keep", "c2", -time.Second, false)
	if got := r.view("keep", false); got.state != api.StateDetached {
		t.Errorf("keep expired: got %v, want detached", got.state)
	}
}

func TestLeaseNoEntryUsesTmuxAttachment(t *testing.T) {
	r := newLeaseRegistry()
	if got := r.view("x", true); got.state != api.StateAttached {
		t.Errorf("tmux-attached without lease: got %v, want attached", got.state)
	}
	if got := r.view("x", false); got.state != api.StateDetached {
		t.Errorf("no lease, no tmux client: got %v, want detached", got.state)
	}
}

func TestLeaseReleaseClientGuard(t *testing.T) {
	r := newLeaseRegistry()
	r.Renew("s", "owner", time.Minute, true)
	// A stale/other client must not drop the owner's lease.
	r.Release("s", "intruder")
	if got := r.view("s", false); got.state != api.StateAttached {
		t.Fatalf("intruder dropped lease: got %v", got.state)
	}
	r.Release("s", "owner")
	if got := r.view("s", false); got.state != api.StateDetached {
		t.Fatalf("owner release should detach: got %v", got.state)
	}
}

func TestLeasePrune(t *testing.T) {
	r := newLeaseRegistry()
	r.Renew("gone", "c", time.Minute, true)
	r.Renew("live", "c", time.Minute, true)
	r.Prune(map[string]bool{"live": true})
	if got := r.view("gone", false); got.state != api.StateDetached {
		t.Errorf("pruned entry should be gone (detached): got %v", got.state)
	}
	if got := r.view("live", false); got.state != api.StateAttached {
		t.Errorf("live entry should remain attached: got %v", got.state)
	}
}
