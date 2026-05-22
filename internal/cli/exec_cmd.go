package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/masakasuno1/macker/internal/agent"
	"github.com/masakasuno1/macker/internal/api"
	"github.com/masakasuno1/macker/internal/client"
	"github.com/masakasuno1/macker/internal/collector"
	"github.com/masakasuno1/macker/internal/config"
	"github.com/masakasuno1/macker/internal/eventlog"
	"github.com/masakasuno1/macker/internal/session"
	"github.com/masakasuno1/macker/internal/tailnet"
)

// cmdExec runs an authorized command on a node.
//
//	macker exec <node> -- <program> [args...]
//	macker exec --shell <node> -- "<shell line>"
func cmdExec(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	useShell := fs.Bool("shell", false, "run the command via the remote login shell instead of argv")
	timeout := fs.Int("timeout", 0, "timeout in seconds (0 = agent default)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: macker exec [flags] <node> -- <command>...")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 2 {
		fs.Usage()
		return errors.New("exec: usage: macker exec <node> -- <command>...")
	}
	node, cmdArgs := pos[0], pos[1:]
	// The flag package stops parsing at the first non-flag arg (the node), so
	// the literal "--" separator the user typed is left at the head of the
	// command. Drop it.
	if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
		cmdArgs = cmdArgs[1:]
	}
	if len(cmdArgs) == 0 {
		fs.Usage()
		return errors.New("exec: no command given")
	}

	req := api.ExecRequest{TimeoutSec: *timeout}
	if *useShell {
		req.Command = strings.Join(cmdArgs, " ")
	} else {
		req.Command = cmdArgs[0]
		req.Args = cmdArgs[1:]
	}

	r, err := newResolver(ctx)
	if err != nil {
		return err
	}
	res, err := r.resolve(parseTarget(node))
	if err != nil {
		return err
	}

	resp, err := newClient(r.cfg).Exec(ctx, res.host, req)
	if err != nil {
		// On the local node, fall back to direct execution if the agent is not
		// running (so exec works before the daemon is installed). Remote nodes
		// require the agent — there is no other authorized path.
		if res.local && !client.IsForbidden(err) {
			fmt.Fprintln(os.Stderr, "macker: local agent unreachable, running directly (no audit log)")
			return execDirect(ctx, req)
		}
		return err
	}

	if resp.Stdout != "" {
		fmt.Fprint(os.Stdout, resp.Stdout)
		ensureNewline(os.Stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		fmt.Fprint(os.Stderr, resp.Stderr)
		ensureNewline(os.Stderr, resp.Stderr)
	}
	if resp.TimedOut {
		return fmt.Errorf("exec timed out on %s", res.name)
	}
	if resp.ExitCode != 0 {
		return fmt.Errorf("exec exited %d on %s", resp.ExitCode, res.name)
	}
	return nil
}

func ensureNewline(f *os.File, s string) {
	if !strings.HasSuffix(s, "\n") {
		fmt.Fprintln(f)
	}
}

// execDirect runs a command locally without the agent (no audit).
func execDirect(ctx context.Context, req api.ExecRequest) error {
	var c *exec.Cmd
	if len(req.Args) > 0 {
		c = exec.CommandContext(ctx, req.Command, req.Args...)
	} else {
		c = exec.CommandContext(ctx, "/bin/sh", "-lc", req.Command)
	}
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
	return c.Run()
}

// cmdKill terminates a session on a node.
func cmdKill(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: macker kill <node>:<session>")
	}
	t := parseTarget(args[0])
	if t.session == "" {
		return errors.New("kill: a session must be specified, e.g. macker kill mac-mini:main")
	}

	r, err := newResolver(ctx)
	if err != nil {
		return err
	}
	res, err := r.resolve(t)
	if err != nil {
		return err
	}

	if res.local {
		if err := (session.Manager{}).Kill(ctx, t.session); err != nil {
			return err
		}
	} else {
		if err := newClient(r.cfg).Kill(ctx, res.host, t.session); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "macker: killed %s:%s\n", res.name, t.session)
	return nil
}

