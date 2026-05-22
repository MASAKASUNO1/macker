package attach

import (
	"context"
	"strings"
	"testing"
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
