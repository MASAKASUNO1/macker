package collector

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masakasuno1/macker/internal/config"
	"github.com/masakasuno1/macker/internal/eventlog"
	"github.com/masakasuno1/macker/internal/tailnet"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewServer(store, tailnet.New(""), config.Policy{}) // no tailscale: loopback only
}

func TestCollectThenQueryLoopback(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	body := `{"events":[
      {"id":"01A","tenant":"tn","node":"mac","type":"exec"},
      {"id":"01B","tenant":"tn","node":"mac","type":"exec"}
    ]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/collect", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("collect code = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"stored":2`) {
		t.Fatalf("expected stored:2, got %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/events?tenant=tn&node=mac", nil)
	req.RemoteAddr = "127.0.0.1:1"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "01A") {
		t.Fatalf("query failed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRemoteForbiddenWithoutTailscale(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/collect", strings.NewReader(`{"events":[]}`))
	req.RemoteAddr = "100.64.0.5:40000"
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote without tailscale: code = %d, want 403", rec.Code)
	}
}

func TestDenialIsAudited(t *testing.T) {
	s := newTestServer(t)
	s.SetLocalToken("tok")
	auditPath := filepath.Join(t.TempDir(), "collector.events.jsonl")
	log, err := eventlog.Open(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	s.SetAudit(log, "collector-node", "tn")

	// Loopback without the token => denied AND audited.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.RemoteAddr = "127.0.0.1:1"
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}

	events, err := eventlog.ReadSince(auditPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != eventlog.AuthzDeny {
		t.Fatalf("expected one authz.deny event, got %+v", events)
	}
	if events[0].Node != "collector-node" {
		t.Errorf("denial event node = %q, want collector-node", events[0].Node)
	}
}

func TestTokenGatedLoopback(t *testing.T) {
	s := newTestServer(t)
	s.SetLocalToken("tok")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/collect", strings.NewReader(`{"events":[]}`))
	req.RemoteAddr = "127.0.0.1:1"
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("loopback without token: code = %d, want 403", rec.Code)
	}
}
