package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/masakasuno1/macker/internal/api"
	"github.com/masakasuno1/macker/internal/eventlog"
)

// Shipper tails the local event log and forwards new events to a collector.
// It is the agent side of collector mirroring (DESIGN.md §2): the local log
// stays the source of truth, so if the collector is unreachable the shipper
// simply retries later — no events are lost and nothing blocks.
type Shipper struct {
	collectorURL string
	logPath      string
	cursorPath   string
	localToken   string // sent only to loopback collectors
	interval     time.Duration
	batchSize    int
	hc           *http.Client
}

// NewShipper builds a shipper. collectorURL is a base URL like
// "http://collector.tailnet.ts.net:4478".
func NewShipper(collectorURL, logPath, cursorPath, localToken string) *Shipper {
	return &Shipper{
		collectorURL: strings.TrimRight(collectorURL, "/"),
		logPath:      logPath,
		cursorPath:   cursorPath,
		localToken:   localToken,
		interval:     5 * time.Second,
		batchSize:    500,
		hc:           &http.Client{Timeout: 20 * time.Second},
	}
}

// Run ships in a loop until ctx is cancelled.
func (sh *Shipper) Run(ctx context.Context) {
	log.Printf("macker agent: shipping events to collector %s", sh.collectorURL)
	t := time.NewTicker(sh.interval)
	defer t.Stop()
	for {
		if err := sh.shipOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("macker agent: ship failed (will retry): %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// shipOnce ships all events after the persisted cursor, advancing it only
// after the collector confirms receipt.
func (sh *Shipper) shipOnce(ctx context.Context) error {
	cursor := sh.readCursor()
	events, err := eventlog.ReadSince(sh.logPath, cursor)
	if err != nil {
		return fmt.Errorf("read log: %w", err)
	}
	for len(events) > 0 {
		batch := events
		if len(batch) > sh.batchSize {
			batch = batch[:sh.batchSize]
		}
		newCursor, err := sh.post(ctx, batch)
		if err != nil {
			return err
		}
		if newCursor == "" {
			newCursor = batch[len(batch)-1].ID
		}
		if err := sh.writeCursor(newCursor); err != nil {
			return fmt.Errorf("write cursor: %w", err)
		}
		events = events[len(batch):]
	}
	return nil
}

func (sh *Shipper) post(ctx context.Context, batch []eventlog.Event) (string, error) {
	body, err := json.Marshal(api.CollectRequest{Events: batch})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sh.collectorURL+"/"+api.Version+"/collect", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if sh.localToken != "" && isLoopbackURL(sh.collectorURL) {
		req.Header.Set("Authorization", "Bearer "+sh.localToken)
	}
	resp, err := sh.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return "", fmt.Errorf("collector returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var cr api.CollectResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cr); err != nil {
		return "", err
	}
	return cr.Cursor, nil
}

func (sh *Shipper) readCursor() string {
	b, err := os.ReadFile(sh.cursorPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (sh *Shipper) writeCursor(id string) error {
	if err := os.MkdirAll(filepath.Dir(sh.cursorPath), 0o700); err != nil {
		return err
	}
	tmp := sh.cursorPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, sh.cursorPath) // atomic cursor update
}

// isLoopbackURL reports whether the URL's host is loopback. It parses the host
// properly (rather than substring matching) so the local token is never sent
// to a remote host like "http://localhost.attacker.example".
func isLoopbackURL(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
