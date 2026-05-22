package eventlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendAndReadSince(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	for i := 0; i < 5; i++ {
		e, err := l.Append(Event{Type: Exec, Node: "n", Detail: map[string]any{"i": i}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	all, err := ReadSince(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("got %d events, want 5", len(all))
	}
	// IDs must be strictly increasing (ordering guarantee).
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("events not ordered at %d", i)
		}
	}

	// afterID filters to events strictly greater than the given ID.
	after := ReadSinceMust(t, path, ids[2])
	if len(after) != 2 {
		t.Fatalf("after ids[2]: got %d, want 2", len(after))
	}
}

func ReadSinceMust(t *testing.T, path, after string) []Event {
	t.Helper()
	e, err := ReadSince(path, after)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestReadSkipsTornFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, _ := Open(path)
	_, _ = l.Append(Event{Type: Exec, Node: "n"})
	_ = l.Close()

	// Simulate a crash mid-append by tacking on a partial JSON line.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.WriteString(`{"id":"partial","type":`)
	_ = f.Close()

	got, err := ReadSince(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 (torn line must be skipped)", len(got))
	}
}

func TestReadSkipsMiddleCorruptLine(t *testing.T) {
	// A corrupt line in the MIDDLE must not hide subsequent valid events.
	path := filepath.Join(t.TempDir(), "events.jsonl")
	content := `{"id":"01A","type":"exec","node":"n"}` + "\n" +
		`{not valid json}` + "\n" +
		`{"id":"01C","type":"exec","node":"n"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSince(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 (corrupt middle line must be skipped, not truncate)", len(got))
	}
	if got[0].ID != "01A" || got[1].ID != "01C" {
		t.Fatalf("unexpected events: %+v", got)
	}
}

func TestReadMissingFile(t *testing.T) {
	got, err := ReadSince(filepath.Join(t.TempDir(), "nope.jsonl"), "")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
