// Package cfapi is a thin Cloudflare API v4 client covering exactly what the
// one-click Argo tunnel flow needs: verify a token, resolve a domain's
// account/zone, list/create remotely-managed tunnels, fetch a tunnel's run
// token, push its ingress config, and upsert the DNS CNAME that points the
// hostname at the tunnel.
//
// It is deliberately hand-rolled (net/http + encoding/json) rather than pulling
// cloudflare-go: the surface is six calls, and the panel already keeps its
// dependency footprint tight. DISCIPLINE: control plane only.
package cfapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

// Client talks to the Cloudflare API with a bearer token.
type Client struct {
	token string
	http  *http.Client
}

// New returns a client authenticated with the given API token.
func New(token string) *Client {
	return &Client{
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

// envelope is the standard Cloudflare API response wrapper.
type envelope struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e cfError) String() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }

// APIError carries Cloudflare's own error code + HTTP status so callers can
// distinguish a permission/scope problem (the reused DNS-01 cert token can't
// touch account-level tunnel resources) from a transient or input error and
// guide the operator accordingly. Its Error() keeps the historical
// "cloudflare: <code> <message>" shape so existing string handling is unchanged.
type APIError struct {
	Code       int
	Message    string
	HTTPStatus int
}

func (e *APIError) Error() string { return fmt.Sprintf("cloudflare: %d %s", e.Code, e.Message) }

// IsAuthError reports whether err is a Cloudflare auth/permission failure —
// code 10000 ("Authentication error"), 9109 ("Unauthorized to access requested
// resource"), or an HTTP 401/403. This is the signature of a token that lacks
// the scope a call needs (e.g. a DNS-only token used to create a tunnel).
func IsAuthError(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code == 10000 || ae.Code == 9109 || ae.HTTPStatus == 401 || ae.HTTPStatus == 403
	}
	return false
}

