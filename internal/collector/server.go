package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/masakasuno1/macker/internal/api"
	"github.com/masakasuno1/macker/internal/authz"
	"github.com/masakasuno1/macker/internal/config"
	"github.com/masakasuno1/macker/internal/eventlog"
	"github.com/masakasuno1/macker/internal/netutil"
	"github.com/masakasuno1/macker/internal/tailnet"
)

// Version is the collector build version.
var Version = "0.1.0-dev"

// Server is the collector HTTP server.
type Server struct {
	store      *Store
	ts         *tailnet.Client
	policy     config.Policy
	localToken string
	audit      *eventlog.Log // optional: records authz denials on the collector node
	node       string
	tenant     string
}

// NewServer constructs a collector server. policy gates access the same way the
// agent does (see DESIGN.md §3).
func NewServer(store *Store, ts *tailnet.Client, policy config.Policy) *Server {
	return &Server{store: store, ts: ts, policy: policy}
}

// SetLocalToken sets the loopback auth token (same scheme as the agent).
func (s *Server) SetLocalToken(tok string) { s.localToken = tok }

// SetAudit enables recording of authorization denials to a local log, tagged
// with the collector's own node/tenant. The collector is the audit hub, so its
// own refusals are worth recording.
func (s *Server) SetAudit(log *eventlog.Log, node, tenant string) {
	s.audit, s.node, s.tenant = log, node, tenant
}

// Handler returns the collector's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	// Shipping events is allowed for any authenticated tailnet node (CapAttach).
	mux.HandleFunc("POST /v1/collect", s.requireCap(authz.CapAttach, s.handleCollect))
	// Reading the aggregated audit log is sensitive: require CapExec (owners/
	// exec_allow), so a plain tailnet member cannot read everyone's history.
	mux.HandleFunc("GET /v1/events", s.requireCap(authz.CapExec, s.handleEvents))
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"role":    "collector",
		"version": Version,
		"time":    time.Now().Format(time.RFC3339),
	})
}

// requireCap enforces the shared authorization model and records denials.
func (s *Server) requireCap(need authz.Capability, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := authz.Resolve(s.ts, s.policy, s.localToken, r)
		if p.Cap < need {
			if s.audit != nil {
				_, _ = s.audit.Append(eventlog.Event{
					Type: eventlog.AuthzDeny, Node: s.node, Tenant: s.tenant, Actor: p.Actor(),
					Detail: map[string]any{"need": need.String(), "have": p.Cap.String(), "path": r.URL.Path},
				})
			}
			writeErr(w, http.StatusForbidden, "forbidden: requires "+need.String())
			return
		}
		h(w, r)
	}
}

func (s *Server) handleCollect(w http.ResponseWriter, r *http.Request) {
	var req api.CollectRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	stored := 0
	cursor := ""
	for _, e := range req.Events {
		ok, err := s.store.Append(e)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ok {
			stored++
		}
		if e.ID > cursor {
			cursor = e.ID
		}
	}
	writeJSON(w, http.StatusOK, api.CollectResponse{Stored: stored, Cursor: cursor})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	events, err := s.store.Query(q.Get("tenant"), q.Get("node"), q.Get("after"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.EventsResponse{Events: events})
}

// Serve runs the collector until ctx is cancelled. Like the agent, a bare
// ":port" binds only loopback + Tailscale IPs (not every LAN interface).
func (s *Server) Serve(ctx context.Context, listen string) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	addrs := netutil.BindAddrs(ctx, s.ts, listen)
	return netutil.Serve(ctx, srv, addrs, "collector", Version)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, api.ErrorResponse{Error: msg})
}
