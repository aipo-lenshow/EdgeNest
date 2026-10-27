// Package antiblock holds the node-wide anti-blocking posture: which public
// names clients dial instead of raw IPs, where subscriptions are served from,
// the signed list of mirror subscription origins, and the low-profile switch
// that takes every directly-exposed inbound off the wire.
//
// The model mirrors Argo: an inbound marked edge_bound=true is a plaintext
// WebSocket origin on loopback, and the operator's own reverse proxy (Caddy /
// Nginx behind a Cloudflare-proxied domain) terminates TLS in front of it. The
// VPS address then never appears in a client config for that inbound, so an
// IP-level block does not take the node down.
package antiblock

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// Settings keys. All live in the generic key/value table so no migration is
// needed and an older binary simply ignores them.
const (
	KeyEdgeDomain     = "edge_domain"      // Cloudflare-proxied name in front of edge_bound inbounds
	KeyDirectDomain   = "direct_domain"    // DNS-only name (A record) direct inbounds dial instead of the v4 IP
	KeyDirectDomainV6 = "direct_domain_v6" // DNS-only name (AAAA) for inbounds bound to a v6 address
	KeySubBaseURL     = "sub_base_url"     // public origin subscriptions are handed out under
	KeySubMirrors     = "sub_mirrors"      // JSON []string of extra subscription origins
	KeyStealth        = "stealth_mode"     // "true" = only edge/Argo inbounds run and are subscribed
	KeyGatewayMode    = "edge_gateway"     // "" (auto) | "builtin" | "external"
	KeyOriginOpen     = "edge_origin_open" // "true" = built-in gateway accepts non-Cloudflare clients
	keyMirrorsSeq     = "sub_mirrors_seq"
	keyMirrorsSeed    = "sub_mirrors_ed25519_seed"
)

// Config is the decoded anti-blocking posture.
type Config struct {
	EdgeDomain     string   `json:"edge_domain"`
	DirectDomain   string   `json:"direct_domain"`
	DirectDomainV6 string   `json:"direct_domain_v6"`
	SubBaseURL     string   `json:"sub_base_url"`
	Mirrors        []string `json:"sub_mirrors"`
	Stealth        bool     `json:"stealth_mode"`
	// GatewayMode picks who terminates TLS for the edge domain: "" lets the
	// built-in gateway take :443 when it is free, "builtin" insists on it,
	// "external" leaves it to the operator's own reverse proxy.
	GatewayMode string `json:"gateway_mode"`
	// OriginOpen lets the built-in gateway accept clients that aren't
	// Cloudflare (default false: only Cloudflare's edge can reach :443).
	OriginOpen bool `json:"origin_open"`
}

// Gateway modes.
const (
	GatewayAuto     = ""
	GatewayBuiltin  = "builtin"
	GatewayExternal = "external"
)

// Load reads the posture from the settings table. Missing keys decode to the
// zero Config, which reproduces the historical behaviour exactly.
func Load(s *store.Store) Config {
	get := func(k string) string {
		v, _ := s.GetSetting(k)
		return strings.TrimSpace(v)
	}
	c := Config{
		EdgeDomain:     get(KeyEdgeDomain),
		DirectDomain:   get(KeyDirectDomain),
		DirectDomainV6: get(KeyDirectDomainV6),
		SubBaseURL:     get(KeySubBaseURL),
		Stealth:        get(KeyStealth) == "true",
		GatewayMode:    get(KeyGatewayMode),
		OriginOpen:     get(KeyOriginOpen) == "true",
	}
	if raw := get(KeySubMirrors); raw != "" {
		_ = json.Unmarshal([]byte(raw), &c.Mirrors)
	}
	return c
}

// Normalize validates and canonicalises an operator-submitted Config. Domains
// are lower-cased bare hostnames; URLs are https origins (optionally with a
// path prefix) without a trailing slash.
func Normalize(c Config) (Config, error) {
	var err error
	if c.EdgeDomain, err = normHost(c.EdgeDomain, "edge_domain"); err != nil {
		return c, err
	}
	if c.DirectDomain, err = normHost(c.DirectDomain, "direct_domain"); err != nil {
		return c, err
	}
	if c.DirectDomainV6, err = normHost(c.DirectDomainV6, "direct_domain_v6"); err != nil {
		return c, err
	}
	if c.SubBaseURL, err = normBase(c.SubBaseURL, false); err != nil {
		return c, fmt.Errorf("sub_base_url: %w", err)
	}
	var mirrors []string
	seen := map[string]bool{}
	for _, m := range c.Mirrors {
		n, err := normBase(m, true)
		if err != nil {
			return c, fmt.Errorf("sub_mirrors: %w", err)
		}
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		mirrors = append(mirrors, n)
	}
	c.Mirrors = mirrors
	switch c.GatewayMode {
	case GatewayAuto, GatewayBuiltin, GatewayExternal:
	case "auto":
		c.GatewayMode = GatewayAuto
	default:
		return c, fmt.Errorf("gateway_mode must be auto, builtin or external: %q", c.GatewayMode)
	}
	if c.Stealth && c.EdgeDomain == "" {
		return c, errors.New("stealth_mode needs edge_domain: with no edge name every node would go dark")
	}
	return c, nil
}

