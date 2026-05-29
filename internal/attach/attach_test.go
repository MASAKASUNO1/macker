package attach

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"=main", "'=main'"},
		{"main", "'main'"},
		{"a'b", `'a'\''b'`},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The remote attach command is re-parsed by the node's login shell, so the
// "=name" exact-match target must be quoted (zsh would otherwise treat a
// leading "=" as filename expansion). Guard against a regression that passes a
// bare "=name" token.
func TestRemoteCommandQuotesTarget(t *testing.T) {
	o := Options{Addr: "node.example.ts.net", Session: "main"}
	cmd := o.command(context.Background())

	if !strings.HasSuffix(cmd.Path, "ssh") {
		t.Fatalf("remote attach should run ssh, got %q", cmd.Path)
	}
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "'=main'") {
		t.Errorf("remote command should quote the target as '=main'; args = %q", cmd.Args)
	}
	for _, a := range cmd.Args {
		if a == "=main" {
			t.Errorf("remote command passes a bare =main token (unquoted); args = %q", cmd.Args)
		}
	}
}

// runForwardInput drives forwardInput against a pipe and returns the result.
// Each `write` is sent as one Read on the underlying os.File, with `gap`
// between writes so the timestamps land in different mash windows when needed.
func runForwardInput(t *testing.T, o *Options, writes [][]byte, gap time.Duration) (mashKind, []byte) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = pr.Close() })

	o.In = pr
	var sink syncBuf
	done := make(chan mashKind, 1)
	go func() { done <- o.forwardInput(&sink) }()

	for i, w := range writes {
		if i > 0 && gap > 0 {
			time.Sleep(gap)
		}
		if _, err := pw.Write(w); err != nil {
			t.Fatalf("pw.Write: %v", err)
		}
	}
	// Closing the writer makes forwardInput's Read return io.EOF and exit
	// with mashStdinClosed if it has not already fired a mash.
	_ = pw.Close()

	select {
	case k := <-done:
		return k, sink.Bytes()
	case <-time.After(2 * time.Second):
		t.Fatal("forwardInput did not return")
	}
	return 0, nil
}

// A multi-line paste containing many LF (= ctrl+j) bytes in a single read must
// NOT trigger the detach mash — that's the whole point of the per-read cap.
func TestForwardInputPasteDoesNotDetach(t *testing.T) {
	o := &Options{MashCount: 3, MashWindow: 300 * time.Millisecond,
		DetachMashCount: 3, DetachMashWindow: 300 * time.Millisecond}
	o.defaults()

	paste := []byte("line one\nline two\nline three\nline four\n")
	got, written := runForwardInput(t, o, [][]byte{paste}, 0)
	// Stdin-closed at the end of the helper is fine; what we care about is
	// that no mash gesture fired during the paste.
	if got == mashClose || got == mashDetach {
		t.Fatalf("paste with LFs should not fire a mash; got %v", got)
	}
	if string(written) != string(paste) {
		t.Fatalf("paste bytes must be forwarded verbatim; got %q", written)
	}
}

// Three separate ctrl+j reads inside the window must trigger detach. This is
// the human-mashing case (each press lands as its own read, ~80ms apart).
func TestForwardInputDetachOnSeparateReads(t *testing.T) {
	o := &Options{MashCount: 3, MashWindow: 300 * time.Millisecond,
		DetachMashCount: 3, DetachMashWindow: 300 * time.Millisecond,
		DetachMinGap: 50 * time.Millisecond}
	o.defaults()

	hit := []byte{ctrlJ}
	// 80ms is slower than DetachMinGap (50ms) but the three hits still fit in
	// the 300ms window — the realistic floor for a deliberate human mash.
	got, _ := runForwardInput(t, o, [][]byte{hit, hit, hit}, 80*time.Millisecond)
	if got != mashDetach {
		t.Fatalf("three ctrl+j presses in window should fire detach; got %v", got)
	}
}

