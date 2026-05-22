package cli

import (
	"os"
	"testing"
)

func TestApplyGlobalFlags(t *testing.T) {
	t.Setenv("MACKER_CONTEXT", "")
	args, err := applyGlobalFlags([]string{"macker", "--context", "work", "ls"})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MACKER_CONTEXT") != "work" {
		t.Errorf("MACKER_CONTEXT = %q, want work", os.Getenv("MACKER_CONTEXT"))
	}
	if len(args) != 2 || args[1] != "ls" {
		t.Errorf("args = %v, want [macker ls]", args)
	}

	t.Setenv("MACKER_CONTEXT", "")
	args, err = applyGlobalFlags([]string{"macker", "--context=home", "ls", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MACKER_CONTEXT") != "home" {
		t.Errorf("MACKER_CONTEXT = %q, want home", os.Getenv("MACKER_CONTEXT"))
	}
	if len(args) != 3 {
		t.Errorf("args = %v, want 3 elements", args)
	}

	if _, err := applyGlobalFlags([]string{"macker", "--context"}); err == nil {
		t.Error("expected error for --context without value")
	}

	// A --context AFTER the subcommand / passthrough must NOT be consumed.
	t.Setenv("MACKER_CONTEXT", "")
	args, err = applyGlobalFlags([]string{"macker", "exec", "n1", "--", "tool", "--context", "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MACKER_CONTEXT") != "" {
		t.Errorf("MACKER_CONTEXT was wrongly set to %q from passthrough args", os.Getenv("MACKER_CONTEXT"))
	}
	want := []string{"macker", "exec", "n1", "--", "tool", "--context", "foo"}
	if len(args) != len(want) {
		t.Fatalf("passthrough args mangled: %v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestValidLayouts(t *testing.T) {
	for _, l := range []string{"tiled", "even-horizontal", "main-vertical"} {
		if !validLayouts[l] {
			t.Errorf("%q should be valid", l)
		}
	}
	if validLayouts["bogus"] {
		t.Error("bogus layout should be invalid")
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		in       string
		wantNode string
		wantSess string
	}{
		{"mac-mini", "mac-mini", ""},
		{"mac-mini:main", "mac-mini", "main"},
		{":main", "", "main"},
		{"local", "local", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		got := parseTarget(tt.in)
		if got.node != tt.wantNode || got.session != tt.wantSess {
			t.Errorf("parseTarget(%q) = {%q,%q}, want {%q,%q}", tt.in, got.node, got.session, tt.wantNode, tt.wantSess)
		}
	}
}

func TestIsLocal(t *testing.T) {
	local := []string{"", "self", "local", "SELF", "Local"}
	for _, n := range local {
		if !(target{node: n}).isLocal() {
			t.Errorf("isLocal(%q) = false, want true", n)
		}
	}
	if (target{node: "mac-mini"}).isLocal() {
		t.Error("isLocal(mac-mini) = true, want false")
	}
}

func TestPortFromListen(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{":4477", 4477},
		{"0.0.0.0:5000", 5000},
		{"", 4477},
		{"garbage", 4477},
		{":0", 4477},
	}
	for _, tt := range tests {
		if got := portFromListen(tt.in); got != tt.want {
			t.Errorf("portFromListen(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"/bin/macker", "attach", "mac mini:my'sess"})
	want := `'/bin/macker' 'attach' 'mac mini:my'\''sess'`
	if got != want {
		t.Errorf("shellJoin =\n  %s\nwant\n  %s", got, want)
	}
}

func TestIsAutoSession(t *testing.T) {
	auto := []string{"s-00000000", "s-deadbeef", "s-ff5521da"}
	for _, n := range auto {
		if !isAutoSession(n) {
			t.Errorf("isAutoSession(%q) = false, want true", n)
		}
	}
	named := []string{"main", "work", "s-", "s-123", "s-1234567890", "S-deadbeef", "s-deadbeeg", "session"}
	for _, n := range named {
		if isAutoSession(n) {
			t.Errorf("isAutoSession(%q) = true, want false", n)
		}
	}
}
