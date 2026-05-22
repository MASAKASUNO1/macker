// Package collector implements the optional event collector: a node that
// mirrors the append-only logs of other nodes so there is a central place to
// query history (DESIGN.md §1, §2). The collector is only a mirror — each
// node's own log remains the source of truth — so the collector being down
// never loses data: agents buffer locally and replay on reconnect.
package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/masakasuno1/macker/internal/eventlog"
)

// Store mirrors events into <dir>/<tenant>/<node>.jsonl files, deduping by
// ULID so that replayed events are idempotent.
type Store struct {
	dir  string
	mu   sync.Mutex
	logs map[string]*eventlog.Log // key -> open log
	last map[string]string        // key -> highest stored ULID
}

// NewStore opens (creating) the collector directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("collector: mkdir: %w", err)
	}
	return &Store{dir: dir, logs: map[string]*eventlog.Log{}, last: map[string]string{}}, nil
}

func key(tenant, node string) string { return sanitize(tenant) + "/" + sanitize(node) }

func (s *Store) pathFor(tenant, node string) string {
	return filepath.Join(s.dir, sanitize(tenant), sanitize(node)+".jsonl")
}

// Append stores an event idempotently. It returns true if the event was newly
// stored, false if it was a duplicate/older replay. Events for a given node
// arrive in ULID order, so a single high-water mark suffices for dedupe.
func (s *Store) Append(e eventlog.Event) (bool, error) {
	tenant, node := e.Tenant, e.Node
	if node == "" {
		node = "unknown"
	}
	k := key(tenant, node)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.last[k]; !ok {
		// First touch for this key: recover the high-water mark from disk.
		existing, err := eventlog.ReadSince(s.pathFor(tenant, node), "")
		if err != nil {
			return false, err
		}
		if n := len(existing); n > 0 {
			s.last[k] = existing[n-1].ID
		} else {
			s.last[k] = ""
		}
	}

	if last := s.last[k]; last != "" && e.ID <= last {
		return false, nil // duplicate or out-of-order replay
	}

	log, ok := s.logs[k]
	if !ok {
		l, err := eventlog.Open(s.pathFor(tenant, node))
		if err != nil {
			return false, err
		}
		s.logs[k] = l
		log = l
	}
	if _, err := log.Append(e); err != nil {
		return false, err
	}
	s.last[k] = e.ID
	return true, nil
}

// Query returns mirrored events. If tenant and node are both set it reads that
// one log; otherwise it aggregates across all matching logs. afterID filters
// to events with a greater ULID. Results are not globally sorted across nodes.
func (s *Store) Query(tenant, node, afterID string) ([]eventlog.Event, error) {
	var out []eventlog.Event
	if tenant != "" && node != "" {
		return eventlog.ReadSince(s.pathFor(tenant, node), afterID)
	}
	err := filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return err
		}
		// path = <dir>/<tenant>/<node>.jsonl
		rel, _ := filepath.Rel(s.dir, path)
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 2 {
			return nil
		}
		t := parts[0]
		if tenant != "" && t != sanitize(tenant) {
			return nil
		}
		evs, e := eventlog.ReadSince(path, afterID)
		if e != nil {
			return e
		}
		out = append(out, evs...)
		return nil
	})
	return out, err
}

// Close closes all open logs.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, l := range s.logs {
		if err := l.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// sanitize makes a tenant/node name safe as a single path element.
func sanitize(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	// Guard against names that resolve to traversal.
	if out == "." || out == ".." {
		return "unknown"
	}
	return out
}
