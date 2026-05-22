// Package cli implements the macker command-line interface and wires together
// the agent, client, tailnet, and session packages.
package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/masakasuno1/macker/internal/api"
	"github.com/masakasuno1/macker/internal/config"
	"github.com/masakasuno1/macker/internal/tailnet"
)

// Version is the CLI version, set from main.
var Version = "0.1.0-dev"

const usage = `macker — manage AI-agent/dev sessions across your tailnet

usage:
  macker <node>                     open a fresh session on a node (one per window;
                                    closing it kills only that session)
  macker <node>:<session>           attach to (or create) a named, reattachable session
  macker <node>[:<session>] clear   reset that session (kill; next attach is fresh)
  macker ls                         list nodes and their sessions
  macker <node> ls                  list one node's sessions in detail (for clear/attach decisions)
  macker exec <node> -- <cmd>...    run an authorized command on a node
  macker grid <target>...           open a grid attached to each target
  macker agent                      run the node daemon
  macker collector                  run the event collector daemon
  macker context [ls|use <name>]    show or switch the active context
  macker version                    print version

  (macker attach / kill <node>:<session> also work explicitly)

global flags:
  --context <name>  select a config context (or set MACKER_CONTEXT)

target syntax:
  <node>            a tailnet node name; "self"/"local" or empty means this machine
  <node>:<session>  a specific tmux session on that node

run "macker <command> -h" for command-specific flags.
`

// Main is the CLI entry point. It returns a process exit code.
func Main(args []string) int {
	args, err := applyGlobalFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "macker:", err)
		return 2
	}
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	ctx, stop := signalContext()
	defer stop()

	cmd, rest := args[1], args[2:]
	switch cmd {
	case "ls":
		err = cmdLs(ctx, rest)
	case "attach":
		err = cmdAttach(ctx, rest)
	case "exec":
		err = cmdExec(ctx, rest)
	case "kill":
		err = cmdKill(ctx, rest)
	case "grid":
		err = cmdGrid(ctx, rest)
	case "agent":
		err = cmdAgent(ctx, rest)
	case "collector":
		err = cmdCollector(ctx, rest)
	case "context":
		err = cmdContext(ctx, rest)
	case "version":
		fmt.Println("macker", Version)
		return 0
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		// Not a subcommand: treat the first token as a node target, so
		// `macker mac-mini` attaches, `macker mac-mini clear` resets it, and
		// `macker mac-mini ls` lists just that node's sessions in detail.
		if len(rest) >= 1 && rest[0] == "clear" {
			err = cmdClear(ctx, append([]string{cmd}, rest[1:]...))
		} else if len(rest) == 1 && rest[0] == "ls" {
			err = cmdNodeLs(ctx, cmd)
		} else {
			// Reorder so attach's flag parser sees flags first, then the
			// target last: `macker mac-mini --keep` -> attach [--keep mac-mini].
			err = cmdAttach(ctx, append(rest, cmd))
		}
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "macker:", err)
		return 1
	}
	return 0
}

// applyGlobalFlags extracts a global "--context <name>" (or "--context=name")
// flag that appears BEFORE the subcommand, sets MACKER_CONTEXT, and returns the
// args with the flag removed. Scanning stops at the first non-flag token (the
// subcommand) so it never touches a subcommand's own flags or anything after a
// "--" passthrough (e.g. `macker exec n -- tool --context x`).
func applyGlobalFlags(args []string) ([]string, error) {
	if len(args) == 0 {
		return args, nil
	}
	out := []string{args[0]} // program name
	i := 1
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--context":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--context requires a value")
			}
			os.Setenv("MACKER_CONTEXT", args[i+1])
			i++
		case strings.HasPrefix(a, "--context="):
			os.Setenv("MACKER_CONTEXT", strings.TrimPrefix(a, "--context="))
		case a == "--":
			// Everything from here on is passthrough; stop scanning.
			out = append(out, args[i:]...)
			return out, nil
		case strings.HasPrefix(a, "-"):
			// Some other global-looking flag; leave it for the subcommand layer.
			out = append(out, a)
		default:
			// First non-flag token is the subcommand; stop and keep the rest
			// verbatim so subcommand args/passthrough are untouched.
			out = append(out, args[i:]...)
			return out, nil
		}
	}
	return out, nil
}

// target is a parsed "<node>:<session>" reference.
type target struct {
	node    string // empty means the local machine
	session string // empty means unspecified
}

// parseTarget splits "node", "node:session", or ":session".
func parseTarget(s string) target {
	node, session, found := strings.Cut(s, ":")
	if !found {
		return target{node: s}
	}
	return target{node: node, session: session}
}

func (t target) isLocal() bool {
	switch strings.ToLower(t.node) {
	case "", "self", "local":
		return true
	}
	return false
}

// resolver maps node names to reachable addresses using the tailnet status.
type resolver struct {
	cfg   config.Config
	ts    *tailnet.Client
	nodes []tailnet.Node
}

func newResolver(ctx context.Context) (*resolver, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	r := &resolver{cfg: cfg, ts: tailnet.New(cfg.TailscaleBin)}
	if r.ts.Available() {
		// Best-effort: tailnet status may fail if tailscaled is down; the
		// local path still works without it.
		if nodes, err := r.ts.Status(ctx); err == nil {
			r.nodes = nodes
		}
	}
	return r, nil
}

