package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/masakasuno1/macker/internal/api"
	"github.com/masakasuno1/macker/internal/attach"
	"github.com/masakasuno1/macker/internal/session"
)

// newClientID returns a random identifier for this attach client's lease.
func newClientID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// cmdAttach attaches to (creating if needed) a session on a target node.
func cmdAttach(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	keep := fs.Bool("keep", false, "keep the session alive on intentional close (do not kill)")
	command := fs.String("command", "", "command to run if the session must be created")
	defName := fs.String("session", "main", "session name to use when the target omits one")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: macker attach [flags] <node>[:<session>]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("attach: exactly one target required")
	}

	t := parseTarget(fs.Arg(0))
	if t.session == "" {
		t.session = *defName
	}

	r, err := newResolver(ctx)
	if err != nil {
		return err
	}
	res, err := r.resolve(t)
	if err != nil {
		return err
	}

	// Ensure the session exists (create if missing).
	if err := ensureSession(ctx, r, res, t.session, *command); err != nil {
		return err
	}

	ephemeral := !*keep
	clientID := newClientID()
	const leaseTTL = 30 * time.Second

	// Lease heartbeat: registers/refreshes our hold on the session so the agent
	// can tell "attached" from "orphaned". Best-effort: a local node without a
	// running agent simply has no lease tracking.
	leaseRenew := func() {
		leaseSession(r, res, t.session, clientID, int(leaseTTL.Seconds()), ephemeral)
	}

	// On close: an intentional close of an ephemeral session kills it; anything
	// else (—keep, or a natural detach/disconnect) releases the lease so the
	// session lives on (and, if the holder vanished without this, goes orphaned).
	onClose := func(intentional bool) {
		if intentional && ephemeral {
			releaseSession(r, res, t.session)
			return
		}
		unleaseSession(r, res, t.session, clientID)
	}

	fmt.Fprintf(os.Stderr, "macker: attaching to %s:%s (%s)\n", res.name, t.session, lifecycleLabel(*keep))
	return attach.Run(ctx, attach.Options{
		Local:         res.local,
		Addr:          res.host,
		Session:       t.session,
		LeaseRenew:    leaseRenew,
		LeaseInterval: leaseTTL / 3,
		OnClose:       onClose,
	})
}

func lifecycleLabel(keep bool) string {
	if keep {
		return "keep; ctrl+c×3 or close detaches"
	}
	return "ephemeral; ctrl+c×3 or close kills"
}

// ensureSession makes sure the named session exists on the target.
func ensureSession(ctx context.Context, r *resolver, res resolved, name, command string) error {
	if res.local {
		mgr := session.Manager{}
		// Prefer the local agent for audit; fall back to direct tmux if it is
		// not running, so attach works before the daemon is installed.
		exists, err := mgr.Exists(ctx, name)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		return mgr.New(ctx, name, command)
	}

	c := newClient(r.cfg)
	list, err := c.ListSessions(ctx, res.host)
	if err != nil {
		return fmt.Errorf("contacting agent on %s: %w", res.name, err)
	}
	for _, s := range list.Sessions {
		if s.Name == name {
			return nil
		}
	}
	_, err = c.CreateSession(ctx, res.host, api.CreateSessionRequest{
		Name: name, Command: command, Ephemeral: true,
	})
	return err
}

// releaseSession performs the intentional-close kill for the target session.
func releaseSession(r *resolver, res resolved, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if res.local {
		// Kill directly so it still works when no local agent is running; the
		// agent (if up) drops the lease on its next list/prune.
		err = session.Manager{}.Kill(ctx, name)
		_ = newClient(r.cfg).Kill(ctx, res.host, name) // best-effort: drop lease
	} else {
		err = newClient(r.cfg).Release(ctx, res.host, name)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "macker: release %s:%s failed: %v\n", res.name, name, err)
	}
}

// leaseSession registers/renews the lease (best-effort; requires a reachable
// agent, so a local node without the daemon simply has no lease tracking).
func leaseSession(r *resolver, res resolved, name, clientID string, ttlSec int, ephemeral bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = newClient(r.cfg).Lease(ctx, res.host, name, api.LeaseRequest{
		ClientID: clientID, TTLSec: ttlSec, Ephemeral: ephemeral,
	})
}

// unleaseSession releases the lease (clean detach), best-effort.
func unleaseSession(r *resolver, res resolved, name, clientID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = newClient(r.cfg).Unlease(ctx, res.host, name, clientID)
}

