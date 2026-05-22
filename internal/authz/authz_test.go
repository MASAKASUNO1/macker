package authz

import (
	"testing"

	"github.com/masakasuno1/macker/internal/config"
)

func TestCapabilityFor(t *testing.T) {
	p := config.Policy{
		Owners:      []string{"owner@example.com"},
		ExecAllow:   []string{"dev@example.com"},
		AttachAllow: []string{"viewer@example.com"},
	}
	tests := []struct {
		login string
		want  Capability
	}{
		{"owner@example.com", CapExec},
		{"dev@example.com", CapExec},
		{"viewer@example.com", CapAttach},
		{"stranger@example.com", CapNone},
	}
	for _, tt := range tests {
		if got := CapabilityFor(p, tt.login); got != tt.want {
			t.Errorf("CapabilityFor(%q) = %v, want %v", tt.login, got, tt.want)
		}
	}
}

func TestCapabilityForEmptyAttachAllowIsOpen(t *testing.T) {
	p := config.Policy{Owners: []string{"owner@example.com"}}
	if got := CapabilityFor(p, "anyone@example.com"); got != CapAttach {
		t.Errorf("got %v, want CapAttach for open attach policy", got)
	}
}

func TestCapabilityOrdering(t *testing.T) {
	if !(CapExec > CapAttach && CapAttach > CapNone) {
		t.Fatal("capability ordering must be None < Attach < Exec")
	}
}

func TestIsLoopback(t *testing.T) {
	yes := []string{"127.0.0.1:1234", "[::1]:80", "127.0.0.1"}
	for _, a := range yes {
		if !IsLoopback(a) {
			t.Errorf("IsLoopback(%q) = false, want true", a)
		}
	}
	no := []string{"100.64.0.1:4477", "192.168.1.5:22", "8.8.8.8"}
	for _, a := range no {
		if IsLoopback(a) {
			t.Errorf("IsLoopback(%q) = true, want false", a)
		}
	}
}

func TestActor(t *testing.T) {
	if (Peer{Login: "a@b"}).Actor() != "a@b" {
		t.Error("Actor should prefer login")
	}
	if (Peer{NodeName: "n"}).Actor() != "n" {
		t.Error("Actor should fall back to node name")
	}
	if (Peer{}).Actor() != "unknown" {
		t.Error("Actor should be 'unknown' when empty")
	}
}
