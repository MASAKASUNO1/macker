// Package attach drives an interactive terminal attachment to a remote (or
// local) tmux session and implements macker's session lifecycle gestures.
//
// Lifecycle model (DESIGN.md §4): a session is killed ONLY on an explicit
// close intent — a ctrl+c mash or the terminal window closing (SIGHUP/SIGTERM).
// A ctrl+j mash is an intentional detach that leaves the session alive (handy
// when you want to step away without killing an ephemeral session). A natural
// client exit (e.g. a tmux detach) or a dropped connection never kills the
// session either, so sleeping the laptop preserves it. The kill itself is
// performed by the OnClose callback, which the caller wires to the agent's
// release endpoint for IntentClose on ephemeral sessions; IntentDetach and
// IntentNatural only drop the lease.
package attach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Options configures an attachment.
type Options struct {
	// Local attaches to the local tmux server directly; otherwise ssh is used.
	Local bool
	// Addr is the ssh target (MagicDNS name or Tailscale IP) for remote attach.
	Addr string
	// Session is the validated tmux session name.
	Session string
	// TmuxBin overrides the tmux binary for local attach (default "tmux").
	TmuxBin string
	// SSHArgs are extra ssh options for remote attach.
	SSHArgs []string

	// MashCount and MashWindow define the ctrl+c close gesture: this many
	// ctrl+c bytes within the window triggers an intentional close.
	MashCount  int
	MashWindow time.Duration

	// DetachMashCount and DetachMashWindow define the ctrl+j detach gesture:
	// this many ctrl+j bytes within the window, each in a SEPARATE read from
	// stdin, trigger an intentional detach (session is left alive even if
	// ephemeral). The per-read restriction avoids false positives when pasted
	// text contains LF bytes — a multi-line paste is delivered as one big read
	// (or a few large reads with no real key pause between them).
	DetachMashCount  int
	DetachMashWindow time.Duration

	// DetachMinGap is the minimum spacing between two consecutive detach hits.
	// Reads that arrive faster than this are treated as either OS key-repeat
	// (~30-50ms/repeat) or a paste split across reads — both should not count
	// as a human mash. A natural human mash is 80ms+ between presses, so
	// 50ms gives comfortable headroom.
	DetachMinGap time.Duration

	// GraceShutdown is how long Run waits for the child to exit after SIGTERM
	// before falling back to SIGKILL on an intentional close/detach. Giving the
	// local ssh/tmux client a moment to shut down cleanly lets remote sshd
	// close the session cleanly (so a remote tmux with destroy-unattached or
	// similar is less likely to misinterpret the disconnect).
	GraceShutdown time.Duration

	// LeaseRenew, if non-nil, is called once at start and then every
	// LeaseInterval to refresh the session lease (heartbeat). When the heartbeat
	// stops (sleep/crash) the agent's lease expires and the session becomes
	// orphaned rather than killed (DESIGN.md §4).
	LeaseRenew    func()
	LeaseInterval time.Duration

	// OnClose is invoked exactly once when the attachment ends. The caller
	// decides policy from the intent: kill on IntentClose+ephemeral, otherwise
	// release the lease (clean detach).
	OnClose func(intent Intent)

	// In, Out are the local terminal; defaults to os.Stdin/os.Stdout.
	In  *os.File
	Out *os.File
}

const (
	ctrlC = 0x03
	ctrlJ = 0x0a // == LF == '\n'; this is why paste guards are critical.
)

// Intent describes why the attachment ended; OnClose uses it to choose
// between kill, lease release, or no-op.
type Intent int

const (
	// IntentNatural: child exited on its own (tmux detach, ssh dropped, etc.).
	IntentNatural Intent = iota
	// IntentClose: ctrl+c mash or terminal window close — the user wants the
	// session gone (callers kill on ephemeral, detach on --keep).
	IntentClose
	// IntentDetach: ctrl+j mash — the user wants to step away while leaving
	// the session alive, even when ephemeral.
	IntentDetach
)

func (o *Options) defaults() {
	if o.MashCount <= 0 {
		o.MashCount = 3
	}
	if o.MashWindow <= 0 {
		o.MashWindow = 400 * time.Millisecond
	}
	if o.DetachMashCount <= 0 {
		o.DetachMashCount = 3
	}
	if o.DetachMashWindow <= 0 {
		o.DetachMashWindow = 300 * time.Millisecond
	}
	if o.DetachMinGap <= 0 {
		o.DetachMinGap = 50 * time.Millisecond
	}
	if o.GraceShutdown <= 0 {
		o.GraceShutdown = 500 * time.Millisecond
	}
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.TmuxBin == "" {
		o.TmuxBin = "tmux"
	}
}

