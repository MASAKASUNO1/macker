// Package config loads macker's configuration with sane defaults.
//
// Configuration resolution order (later wins):
//  1. built-in defaults
//  2. config file (JSON) at $MACKER_CONFIG or ~/.config/macker/config.json
//  3. selected environment variables (MACKER_*)
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultPort is the agent's default TCP port.
const DefaultPort = 4477

// Policy describes who may do what to this node. It is intentionally a thin
// allowlist model layered on top of Tailscale identity (see DESIGN.md §3):
// transport trust and membership come from Tailscale; this only decides
// capability. The matching is by tailnet login (e.g. "alice@example.com").
type Policy struct {
	// Owners may do anything, including exec, and can never be locked out.
	Owners []string `json:"owners"`
	// ExecAllow lists logins permitted to run exec (in addition to Owners).
	ExecAllow []string `json:"exec_allow"`
	// AttachAllow lists logins permitted to attach/list. Empty means "any
	// authenticated tailnet peer" (membership is already enforced by Tailscale).
	AttachAllow []string `json:"attach_allow"`
}

// Context is one named profile. macker is multi-tenant in the sense that a
// "tenant" is a tailnet: each context targets one tailnet (its own tailscale
// CLI/socket, collector, policy, and isolated state dir), so a single machine
// can participate in several tailnets without their state colliding (see
// DESIGN.md §8). The fields mirror Config minus the resolved-only fields.
type Context struct {
	Node         string `json:"node,omitempty"`
	Listen       string `json:"listen,omitempty"`
	TailscaleBin string `json:"tailscale_bin,omitempty"`
	DataDir      string `json:"data_dir,omitempty"`
	Collector    string `json:"collector,omitempty"`
	// Tenant names the tailnet this context targets. If empty it is derived at
	// runtime from `tailscale status` (CurrentTailnet).
	Tenant string `json:"tenant,omitempty"`
	Policy Policy `json:"policy,omitempty"`
}

// Config is the resolved runtime configuration for the selected context.
type Config struct {
	// Context is the name of the selected context ("default" when unnamed).
	Context string `json:"-"`
	// Node is this machine's logical name (defaults to the hostname).
	Node string `json:"node"`
	// Listen is the agent's bind address.
	Listen string `json:"listen"`
	// TailscaleBin is the path to the tailscale CLI; auto-resolved if empty.
	TailscaleBin string `json:"tailscale_bin"`
	// DataDir holds the event log and other state (per-context).
	DataDir string `json:"data_dir"`
	// Collector, if set, is the base URL of a collector to mirror events to.
	Collector string `json:"collector"`
	// Tenant is the tailnet this context targets (may be derived at runtime).
	Tenant string `json:"tenant"`
	// Policy is the authorization policy for inbound requests.
	Policy Policy `json:"policy"`
}

// DefaultContextName is used when no context is named.
const DefaultContextName = "default"

// file is the on-disk config format. It supports both a multi-context form
// (current_context + contexts) and a legacy single-context form where the
// fields live at the top level (the embedded Context).
type file struct {
	CurrentContext string             `json:"current_context,omitempty"`
	Contexts       map[string]Context `json:"contexts,omitempty"`
	Context                           // legacy top-level fields
}

// EventLogPath is the path to this node's append-only event log.
func (c *Config) EventLogPath() string {
	return filepath.Join(c.DataDir, "events.jsonl")
}

// TokenPath is the path to the local loopback auth token. The token gates
// loopback access so that a *different* local user on a multi-user host cannot
// drive the agent: only the user who owns this 0600 file (the agent's own
// user) can read it and present it (see DESIGN.md §3).
func (c *Config) TokenPath() string {
	return filepath.Join(c.DataDir, "agent.token")
}

// ShipperCursorPath is where the agent records the last event ID it has
// successfully shipped to the collector.
func (c *Config) ShipperCursorPath() string {
	return filepath.Join(c.DataDir, "shipper.cursor")
}

// CollectorDir is the root under which a collector stores mirrored logs,
// organized as <CollectorDir>/<tenant>/<node>.jsonl.
func (c *Config) CollectorDir() string {
	return filepath.Join(c.DataDir, "collector")
}