// resolved describes how to reach a target.
type resolved struct {
	name  string // resolved node name
	host  string // host to dial (loopback for local)
	local bool
	node  *tailnet.Node
}

func (r *resolver) resolve(t target) (resolved, error) {
	if t.isLocal() {
		return resolved{name: r.cfg.Node, host: "127.0.0.1", local: true}, nil
	}
	if strings.EqualFold(t.node, r.cfg.Node) {
		return resolved{name: r.cfg.Node, host: "127.0.0.1", local: true}, nil
	}
	for i := range r.nodes {
		n := &r.nodes[i]
		if strings.EqualFold(n.Name, t.node) {
			if n.Self {
				return resolved{name: n.Name, host: "127.0.0.1", local: true, node: n}, nil
			}
			addr := n.Addr()
			if addr == "" {
				return resolved{}, fmt.Errorf("node %q has no reachable address", t.node)
			}
			return resolved{name: n.Name, host: addr, local: false, node: n}, nil
		}
	}
	if !r.ts.Available() {
		return resolved{}, fmt.Errorf("node %q not found (Tailscale CLI unavailable; only the local node is reachable)", t.node)
	}
	return resolved{}, fmt.Errorf("node %q not found on the tailnet", t.node)
}

// cmdLs lists nodes and sessions.
func cmdLs(ctx context.Context, args []string) error {
	r, err := newResolver(ctx)
	if err != nil {
		return err
	}

	nodes := r.nodes
	if len(nodes) == 0 {
		// No tailnet view: show just the local node.
		nodes = []tailnet.Node{{Name: r.cfg.Node, Self: true, Online: true, OS: "local"}}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Self != nodes[j].Self {
			return nodes[i].Self // self first
		}
		return nodes[i].Name < nodes[j].Name
	})

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tSTATUS\tOS\tADDR\tSESSIONS")
	for _, n := range nodes {
		status := "offline"
		if n.Online {
			status = "online"
		}
		host := n.Addr()
		if n.Self {
			host = "127.0.0.1"
		}
		sessSummary := "-"
		if n.Online {
			sessSummary = sessionsSummary(ctx, r, n, host)
		}
		name := n.Name
		if n.Self {
			name += " *"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, status, n.OS, n.Addr(), sessSummary)
	}
	return tw.Flush()
}

// sessionsSummary fetches and summarizes a node's sessions, tolerating errors.
func sessionsSummary(ctx context.Context, r *resolver, n tailnet.Node, host string) string {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	c := newClient(r.cfg)
	resp, err := c.ListSessions(ctx, host)
	if err != nil {
		return "(agent?)"
	}
	if len(resp.Sessions) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		parts = append(parts, fmt.Sprintf("%s[%s,%s]", s.Name, s.Kind, stateMark(s.State)))
	}
	return strings.Join(parts, " ")
}

// stateMark renders a session state compactly for the ls table.
func stateMark(st api.SessionState) string {
	switch st {
	case api.StateAttached:
		return "attached"
	case api.StateOrphaned:
		return "orphaned"
	default:
		return "detached"
	}
}

// cmdNodeLs lists a single node's sessions in detail: `macker <node> ls`. It is
// the per-node companion to `macker ls`, meant to inform attach/clear decisions
// (which sessions are orphaned, how old they are, what is running in them).
func cmdNodeLs(ctx context.Context, nodeArg string) error {
	t := parseTarget(nodeArg)

	r, err := newResolver(ctx)
	if err != nil {
		return err
	}
	res, err := r.resolve(t)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	resp, err := newClient(r.cfg).ListSessions(ctx, res.host)
	if err != nil {
		return fmt.Errorf("could not reach %s — is its agent running? (%w)", res.name, err)
	}
	if len(resp.Sessions) == 0 {
		fmt.Printf("%s: no sessions\n", res.name)
		return nil
	}

	// Surface cleanup candidates first: orphaned, then detached, then attached;
	// ties broken by name. This puts the most "clearable" sessions at the top.
	rank := func(st api.SessionState) int {
		switch st {
		case api.StateOrphaned:
			return 0
		case api.StateDetached:
			return 1
		default: // attached
			return 2
		}
	}
	sort.Slice(resp.Sessions, func(i, j int) bool {
		if ri, rj := rank(resp.Sessions[i].State), rank(resp.Sessions[j].State); ri != rj {
			return ri < rj
		}
		return resp.Sessions[i].Name < resp.Sessions[j].Name
	})

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tKIND\tSTATE\tATTACHED\tWINDOWS\tAGE\tCOMMAND")
	orphans := 0
	for _, s := range resp.Sessions {
		if s.State == api.StateOrphaned {
			orphans++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			s.Name, s.Kind, stateMark(s.State), s.Attached, s.Windows,
			shortDur(time.Since(s.Created)), s.Command)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if orphans > 0 {
		fmt.Fprintf(os.Stderr, "\n%d orphaned; clean up with: macker %s:<session> clear\n", orphans, t.node)
	}
	return nil
}

// shortDur renders a duration compactly for the age column (e.g. "12s", "5m",
// "3h", "2d"), picking the largest single unit. Negative/zero clamps to "0s".
func shortDur(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
