package collector

import (
	"testing"

	"github.com/masakasuno1/macker/internal/eventlog"
	"github.com/masakasuno1/macker/internal/ulid"
)

func TestStoreAppendIdempotent(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id1, id2 := ulid.New(), ulid.New()
	e1 := eventlog.Event{ID: id1, Tenant: "tn", Node: "mac-mini", Type: eventlog.Exec}
	e2 := eventlog.Event{ID: id2, Tenant: "tn", Node: "mac-mini", Type: eventlog.Exec}

	if ok, _ := s.Append(e1); !ok {
		t.Fatal("first append should store")
	}
	if ok, _ := s.Append(e2); !ok {
		t.Fatal("second append should store")
	}
	// Replays must be deduped.
	if ok, _ := s.Append(e1); ok {
		t.Fatal("replay of e1 should be deduped")
	}
	if ok, _ := s.Append(e2); ok {
		t.Fatal("replay of e2 should be deduped")
	}

	got, err := s.Query("tn", "mac-mini", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("query returned %d, want 2", len(got))
	}
}

func TestStoreDedupAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	id := ulid.New()
	e := eventlog.Event{ID: id, Tenant: "tn", Node: "n", Type: eventlog.Exec}

	s1, _ := NewStore(dir)
	if ok, _ := s1.Append(e); !ok {
		t.Fatal("store should accept")
	}
	s1.Close()

	// A fresh Store must recover the high-water mark from disk and dedup.
	s2, _ := NewStore(dir)
	defer s2.Close()
	if ok, _ := s2.Append(e); ok {
		t.Fatal("reopened store should dedup the replay")
	}
}

func TestStoreQueryAggregates(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	defer s.Close()
	s.Append(eventlog.Event{ID: ulid.New(), Tenant: "tn", Node: "a", Type: eventlog.Exec})
	s.Append(eventlog.Event{ID: ulid.New(), Tenant: "tn", Node: "b", Type: eventlog.Exec})

	all, err := s.Query("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("aggregate query = %d, want 2", len(all))
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"":           "unknown",
		"ok.name-1":  "ok.name-1",
		"a/b":        "a_b",
		"..":         "unknown",
		"with space": "with_space",
		"x;rm -rf /": "x_rm_-rf__",
	}
	for in, want := range cases {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
