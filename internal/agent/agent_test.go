package agent

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masakasuno1/macker/internal/api"
	"github.com/masakasuno1/macker/internal/config"
	"github.com/masakasuno1/macker/internal/eventlog"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	log, err := eventlog.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	cfg := config.Config{Node: "testnode"}
	// ts is nil: no remote authorization is possible, so only loopback works.
	return New(cfg, nil, log)
}

func TestHealthLoopback(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health: code = %d, want 200", rec.Code)
	}
}

func TestRemotePeerForbiddenWithoutTailscale(t *testing.T) {
	s := newTestServer(t)
	// A non-loopback peer with no tailscale resolver must fail closed.
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/sessions"},
		{http.MethodPost, "/v1/exec"},
		{http.MethodDelete, "/v1/sessions/main"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, c.path, nil)
		req.RemoteAddr = "100.64.1.2:40000"
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: code = %d, want 403", c.method, c.path, rec.Code)
		}
	}
}

func TestExecLoopback(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	body := `{"command":"echo","args":["hi"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/exec", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("exec: code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hi") {
		t.Fatalf("exec output missing 'hi': %s", rec.Body.String())
	}
}

func TestLoopbackTokenRequired(t *testing.T) {
	s := newTestServer(t)
	s.SetLocalToken("sekret")

	// Loopback without the token must be denied even though it is loopback.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("loopback without token: code = %d, want 403", rec.Code)
	}

	// Loopback with the correct token is the owner.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Authorization", "Bearer sekret")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback with token: code = %d, want 200", rec.Code)
	}
}

func TestLeaseLifecycleAndListState(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	// Register a lease for a session named "main".
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/main/lease",
		strings.NewReader(`{"client_id":"c1","ttl_sec":60,"ephemeral":true}`))
	req.RemoteAddr = "127.0.0.1:1"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("lease code = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if got := s.leases.view("main", false); got.state != api.StateAttached {
		t.Fatalf("after lease, state = %v, want attached", got.state)
	}

	// Unlease cleanly => detached.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/sessions/main/unlease",
		strings.NewReader(`{"client_id":"c1"}`))
	req.RemoteAddr = "127.0.0.1:1"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unlease code = %d, want 204", rec.Code)
	}
	if got := s.leases.view("main", false); got.state != api.StateDetached {
		t.Fatalf("after unlease, state = %v, want detached", got.state)
	}
}

func TestExecRejectsUnknownFields(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/exec", strings.NewReader(`{"cmd":"echo"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: code = %d, want 400", rec.Code)
	}
}
