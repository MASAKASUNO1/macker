package cli

import (
	"os"
	"strings"
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

func TestRewriteBareAttachArgs(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		rest     []string
		wantTgt  string
		wantArgs []string
	}{
		{"bare node", "mac-mini", nil, "mac-mini", nil},
		{"colon target only", "mac-mini:dev", nil, "mac-mini:dev", nil},
		{"space-form session", "mac-mini", []string{"dev"}, "mac-mini:dev", nil},
		{"space-form index", "mac-mini", []string{"0"}, "mac-mini:0", nil},
		{"keep flag only", "mac-mini", []string{"--keep"}, "mac-mini", []string{"--keep"}},
		{"flag then session", "mac-mini", []string{"--keep", "dev"}, "mac-mini:dev", []string{"--keep"}},
		{"session then flag", "mac-mini", []string{"dev", "--keep"}, "mac-mini:dev", []string{"--keep"}},
		{"colon target with flag", "mac-mini:dev", []string{"--keep"}, "mac-mini:dev", []string{"--keep"}},
		{"colon target ignores extra", "mac-mini:dev", []string{"other"}, "mac-mini:dev", []string{"other"}},
		{"extra non-flag passes through", "mac-mini", []string{"dev", "stray"}, "mac-mini:dev", []string{"stray"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTgt, gotArgs := rewriteBareAttachArgs(tt.cmd, tt.rest)
			if gotTgt != tt.wantTgt {
				t.Errorf("target = %q, want %q", gotTgt, tt.wantTgt)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("flags = %v, want %v", gotArgs, tt.wantArgs)
			} else {
				for i := range gotArgs {
					if gotArgs[i] != tt.wantArgs[i] {
						t.Errorf("flag %d = %q, want %q", i, gotArgs[i], tt.wantArgs[i])
					}
				}
			}
		})
	}
}

func TestIsIndexLiteral(t *testing.T) {
	yes := []string{"0", "1", "12", "9999", "999999"}
	for _, s := range yes {
		if !isIndexLiteral(s) {
			t.Errorf("isIndexLiteral(%q) = false, want true", s)
		}
	}
	// Excluded: non-digits, leading zeros (real names like "007"), and
	// over-long inputs that would silently overflow strconv.Atoi.
	no := []string{
		"", "dev", "s-deadbeef", "0a", "-1", " 0", "0 ", "1.0",
		"007", "00", "0123",
		"1234567", "99999999999999999999",
	}
	for _, s := range no {
		if isIndexLiteral(s) {
			t.Errorf("isIndexLiteral(%q) = true, want false", s)
		}
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

func TestZshCompletionScript(t *testing.T) {
	for _, want := range []string{"#compdef macker", "_macker()", "compdef _macker macker", "__complete nodes", "__complete sessions"} {
		if !strings.Contains(zshCompletionScript, want) {
			t.Errorf("zsh completion script missing %q", want)
		}
	}
	if len(completionSubcommands) == 0 {
		t.Error("completionSubcommands is empty")
	}
}
