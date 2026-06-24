package warp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// cfRegisterEndpoint is the Cloudflare WARP free-tier registration endpoint.
// The version path segment (v0a2483) is the stable consumer-facing one wgcf
// uses; it has not rotated since 2023. Override via NewClient when needed.
const cfRegisterEndpoint = "https://api.cloudflareclient.com/v0a2483/reg"

// Endpoint is the Cloudflare WARP WireGuard peer endpoint. We hard-code it
// because the registration response does not always include a stable
// hostname; sing-box and xray both prefer the dialable hostname form.
const Endpoint = "engage.cloudflareclient.com:2408"

// Result is the subset of the Cloudflare registration response EdgeNest
// stores. It maps 1:1 to WarpConfig DB fields so the API handler can drop
// it straight into the row.
type Result struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"` // peer (Cloudflare) public key
	Address4   string `json:"address4"`   // e.g. 172.16.0.2/32
	Address6   string `json:"address6"`   // e.g. 2606:4700:110:abcd:.../128
	Reserved   []int  `json:"reserved"`   // 3-byte client ID, decoded from base64
	Endpoint   string `json:"endpoint"`
}

// Client makes the registration HTTP call. Construct via NewClient and reuse
// across requests; the underlying net/http.Client manages connection pooling.
type Client struct {
	endpoint string
	http     *http.Client
}

// NewClient returns a Client with a 10s timeout that hits the live Cloudflare
// registration endpoint. Tests override `endpoint` to point at httptest.
func NewClient() *Client {
	return &Client{
		endpoint: cfRegisterEndpoint,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// WithEndpoint returns a copy of c that POSTs to a different URL. Used in
// tests; never call this in production paths.
func (c *Client) WithEndpoint(endpoint string) *Client {
	return &Client{endpoint: endpoint, http: c.http}
}

type regRequest struct {
	Key          string `json:"key"`
	InstallID    string `json:"install_id"`
	FCMToken     string `json:"fcm_token"`
	Tos          string `json:"tos"`
	Model        string `json:"model"`
	SerialNumber string `json:"serial_number"`
	Locale       string `json:"locale"`
}

type regResponse struct {
	Config struct {
		ClientID string `json:"client_id"` // base64 of the 3-byte reserved field
		Peers    []struct {
			PublicKey string `json:"public_key"`
			Endpoint  struct {
				Host string `json:"host"`
				V4   string `json:"v4"`
				V6   string `json:"v6"`
			} `json:"endpoint"`
		} `json:"peers"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
}

// Register provisions a fresh Cloudflare WARP account, returns the credentials
// ready to be persisted. Generates a WireGuard keypair locally — the private
// key never leaves the panel host.
//
// The Cloudflare endpoint is rate-limited per source IP; expect transient 429s
// during automated tests. Production call sites should retry with backoff.
func (c *Client) Register(ctx context.Context) (Result, error) {
	kp, err := GenerateKeypair()
	if err != nil {
		return Result{}, fmt.Errorf("generate wg keypair: %w", err)
	}

	body := regRequest{
		Key:          kp.PublicKey,
		InstallID:    "",
		FCMToken:     "",
		Tos:          time.Now().UTC().Format(time.RFC3339Nano),
		Model:        "PC",
		SerialNumber: time.Now().Format("20060102150405"),
		Locale:       "en_US",
	}
	raw, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return Result{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	req.Header.Set("CF-Client-Version", "a-6.10-2483")

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("call Cloudflare WARP API: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		// Cloudflare returns JSON error bodies; pass through the first 200
		// chars so the operator can see what was rejected.
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return Result{}, fmt.Errorf("Cloudflare WARP API returned %d: %s", resp.StatusCode, snippet)
	}

	var parsed regResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Result{}, fmt.Errorf("parse response: %w", err)
	}
	if len(parsed.Config.Peers) == 0 {
		return Result{}, fmt.Errorf("Cloudflare response missing peers")
	}

	reserved, err := decodeClientID(parsed.Config.ClientID)
	if err != nil {
		return Result{}, fmt.Errorf("decode client_id: %w", err)
	}

	addr4 := strings.TrimSpace(parsed.Config.Interface.Addresses.V4)
	addr6 := strings.TrimSpace(parsed.Config.Interface.Addresses.V6)
	if addr4 == "" {
		return Result{}, fmt.Errorf("Cloudflare response missing IPv4 address")
	}

	return Result{
		PrivateKey: kp.PrivateKey,
		PublicKey:  parsed.Config.Peers[0].PublicKey,
		Address4:   addr4 + "/32",
		Address6:   appendMask(addr6, "/128"),
		Reserved:   reserved,
		Endpoint:   Endpoint,
	}, nil
}

// decodeClientID converts Cloudflare's base64 client_id (typically 3 bytes)
// into the []int form sing-box / xray write into the "reserved" field.
func decodeClientID(s string) ([]int, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	out := make([]int, len(raw))
	for i, b := range raw {
		out[i] = int(b)
	}
	return out, nil
}

func appendMask(addr, mask string) string {
	if addr == "" {
		return ""
	}
	return addr + mask
}
