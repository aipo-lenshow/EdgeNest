package share

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// ErrNotFound is returned when a token does not resolve to an active
// subscription. The HTTP layer translates this to 404 without revealing
// whether the token was wrong, revoked or expired (don't leak token state).
var ErrNotFound = errors.New("subscription not found or inactive")

// Resolver turns a token into a list of URIs.
type Resolver struct {
	store      *store.Store
	host       string
	cdnPool    []string            // optional Cloudflare anycast IPs; used by CDN-mode inbounds
	argoHost   string              // running Argo tunnel hostname (temp or named); empty = no tunnel
	capability core.NodeCapability // node IP family detection from install.sh
}

// Bundle is the resolved (inbound, client) pair for one row of a subscription.
// Format-specific encoders (Clash YAML / sing-box JSON / Quantumult X) take a
// slice of these and render to their native shape.
//
// EffectiveHost overrides the global `host` parameter for the bundle's server
// field (and ONLY the server field — SNI / Host header stay as the operator
// configured them on the inbound). Set when the inbound opts into the CDN
// preferred-IP pool: the encoder dials a Cloudflare anycast IP while still
// reporting the user's domain in TLS SNI / HTTP Host. Empty = no override.
type Bundle struct {
	Inbound       *model.Inbound
	Client        model.Client
	EffectiveHost string
}

// NewResolver constructs a Resolver. shareHost is the FQDN/IP clients dial
// when neither CDN nor Argo overrides apply and the inbound has no
// SubscriptionHost set; cdnPool is the optional list of Cloudflare anycast
// IPs that inbounds marked `cdn_mode=true` resolve to; argoHost is the live
// Argo tunnel hostname that inbounds marked `argo_bound=true` resolve to.
// capability is the install-time IP family detection — its IPv4Addrs /
// IPv6Addrs supply the multi-IP fallback when SubscriptionHost is empty
// (e.g. an inbound migrated from a legacy row).
//
// Per-bundle precedence: argoHost > cdnPool pick > inbound.SubscriptionHost >
// shareHost.
func NewResolver(s *store.Store, shareHost string, cdnPool []string, argoHost string, capability core.NodeCapability) *Resolver {
	return &Resolver{store: s, host: shareHost, cdnPool: cdnPool, argoHost: argoHost, capability: capability}
}

// CDNPool returns the Cloudflare anycast IPs configured for CDN-mode
// inbounds. Encoders that build bundles externally (e.g. the admin preview
// path) call this to mirror the same per-bundle host substitution logic.
func (r *Resolver) CDNPool() []string { return r.cdnPool }

