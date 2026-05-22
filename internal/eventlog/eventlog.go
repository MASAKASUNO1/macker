// Package eventlog implements the append-only, line-delimited JSON event log
// that is the source of truth for each node.
//
// Design notes (see DESIGN.md §2):
//   - Each node owns its own log. The collector is only a mirror, so the log
//     must be durable and replayable on its own.
//   - Records are newline-delimited JSON ("JSONL"): cheap to append, trivially
//     tailed, and recoverable even if the last line is torn by a crash
//     (a partial final line is skipped on read).
//   - Every event carries a ULID, which both orders events and lets a
//     collector dedupe replayed events idempotently.
package eventlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/masakasuno1/macker/internal/ulid"
)

// Type enumerates the kinds of events recorded.
type Type string

const (
	SessionOpen  Type = "session.open"
	SessionClose Type = "session.close"
	Attach       Type = "attach"
	Detach       Type = "detach"
	Release      Type = "release"
	Exec         Type = "exec"
	AuthzDeny    Type = "authz.deny"
)

// Event is a single record in the log.
type Event struct {
	ID      string         `json:"id"`               // ULID, also the ordering key
	Time    time.Time      `json:"time"`             // wall-clock time of the event
	Type    Type           `json:"type"`             // event kind
	Tenant  string         `json:"tenant,omitempty"` // tailnet this event belongs to
	Node    string         `json:"node"`             // node the event pertains to
	Session string         `json:"session,omitempty"`
	Actor   string         `json:"actor,omitempty"` // tailnet identity that caused it
	Detail  map[string]any `json:"detail,omitempty"`
}

// Log is a concurrency-safe append-only event log backed by a file.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	path string
}

// Open opens (creating if needed) the log at path. The parent directory is
// created with 0o700 because the log can contain command lines and actor
// identities and should not be world-readable.
func Open(path string) (*Log, error) {
	if path == "" {
		return nil, errors.New("eventlog: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("eventlog: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open: %w", err)
	}
	return &Log{f: f, w: bufio.NewWriter(f), path: path}, nil
}

// Append writes an event. ID and Time are filled in if empty. The write is
// flushed and fsync'd before returning so that an event survives a crash
// immediately after the call (durability matters for audit records).
func (l *Log) Append(e Event) (Event, error) {
	if e.ID == "" {
		e.ID = ulid.New()
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return e, fmt.Errorf("eventlog: marshal: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(b); err != nil {
		return e, err
	}
	if err := l.w.WriteByte('\n'); err != nil {
		return e, err
	}
	if err := l.w.Flush(); err != nil {
		return e, err
	}
	if err := l.f.Sync(); err != nil {
		return e, err
	}
	return e, nil
}

// Close flushes and closes the underlying file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.w.Flush(); err != nil {
		_ = l.f.Close()
		return err
	}
	return l.f.Close()
}

// ReadSince reads events with ID strictly greater than afterID. An empty
// afterID returns all events. A torn final line (interrupted append) is
// silently skipped. The path is opened independently so reads do not contend
// with the append path's buffered writer.
func ReadSince(path, afterID string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Event
	var skipped int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			// Skip a corrupt line rather than erroring or stopping: a torn
			// final line from a crash mid-append must not make the log
			// unreadable, and a single bad line in the middle must not hide
			// every subsequent (valid) audit record. Account for it so callers
			// can surface corruption if they care.
			skipped++
			continue
		}
		if afterID == "" || e.ID > afterID {
			out = append(out, e)
		}
	}
	if skipped > 0 {
		// Surface corruption so an operator can notice it; we still return the
		// valid events rather than failing the whole read.
		log.Printf("eventlog: skipped %d corrupt line(s) in %s", skipped, path)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return out, err
	}
	return out, nil
}