// command builds the child process that attaches to the session.
func (o *Options) command(ctx context.Context) *exec.Cmd {
	target := "=" + o.Session // exact-name match in tmux
	if o.Local {
		// Local attach execs tmux directly (no shell), so the "=" target is safe.
		return exec.CommandContext(ctx, o.TmuxBin, "attach-session", "-t", target)
	}
	args := []string{"-tt"} // force remote PTY allocation
	args = append(args, o.SSHArgs...)
	// ssh joins the remaining args into one string that the remote login shell
	// re-parses, so a bare "=name" target is mangled there — zsh treats a
	// leading "=" as filename expansion and fails with "name not found". Single-
	// quote the whole tmux invocation so the remote shell passes it through
	// verbatim (exact-name match preserved). `exec` replaces the shell so
	// signals and the PTY map straight onto tmux.
	remote := "exec tmux attach-session -t " + shellQuote(target)
	args = append(args, o.Addr, remote)
	return exec.CommandContext(ctx, "ssh", args...)
}

// shellQuote wraps s in single quotes for a POSIX/zsh remote shell so it is
// treated as a literal: no "=" expansion, globbing, or word splitting.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// closeReason explains why the attach loop ended.
type closeReason int

const (
	reasonChildExit  closeReason = iota // tmux/ssh exited on its own (detach/end)
	reasonCloseMash                     // user mashed ctrl+c (kill)
	reasonDetachMash                    // user mashed ctrl+j (detach, session survives)
	reasonSignal                        // terminal window closed
)

// Run attaches and blocks until the session is detached, ends, or is closed.
func Run(ctx context.Context, o Options) error {
	o.defaults()

	if !term.IsTerminal(int(o.In.Fd())) {
		return errors.New("attach: stdin is not a terminal")
	}

	cmd := o.command(ctx)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("attach: start: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// quit is closed when Run returns, so background goroutines can exit
	// instead of leaking for the (short) remaining process lifetime.
	quit := make(chan struct{})
	defer close(quit)

	// Lease heartbeat: register now, then refresh on an interval until quit.
	if o.LeaseRenew != nil && o.LeaseInterval > 0 {
		o.LeaseRenew()
		go func() {
			t := time.NewTicker(o.LeaseInterval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					o.LeaseRenew()
				case <-quit:
					return
				}
			}
		}()
	}

	// Mirror window size to the child, now and on every SIGWINCH.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for {
			select {
			case <-winch:
				_ = pty.InheritSize(o.In, ptmx)
			case <-quit:
				return
			}
		}
	}()
	winch <- syscall.SIGWINCH // set initial size

	// Put the local terminal in raw mode so ctrl+c is delivered as a byte
	// (0x03) rather than raising SIGINT locally — we both forward it and count
	// it for the mash gesture.
	oldState, err := term.MakeRaw(int(o.In.Fd()))
	if err != nil {
		return fmt.Errorf("attach: raw mode: %w", err)
	}
	restore := func() { _ = term.Restore(int(o.In.Fd()), oldState) }
	defer restore()

	// Intentional-close signals: closing the terminal window delivers SIGHUP;
	// SIGTERM covers a kill of the macker process. Sleep delivers neither, so
	// the session survives.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// 3 producers (child reader, input forwarder, signal watcher), each can
	// send at most once — buffered so a late send never blocks.
	done := make(chan closeReason, 3)

	// child output -> screen; EOF means the child exited.
	go func() {
		_, _ = io.Copy(o.Out, ptmx)
		done <- reasonChildExit
	}()

	// keyboard -> child, with mash detection. NOTE: a blocking os.Stdin.Read
	// here cannot be interrupted portably, so on an intentional close this
	// goroutine remains blocked until the process exits moments later. Run is
	// invoked once per (short-lived) macker process, so this does not
	// accumulate; grid spawns a separate process per pane rather than reusing
	// Run in-process.
	go func() {
		switch o.forwardInput(ptmx) {
		case mashClose:
			done <- reasonCloseMash
		case mashDetach:
			done <- reasonDetachMash
		case mashStdinClosed:
			// Stdin EOF/error = local terminal hung up. Route this to the
			// same intent path as SIGHUP so a window-close still kills
			// ephemeral sessions regardless of which goroutine wins.
			done <- reasonSignal
		case mashChildGone:
			// Child wrote-side EOF — natural exit, mirror the reader.
			done <- reasonChildExit
		default:
			// Defensive: a future mashKind would otherwise drop silently
			// and let <-done deadlock. Treat the unknown case as child
			// exit (no kill), matching the safe-side default.
			done <- reasonChildExit
		}
	}()

	go func() {
		<-sigCh
		done <- reasonSignal
	}()

	reason := <-done

	var intent Intent
	switch reason {
	case reasonCloseMash, reasonSignal:
		intent = IntentClose
	case reasonDetachMash:
		intent = IntentDetach
	default:
		intent = IntentNatural
	}

	// Wait for the child in a dedicated goroutine so we can race it against a
	// grace period when we need to force-stop the local tmux/ssh client.
	waitCh := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitCh)
	}()

	if intent != IntentNatural {
		// Graceful first: SIGTERM lets ssh tear down the remote session
		// cleanly (no PTY-hangup race), so a remote tmux with
		// destroy-unattached/exit-empty won't misread the disconnect as
		// "session went away". If the client doesn't exit in time, escalate.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-waitCh:
		case <-time.After(o.GraceShutdown):
			_ = cmd.Process.Kill()
			<-waitCh
		}
	} else {
		<-waitCh
	}

	restore()
	if o.OnClose != nil {
		o.OnClose(intent)
	}
	return nil
}