// Resolve translates a raw token into the user's URI bundle. Workflow:
//  1. Hash the token, look up Subscription row.
//  2. Reject revoked / expired.
//  3. Load the seed Client to discover the user's email.
//  4. Fan out to every enabled sibling client with the same email, across
//     enabled inbounds (subject to AllowedNodes/AllowedInbounds filters).
//  5. Build a URI per (client, inbound) pair.
func (r *Resolver) Resolve(token string) ([]string, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	hash := store.HashToken(token)
	sub, err := r.store.GetSubscriptionByTokenHash(hash)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.Revoked {
		return nil, ErrNotFound
	}
	if sub.ExpiresAt > 0 && sub.ExpiresAt < time.Now().Unix() {
		return nil, ErrNotFound
	}

	seedClient, err := r.store.GetClient(sub.ClientID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !seedClient.Enabled || seedClient.Email == "" {
		return nil, ErrNotFound
	}

	allowedNodes := decodeIDList(sub.AllowedNodes)
	allowedInboundIDs, allowedInboundTags := decodeAllowedInbounds(sub.AllowedInbounds)

	siblings, err := r.store.ListEnabledClientsByEmail(seedClient.Email)
	if err != nil {
		return nil, err
	}

	var uris []string
	for _, c := range siblings {
		in, err := r.store.GetInbound(c.InboundID)
		if err != nil {
			continue
		}
		if !in.Enabled {
			continue
		}
		if len(allowedNodes) > 0 && !idIn(in.NodeID, allowedNodes) {
			continue
		}
		if !inboundAllowed(in, allowedInboundIDs, allowedInboundTags) {
			continue
		}
		if r.argoUnavailable(in) {
			continue
		}
		// BuildURIForClient with a fresh local copy of the client.
		client := c
		encIn, host := r.encodingView(in, client)
		uri, err := BuildURIForClient(encIn, &client, host)
		if err != nil {
			// One bad inbound shouldn't kill the whole bundle.
			continue
		}
		if uri != "" {
			uris = append(uris, uri)
		}
	}
	return uris, nil
}

// argoUnavailable reports whether in is an Argo-bound inbound whose tunnel is
// not currently running. Such an inbound listens on 127.0.0.1 only (cloudflared
// is its sole origin client), so emitting it into a subscription would hand out
// a dead VPS:loopback-port link. We omit it until the tunnel is up; the panel
// surfaces a "start the tunnel" badge so the operator knows why it's missing.
// Once argoHost is set, argoEncodingInbound rewrites it to the tunnel edge view
// and it belongs in the subscription again.
func (r *Resolver) argoUnavailable(in *model.Inbound) bool {
	if r.argoHost != "" {
		return false
	}
	var s map[string]any
	if in.Settings != "" {
		_ = json.Unmarshal([]byte(in.Settings), &s)
	}
	return truthyBool(s["argo_bound"])
}

// effectiveHost returns the host string to embed as the `server` field for
// (inbound, client). Argo binding takes precedence over CDN preferred-IP
// routing, which in turn takes precedence over the global share host —
// matches PROJECT_LIFECYCLE: pick the path that hides the VPS the most.
func (r *Resolver) effectiveHost(in *model.Inbound, c model.Client) string {
	var settings map[string]any
	if in.Settings != "" {
		_ = json.Unmarshal([]byte(in.Settings), &settings)
	}
	// Argo wins: if the inbound is bound to the tunnel and a hostname is
	// available, every client dials the tunnel.
	if r.argoHost != "" && truthyBool(settings["argo_bound"]) && isCDNCompatibleType(in.Type) {
		return r.argoHost
	}
	if pick := PickCDNHost(in, c, settings, r.cdnPool); pick != "" {
		return pick
	}
	// Per-inbound subscription host: the literal IP the wizard's HostChooser
	// picked from the v4 or v6 list. Set per inbound so a dual-stack node
	// can mix v4 inbounds and v6 inbounds on the same sing-box (each binds
	// to its specific listen IP, each URI carries its specific server IP).
	// Empty = inbound migrated from a legacy row; fall through to global
	// shareHost so old data keeps working.
	if in.SubscriptionHost != "" {
		return in.SubscriptionHost
	}
	return r.host
}

// encodingView returns the (inbound, host) pair the format encoders should
// render for this (inbound, client). For most inbounds it's the inbound
// verbatim plus effectiveHost. For an Argo-bound inbound it returns a
// throwaway copy rewritten for the Cloudflare-edge view (see
// argoEncodingInbound). Both Resolve (URI list) and ResolveBundles (native
// formats) funnel through here so every output channel agrees.
func (r *Resolver) encodingView(in *model.Inbound, c model.Client) (*model.Inbound, string) {
	host := r.effectiveHost(in, c)
	if r.argoHost != "" && host == r.argoHost {
		return r.argoEncodingInbound(in), host
	}
	return in, host
}

// argoEncodingInbound returns a copy of in rewritten so the share encoders
// emit a client config that reaches the Cloudflare Tunnel edge — NOT the
// loopback plaintext origin the engine actually serves.
//
// The origin inbound is plaintext WebSocket on 127.0.0.1 (cloudflared speaks
// plain HTTP to it). The client, however, must dial the tunnel hostname over
// TLS on a Cloudflare HTTPS port (443) with SNI + ws Host = the tunnel
// hostname, because Cloudflare terminates TLS at its edge and routes the
// quick/named tunnel by that hostname. So the encoding copy:
//   - sets tls_cert_path to a sentinel → every encoder flips to TLS/WSS on;
//   - sets acme_managed=true → trycloudflare presents a real (publicly
//     trusted) certificate, so clients should verify, not skip;
//   - drops self_signed → no skip-cert-verify path;
//   - overrides sni and ws_host to the tunnel hostname;
//   - keeps ws_path (must match the loopback origin);
//   - dials port 443 (a Cloudflare HTTPS-proxied port).
//
// The sentinel cert path is never dereferenced by the encoders (they only
// test it for non-emptiness) and never reaches the engine renderer, which
// runs against the real inbound.
func (r *Resolver) argoEncodingInbound(in *model.Inbound) *model.Inbound {
	var s map[string]any
	if in.Settings != "" {
		_ = json.Unmarshal([]byte(in.Settings), &s)
	}
	if s == nil {
		s = map[string]any{}
	}
	s["tls_cert_path"] = "argo-edge"
	s["acme_managed"] = "true"
	delete(s, "self_signed")
	s["sni"] = r.argoHost
	s["ws_host"] = r.argoHost
	b, _ := json.Marshal(s)
	cp := *in
	cp.Settings = string(b)
	cp.Port = 443
	return &cp
}

// ResolveBundles returns the raw (inbound, client) pairs a token resolves to,
// applying the same allow-list filters as Resolve. Format-specific encoders
// use this; Resolve itself is layered on top for the URI list path.
func (r *Resolver) ResolveBundles(token string) ([]Bundle, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	hash := store.HashToken(token)
	sub, err := r.store.GetSubscriptionByTokenHash(hash)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.Revoked {
		return nil, ErrNotFound
	}
	if sub.ExpiresAt > 0 && sub.ExpiresAt < time.Now().Unix() {
		return nil, ErrNotFound
	}
	seedClient, err := r.store.GetClient(sub.ClientID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !seedClient.Enabled || seedClient.Email == "" {
		return nil, ErrNotFound
	}
	allowedNodes := decodeIDList(sub.AllowedNodes)
	allowedInboundIDs, allowedInboundTags := decodeAllowedInbounds(sub.AllowedInbounds)
	siblings, err := r.store.ListEnabledClientsByEmail(seedClient.Email)
	if err != nil {
		return nil, err
	}
	var out []Bundle
	for _, c := range siblings {
		in, err := r.store.GetInbound(c.InboundID)
		if err != nil || !in.Enabled {
			continue
		}
		if len(allowedNodes) > 0 && !idIn(in.NodeID, allowedNodes) {
			continue
		}
		if !inboundAllowed(in, allowedInboundIDs, allowedInboundTags) {
			continue
		}
		if r.argoUnavailable(in) {
			continue
		}
		client := c
		encIn, host := r.encodingView(in, client)
		eff := ""
		if host != r.host {
			eff = host
		}
		out = append(out, Bundle{Inbound: encIn, Client: client, EffectiveHost: eff})
	}
	return out, nil
}

// Host returns the share host (FQDN/IP) clients should dial. Format encoders
// need this for the server field of their native representations.
func (r *Resolver) Host() string { return r.host }

// ShareLink is one preview entry: the importable URI plus the REAL server host
// it belongs to. ServerHost is the inbound's own SubscriptionHost (the actual
// VPS IP), NOT the host embedded in the URI — for a CDN inbound the URI carries
// a Cloudflare anycast IP and for Argo a tunnel hostname, but both still "belong
// to" the real server IP. The panel's host filter groups by ServerHost so CF
// IPs never become filter options and a CDN link surfaces alongside its real IP.
type ShareLink struct {
	URI        string `json:"uri"`
	ServerHost string `json:"server_host"`
	CDN        bool   `json:"cdn"`
	Argo       bool   `json:"argo"`
}

// realServerHost is the actual VPS IP an inbound is reached at, ignoring any
// CDN / Argo host rewrite applied to the emitted URI.
func (r *Resolver) realServerHost(in *model.Inbound) string {
	if in.SubscriptionHost != "" {
		return in.SubscriptionHost
	}
	return r.host
}

// BuildBundleForClient is a sibling helper for admin "preview" links: given a
// single client, produce a ShareLink for every enabled inbound where the same
// email is provisioned. Bypasses the Subscription row. The slice is non-nil so
// the JSON encoder emits [] (not null) for a fully-disabled/expired user — the
// panel reads .length and null would crash the share modal.
func (r *Resolver) BuildBundleForClient(c *model.Client) ([]ShareLink, error) {
	if c == nil || c.Email == "" {
		return nil, errors.New("client.email required")
	}
	siblings, err := r.store.ListEnabledClientsByEmail(c.Email)
	if err != nil {
		return nil, err
	}
	links := []ShareLink{}
	for _, sc := range siblings {
		in, err := r.store.GetInbound(sc.InboundID)
		if err != nil {
			continue
		}
		if !in.Enabled {
			continue
		}
		if r.argoUnavailable(in) {
			continue
		}
		client := sc
		encIn, host := r.encodingView(in, client)
		uri, err := BuildURIForClient(encIn, &client, host)
		if err != nil || uri == "" {
			continue
		}
		var settings map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &settings)
		}
		isArgo := r.argoHost != "" && truthyBool(settings["argo_bound"]) && isCDNCompatibleType(in.Type)
		isCDN := !isArgo && PickCDNHost(in, client, settings, r.cdnPool) != ""
		links = append(links, ShareLink{
			URI:        uri,
			ServerHost: r.realServerHost(in),
			CDN:        isCDN,
			Argo:       isArgo,
		})
	}
	return links, nil
}

