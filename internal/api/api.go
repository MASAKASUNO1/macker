// Package api defines the request/response types exchanged between the macker
// client and agent over the tailnet. Keeping them in one place lets both sides
// share a single definition and version the protocol coherently.
package api

import (
	"github.com/masakasuno1/macker/internal/eventlog"
	"github.com/masakasuno1/macker/internal/session"
)

// Version is the protocol version. Bump on breaking wire changes.
const Version = "v1"

// HealthResponse is returned by GET /v1/health.
type HealthResponse struct {
	Node    string `json:"node"`
	Version string `json:"version"`
	Time    string `json:"time"` // RFC3339
}

// SessionState is the lifecycle state of a session as seen by the agent,
// combining tmux attachment with macker's lease tracking (see DESIGN.md §4).
type SessionState string

const (
	StateAttached SessionState = "attached" // a client currently holds a live lease (or tmux client attached)
	StateOrphaned SessionState = "orphaned" // ephemeral session whose holder vanished (sleep/crash) without a clean exit
	StateDetached SessionState = "detached" // alive but nobody attached (cleanly detached or external)
)

// SessionView is a session plus its derived lifecycle state and lease holder.
type SessionView struct {
	session.Session
	State  SessionState `json:"state"`
	Client string       `json:"client,omitempty"` // lease holder id, if any
}

// SessionsResponse is returned by GET /v1/sessions.
type SessionsResponse struct {
	Node     string        `json:"node"`
	Sessions []SessionView `json:"sessions"`
}

// LeaseRequest registers or renews a client's lease on a session (heartbeat).
type LeaseRequest struct {
	ClientID  string `json:"client_id"`
	TTLSec    int    `json:"ttl_sec"`
	Ephemeral bool   `json:"ephemeral"`
}

// UnleaseRequest releases a client's lease without killing the session (a
// clean detach).
type UnleaseRequest struct {
	ClientID string `json:"client_id"`
}

// CreateSessionRequest is the body of POST /v1/sessions.
type CreateSessionRequest struct {
	Name      string `json:"name"`
	Command   string `json:"command,omitempty"`   // empty starts the default shell
	Ephemeral bool   `json:"ephemeral,omitempty"` // advisory; lifecycle is client-driven
}

// CreateSessionResponse is returned by POST /v1/sessions.
type CreateSessionResponse struct {
	Session session.Session `json:"session"`
}

// ExecRequest is the body of POST /v1/exec.
//
// If Args is non-empty, Command is the program and Args its arguments, run
// without a shell. If Args is empty, Command is run via the user's shell
// ("sh -lc"), which is convenient but means the caller is responsible for
// quoting. Either way this is arbitrary remote execution, gated by CapExec.
type ExecRequest struct {
	Command    string   `json:"command"`
	Args       []string `json:"args,omitempty"`
	TimeoutSec int      `json:"timeout_sec,omitempty"`
}

// ExecResponse is returned by POST /v1/exec.
type ExecResponse struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
}

// EventsResponse is returned by GET /v1/events.
type EventsResponse struct {
	Node   string           `json:"node"`
	Events []eventlog.Event `json:"events"`
}

// CollectRequest is the body of POST /v1/collect, sent by an agent's shipper
// to a collector. Events are ordered by ULID.
type CollectRequest struct {
	Events []eventlog.Event `json:"events"`
}

// CollectResponse reports how many events the collector newly stored (vs.
// deduped) and the highest event ID it now holds for the source node.
type CollectResponse struct {
	Stored int    `json:"stored"`
	Cursor string `json:"cursor"` // highest ID accepted (for the shipper to advance)
}

// ErrorResponse is the body for non-2xx responses.
type ErrorResponse struct {
	Error string `json:"error"`
}
