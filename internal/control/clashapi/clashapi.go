// Package clashapi is a minimal read-only client for sing-box's clash_api
// controller. The panel's live domain-capture polls GET /connections to learn
// which domains a client's real traffic reached (the sniffed Host per active
// connection) — the only reliable way to capture the domains a service uses
// after login/playback, which a headless page visit can't see.
package clashapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Conn is one active connection's routing-relevant metadata. Upload/Download are
// the connection's cumulative byte counters — the live-capture's relevance
// signal: domains the session actually moved data through (vs telemetry pings
// that barely transfer anything).
type Conn struct {
	ID       string // clash connection id (stable per connection)
	Host     string // sniffed domain (SNI/HTTP host); empty when not sniffed
	SourceIP string // the client that opened the connection
	DestIP   string
	DestPort string
	Network  string // tcp | udp
	Upload   int64
	Download int64
}

// Client talks to a clash_api controller (loopback) with a bearer secret.
type Client struct {
	base   string
	secret string
	hc     *http.Client
}

// New builds a client for controller "host:port" and its secret.
func New(controller, secret string) *Client {
	return &Client{
		base:   "http://" + controller,
		secret: secret,
		hc:     &http.Client{Timeout: 5 * time.Second},
	}
}

// Connections returns the controller's current active-connection snapshot.
func (c *Client) Connections(ctx context.Context) ([]Conn, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/connections", nil)
	if err != nil {
		return nil, err
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clash_api /connections: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, err
	}
	return parseConnections(body)
}

// parseConnections extracts Conns from a clash /connections payload. Split out
// for testing against captured fixtures.
func parseConnections(body []byte) ([]Conn, error) {
	var raw struct {
		Connections []struct {
			ID       string `json:"id"`
			Upload   int64  `json:"upload"`
			Download int64  `json:"download"`
			Metadata struct {
				Host            string `json:"host"`
				SourceIP        string `json:"sourceIP"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
				Network         string `json:"network"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]Conn, 0, len(raw.Connections))
	for _, c := range raw.Connections {
		m := c.Metadata
		out = append(out, Conn{
			ID: c.ID, Host: m.Host, SourceIP: m.SourceIP, DestIP: m.DestinationIP,
			DestPort: m.DestinationPort, Network: m.Network,
			Upload: c.Upload, Download: c.Download,
		})
	}
	return out, nil
}