// do performs a request and unmarshals the `result` field into out. A non-2xx
// status or success=false surfaces the first Cloudflare error message.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("cloudflare: decode response (HTTP %d): %w", resp.StatusCode, err)
	}
	if !env.Success {
		if len(env.Errors) > 0 {
			return &APIError{
				Code:       env.Errors[0].Code,
				Message:    env.Errors[0].Message,
				HTTPStatus: resp.StatusCode,
			}
		}
		return fmt.Errorf("cloudflare: request failed (HTTP %d)", resp.StatusCode)
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// VerifyToken checks the token is live (and thus that the operator pasted it
// correctly) before any mutating call.
func (c *Client) VerifyToken(ctx context.Context) error {
	if c.token == "" {
		return fmt.Errorf("empty token")
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, &res); err != nil {
		return err
	}
	if res.Status != "active" {
		return fmt.Errorf("token status is %q (expected active)", res.Status)
	}
	return nil
}

// Zone is a Cloudflare DNS zone plus its owning account.
type Zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

// ZoneForDomain finds the zone whose name is the longest suffix of domain (so
// "a.b.example.com" resolves to the "example.com" zone). Returns the zone with
// its account id — both are needed for the tunnel + DNS calls and are derived
// from the token, never asked of the operator.
func (c *Client) ZoneForDomain(ctx context.Context, domain string) (*Zone, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, fmt.Errorf("empty domain")
	}
	zones, err := c.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	var best *Zone
	for i := range zones {
		z := &zones[i]
		name := strings.ToLower(z.Name)
		if domain == name || strings.HasSuffix(domain, "."+name) {
			if best == nil || len(name) > len(best.Name) {
				best = z
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no Cloudflare zone found for %q — is the domain on this account?", domain)
	}
	return best, nil
}

// ListZones returns every zone the token can read (Zone:Read), each with its
// owning account. The one-click flow uses this to offer the operator a domain
// to hang the tunnel under, instead of asking them to retype it. A DNS-only
// token (the reused DNS-01 cert token) can still read its zones, so this works
// before we know whether the token also has tunnel permission.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	if err := c.do(ctx, http.MethodGet, "/zones?per_page=50", nil, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

// Tunnel is a remotely-managed (config_src=cloudflare) cloudflared tunnel.
type Tunnel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Deleted string `json:"deleted_at"`
}

// ListTunnels returns the account's non-deleted tunnels so the operator can
// reuse an existing one instead of creating a duplicate.
func (c *Client) ListTunnels(ctx context.Context, accountID string) ([]Tunnel, error) {
	var ts []Tunnel
	if err := c.do(ctx, http.MethodGet,
		"/accounts/"+accountID+"/cfd_tunnel?is_deleted=false&per_page=100", nil, &ts); err != nil {
		return nil, err
	}
	return ts, nil
}

// CreateTunnel makes a new remotely-managed tunnel and returns it along with the
// run token (the `cloudflared tunnel run --token` credential).
func (c *Client) CreateTunnel(ctx context.Context, accountID, name string) (*Tunnel, string, error) {
	var res struct {
		Tunnel
		Token string `json:"token"`
	}
	body := map[string]any{"name": name, "config_src": "cloudflare"}
	if err := c.do(ctx, http.MethodPost, "/accounts/"+accountID+"/cfd_tunnel", body, &res); err != nil {
		return nil, "", err
	}
	token := res.Token
	if token == "" {
		// Some API versions don't echo the token on create; fetch it explicitly.
		t, err := c.TunnelToken(ctx, accountID, res.ID)
		if err != nil {
			return nil, "", err
		}
		token = t
	}
	return &res.Tunnel, token, nil
}

// TunnelToken fetches the run token for an existing tunnel.
func (c *Client) TunnelToken(ctx context.Context, accountID, tunnelID string) (string, error) {
	var token string
	if err := c.do(ctx, http.MethodGet,
		"/accounts/"+accountID+"/cfd_tunnel/"+tunnelID+"/token", nil, &token); err != nil {
		return "", err
	}
	return token, nil
}

// SetIngress pushes the tunnel's ingress config: route hostname → service
// (e.g. http://localhost:2083), with a catch-all 404 as the required final
// rule. This is what makes a config_src=cloudflare tunnel actually forward.
func (c *Client) SetIngress(ctx context.Context, accountID, tunnelID, hostname, service string) error {
	body := map[string]any{
		"config": map[string]any{
			"ingress": []map[string]any{
				{"hostname": hostname, "service": service},
				{"service": "http_status:404"},
			},
		},
	}
	return c.do(ctx, http.MethodPut,
		"/accounts/"+accountID+"/cfd_tunnel/"+tunnelID+"/configurations", body, nil)
}

// dnsRecord is the subset of a DNS record we read/write.
type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// Record is the exported view of a DNS record for conflict checks.
type Record struct {
	Type    string
	Name    string
	Content string
	Proxied bool // true = orange-cloud (proxied through Cloudflare)
}

// LookupRecords returns the DNS records on a zone matching the exact name. Used
// to detect a hostname already wired for direct/CDN (an A/AAAA record) before
// the tunnel flow would overwrite it with a CNAME.
func (c *Client) LookupRecords(ctx context.Context, zoneID, name string) ([]Record, error) {
	var recs []dnsRecord
	if err := c.do(ctx, http.MethodGet,
		"/zones/"+zoneID+"/dns_records?name="+name, nil, &recs); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(recs))
	for _, r := range recs {
		out = append(out, Record{Type: r.Type, Name: r.Name, Content: r.Content, Proxied: r.Proxied})
	}
	return out, nil
}

// TunnelHostnames returns the public hostnames a tunnel routes (its ingress
// rule hostnames), lowercased. Used to hide tunnels that already serve a domain
// claimed for CDN from the reuse picker. A tunnel with no remote config yields
// an empty list.
func (c *Client) TunnelHostnames(ctx context.Context, accountID, tunnelID string) ([]string, error) {
	var res struct {
		Config struct {
			Ingress []struct {
				Hostname string `json:"hostname"`
			} `json:"ingress"`
		} `json:"config"`
	}
	if err := c.do(ctx, http.MethodGet,
		"/accounts/"+accountID+"/cfd_tunnel/"+tunnelID+"/configurations", nil, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, ing := range res.Config.Ingress {
		if h := strings.ToLower(strings.TrimSpace(ing.Hostname)); h != "" {
			out = append(out, h)
		}
	}
	return out, nil
}

// UpsertTunnelCNAME points hostname at <tunnelID>.cfargotunnel.com via a proxied
// CNAME, creating or updating the record so re-running the flow is idempotent.
func (c *Client) UpsertTunnelCNAME(ctx context.Context, zoneID, hostname, tunnelID string) error {
	content := tunnelID + ".cfargotunnel.com"
	// Look for an existing record on this exact name.
	var existing []dnsRecord
	if err := c.do(ctx, http.MethodGet,
		"/zones/"+zoneID+"/dns_records?name="+hostname, nil, &existing); err != nil {
		return err
	}
	body := map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": content,
		"proxied": true,
		"ttl":     1, // 1 = automatic
	}
	for _, r := range existing {
		if strings.EqualFold(r.Name, hostname) {
			return c.do(ctx, http.MethodPut,
				"/zones/"+zoneID+"/dns_records/"+r.ID, body, nil)
		}
	}
	return c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body, nil)
}