// cmdClear resets sessions on a node: `macker <node>[:<session>] clear`.
//   - With a session (`<node>:<name> clear`) it kills that one session.
//   - Without one (`<node> clear`) it reaps the auto sessions — the unnamed
//     `s-xxxxxxxx` sessions a bare `macker <node>` opens — and leaves named
//     (deliberately persistent) sessions alone.
func cmdClear(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: macker <node>[:<session>] clear")
	}
	t := parseTarget(args[0])

	r, err := newResolver(ctx)
	if err != nil {
		return err
	}
	res, err := r.resolve(t)
	if err != nil {
		return err
	}

	kill := func(name string) error {
		if res.local {
			return (session.Manager{}).Kill(ctx, name)
		}
		return newClient(r.cfg).Kill(ctx, res.host, name)
	}

	// Explicit session: kill just it.
	if t.session != "" {
		if err := kill(t.session); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "macker: reset %s:%s\n", res.name, t.session)
		return nil
	}

	// Bare node: reap auto sessions only.
	names, err := listSessionNames(ctx, r, res)
	if err != nil {
		return fmt.Errorf("listing sessions on %s: %w", res.name, err)
	}
	var killed []string
	for _, n := range names {
		if !isAutoSession(n) {
			continue
		}
		if err := kill(n); err != nil {
			fmt.Fprintf(os.Stderr, "macker: clear %s:%s failed: %v\n", res.name, n, err)
			continue
		}
		killed = append(killed, n)
	}
	if len(killed) == 0 {
		fmt.Fprintf(os.Stderr, "macker: %s has no auto sessions to clear (named sessions are left alone; use '%s:<name> clear' for those)\n", res.name, t.node)
		return nil
	}
	fmt.Fprintf(os.Stderr, "macker: cleared %d auto session(s) on %s: %s\n", len(killed), res.name, strings.Join(killed, ", "))
	return nil
}

// listSessionNames returns the session names on a target, local or remote.
func listSessionNames(ctx context.Context, r *resolver, res resolved) ([]string, error) {
	if res.local {
		ss, err := (session.Manager{}).List(ctx)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(ss))
		for i, s := range ss {
			names[i] = s.Name
		}
		return names, nil
	}
	resp, err := newClient(r.cfg).ListSessions(ctx, res.host)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(resp.Sessions))
	for i, s := range resp.Sessions {
		names[i] = s.Name
	}
	return names, nil
}

// cmdAgent runs the node daemon until interrupted.
func cmdAgent(parent context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	listen := fs.String("listen", "", "override listen address (e.g. :4477)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: macker agent [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}

	log, err := eventlog.Open(cfg.EventLogPath())
	if err != nil {
		return fmt.Errorf("agent: open event log: %w", err)
	}
	defer log.Close()

	ts := tailnet.New(cfg.TailscaleBin)
	if !ts.Available() {
		fmt.Fprintln(os.Stderr, "macker agent: WARNING: tailscale CLI not found; remote peers cannot be authorized (loopback only)")
	}

	// Derive the tenant (tailnet) for event tagging if not configured.
	if cfg.Tenant == "" && ts.Available() {
		if tenant, err := ts.Tenant(parent); err == nil && tenant != "" {
			cfg.Tenant = tenant
		}
	}

	// The local token gates loopback access so other local users cannot drive
	// the agent. Generated on first run, persisted 0600.
	tok, err := config.LoadOrCreateToken(cfg.TokenPath())
	if err != nil {
		return err
	}

	srv := agent.New(cfg, ts, log)
	srv.SetLocalToken(tok)

	// Trust this account's other devices for exec (no per-node owner config
	// needed for a single owner's machines).
	if ts.Available() {
		if login, err := ts.SelfLogin(parent); err == nil && login != "" {
			srv.SetSelfLogin(login)
			fmt.Fprintf(os.Stderr, "macker agent: trusting same-account devices (%s) for exec\n", login)
		}
	}

	// The agent is a daemon: trap SIGINT and SIGTERM for graceful exit,
	// layered on the parent context.
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// If a collector is configured, ship local events to it in the background.
	if cfg.Collector != "" {
		sh := agent.NewShipper(cfg.Collector, cfg.EventLogPath(), cfg.ShipperCursorPath(), tok)
		go sh.Run(ctx)
	}

	return srv.Serve(ctx)
}

// cmdCollector runs the event collector daemon.
func cmdCollector(parent context.Context, args []string) error {
	fs := flag.NewFlagSet("collector", flag.ContinueOnError)
	listen := fs.String("listen", ":4478", "listen address")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: macker collector [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	store, err := collector.NewStore(cfg.CollectorDir())
	if err != nil {
		return err
	}
	defer store.Close()

	ts := tailnet.New(cfg.TailscaleBin)
	tok, err := config.LoadOrCreateToken(cfg.TokenPath())
	if err != nil {
		return err
	}
	if cfg.Tenant == "" && ts.Available() {
		if tenant, err := ts.Tenant(parent); err == nil && tenant != "" {
			cfg.Tenant = tenant
		}
	}

	// Local audit log for the collector node (records authorization denials),
	// kept separate from mirrored node logs.
	auditLog, err := eventlog.Open(filepath.Join(cfg.DataDir, "collector.events.jsonl"))
	if err != nil {
		return fmt.Errorf("collector: open audit log: %w", err)
	}
	defer auditLog.Close()

	srv := collector.NewServer(store, ts, cfg.Policy)
	srv.SetLocalToken(tok)
	srv.SetAudit(auditLog, cfg.Node, cfg.Tenant)
	if ts.Available() {
		if login, err := ts.SelfLogin(parent); err == nil && login != "" {
			srv.SetSelfLogin(login)
		}
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Serve(ctx, *listen)
}