func normHost(v, field string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.TrimSuffix(v, ".")
	if v == "" {
		return "", nil
	}
	if net.ParseIP(v) != nil {
		return "", fmt.Errorf("%s must be a domain name, not an IP address", field)
	}
	if strings.ContainsAny(v, "/: ") || !strings.Contains(v, ".") {
		return "", fmt.Errorf("%s is not a valid domain name: %q", field, v)
	}
	return v, nil
}

// normBase accepts an https origin with an optional path prefix. httpsOnly is
// enforced for mirrors (they carry the token to a third host); the primary
// base may be plain http for operators still on the IP:port panel URL.
func normBase(v string, httpsOnly bool) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("not an absolute URL: %q", v)
	}
	if httpsOnly && u.Scheme != "https" {
		return "", fmt.Errorf("mirrors must use https: %q", v)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("must not carry a query or fragment: %q", v)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// Save persists c (already Normalize'd). The mirror list's sequence number is
// bumped whenever the list changes so clients accept the new one.
func Save(s *store.Store, c Config) error {
	prev := Load(s)
	mirrors, _ := json.Marshal(c.Mirrors)
	if len(c.Mirrors) == 0 {
		mirrors = nil
	}
	flag := func(b bool) string {
		if b {
			return "true"
		}
		return ""
	}
	for k, v := range map[string]string{
		KeyEdgeDomain:     c.EdgeDomain,
		KeyDirectDomain:   c.DirectDomain,
		KeyDirectDomainV6: c.DirectDomainV6,
		KeySubBaseURL:     c.SubBaseURL,
		KeySubMirrors:     string(mirrors),
		KeyStealth:        flag(c.Stealth),
		KeyGatewayMode:    c.GatewayMode,
		KeyOriginOpen:     flag(c.OriginOpen),
	} {
		if err := s.SetSetting(k, v); err != nil {
			return err
		}
	}
	if strings.Join(prev.Mirrors, "\n") != strings.Join(c.Mirrors, "\n") {
		return s.SetSetting(keyMirrorsSeq, strconv.FormatInt(time.Now().Unix(), 10))
	}
	return nil
}

// Shielded reports whether an inbound sits behind Cloudflare (edge reverse
// proxy or Argo tunnel) and therefore never exposes the VPS address.
func Shielded(settings map[string]any) bool {
	return truthy(settings["edge_bound"]) || truthy(settings["argo_bound"])
}

// Runs reports whether an enabled inbound should be served right now: in
// low-profile mode only shielded inbounds stay up.
func (c Config) Runs(settings map[string]any) bool {
	return !c.Stealth || Shielded(settings)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "yes"
	}
	return false
}

// MirrorsHeader returns the value of the X-EdgeNest-Mirrors subscription
// header ("<payload>.<sig>", both base64url without padding), or "" when no
// mirrors are configured. The signing key is generated on first use and kept
// in the settings table; clients pin its public half on first sight.
func MirrorsHeader(s *store.Store) (string, error) {
	c := Load(s)
	bases := c.Mirrors
	if len(bases) == 0 {
		return "", nil
	}
	if c.SubBaseURL != "" && strings.HasPrefix(c.SubBaseURL, "https://") {
		bases = append([]string{c.SubBaseURL}, bases...)
	}
	priv, err := signingKey(s)
	if err != nil {
		return "", err
	}
	seqRaw, _ := s.GetSetting(keyMirrorsSeq)
	seq, _ := strconv.ParseInt(seqRaw, 10, 64)
	if seq == 0 {
		seq = 1
	}
	payload, err := json.Marshal(struct {
		V     int      `json:"v"`
		Seq   int64    `json:"seq"`
		Pub   string   `json:"pub"`
		Bases []string `json:"bases"`
	}{1, seq, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)), bases})
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(sig), nil
}

// PublicKey returns the mirror-list signing key (base64), creating it if
// needed, so the panel can show operators what clients will pin.
func PublicKey(s *store.Store) (string, error) {
	priv, err := signingKey(s)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)), nil
}

func signingKey(s *store.Store) (ed25519.PrivateKey, error) {
	if raw, _ := s.GetSetting(keyMirrorsSeed); raw != "" {
		seed, err := base64.StdEncoding.DecodeString(raw)
		if err == nil && len(seed) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(seed), nil
		}
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if err := s.SetSetting(keyMirrorsSeed, base64.StdEncoding.EncodeToString(seed)); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// EdgeRoute is one loopback origin the operator's reverse proxy must forward.
type EdgeRoute struct {
	Path string
	Port int
}

// CaddySnippet renders the Caddyfile site block for the edge domain: the
// subscription path to the panel, each edge inbound's WebSocket path to its
// loopback port, and a bare 404 for everything else.
func CaddySnippet(c Config, panelPort int, routes []EdgeRoute) string {
	if c.EdgeDomain == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s {\n", c.EdgeDomain)
	if panelPort > 0 {
		fmt.Fprintf(&b, "\thandle /sub/* {\n\t\treverse_proxy 127.0.0.1:%d\n\t}\n", panelPort)
	}
	for _, r := range routes {
		fmt.Fprintf(&b, "\thandle %s {\n\t\treverse_proxy 127.0.0.1:%d\n\t}\n", r.Path, r.Port)
	}
	b.WriteString("\thandle {\n\t\trespond 404\n\t}\n}\n")
	return b.String()
}
