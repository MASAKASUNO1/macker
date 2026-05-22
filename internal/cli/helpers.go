package cli

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/masakasuno1/macker/internal/client"
	"github.com/masakasuno1/macker/internal/config"
)

// signalContext returns a context cancelled on SIGINT. It deliberately does
// NOT trap SIGTERM/SIGHUP: those are the "intentional close" signals that the
// attach loop handles itself, and trapping them here would race with it.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// newClient builds an agent client using the port from the local config. It
// loads the local loopback token if present; the client only ever sends it to
// loopback hosts, so it is never leaked to remote nodes.
func newClient(cfg config.Config) *client.Client {
	c := client.New(portFromListen(cfg.Listen))
	if tok := config.LoadToken(cfg.TokenPath()); tok != "" {
		c.SetLocalToken(tok)
	}
	return c
}

// portFromListen extracts the TCP port from a listen address like ":4477" or
// "0.0.0.0:4477", falling back to the default.
func portFromListen(listen string) int {
	if listen == "" {
		return config.DefaultPort
	}
	i := strings.LastIndexByte(listen, ':')
	if i < 0 {
		return config.DefaultPort
	}
	p, err := strconv.Atoi(listen[i+1:])
	if err != nil || p <= 0 {
		return config.DefaultPort
	}
	return p
}

// shellJoin renders argv as a POSIX shell command line, single-quoting each
// argument so it is passed through tmux/sh literally and safely.
func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " ")
}