// DefaultConfigPath returns the path of the config file, honoring
// $MACKER_CONFIG and $XDG_CONFIG_HOME.
func DefaultConfigPath() string {
	if p := os.Getenv("MACKER_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(base, "macker", "config.json")
}

func defaultDataDir() string {
	if p := os.Getenv("MACKER_DATA_DIR"); p != "" {
		return p
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(base, "macker")
}

// Defaults returns a Config populated with built-in defaults (default context).
func Defaults() Config {
	node, _ := os.Hostname()
	node = strings.TrimSuffix(node, ".local")
	return Config{
		Context:      DefaultContextName,
		Node:         node,
		Listen:       fmt.Sprintf(":%d", DefaultPort),
		TailscaleBin: ResolveTailscaleBin(),
		DataDir:      defaultDataDir(),
	}
}

// readFile reads and parses the config file. A missing file yields a zero
// value and no error.
func readFile() (file, error) {
	var f file
	path := DefaultConfigPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return f, nil
		}
		return f, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return f, nil
}

// selectedContextName resolves which context to use: $MACKER_CONTEXT wins,
// then the file's current_context, then "default".
func selectedContextName(f file) string {
	if v := os.Getenv("MACKER_CONTEXT"); v != "" {
		return v
	}
	if f.CurrentContext != "" {
		return f.CurrentContext
	}
	return DefaultContextName
}

// Load resolves configuration for the selected context from defaults, the
// config file, and the environment. A missing config file is not an error.
func Load() (Config, error) {
	f, err := readFile()
	if err != nil {
		return Config{}, err
	}

	name := selectedContextName(f)
	cfg := Defaults()
	cfg.Context = name

	if len(f.Contexts) > 0 {
		ctx, ok := f.Contexts[name]
		if !ok {
			return cfg, fmt.Errorf("config: context %q not found (have: %s)", name, strings.Join(contextNames(f), ", "))
		}
		overlay(&cfg, ctx)
	} else {
		// Legacy single-context form: top-level fields.
		overlay(&cfg, f.Context)
	}

	applyEnv(&cfg)

	// Isolate non-default contexts under their own state subdir unless the
	// context set an explicit DataDir or MACKER_DATA_DIR was provided.
	if cfg.DataDir == defaultDataDir() && cfg.Context != DefaultContextName && os.Getenv("MACKER_DATA_DIR") == "" {
		cfg.DataDir = filepath.Join(defaultDataDir(), "contexts", cfg.Context)
	}

	if cfg.Node == "" {
		return cfg, errors.New("config: node name is empty and hostname could not be determined")
	}
	return cfg, nil
}

// overlay copies non-empty fields from a Context onto a Config.
func overlay(cfg *Config, c Context) {
	if c.Node != "" {
		cfg.Node = c.Node
	}
	if c.Listen != "" {
		cfg.Listen = c.Listen
	}
	if c.TailscaleBin != "" {
		cfg.TailscaleBin = c.TailscaleBin
	}
	if c.DataDir != "" {
		cfg.DataDir = c.DataDir
	}
	if c.Collector != "" {
		cfg.Collector = c.Collector
	}
	if c.Tenant != "" {
		cfg.Tenant = c.Tenant
	}
	if c.Policy.Owners != nil {
		cfg.Policy.Owners = c.Policy.Owners
	}
	if c.Policy.ExecAllow != nil {
		cfg.Policy.ExecAllow = c.Policy.ExecAllow
	}
	if c.Policy.AttachAllow != nil {
		cfg.Policy.AttachAllow = c.Policy.AttachAllow
	}
}

func contextNames(f file) []string {
	names := make([]string, 0, len(f.Contexts))
	for n := range f.Contexts {
		names = append(names, n)
	}
	return names
}

// Contexts returns the names of configured contexts and the current one.
func Contexts() (names []string, current string, err error) {
	f, err := readFile()
	if err != nil {
		return nil, "", err
	}
	return contextNames(f), selectedContextName(f), nil
}

// SetCurrentContext rewrites the config file's current_context. The named
// context must exist.
func SetCurrentContext(name string) error {
	f, err := readFile()
	if err != nil {
		return err
	}
	if _, ok := f.Contexts[name]; !ok {
		return fmt.Errorf("config: context %q not found", name)
	}
	f.CurrentContext = name
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path := DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("MACKER_NODE"); v != "" {
		cfg.Node = v
	}
	if v := os.Getenv("MACKER_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("MACKER_TAILSCALE_BIN"); v != "" {
		cfg.TailscaleBin = v
	}
	if v := os.Getenv("MACKER_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("MACKER_COLLECTOR"); v != "" {
		cfg.Collector = v
	}
}

// LoadOrCreateToken returns the local auth token, generating and persisting a
// new random one (0600) if none exists. Called by the agent at startup.
//
// Creation is race-safe: it writes with O_CREATE|O_EXCL, so if two agents
// start at once exactly one wins and the loser re-reads the winner's token
// rather than clobbering it (which would otherwise desync a running daemon).
func LoadOrCreateToken(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("config: mkdir for token: %w", err)
	}
	for {
		if b, err := os.ReadFile(path); err == nil {
			if tok := strings.TrimSpace(string(b)); tok != "" {
				return tok, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("config: read token: %w", err)
		}

		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", fmt.Errorf("config: generate token: %w", err)
		}
		tok := hex.EncodeToString(raw[:])

		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue // another agent created it first; loop to read it
			}
			return "", fmt.Errorf("config: create token: %w", err)
		}
		if _, err := f.WriteString(tok + "\n"); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("config: write token: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("config: close token: %w", err)
		}
		return tok, nil
	}
}

// LoadToken reads the local auth token. It returns an empty string (no error)
// if the token does not exist, so a client can simply omit it.
func LoadToken(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// candidateTailscalePaths lists where the tailscale CLI commonly lives,
// including the macOS standalone/App Store app bundle.
var candidateTailscalePaths = []string{
	"/opt/homebrew/bin/tailscale",
	"/usr/local/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	"/usr/bin/tailscale",
}

// ResolveTailscaleBin finds the tailscale CLI, preferring $PATH and falling
// back to well-known locations. Returns "" if none is found.
func ResolveTailscaleBin() string {
	if v := os.Getenv("MACKER_TAILSCALE_BIN"); v != "" {
		return v
	}
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	for _, p := range candidateTailscalePaths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
