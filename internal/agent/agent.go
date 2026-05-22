// Package agent implements the macker node daemon: an HTTP control plane that
// reports tmux sessions, runs authorized remote exec, and records an
// append-only audit/event log. It deliberately does NOT carry the terminal
// stream — that travels over ssh+tmux (see DESIGN.md §1, §3, §5).
//
// IMPORTANT (macOS): the agent must run as a LaunchAgent inside the GUI login
// session so that child processes inherit Keychain access. Running it as a
// pre-login LaunchDaemon would break Keychain-dependent tools like Claude Code.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/masakasuno1/macker/internal/api"
	"github.com/masakasuno1/macker/internal/authz"
	"github.com/masakasuno1/macker/internal/config"
	"github.com/masakasuno1/macker/internal/eventlog"
	"github.com/masakasuno1/macker/internal/session"
	"github.com/masakasuno1/macker/internal/tailnet"
)

// Version is the agent build version, surfaced via /v1/health.
var Version = "0.1.0-dev"

// defaultExecTimeout bounds an exec that does not specify one.
const defaultExecTimeout = 5 * time.Minute

// maxExecTimeout caps how long any single exec may run.
const maxExecTimeout = 30 * time.Minute

// maxExecOutput caps captured stdout/stderr to avoid unbounded memory use.
const maxExecOutput = 1 << 20 // 1 MiB each

// Server is the agent HTTP server.
type Server struct {
	cfg        config.Config
	ts         *tailnet.Client
	sess       session.Manager
	log        *eventlog.Log
	leases     *leaseRegistry
	localToken string // token required for loopback callers; "" disables the check
}

// New constructs an agent server.
func New(cfg config.Config, ts *tailnet.Client, log *eventlog.Log) *Server {
	return &Server{cfg: cfg, ts: ts, sess: session.Manager{}, log: log, leases: newLeaseRegistry()}
}

// SetLocalToken sets the token a loopback caller must present to be trusted as
// the local owner. The real daemon always sets this; tests may leave it empty.
func (s *Server) SetLocalToken(tok string) { s.localToken = tok }

// Handler returns the agent's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/sessions", s.requireCap(authz.CapAttach, s.handleListSessions))
	mux.HandleFunc("POST /v1/sessions", s.requireCap(authz.CapExec, s.handleCreateSession))
	mux.HandleFunc("POST /v1/sessions/{name}/release", s.requireCap(authz.CapAttach, s.handleRelease))
	mux.HandleFunc("POST /v1/sessions/{name}/lease", s.requireCap(authz.CapAttach, s.handleLease))
	mux.HandleFunc("POST /v1/sessions/{name}/unlease", s.requireCap(authz.CapAttach, s.handleUnlease))
	mux.HandleFunc("DELETE /v1/sessions/{name}", s.requireCap(authz.CapExec, s.handleKill))
	mux.HandleFunc("POST /v1/exec", s.requireCap(authz.CapExec, s.handleExec))
	mux.HandleFunc("GET /v1/events", s.requireCap(authz.CapAttach, s.handleEvents))
	return logRequests(mux)
}

