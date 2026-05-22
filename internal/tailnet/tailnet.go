// Package tailnet wraps the tailscale CLI to provide device discovery and
// peer identity. macker deliberately delegates membership, transport
// encryption (WireGuard), and identity to Tailscale rather than reimplementing
// any of it (see DESIGN.md §1, §3).
package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// ErrNoTailscale indicates the tailscale CLI could not be located.
var ErrNoTailscale = errors.New("tailnet: tailscale CLI not found (set MACKER_TAILSCALE_BIN or install Tailscale)")

// Client runs the tailscale CLI.
type Client struct {
	bin string
}

// New returns a Client using the given tailscale binary path.
func New(bin string) *Client { return &Client{bin: bin} }

// Available reports whether a tailscale binary path is configured.
func (c *Client) Available() bool { return c.bin != "" }

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.bin == "" {
		return nil, ErrNoTailscale
	}
	cmd := exec.CommandContext(ctx, c.bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg != "" {
			return nil, fmt.Errorf("tailnet: %s %s: %v: %s", c.bin, strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("tailnet: %s %s: %w", c.bin, strings.Join(args, " "), err)
	}
	return out.Bytes(), nil
}

// Node is a device on the tailnet, normalized from a tailscale status peer.
type Node struct {
	Name    string   // short, stable name (ComputedName/HostName)
	DNSName string   // MagicDNS name (may be empty)
	OS      string   // reported OS
	IPs     []string // Tailscale IPs
	Online  bool     // currently reachable per the control plane
	Self    bool     // true for the local node
}

// Addr returns the best address to reach the node: its MagicDNS name if
// present, otherwise its first Tailscale IP.
func (n Node) Addr() string {
	if n.DNSName != "" {
		return strings.TrimSuffix(n.DNSName, ".")
	}
	if len(n.IPs) > 0 {
		return n.IPs[0]
	}
	return ""
}

// rawPeer mirrors the subset of `tailscale status --json` we consume.
type rawPeer struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	OS           string   `json:"OS"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
}

type rawStatus struct {
	Self           *rawPeer           `json:"Self"`
	Peer           map[string]rawPeer `json:"Peer"`
	MagicDNSSuffix string             `json:"MagicDNSSuffix"`
	CurrentTailnet *struct {
		Name           string `json:"Name"`
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
	} `json:"CurrentTailnet"`
}

func (p rawPeer) toNode(self bool) Node {
	name := p.HostName
	if d := strings.TrimSuffix(p.DNSName, "."); d != "" {
		// Use the leftmost MagicDNS label as the friendly name if present.
		if i := strings.IndexByte(d, '.'); i > 0 {
			name = d[:i]
		}
	}
	return Node{
		Name:    name,
		DNSName: p.DNSName,
		OS:      p.OS,
		IPs:     p.TailscaleIPs,
		Online:  self || p.Online, // Self is always "online" from its own view
		Self:    self,
	}
}

// Status returns all tailnet nodes, including self.
func (c *Client) Status(ctx context.Context) ([]Node, error) {
	rs, err := c.status(ctx)
	if err != nil {
		return nil, err
	}
	var nodes []Node
	if rs.Self != nil {
		nodes = append(nodes, rs.Self.toNode(true))
	}
	for _, p := range rs.Peer {
		nodes = append(nodes, p.toNode(false))
	}
	return nodes, nil
}

func (c *Client) status(ctx context.Context) (*rawStatus, error) {
	b, err := c.run(ctx, "status", "--json")
	if err != nil {
		return nil, err
	}
	var rs rawStatus
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("tailnet: parse status: %w", err)
	}
	return &rs, nil
}

// Tenant returns a stable identifier for the current tailnet, used to
// partition events by tenant. It prefers CurrentTailnet.Name, then the
// MagicDNS suffix. Returns "" if it cannot be determined.
func (c *Client) Tenant(ctx context.Context) (string, error) {
	rs, err := c.status(ctx)
	if err != nil {
		return "", err
	}
	if rs.CurrentTailnet != nil && rs.CurrentTailnet.Name != "" {
		return rs.CurrentTailnet.Name, nil
	}
	if rs.CurrentTailnet != nil && rs.CurrentTailnet.MagicDNSSuffix != "" {
		return rs.CurrentTailnet.MagicDNSSuffix, nil
	}
	if rs.MagicDNSSuffix != "" {
		return rs.MagicDNSSuffix, nil
	}
	return "", nil
}

// Identity is the resolved identity of a peer making a request.
type Identity struct {
	Login    string // tailnet login, e.g. "alice@example.com"
	NodeName string // peer node name
}

type rawWhois struct {
	Node *struct {
		Name         string `json:"Name"`
		ComputedName string `json:"ComputedName"`
	} `json:"Node"`
	UserProfile *struct {
		LoginName   string `json:"LoginName"`
		DisplayName string `json:"DisplayName"`
	} `json:"UserProfile"`
}

// WhoIs resolves the identity behind a remote address (host:port). This is the
// authorization primitive for the agent: it asks the local tailscaled who owns
// the connecting IP, which cannot be spoofed by a peer.
func (c *Client) WhoIs(ctx context.Context, remoteAddr string) (*Identity, error) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// Maybe it's already a bare IP.
		host = remoteAddr
	}
	b, err := c.run(ctx, "whois", "--json", host)
	if err != nil {
		return nil, err
	}
	var rw rawWhois
	if err := json.Unmarshal(b, &rw); err != nil {
		return nil, fmt.Errorf("tailnet: parse whois: %w", err)
	}
	id := &Identity{}
	if rw.UserProfile != nil {
		id.Login = rw.UserProfile.LoginName
	}
	if rw.Node != nil {
		id.NodeName = rw.Node.ComputedName
		if id.NodeName == "" {
			id.NodeName = rw.Node.Name
		}
	}
	if id.Login == "" && id.NodeName == "" {
		return nil, errors.New("tailnet: whois returned no identity")
	}
	return id, nil
}
