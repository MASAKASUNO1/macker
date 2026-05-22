package session

import "testing"

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