// requireCap wraps a handler with identity resolution and a capability check.
func (s *Server) requireCap(need authz.Capability, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.authResolve(r)
		if p.Cap < need {
			s.audit(eventlog.Event{
				Type:  eventlog.AuthzDeny,
				Node:  s.cfg.Node,
				Actor: actorString(p),
				Detail: map[string]any{
					"need": need.String(),
					"have": p.Cap.String(),
					"path": r.URL.Path,
				},
			})
			writeErr(w, http.StatusForbidden, "forbidden: requires "+need.String())
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, p))
		h(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Health is reachable by any peer that can connect; it leaks only the node
	// name and version, which membership already implies.
	writeJSON(w, http.StatusOK, api.HealthResponse{
		Node:    s.cfg.Node,
		Version: Version,
		Time:    time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.sess.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Prune lease entries for sessions that no longer exist, then derive state.
	alive := make(map[string]bool, len(sessions))
	for _, ss := range sessions {
		alive[ss.Name] = true
	}
	s.leases.Prune(alive)

	views := make([]api.SessionView, 0, len(sessions))
	for _, ss := range sessions {
		info := s.leases.view(ss.Name, ss.Attached > 0)
		views = append(views, api.SessionView{Session: ss, State: info.state, Client: info.client})
	}
	writeJSON(w, http.StatusOK, api.SessionsResponse{Node: s.cfg.Node, Sessions: views})
}

func (s *Server) handleLease(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req api.LeaseRequest
	if !decode(w, r, &req) {
		return
	}
	if req.ClientID == "" {
		writeErr(w, http.StatusBadRequest, "client_id is required")
		return
	}
	ttl := time.Duration(req.TTLSec) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	s.leases.Renew(name, req.ClientID, ttl, req.Ephemeral)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnlease(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req api.UnleaseRequest
	if !decode(w, r, &req) {
		return
	}
	s.leases.Release(name, req.ClientID)
	p, _ := peerFrom(r.Context())
	s.audit(eventlog.Event{Type: eventlog.Detach, Node: s.cfg.Node, Session: name, Actor: actorString(p)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req api.CreateSessionRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.sess.New(r.Context(), req.Name, req.Command); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p, _ := peerFrom(r.Context())
	s.audit(eventlog.Event{
		Type: eventlog.SessionOpen, Node: s.cfg.Node, Session: req.Name, Actor: actorString(p),
		Detail: map[string]any{"command": req.Command, "ephemeral": req.Ephemeral},
	})

	// Re-list to return the created session with its classification.
	created := session.Session{Name: req.Name}
	if list, err := s.sess.List(r.Context()); err == nil {
		for _, ss := range list {
			if ss.Name == req.Name {
				created = ss
				break
			}
		}
	}
	writeJSON(w, http.StatusCreated, api.CreateSessionResponse{Session: created})
}

// handleRelease is the lifecycle endpoint a client calls on intentional exit
// (ctrl+c mash or clean window close). It kills the session. Note that this is
// the ONLY path that kills on disconnect: a lost heartbeat alone never kills,
// so sleeping the laptop preserves the session (see DESIGN.md §4).
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.sess.Kill(r.Context(), name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.leases.Drop(name)
	p, _ := peerFrom(r.Context())
	s.audit(eventlog.Event{Type: eventlog.Release, Node: s.cfg.Node, Session: name, Actor: actorString(p)})
	s.audit(eventlog.Event{Type: eventlog.SessionClose, Node: s.cfg.Node, Session: name, Actor: actorString(p),
		Detail: map[string]any{"reason": "release"}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.sess.Kill(r.Context(), name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.leases.Drop(name)
	p, _ := peerFrom(r.Context())
	s.audit(eventlog.Event{Type: eventlog.SessionClose, Node: s.cfg.Node, Session: name, Actor: actorString(p),
		Detail: map[string]any{"reason": "kill"}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	var req api.ExecRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeErr(w, http.StatusBadRequest, "command is required")
		return
	}

	timeout := defaultExecTimeout
	if req.TimeoutSec > 0 {
		timeout = time.Duration(req.TimeoutSec) * time.Second
		if timeout > maxExecTimeout {
			timeout = maxExecTimeout
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	if len(req.Args) > 0 {
		cmd = exec.CommandContext(ctx, req.Command, req.Args...)
	} else {
		// Run via login shell so PATH/aliases and the GUI session environment
		// (and thus Keychain access) are present.
		cmd = exec.CommandContext(ctx, "/bin/sh", "-lc", req.Command)
	}
	var stdout, stderr capWriter
	stdout.limit, stderr.limit = maxExecOutput, maxExecOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	p, _ := peerFrom(r.Context())
	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start)

	resp := api.ExecResponse{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: dur.Milliseconds(),
		TimedOut:   errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	resp.ExitCode = exitCode(runErr)

	s.audit(eventlog.Event{
		Type: eventlog.Exec, Node: s.cfg.Node, Actor: actorString(p),
		Detail: map[string]any{
			"command":     req.Command,
			"args":        req.Args,
			"exit_code":   resp.ExitCode,
			"timed_out":   resp.TimedOut,
			"duration_ms": resp.DurationMS,
		},
	})
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	after := r.URL.Query().Get("after")
	events, err := eventlog.ReadSince(s.cfg.EventLogPath(), after)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.EventsResponse{Node: s.cfg.Node, Events: events})
}

// audit appends an event, ignoring errors after logging would be circular.
// Audit durability is best-effort here; the append itself fsyncs.
func (s *Server) audit(e eventlog.Event) {
	if s.log == nil {
		return
	}
	if e.Tenant == "" {
		e.Tenant = s.cfg.Tenant
	}
	_, _ = s.log.Append(e)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// --- small HTTP helpers ---

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, api.ErrorResponse{Error: msg})
}

// capWriter accumulates output up to a byte limit, then discards the rest.
type capWriter struct {
	buf      []byte
	limit    int
	overflow bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.limit - len(c.buf); room > 0 {
		if len(p) > room {
			c.buf = append(c.buf, p[:room]...)
			c.overflow = true
		} else {
			c.buf = append(c.buf, p...)
		}
	} else if len(p) > 0 {
		c.overflow = true
	}
	return len(p), nil // always claim full write so the process is not blocked
}

func (c *capWriter) String() string {
	if c.overflow {
		return string(c.buf) + "\n...[truncated]"
	}
	return string(c.buf)
}