// A paste that is split across multiple reads (each containing one or more
// LFs) must NOT fire detach: either the per-read paste-shape filter (multi-LF
// reads) or the inter-read min-gap (LFs arriving too fast) catches it. This
// is the case codex review flagged as a regression.
func TestForwardInputSplitPasteDoesNotDetach(t *testing.T) {
	o := &Options{MashCount: 3, MashWindow: 300 * time.Millisecond,
		DetachMashCount: 3, DetachMashWindow: 300 * time.Millisecond,
		DetachMinGap: 50 * time.Millisecond}
	o.defaults()

	// Three chunks of "fragment\n" arriving back-to-back simulate a chunked
	// paste. Gap (20ms) is intentionally below DetachMinGap.
	chunk := []byte("fragment\n")
	got, _ := runForwardInput(t, o, [][]byte{chunk, chunk, chunk}, 20*time.Millisecond)
	if got == mashClose || got == mashDetach {
		t.Fatalf("chunked paste should not fire a mash; got %v", got)
	}
}

// A long stream of single-LF reads arriving every 20ms (well under
// DetachMinGap=50ms) must NOT fire detach. This is the codex-flagged
// regression: if ignored reads do not advance the gap clock, the stream is
// effectively sampled at the gap rate and three hits accumulate in the
// 300ms window. Any LF read — even one that fails the guards — must reset
// the clock.
func TestForwardInputLongChunkedPasteDoesNotDetach(t *testing.T) {
	o := &Options{MashCount: 3, MashWindow: 300 * time.Millisecond,
		DetachMashCount: 3, DetachMashWindow: 300 * time.Millisecond,
		DetachMinGap: 50 * time.Millisecond}
	o.defaults()

	hit := []byte{ctrlJ}
	writes := make([][]byte, 7) // 7 reads * 20ms = 140ms — comfortably > 3*50ms
	for i := range writes {
		writes[i] = hit
	}
	got, _ := runForwardInput(t, o, writes, 20*time.Millisecond)
	if got == mashClose || got == mashDetach {
		t.Fatalf("long chunked single-LF stream should not fire a mash; got %v", got)
	}
}

// Three LFs arriving faster than DetachMinGap (a la OS key-repeat) must NOT
// fire detach. Real human mashes are 80ms+ between presses.
func TestForwardInputKeyRepeatDoesNotDetach(t *testing.T) {
	o := &Options{MashCount: 3, MashWindow: 300 * time.Millisecond,
		DetachMashCount: 3, DetachMashWindow: 300 * time.Millisecond,
		DetachMinGap: 50 * time.Millisecond}
	o.defaults()

	hit := []byte{ctrlJ}
	// 30ms gap simulates key-repeat (~33 Hz).
	got, _ := runForwardInput(t, o, [][]byte{hit, hit, hit}, 30*time.Millisecond)
	if got == mashClose || got == mashDetach {
		t.Fatalf("key-repeat-speed LFs should not fire a mash; got %v", got)
	}
}

// Close mash beats detach mash in the same read so a panicked user mashing
// ctrl+c still gets the kill behaviour even if ctrl+j happens to be queued.
func TestForwardInputCloseBeatsDetach(t *testing.T) {
	o := &Options{MashCount: 2, MashWindow: 300 * time.Millisecond,
		DetachMashCount: 2, DetachMashWindow: 300 * time.Millisecond,
		DetachMinGap: 50 * time.Millisecond}
	o.defaults()

	// First read primes both trackers with one hit each.
	prime := []byte{ctrlC, ctrlJ}
	// Second read meets both thresholds in the same read. Gap is above
	// DetachMinGap so the detach tracker is allowed to count this read.
	bothFire := []byte{ctrlC, ctrlJ}
	got, _ := runForwardInput(t, o, [][]byte{prime, bothFire}, 80*time.Millisecond)
	if got != mashClose {
		t.Fatalf("close mash should beat detach in the same read; got %v", got)
	}
}

