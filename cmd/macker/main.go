// Command macker is the CLI and node daemon for managing AI-agent/dev sessions
// across a Tailscale tailnet. See DESIGN.md for the architecture.
package main

import (
	"os"

	"github.com/masakasuno1/macker/internal/agent"
	"github.com/masakasuno1/macker/internal/cli"
	"github.com/masakasuno1/macker/internal/collector"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	cli.Version = version
	agent.Version = version
	collector.Version = version
	os.Exit(cli.Main(os.Args))
}