func decodeIDList(s string) []uint {
	if s == "" {
		return nil
	}
	var ids []uint
	_ = json.Unmarshal([]byte(s), &ids)
	return ids
}
func decodeStringList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// decodeAllowedInbounds parses a subscription's allowed_inbounds field. The
// field historically stored a JSON list of tag strings ([]string); newer
// subscriptions store a JSON list of inbound IDs ([]uint). Both shapes are
// recognised so legacy data keeps working after the migration runs (and
// continues working even if the migration is skipped — e.g. when a stale
// subscription row predates the schema bump).
//
// Why move to IDs: editing an inbound's tag (which is the engine-visible
// identifier used by sing-box.json and the URI form) used to silently break
// existing subscriptions, since the tag-string reference would then point
// at nothing. IDs are immutable for the lifetime of the row, so the
// subscription stays correct across tag/remark renames.
func decodeAllowedInbounds(s string) (ids []uint, tags []string) {
	if s == "" {
		return nil, nil
	}
	// Try the ID array shape first (current schema). On failure fall back
	// to the legacy tag-string shape — the two are JSON-distinguishable
	// since one has numeric tokens and the other has string tokens.
	if json.Unmarshal([]byte(s), &ids) == nil {
		return ids, nil
	}
	_ = json.Unmarshal([]byte(s), &tags)
	return nil, tags
}

// inboundAllowed reports whether the inbound passes a subscription's
// allowed_inbounds filter. An empty filter (both lists nil/empty) means
// "all inbounds allowed", matching the historical semantics.
func inboundAllowed(in *model.Inbound, ids []uint, tags []string) bool {
	if len(ids) == 0 && len(tags) == 0 {
		return true
	}
	return idIn(in.ID, ids) || strIn(in.Tag, tags)
}
func idIn(id uint, list []uint) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}
func strIn(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