// validLayouts are the tmux layouts grid accepts.
var validLayouts = map[string]bool{
	"tiled": true, "even-horizontal": true, "even-vertical": true,
	"main-horizontal": true, "main-vertical": true,
}

// cmdGrid opens one pane/window attached per target. The default mode tiles the
// targets as panes in a single local tmux grid; closing it sends SIGHUP to each
// pane's `macker attach`, an intentional close (ephemeral sessions are killed).
// The experimental "windows" mode opens a separate native terminal window per
// target instead.
func cmdGrid(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("grid", flag.ContinueOnError)
	keep := fs.Bool("keep", false, "pass --keep to each attach")
	layout := fs.String("layout", "tiled", "tmux layout: tiled|even-horizontal|even-vertical|main-horizontal|main-vertical")
	mode := fs.String("mode", "tmux", "grid mode: tmux (panes) or windows (experimental, native windows)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: macker grid [flags] <target>...")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	targets := fs.Args()
	if len(targets) == 0 {
		fs.Usage()
		return errors.New("grid: at least one target required")
	}
	if !validLayouts[*layout] {
		return fmt.Errorf("grid: invalid layout %q", *layout)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	attachCmd := func(tgt string) string {
		argv := []string{self, "attach"}
		if *keep {
			argv = append(argv, "--keep")
		}
		argv = append(argv, tgt)
		return shellJoin(argv)
	}

	if *mode == "windows" {
		return gridWindows(ctx, targets, attachCmd)
	}
	return gridTmux(ctx, targets, *layout, attachCmd)
}

// gridTmux builds a private-socket tmux session with one pane per target, each
// pane titled with its target, and attaches the user to it.
func gridTmux(ctx context.Context, targets []string, layout string, attachCmd func(string) string) error {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("grid requires tmux: %w", err)
	}
	const sock = "mackergrid"
	const grid = "grid"
	// Tear down any previous grid so panes start clean.
	_ = exec.CommandContext(ctx, tmuxBin, "-L", sock, "kill-session", "-t", "="+grid).Run()

	// new-session prints the first pane's id with -P -F.
	pid, err := tmuxCapture(ctx, tmuxBin, sock,
		"new-session", "-d", "-s", grid, "-n", grid, "-P", "-F", "#{pane_id}", attachCmd(targets[0]))
	if err != nil {
		return fmt.Errorf("grid: new-session: %w", err)
	}
	setPaneTitle(ctx, tmuxBin, sock, pid, targets[0])

	for _, tgt := range targets[1:] {
		pid, err := tmuxCapture(ctx, tmuxBin, sock,
			"split-window", "-t", "="+grid, "-P", "-F", "#{pane_id}", attachCmd(tgt))
		if err != nil {
			return fmt.Errorf("grid: split-window: %w", err)
		}
		setPaneTitle(ctx, tmuxBin, sock, pid, tgt)
		// Re-tile after each split so panes stay evenly sized.
		_ = exec.CommandContext(ctx, tmuxBin, "-L", sock, "select-layout", "-t", "="+grid, "tiled").Run()
	}

	// Final layout and per-pane title borders.
	_ = exec.CommandContext(ctx, tmuxBin, "-L", sock, "select-layout", "-t", "="+grid, layout).Run()
	_ = exec.CommandContext(ctx, tmuxBin, "-L", sock, "set-option", "-t", "="+grid, "pane-border-status", "top").Run()
	_ = exec.CommandContext(ctx, tmuxBin, "-L", sock, "set-option", "-t", "="+grid, "pane-border-format", " #{pane_title} ").Run()
	_ = exec.CommandContext(ctx, tmuxBin, "-L", sock, "set-option", "-t", "="+grid, "mouse", "on").Run()

	// Attach the user to the grid (replaces the current process's terminal).
	c := exec.CommandContext(ctx, tmuxBin, "-L", sock, "attach-session", "-t", "="+grid)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// tmuxCapture runs a tmux command and returns trimmed stdout.
func tmuxCapture(ctx context.Context, tmuxBin, sock string, args ...string) (string, error) {
	full := append([]string{"-L", sock}, args...)
	out, err := exec.CommandContext(ctx, tmuxBin, full...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func setPaneTitle(ctx context.Context, tmuxBin, sock, paneID, title string) {
	if paneID == "" {
		return
	}
	_ = exec.CommandContext(ctx, tmuxBin, "-L", sock, "select-pane", "-t", paneID, "-T", title).Run()
}
