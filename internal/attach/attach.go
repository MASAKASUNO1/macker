// Package attach drives an interactive terminal attachment to a remote (or
// local) tmux session and implements macker's session lifecycle gestures.
//
// Lifecycle model (DESIGN.md §4): a session is killed ONLY on an explicit
// intent signal — a ctrl+c mash or the terminal window closing (SIGHUP/SIGTERM).
// A natural client exit (e.g. a tmux detach) or a dropped connection never
// kills the session, so sleeping the laptop preserves it. The kill itself is
// performed by OnIntentionalClose, which the caller wires to the agent's
// release endpoint for ephemeral sessions, or leaves nil for --keep sessions
// (where an intentional close merely detaches).
package attach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
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

	// MashCount and MashWindow define the ctrl+c mash gesture: this many
	// ctrl+c bytes within the window triggers an intentional close.
	MashCount  int
	MashWindow time.Duration

	// LeaseRenew, if non-nil, is called once at start and then every
	// LeaseInterval to refresh the session lease (heartbeat). When the heartbeat
	// stops (sleep/crash) the agent's lease expires and the session becomes
	// orphaned rather than killed (DESIGN.md §4).
	LeaseRenew    func()
	LeaseInterval time.Duration

	// OnClose is invoked exactly once when the attachment ends. intentional is
	// true for a ctrl+c mash or window close, false for a natural exit (tmux
	// detach / session end). The caller decides policy: kill on intentional+
	// ephemeral, otherwise release the lease (clean detach).
	OnClose func(intentional bool)

	// In, Out are the local terminal; defaults to os.Stdin/os.Stdout.
	In  *os.File
	Out *os.File
}

const ctrlC = 0x03

func (o *Options) defaults() {
	if o.MashCount <= 0 {
		o.MashCount = 3
	}
	if o.MashWindow <= 0 {
		o.MashWindow = 400 * time.Millisecond
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
		return exec.CommandContext(ctx, o.TmuxBin, "attach-session", "-t", target)
	}
	args := []string{"-tt"} // force remote PTY allocation
	args = append(args, o.SSHArgs...)
	args = append(args, o.Addr, "tmux", "attach-session", "-t", target)
	return exec.CommandContext(ctx, "ssh", args...)
}

// closeReason explains why the attach loop ended.
type closeReason int

const (
	reasonChildExit closeReason = iota // tmux/ssh exited on its own (detach/end)
	reasonMash                         // user mashed ctrl+c
	reasonSignal                       // terminal window closed
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
		if o.forwardInput(ptmx) {
			done <- reasonMash
		}
	}()

	go func() {
		<-sigCh
		done <- reasonSignal
	}()

	reason := <-done

	intentional := reason == reasonMash || reason == reasonSignal
	if intentional {
		// Stop the local tmux/ssh client.
		_ = cmd.Process.Kill()
	}
	_, _ = cmd.Process.Wait()
	restore()
	if o.OnClose != nil {
		o.OnClose(intentional)
	}
	return nil
}

// forwardInput copies keyboard input to the child and watches for a ctrl+c
// mash. It returns true if the mash gesture fired. Every byte (including each
// ctrl+c) is forwarded so a single ctrl+c still interrupts the remote program
// normally.
func (o *Options) forwardInput(w io.Writer) bool {
	buf := make([]byte, 4096)
	var hits []time.Time
	for {
		n, err := o.In.Read(buf)
		if n > 0 {
			now := time.Now()
			for _, b := range buf[:n] {
				if b == ctrlC {
					hits = append(hits, now)
				}
			}
			// Drop hits older than the window.
			cutoff := now.Add(-o.MashWindow)
			kept := hits[:0]
			for _, t := range hits {
				if t.After(cutoff) {
					kept = append(kept, t)
				}
			}
			hits = kept

			if _, werr := w.Write(buf[:n]); werr != nil {
				return false
			}
			if len(hits) >= o.MashCount {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}