// mashKind reports how forwardInput exited.
type mashKind int

const (
	// mashClose: ctrl+c mash fired (intentional close).
	mashClose mashKind = iota + 1
	// mashDetach: ctrl+j mash fired (intentional detach, session survives).
	mashDetach
	// mashStdinClosed: read on stdin returned an error. The local terminal
	// is gone (the window was closed, or stdin was redirected and EOFed) —
	// treat it as a close signal so ephemeral sessions are still killed even
	// if the SIGHUP goroutine loses the race to deliver its reason first.
	mashStdinClosed
	// mashChildGone: write to the child pty failed. The remote/child side
	// went away on its own — that's a natural exit (no kill).
	mashChildGone
)

// mashTracker tracks repeated byte hits within a sliding window.
type mashTracker struct {
	window time.Duration
	count  int
	hits   []time.Time
}

// record adds a hit at now, evicts hits older than the window, and reports
// whether the threshold is now met.
func (m *mashTracker) record(now time.Time) bool {
	m.hits = append(m.hits, now)
	cutoff := now.Add(-m.window)
	kept := m.hits[:0]
	for _, t := range m.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	m.hits = kept
	return len(m.hits) >= m.count
}

// forwardInput copies keyboard input to the child and watches for both the
// ctrl+c close mash and the ctrl+j detach mash. Every byte (including each
// ctrl+c / ctrl+j) is forwarded so a single press still reaches the remote
// program normally.
//
// Detach (ctrl+j == LF) is fragile because LF bytes show up in pasted text
// and in OS key-repeat, neither of which is a real "mash". Three guards keep
// the heuristic honest:
//
//  1. **Per-read cap**: at most one ctrl+j hit is counted per Read call. A
//     normal multi-line paste lands in one big read, so its many LFs only
//     contribute one (filtered, see #2) candidate.
//  2. **Paste-shape filter**: if a single read contains more than one LF, it
//     looks like text, not a mash — skip it entirely.
//  3. **Inter-read min gap**: a read that arrives less than DetachMinGap
//     after the previous detach hit is treated as key-repeat or a paste
//     split across reads, and is not counted. A human mash is 80ms+ between
//     presses; key-repeat is 30-50ms/repeat.
//
// Ctrl+c uses the same per-read cap (rare but possible to mistype repeats)
// and is otherwise unguarded — control bytes don't show up in normal paste
// text and the user kill intent is unambiguous when they hit it three times.
func (o *Options) forwardInput(w io.Writer) mashKind {
	buf := make([]byte, 4096)
	closeT := mashTracker{window: o.MashWindow, count: o.MashCount}
	detachT := mashTracker{window: o.DetachMashWindow, count: o.DetachMashCount}
	var lastDetachHit time.Time
	for {
		n, err := o.In.Read(buf)
		if n > 0 {
			now := time.Now()
			var sawClose bool
			var detachByteCount int
			for _, b := range buf[:n] {
				switch b {
				case ctrlC:
					sawClose = true
				case ctrlJ:
					detachByteCount++
				}
			}

			// A detach hit must satisfy all three guards: per-read cap
			// (detachByteCount == 1), paste-shape filter (>=2 LFs means paste,
			// not mash), and inter-read min gap. Any read containing LF —
			// even one that fails the guards — advances lastDetachHit so a
			// long chunked paste keeps the gap clock ticking; otherwise a
			// stream of single-LF reads arriving every 20ms would still
			// accumulate three hits at the 50ms sampling rate set by the gap.
			var gotClose, gotDetach bool
			if sawClose {
				gotClose = closeT.record(now)
			}
			if detachByteCount > 0 {
				qualifying := detachByteCount == 1 &&
					(lastDetachHit.IsZero() || now.Sub(lastDetachHit) >= o.DetachMinGap)
				lastDetachHit = now
				if qualifying {
					gotDetach = detachT.record(now)
				}
			}

			if _, werr := w.Write(buf[:n]); werr != nil {
				// Write side died: the child (ssh/tmux) went away on its
				// own. Natural exit, no kill.
				return mashChildGone
			}
			// Close wins if both fire in the same read (kill is a stronger
			// signal than detach).
			if gotClose {
				return mashClose
			}
			if gotDetach {
				return mashDetach
			}
		}
		if err != nil {
			// Read side died: the local terminal hung up (window closed)
			// or stdin was redirected and EOFed. Either way we have no
			// terminal anymore — escalate to a close signal so an ephemeral
			// session still gets killed even if SIGHUP loses the race.
			return mashStdinClosed
		}
	}
}
