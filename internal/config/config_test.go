package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points config at a temp config file and state dir.
func isolate(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.json")
	stateDir = filepath.Join(dir, "state")
	t.Setenv("MACKER_CONFIG", cfgPath)
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("MACKER_DATA_DIR", "")
	t.Setenv("MACKER_CONTEXT", "")
	t.Setenv("MACKER_COLLECTOR", "")
	return cfgPath, stateDir
}

func TestLoadDefaultsNoFile(t *testing.T) {
	isolate(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Context != DefaultContextName {
		t.Errorf("context = %q, want %q", cfg.Context, DefaultContextName)
	}
	if cfg.Node == "" {
		t.Error("node should default to hostname")
	}
}

func TestLoadLegacyTopLevel(t *testing.T) {
	cfgPath, _ := isolate(t)
	os.WriteFile(cfgPath, []byte(`{"node":"legacy","collector":"http://c:4478"}`), 0o600)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Node != "legacy" || cfg.Collector != "http://c:4478" {
		t.Errorf("legacy fields not applied: %+v", cfg)
	}
}

func TestLoadContextsAndIsolation(t *testing.T) {
	cfgPath, stateDir := isolate(t)
	os.WriteFile(cfgPath, []byte(`{
      "current_context":"work",
      "contexts":{
        "work":{"node":"work-node","collector":"http://w:4478"},
        "home":{"node":"home-node"}
      }
    }`), 0o600)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Context != "work" || cfg.Node != "work-node" {
		t.Fatalf("wrong context resolved: %+v", cfg)
	}
	// Non-default context must isolate its state dir.
	wantDir := filepath.Join(stateDir, "macker", "contexts", "work")
	if cfg.DataDir != wantDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, wantDir)
	}
}

func TestMackerContextEnvOverrides(t *testing.T) {
	cfgPath, _ := isolate(t)
	os.WriteFile(cfgPath, []byte(`{
      "current_context":"work",
      "contexts":{"work":{"node":"w"},"home":{"node":"h"}}
    }`), 0o600)
	t.Setenv("MACKER_CONTEXT", "home")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Context != "home" || cfg.Node != "h" {
		t.Fatalf("env did not override context: %+v", cfg)
	}
}

func TestLoadUnknownContextErrors(t *testing.T) {
	cfgPath, _ := isolate(t)
	os.WriteFile(cfgPath, []byte(`{"contexts":{"work":{"node":"w"}}}`), 0o600)
	t.Setenv("MACKER_CONTEXT", "nope")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for unknown context")
	}
}

func TestSetCurrentContextRoundTrip(t *testing.T) {
	cfgPath, _ := isolate(t)
	os.WriteFile(cfgPath, []byte(`{"contexts":{"work":{"node":"w"},"home":{"node":"h"}}}`), 0o600)

	if err := SetCurrentContext("home"); err != nil {
		t.Fatal(err)
	}
	names, current, err := Contexts()
	if err != nil {
		t.Fatal(err)
	}
	if current != "home" {
		t.Errorf("current = %q, want home", current)
	}
	if len(names) != 2 {
		t.Errorf("names = %v, want 2", names)
	}
	if err := SetCurrentContext("missing"); err == nil {
		t.Error("expected error for missing context")
	}
}

func TestCollectorAndCursorPaths(t *testing.T) {
	c := Config{DataDir: "/x/y"}
	if !strings.HasSuffix(c.CollectorDir(), filepath.Join("y", "collector")) {
		t.Errorf("CollectorDir = %q", c.CollectorDir())
	}
	if !strings.HasSuffix(c.ShipperCursorPath(), "shipper.cursor") {
		t.Errorf("ShipperCursorPath = %q", c.ShipperCursorPath())
	}
}
