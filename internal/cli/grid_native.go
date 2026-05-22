package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// gridWindows opens one native terminal window per target (experimental,
// macOS-only). It auto-detects the terminal from $TERM_PROGRAM and falls back
// to the tmux pane grid when the terminal is unknown or unsupported, so the
// command always does something useful.
func gridWindows(ctx context.Context, targets []string, attachCmd func(string) string) error {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "macker grid: windows mode is macOS-only; falling back to tmux panes")
		return gridTmux(ctx, targets, "tiled", attachCmd)
	}

	term := os.Getenv("TERM_PROGRAM")
	open := windowOpener(term)
	if open == nil {
		fmt.Fprintf(os.Stderr, "macker grid: terminal %q not supported for windows mode; falling back to tmux panes\n", term)
		return gridTmux(ctx, targets, "tiled", attachCmd)
	}

	var firstErr error
	for _, tgt := range targets {
		if err := open(ctx, attachCmd(tgt)); err != nil {
			fmt.Fprintf(os.Stderr, "macker grid: opening window for %q failed: %v\n", tgt, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return fmt.Errorf("grid windows: at least one window failed to open: %w", firstErr)
	}
	fmt.Fprintf(os.Stderr, "macker grid: opened %d window(s) in %s\n", len(targets), term)
	return nil
}

// windowOpener returns a function that opens one window running cmd, or nil if
// the terminal is unsupported. cmd is a POSIX shell command line.
func windowOpener(term string) func(context.Context, string) error {
	switch term {
	case "ghostty":
		return func(ctx context.Context, cmd string) error {
			// Ghostty runs a command with -e; open a new instance per window.
			return exec.CommandContext(ctx, "open", "-na", "Ghostty", "--args", "-e", "/bin/sh", "-lc", cmd).Run()
		}
	case "iTerm.app":
		return func(ctx context.Context, cmd string) error {
			return runAppleScript(ctx, fmt.Sprintf(
				`tell application "iTerm"
  create window with default profile
  tell current session of current window to write text %s
end tell`, appleScriptString(cmd)))
		}
	case "Apple_Terminal":
		return func(ctx context.Context, cmd string) error {
			return runAppleScript(ctx, fmt.Sprintf(`tell application "Terminal" to do script %s`, appleScriptString(cmd)))
		}
	default:
		return nil
	}
}

func runAppleScript(ctx context.Context, script string) error {
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

// appleScriptString quotes a string as an AppleScript literal.
func appleScriptString(s string) string {
	// AppleScript strings use double quotes; escape backslashes and quotes.
	var b []byte
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '"':
			b = append(b, '\\', s[i])
		default:
			b = append(b, s[i])
		}
	}
	b = append(b, '"')
	return string(b)
}
