// Package session wraps tmux to enumerate, create, and kill sessions on the
// local host. The agent uses it to report what is running and to manage
// lifecycle; the actual terminal stream is carried by ssh+tmux, not by this
// package (see DESIGN.md §5).
package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind classifies what a session appears to be running.
type Kind string

const (
	KindClaude  Kind = "claude" // a Claude Code (or similar agent) session
	KindShell   Kind = "shell"
	KindUnknown Kind = "unknown"
)

// Session describes a single tmux session.
type Session struct {
	Name     string    `json:"name"`
	Created  time.Time `json:"created"`
	Attached int       `json:"attached"` // number of attached clients
	Windows  int       `json:"windows"`
	Kind     Kind      `json:"kind"`
	Command  string    `json:"command"` // representative foreground command
}

// Manager runs tmux commands. The zero value uses "tmux" from PATH.
type Manager struct {
	Bin string // tmux binary, defaults to "tmux"
}

func (m Manager) bin() string {
	if m.Bin != "" {
		return m.Bin
	}
	return "tmux"
}

// noServerMarkers are substrings tmux prints when no server is running, which
// we treat as "zero sessions" rather than an error.
var noServerMarkers = []string{"no server running", "no current session", "error connecting to"}

// shJoin single-quotes each part into one POSIX shell command string.
func shJoin(parts []string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func (m Manager) run(ctx context.Context, args ...string) (string, error) {
	// Run tmux through a login shell (`/bin/sh -lc`) instead of exec'ing it
	// directly. On macOS a tmux client spawned straight from the launchd-managed
	// agent cannot see or connect to the tmux server, so list/has-session report
	// no sessions even when they exist (and attach works) — which broke
	// `macker ls` and session cleanup. The same command via a login shell
	// connects fine (this is the path `macker exec` already used successfully).
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", shJoin(append([]string{m.bin()}, args...)))
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.ToLower(strings.TrimSpace(errb.String()))
		for _, mk := range noServerMarkers {
			if strings.Contains(msg, mk) {
				return "", errNoServer
			}
		}
		if msg != "" {
			return "", fmt.Errorf("tmux %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return out.String(), nil
}

var errNoServer = errors.New("tmux: no server running")

// fieldSep separates tmux -F fields. It must be a PRINTABLE character: tmux
// sanitizes non-printable control bytes in -F output to "_", so a tab (0x09) or
// US (0x1f) separator comes back as "_" and splitting drops every field. "|"
// survives and never appears in a session name (alnum/-/_), the numeric
// created/attached/windows fields, or a process name; pane titles may contain
// it, so callers that parse a title split with a field limit and keep it last.
const fieldSep = "|"
const listFmt = "#{session_name}" + fieldSep + "#{session_created}" + fieldSep + "#{session_attached}" + fieldSep + "#{session_windows}"
const paneFmt = "#{session_name}" + fieldSep + "#{pane_current_command}" + fieldSep + "#{pane_title}"

// List returns all tmux sessions on the host, classified by kind. When no
// tmux server is running it returns an empty slice and no error.
func (m Manager) List(ctx context.Context) ([]Session, error) {
	out, err := m.run(ctx, "list-sessions", "-F", listFmt)
	if err != nil {
		if errors.Is(err, errNoServer) {
			return nil, nil
		}
		return nil, err
	}

	panes, _ := m.panesBySession(ctx) // best-effort classification

	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, fieldSep)
		if len(f) < 4 {
			continue
		}
		s := Session{Name: f[0]}
		if secs, err := strconv.ParseInt(f[1], 10, 64); err == nil {
			s.Created = time.Unix(secs, 0)
		}
		s.Attached, _ = strconv.Atoi(f[2])
		s.Windows, _ = strconv.Atoi(f[3])
		s.Kind, s.Command = classify(panes[s.Name])
		sessions = append(sessions, s)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
	return sessions, nil
}

// paneInfo is a single pane's identifying strings.
type paneInfo struct {
	command string
	title   string
}

func (m Manager) panesBySession(ctx context.Context) (map[string][]paneInfo, error) {
	out, err := m.run(ctx, "list-panes", "-a", "-F", paneFmt)
	if err != nil {
		return nil, err
	}
	res := map[string][]paneInfo{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, fieldSep, 3)
		if len(f) < 2 {
			continue
		}
		pi := paneInfo{command: f[1]}
		if len(f) == 3 {
			pi.title = f[2]
		}
		res[f[0]] = append(res[f[0]], pi)
	}
	return res, nil
}

// classify infers the session kind from its panes. A pane that looks like a
// Claude Code agent wins; otherwise we report the most common foreground
// command as a shell/unknown session.
func classify(panes []paneInfo) (Kind, string) {
	if len(panes) == 0 {
		return KindUnknown, ""
	}
	counts := map[string]int{}
	for _, p := range panes {
		hay := strings.ToLower(p.command + " " + p.title)
		if strings.Contains(hay, "claude") {
			return KindClaude, "claude"
		}
		counts[p.command]++
	}
	best, bestN := "", 0
	for cmd, n := range counts {
		if n > bestN {
			best, bestN = cmd, n
		}
	}
	if isShell(best) {
		return KindShell, best
	}
	return KindUnknown, best
}

func isShell(cmd string) bool {
	switch cmd {
	case "zsh", "bash", "sh", "fish", "-zsh", "-bash":
		return true
	}
	return false
}

// Exists reports whether a session with the given name exists.
func (m Manager) Exists(ctx context.Context, name string) (bool, error) {
	if !validName(name) {
		return false, fmt.Errorf("session: invalid name %q", name)
	}
	_, err := m.run(ctx, "has-session", "-t", "="+name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errNoServer) {
		return false, nil
	}
	// Any other non-zero exit (e.g. "can't find session") means "not found".
	return false, nil
}

// New creates a detached session. If command is empty, the user's default
// shell is started. It is idempotent: if a session of this name already exists
// it returns nil rather than erroring. This matters because list-sessions can
// under-report on macOS (a tmux server started in a different launchd session
// is not always visible to the agent), so the caller's "list, then create if
// missing" check may wrongly decide to create; tolerating the duplicate keeps
// attach working in that case instead of failing with a 400.
func (m Manager) New(ctx context.Context, name, command string) error {
	if !validName(name) {
		return fmt.Errorf("session: invalid name %q", name)
	}
	args := []string{"new-session", "-d", "-s", name}
	// Start in the user's home directory. The agent runs as a macOS LaunchAgent
	// whose working directory is "/", and a new tmux session would otherwise
	// inherit it — opening every macker session in "/" (read-only, which prompts
	// like Starship flag with a lock icon). Best-effort: skip -c if home is
	// unknown.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		args = append(args, "-c", home)
	}
	if command != "" {
		// "--" stops tmux option parsing so a command starting with "-" is not
		// misread as a flag.
		args = append(args, "--", command)
	}
	_, err := m.run(ctx, args...)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate session") {
		return nil
	}
	return err
}

