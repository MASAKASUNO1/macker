// Package client is the transport-only HTTP client the macker CLI uses to talk
// to a node's agent over the tailnet. Address resolution (name -> tailnet
// address) lives in the CLI layer; this package just speaks the protocol.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/masakasuno1/macker/internal/api"
	"github.com/masakasuno1/macker/internal/config"
)

// Client talks to agents. Addresses passed to its methods are bare hosts
// (MagicDNS name or Tailscale IP); the configured port is appended here.
type Client struct {
	hc         *http.Client
	port       int
	localToken string // presented ONLY to loopback hosts (see SetLocalToken)
}

// New returns a Client. If port is zero, the default agent port is used.
func New(port int) *Client {
	if port == 0 {
		port = config.DefaultPort
	}
	return &Client{
		hc:   &http.Client{Timeout: 30 * time.Second},
		port: port,
	}
}

// SetLocalToken sets the loopback auth token. It is sent only when the target
// host is loopback, so the local token is never leaked to remote nodes.
func (c *Client) SetLocalToken(tok string) { c.localToken = tok }

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) base(host string) string {
	return "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", c.port)) + "/" + api.Version
}

// apiError is returned for non-2xx responses, preserving the status code.
type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("agent returned %d: %s", e.Status, e.Msg)
	}
	return fmt.Sprintf("agent returned %d", e.Status)
}

func (c *Client) do(ctx context.Context, method, host, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base(host)+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.localToken != "" && isLoopbackHost(host) {
		req.Header.Set("Authorization", "Bearer "+c.localToken)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var er api.ErrorResponse
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&er)
		return &apiError{Status: resp.StatusCode, Msg: er.Error}
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
	}
	return nil
}

// Health pings a node's agent.
func (c *Client) Health(ctx context.Context, host string) (api.HealthResponse, error) {
	var r api.HealthResponse
	err := c.do(ctx, http.MethodGet, host, "/health", nil, &r)
	return r, err
}

// ListSessions returns the sessions on a node.
func (c *Client) ListSessions(ctx context.Context, host string) (api.SessionsResponse, error) {
	var r api.SessionsResponse
	err := c.do(ctx, http.MethodGet, host, "/sessions", nil, &r)
	return r, err
}

// CreateSession creates a session on a node.
func (c *Client) CreateSession(ctx context.Context, host string, req api.CreateSessionRequest) (api.CreateSessionResponse, error) {
	var r api.CreateSessionResponse
	err := c.do(ctx, http.MethodPost, host, "/sessions", req, &r)
	return r, err
}

// Release performs the intentional-close lifecycle action (kills the session).
func (c *Client) Release(ctx context.Context, host, name string) error {
	return c.do(ctx, http.MethodPost, host, "/sessions/"+url.PathEscape(name)+"/release", nil, nil)
}

// Lease registers or renews the caller's lease (heartbeat) on a session.
func (c *Client) Lease(ctx context.Context, host, name string, req api.LeaseRequest) error {
	return c.do(ctx, http.MethodPost, host, "/sessions/"+url.PathEscape(name)+"/lease", req, nil)
}

// Unlease releases the caller's lease without killing the session (clean detach).
func (c *Client) Unlease(ctx context.Context, host, name, clientID string) error {
	return c.do(ctx, http.MethodPost, host, "/sessions/"+url.PathEscape(name)+"/unlease", api.UnleaseRequest{ClientID: clientID}, nil)
}

// Kill administratively terminates a session.
func (c *Client) Kill(ctx context.Context, host, name string) error {
	return c.do(ctx, http.MethodDelete, host, "/sessions/"+url.PathEscape(name), nil, nil)
}

// Exec runs an authorized command on a node.
func (c *Client) Exec(ctx context.Context, host string, req api.ExecRequest) (api.ExecResponse, error) {
	var r api.ExecResponse
	err := c.do(ctx, http.MethodPost, host, "/exec", req, &r)
	return r, err
}

// IsForbidden reports whether err is an authorization (403) error.
func IsForbidden(err error) bool {
	var ae *apiError
	return AsAPIError(err, &ae) && ae.Status == http.StatusForbidden
}

// AsAPIError unwraps err into an *apiError if possible.
func AsAPIError(err error, target **apiError) bool {
	for err != nil {
		if ae, ok := err.(*apiError); ok {
			*target = ae
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
