package tailnet

import (
	"encoding/json"
	"testing"
)

const sampleStatus = `{
  "Self": {
    "HostName": "mac-mini",
    "DNSName": "mac-mini.tail1234.ts.net.",
    "OS": "macOS",
    "TailscaleIPs": ["100.64.0.1"],
    "Online": true
  },
  "Peer": {
    "key1": {
      "HostName": "macbook",
      "DNSName": "macbook.tail1234.ts.net.",
      "OS": "macOS",
      "TailscaleIPs": ["100.64.0.2"],
      "Online": false
    }
  }
}`

func TestParseStatusToNode(t *testing.T) {
	var rs rawStatus
	if err := json.Unmarshal([]byte(sampleStatus), &rs); err != nil {
		t.Fatal(err)
	}
	self := rs.Self.toNode(true)
	if self.Name != "mac-mini" {
		t.Errorf("self name = %q, want mac-mini", self.Name)
	}
	if !self.Self || !self.Online {
		t.Errorf("self flags wrong: %+v", self)
	}
	if self.Addr() != "mac-mini.tail1234.ts.net" {
		t.Errorf("self addr = %q", self.Addr())
	}

	peer := rs.Peer["key1"].toNode(false)
	if peer.Name != "macbook" || peer.Online {
		t.Errorf("peer wrong: %+v", peer)
	}
}

func TestNodeAddrFallsBackToIP(t *testing.T) {
	n := Node{IPs: []string{"100.64.0.9"}}
	if n.Addr() != "100.64.0.9" {
		t.Errorf("addr = %q, want IP fallback", n.Addr())
	}
	empty := Node{}
	if empty.Addr() != "" {
		t.Errorf("addr = %q, want empty", empty.Addr())
	}
}

const sampleWhois = `{
  "Node": {"Name": "macbook.tail1234.ts.net.", "ComputedName": "macbook"},
  "UserProfile": {"LoginName": "alice@example.com", "DisplayName": "Alice"}
}`

func TestParseWhois(t *testing.T) {
	var rw rawWhois
	if err := json.Unmarshal([]byte(sampleWhois), &rw); err != nil {
		t.Fatal(err)
	}
	if rw.UserProfile.LoginName != "alice@example.com" {
		t.Errorf("login = %q", rw.UserProfile.LoginName)
	}
	if rw.Node.ComputedName != "macbook" {
		t.Errorf("computed name = %q", rw.Node.ComputedName)
	}
}