// Stdin closing on its own (window hung up) must surface as mashStdinClosed
// so the caller can route it to the same close-intent path as SIGHUP.
// Otherwise an ephemeral session could survive a window close depending on
// which goroutine wins the race to deliver its reason first.
func TestForwardInputStdinCloseSurfacesAsStdinClosed(t *testing.T) {
	o := &Options{}
	o.defaults()

	// No writes: helper closes pw immediately, forwardInput sees EOF.
	got, _ := runForwardInput(t, o, nil, 0)
	if got != mashStdinClosed {
		t.Fatalf("stdin close should surface as mashStdinClosed; got %v", got)
	}
}

// A write-side failure (child pty closed first) must surface as mashChildGone
// so the caller treats it as a natural exit, not a close intent.
func TestForwardInputWriteFailureSurfacesAsChildGone(t *testing.T) {
	o := &Options{}
	o.defaults()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()
	o.In = pr

	// Sink whose Write fails immediately to simulate a dead child pty.
	deadChild := failingWriter{}
	done := make(chan mashKind, 1)
	go func() { done <- o.forwardInput(deadChild) }()

	// Feed at least one byte so the write attempt happens.
	if _, err := pw.Write([]byte{'a'}); err != nil {
		t.Fatalf("pw.Write: %v", err)
	}

	select {
	case got := <-done:
		if got != mashChildGone {
			t.Fatalf("write failure should surface as mashChildGone; got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forwardInput did not return")
	}
	_ = pw.Close()
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// syncBuf is a minimal io.Writer used as a sink in forwardInput tests.
// bytes.Buffer would also work but we keep this trivial to avoid pulling
// in bytes just for the test.
type syncBuf struct {
	b []byte
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}
func (s *syncBuf) Bytes() []byte { return s.b }

// io is imported but only used transitively in helpers — silence "imported
// and not used" if a future edit removes the reference.
var _ io.Writer = (*syncBuf)(nil)

// mashTracker fires once `count` hits land within `window`. Below the
// threshold or outside the window it must stay silent.
func TestMashTracker(t *testing.T) {
	m := mashTracker{window: 400 * time.Millisecond, count: 2}
	base := time.Unix(0, 0)

	if m.record(base) {
		t.Fatal("1 hit should not fire")
	}
	if !m.record(base.Add(100 * time.Millisecond)) {
		t.Fatal("2 hits inside the window should fire")
	}

	// Outside the window: only the new hit remains, so it must not fire again.
	m = mashTracker{window: 400 * time.Millisecond, count: 2}
	_ = m.record(base)
	if m.record(base.Add(500 * time.Millisecond)) {
		t.Fatal("second hit outside the window should not fire")
	}
}

// Run's caller pre-creates the remote session via ensureSession; an early
// error (here: a non-TTY stdin) must still fire OnClose so the caller can
// clean up the session it just created — otherwise an ephemeral session
// orphans on the agent for every failed attach.
func TestRunFiresOnCloseOnEarlyError(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()

	var got Intent = -1
	o := Options{
		In:      pr, // pipes are not TTYs → triggers the early-return path
		Out:     pw,
		Session: "test",
		Local:   true,
		OnClose: func(intent Intent) { got = intent },
	}
	if err := Run(context.Background(), o); err == nil {
		t.Fatal("expected early-error from non-TTY stdin")
	}
	if got != IntentClose {
		t.Fatalf("OnClose intent on early error = %v, want IntentClose", got)
	}
}

// Local attach execs tmux directly (no shell), so the bare "=name" target is
// correct there and must be preserved for exact-name matching.
func TestLocalCommandUsesBareTarget(t *testing.T) {
	o := Options{Local: true, TmuxBin: "tmux", Session: "main"}
	cmd := o.command(context.Background())

	if !strings.HasSuffix(cmd.Path, "tmux") {
		t.Fatalf("local attach should run tmux, got %q", cmd.Path)
	}
	found := false
	for _, a := range cmd.Args {
		if a == "=main" {
			found = true
		}
	}
	if !found {
		t.Errorf("local command should pass exact-match target =main; args = %q", cmd.Args)
	}
}
