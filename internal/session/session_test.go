package session

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// New is idempotent: creating a session that already exists must not error
// (tmux's "duplicate session" is tolerated). Skipped when tmux is unavailable.
func TestNewIdempotent(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Isolate from the user's tmux server via a throwaway socket dir. It lives
	// under /tmp (not t.TempDir()) to keep the unix socket path under the macOS
	// ~104-char limit.
	dir, err := os.MkdirTemp("/tmp", "mk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMUX_TMPDIR", dir)

	m := Manager{}
	ctx := context.Background()
	const name = "mackertest"
	t.Cleanup(func() { _ = m.Kill(ctx, name) })

	if err := m.New(ctx, name, ""); err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := m.New(ctx, name, ""); err != nil {
		t.Fatalf("second New (duplicate) should be tolerated, got: %v", err)
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"main", "macker_grid", "dev-1", "ABC123"}
	for _, n := range valid {
		if !validName(n) {
			t.Errorf("validName(%q) = false, want true", n)
		}
	}
	invalid := []string{"", "-flag", "has space", "has:colon", "has.dot", "rm;ls", "name$x"}
	for _, n := range invalid {
		if validName(n) {
			t.Errorf("validName(%q) = true, want false", n)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name  string
		panes []paneInfo
		want  Kind
	}{
		{"claude by command", []paneInfo{{command: "claude"}}, KindClaude},
		{"claude by title", []paneInfo{{command: "node", title: "claude code"}}, KindClaude},
		{"shell", []paneInfo{{command: "zsh"}}, KindShell},
		{"unknown program", []paneInfo{{command: "vim"}}, KindUnknown},
		{"empty", nil, KindUnknown},
	}
	for _, tt := range tests {
		got, _ := classify(tt.panes)
		if got != tt.want {
			t.Errorf("%s: classify = %q, want %q", tt.name, got, tt.want)
		}
	}
}