// nodePalette is a set of distinct, readable tmux 256-colour backgrounds (each
// with a contrasting foreground) used to tint a session's status bar per node.
var nodePalette = []struct{ bg, fg string }{
	{"24", "255"},  // deep blue
	{"28", "255"},  // green
	{"88", "255"},  // dark red
	{"130", "255"}, // orange
	{"54", "255"},  // purple
	{"23", "255"},  // teal
	{"94", "255"},  // brown
	{"240", "255"}, // gray
	{"19", "255"},  // blue
	{"64", "255"},  // olive
	{"125", "255"}, // magenta
	{"166", "255"}, // burnt orange
}

// NodeColor returns a stable status-bar background/foreground for a node name,
// so every session on a node shares one colour and different nodes differ.
func NodeColor(node string) (bg, fg string) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(node))
	p := nodePalette[h.Sum32()%uint32(len(nodePalette))]
	return p.bg, p.fg
}

// ApplyNodeStyle tints a session's tmux status bar with the node's colour and
// labels it with the node name, so an attached client can tell at a glance
// which machine it is on. Best-effort and idempotent.
func (m Manager) ApplyNodeStyle(ctx context.Context, name, node string) error {
	if !validName(name) {
		return fmt.Errorf("session: invalid name %q", name)
	}
	bg, fg := NodeColor(node)
	style := fmt.Sprintf("bg=colour%s,fg=colour%s", bg, fg)
	// NOTE: use the plain session name as the target, NOT "=name". tmux's
	// set-option does not accept the "=" exact-match prefix (it reports
	// "no such session: =name"), unlike attach/has/kill-session. Session names
	// here are unique, so a plain target matches the right one.
	// Tint the whole bar, label the node on the left, and widen the left cell so
	// long node names are not truncated.
	if _, err := m.run(ctx, "set-option", "-t", name, "status-style", style); err != nil {
		return err
	}
	_, _ = m.run(ctx, "set-option", "-t", name, "status-left-length", "40")
	_, _ = m.run(ctx, "set-option", "-t", name, "status-left", fmt.Sprintf(" %s ", node))
	return nil
}

// Kill terminates a session by exact name.
func (m Manager) Kill(ctx context.Context, name string) error {
	if !validName(name) {
		return fmt.Errorf("session: invalid name %q", name)
	}
	_, err := m.run(ctx, "kill-session", "-t", "="+name)
	if errors.Is(err, errNoServer) {
		return nil // nothing to kill
	}
	return err
}

// validName guards against argument/option injection through session names.
// tmux session names cannot contain ':' or '.', and we additionally forbid
// leading dashes and whitespace so a name can never be read as a flag.
func validName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	if strings.HasPrefix(name, "-") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
