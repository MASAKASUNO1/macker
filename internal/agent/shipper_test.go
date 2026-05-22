package agent

import "testing"

func TestIsLoopbackURL(t *testing.T) {
	loopback := []string{
		"http://127.0.0.1:4478",
		"http://[::1]:4478",
		"http://localhost:4478",
		"http://localhost",
	}
	for _, u := range loopback {
		if !isLoopbackURL(u) {
			t.Errorf("isLoopbackURL(%q) = false, want true", u)
		}
	}
	// These must NOT be treated as loopback — the local token must not leak to
	// a lookalike remote host.
	remote := []string{
		"http://localhost.attacker.example:4478",
		"http://127.0.0.1.attacker.example/",
		"http://hub.work.ts.net:4478",
		"http://100.64.0.1:4478",
		"",
		"://bad",
	}
	for _, u := range remote {
		if isLoopbackURL(u) {
			t.Errorf("isLoopbackURL(%q) = true, want false", u)
		}
	}
}
