package xray

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// isLocalIP — same probe as singbox.isLocalIP, kept package-local so the
// engine packages stay independent. Returns true if host is a string IP
// literal currently bound to a local interface, false otherwise (NAT'd
// public-IP capability values pass through false and the v6 source-bind
// is skipped — the dial then uses the default source-selection path
// instead of failing on a non-local bind).
func isLocalIP(host string) bool {
	target := net.ParseIP(host)
	if target == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipnet.IP.Equal(target) {
			return true
		}
	}
	return false
}

// inboundFamily classifies an InboundSpec by its wizard-picked SubscriptionHost
// — mirrors singbox.inboundFamily. The xray engine uses it to pin v6 inbounds
// to freedom-v6 (UseIPv6) and v4 inbounds to freedom-v4 (UseIPv4), keeping
// the egress family consistent with the inbound family. Returns "" for Argo
// / legacy / non-IP-literal SubscriptionHost — those inbounds fall through
// to the default freedom outbound.
func inboundFamily(in core.InboundSpec) string {
	if in.SubscriptionHost == "" {
		return ""
	}
	ip := net.ParseIP(in.SubscriptionHost)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		return "v4"
	}
	return "v6"
}

// capabilityPath is overridable from tests.
var capabilityPath = core.DefaultCapabilityPath

// effectiveDomainStrategy maps node capability to xray's freedom
// domainStrategy (UseIPv4 / UseIPv6 / "" = AsIs for dual-stack).
//
// Mirrors singbox.effectiveStrategy — the per-user override that used to
// live on Advanced.PreferIPVersion was removed in a later revision, replaced by the
// per-inbound subscription_host_family that targets the URI side instead.
func effectiveDomainStrategy(cap core.NodeCapability) string {
	switch {
	case cap.IPv4 && !cap.IPv6Global:
		return "UseIPv4"
	case !cap.IPv4 && cap.IPv6Global:
		return "UseIPv6"
	default:
		return ""
	}
}

// render maps DesiredConfig → xray-core v1.x JSON bytes (indented).
//
// Only inbounds with Engine == EngineXray are emitted. Outbounds + routes are
// shared across engines.
//
// Invariant I1: every client's "email" MUST equal Client.Email (which is also
// the xray stats key). renderClients enforces this.
func render(cfg core.DesiredConfig) ([]byte, error) {
	doc := xrayRoot{
		Log:       map[string]any{"loglevel": "warning"},
		Inbounds:  []map[string]any{},
		Outbounds: []map[string]any{},
		Routing:   map[string]any{"rules": []map[string]any{}},
	}

	for _, in := range cfg.Inbounds {
		// Registry already filtered by engine; we accept empty Engine to be
		// tolerant of callers that pass raw configs (tests, future callers).
		if in.Engine != "" && in.Engine != core.EngineXray {
			continue
		}
		// Skip inbounds with no clients yet — same reason as sing-box: the user
		// is mid-wizard and we Apply between POST /inbounds and POST /clients.
		// renderClients enforces ≥1 client (Invariant I1); skip rather than
		// fail so the wizard's second step can succeed.
		if len(in.Clients) == 0 {
			log.Printf("xray: skip inbound %q (%s): no clients yet, will render on next client add",
				in.Tag, in.Type)
			continue
		}
		rendered, err := renderInbound(in)
		if err != nil {
			return nil, fmt.Errorf("inbound %q (%s): %w", in.Tag, in.Type, err)
		}
		doc.Inbounds = append(doc.Inbounds, rendered)
	}

	// Capability drives the freedom outbound's domainStrategy. Both
	// freedom-v4 (UseIPv4) and freedom-v6 (UseIPv6) are always emitted so
	// route rules can pin a family explicitly without us banning the other
	// (ygkkk model: don't reject the unreachable family, let the client's
	// happy-eyeballs handle it).
	cap := core.ReadNodeCapability(capabilityPath)
	domainStrategy := effectiveDomainStrategy(cap)

	freedomOutbound := map[string]any{
		"protocol": "freedom",
		"tag":      "direct",
	}
	if domainStrategy != "" {
		freedomOutbound["settings"] = map[string]any{
			"domainStrategy": domainStrategy,
		}
	}
	freedomV4 := map[string]any{
		"protocol": "freedom",
		"tag":      "direct-v4",
		"settings": map[string]any{"domainStrategy": "UseIPv4"},
	}
	freedomV6 := map[string]any{
		"protocol": "freedom",
		"tag":      "direct-v6",
		"settings": map[string]any{"domainStrategy": "UseIPv6"},
	}
	// IPv6 outbound source-bind, same diagnosis as sing-box:
	// xray's auto-source on hosts with a non-conventional v6 default route
	// (Vultr/RamNode ipv6net 6in4 tunnel: `default dev ipv6net`) picks the
	// v4 family interface and the v6 dial returns ENETUNREACH. xray's
	// outbound-level `sendThrough` field forces the source address; the
	// kernel resolves the egress interface from the source-IP route table
	// and lands on ipv6net every time. IPv4 is intentionally untouched —
	// see the equivalent comment in internal/node/engine/singbox/render.go
	// for the v0.10.0609-ipv4-baseline rationale.
	if cap.IPv6Global && cap.IPv6Addr != "" && isLocalIP(cap.IPv6Addr) {
		freedomV6["sendThrough"] = cap.IPv6Addr
	}

	doc.Outbounds = append(doc.Outbounds,
		freedomOutbound,
		freedomV4,
		freedomV6,
		map[string]any{"protocol": "blackhole", "tag": "block"},
	)
	for _, ob := range cfg.Outbounds {
		rendered, err := renderOutbound(ob)
		if err != nil {
			return nil, fmt.Errorf("outbound %q: %w", ob.Tag, err)
		}
		doc.Outbounds = append(doc.Outbounds, rendered)
	}
	// WARP outbound (wireguard) when enabled.
	if cfg.Warp != nil && cfg.Warp.Enabled {
		doc.Outbounds = append(doc.Outbounds, renderWarpOutbound(*cfg.Warp))
	}

	rules := make([]map[string]any, 0, len(cfg.Routes)+2)

	// User domain routes first (e.g. WARP presets: claude.ai → warp). xray
	// routing is first-match too, and family-pinning below matches by inboundTag
	// (every v4/v6 inbound) — if it ran first it would send ALL proxied traffic
	// to direct-v4/v6 and the domain rules would never fire. Specific before
	// broad. Skip rules whose outbound we didn't emit (e.g. warp while disabled)
	// so xray never gets a dangling outboundTag; they reactivate once enabled.
	validOutbounds := make(map[string]bool, len(doc.Outbounds))
	for _, ob := range doc.Outbounds {
		if tag, ok := ob["tag"].(string); ok {
			validOutbounds[tag] = true
		}
	}
	for _, r := range cfg.Routes {
		if !validOutbounds[r.Outbound] {
			log.Printf("xray: skip route %s=%s → outbound %q not active (e.g. WARP not enabled)",
				r.Type, r.Value, r.Outbound)
			continue
		}
		rule, err := renderRoute(r)
		if err != nil {
			return nil, fmt.Errorf("route %s=%s: %w", r.Type, r.Value, err)
		}
		rules = append(rules, rule)
	}

	// Family pinning — the fix mirroring singbox/render.go: v6 inbounds
	// egress through freedom-v6 (UseIPv6), v4 inbounds through freedom-v4
	// (UseIPv4). Without this, dual-stack nodes' default `freedom` outbound
	// has no domainStrategy and xray picks v4 first on AF_UNSPEC dials, so a
	// client who deliberately connected through a v6 inbound silently
	// egresses via v4. Runs AFTER the domain routes above (specific wins).
	var v4Tags, v6Tags []string
	for _, in := range cfg.Inbounds {
		if in.Engine != "" && in.Engine != core.EngineXray {
			continue
		}
		if len(in.Clients) == 0 {
			continue
		}
		switch inboundFamily(in) {
		case "v4":
			v4Tags = append(v4Tags, in.Tag)
		case "v6":
			v6Tags = append(v6Tags, in.Tag)
		}
	}
	if len(v6Tags) > 0 {
		rules = append(rules, map[string]any{
			"type":        "field",
			"inboundTag":  v6Tags,
			"outboundTag": "direct-v6",
		})
	}
	if len(v4Tags) > 0 {
		rules = append(rules, map[string]any{
			"type":        "field",
			"inboundTag":  v4Tags,
			"outboundTag": "direct-v4",
		})
	}
	doc.Routing = map[string]any{
		"domainStrategy": "IPIfNonMatch",
		"rules":          rules,
	}

	// xray_api: per-user traffic counters for quota enforcement. xray-core's
	// stats/api/commander apps are in the stock release (no build tag), so this
	// is just config. api.listen exposes the StatsService gRPC on loopback
	// without the legacy dokodemo-door + routing-rule dance. policy.levels."0"
	// turns on per-user up/down counters for level-0 users (all our clients —
	// each carries an email == Client.Email, the stats key). Orthogonal to
	// routing/outbounds, so the data path is unchanged whether present or not.
	if cfg.XRayAPI != nil && cfg.XRayAPI.Controller != "" {
		doc.Stats = map[string]any{}
		doc.API = map[string]any{
			"tag":      "api",
			"listen":   cfg.XRayAPI.Controller,
			"services": []string{"StatsService"},
		}
		doc.Policy = map[string]any{
			"levels": map[string]any{
				"0": map[string]any{
					"statsUserUplink":   true,
					"statsUserDownlink": true,
				},
			},
			"system": map[string]any{
				"statsInboundUplink":   false,
				"statsInboundDownlink": false,
			},
		}
	}

	return json.MarshalIndent(doc, "", "  ")
}

type xrayRoot struct {
	Log map[string]any `json:"log"`
	API map[string]any `json:"api,omitempty"`
	// Stats is `any`, not map: xray needs the literal `"stats": {}` to enable
	// the counter store, but json omitempty drops an empty map. A non-nil
	// interface holding an empty map is only omitted when the interface itself
	// is nil, so this renders `{}` when set and is absent when left nil.
	Stats     any              `json:"stats,omitempty"`
	Policy    map[string]any   `json:"policy,omitempty"`
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
	Routing   map[string]any   `json:"routing"`
}

// renderInbound dispatches to the protocol-specific renderer.
func renderInbound(in core.InboundSpec) (map[string]any, error) {
	switch in.Type {
	case "vless-xhttp":
		return renderVLESSXHTTP(in)
	default:
		// "anytls" used to be routed here, but xray-core mainline (verified
		// against v26.3.27) does not implement anytls as inbound OR outbound.
		// It moved to the sing-box engine (which has had it since v1.12).
		return nil, fmt.Errorf("unsupported xray inbound type %q", in.Type)
	}
}

// renderVLESSXHTTP emits a VLESS-XHTTP inbound, optionally wrapped in Reality
// or plain TLS.
//
// Required settings:
//   - xhttp_path           string  ("/xhttp" default)
//
// Reality mode (security == "reality"):
//   - sni                  string  required (e.g. www.microsoft.com)
//   - reality_private_key  string  required
//   - short_ids            []string optional ([""] default)
//   - server_port_target   int     optional (443 default)
//   - server_name_target   string  optional (defaults to sni)
//
// TLS mode (security == "tls"):
//   - tls_cert_path        string  required
//   - tls_key_path         string  required
//   - sni                  string  optional (server certificate SNI hint)
//
// "none" mode (plaintext xhttp, e.g. behind CDN):
//   - no extra keys required.
func renderVLESSXHTTP(in core.InboundSpec) (map[string]any, error) {
	security := getString(in.Settings, "security", "reality")
	clients, err := renderClients(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
		if c.UUID == "" {
			return nil, fmt.Errorf("client %q missing UUID", c.Email)
		}
		// xray-core refuses to start a vless inbound whose user carries
		// flow=xtls-rprx-vision over any transport other than raw TCP. The
		// wizard reuses one Reality-tuned client across the run, so the flow
		// leaks into xhttp inbounds unless we strip it here.
		return map[string]any{
			"id":    c.UUID,
			"email": c.Email, // Invariant I1: stats key
			"flow":  "",
		}, nil
	})
	if err != nil {
		return nil, err
	}

	stream := map[string]any{
		"network":  "xhttp",
		"security": security,
		"xhttpSettings": map[string]any{
			"path": getString(in.Settings, "xhttp_path", "/xhttp"),
			"host": getString(in.Settings, "xhttp_host", ""),
		},
	}

	switch security {
	case "reality":
		sni, err := requireString(in.Settings, "sni")
		if err != nil {
			return nil, err
		}
		privKey, err := requireString(in.Settings, "reality_private_key")
		if err != nil {
			return nil, err
		}
		shortIDs := getStringSlice(in.Settings, "short_ids", []string{""})
		targetPort := getInt(in.Settings, "server_port_target", 443)
		targetHost := getString(in.Settings, "server_name_target", sni)
		stream["realitySettings"] = map[string]any{
			"show":        false,
			"dest":        fmt.Sprintf("%s:%d", targetHost, targetPort),
			"serverNames": []string{sni},
			"privateKey":  privKey,
			"shortIds":    shortIDs,
		}
	case "tls":
		certPath, err := requireString(in.Settings, "tls_cert_path")
		if err != nil {
			return nil, err
		}
		keyPath, err := requireString(in.Settings, "tls_key_path")
		if err != nil {
			return nil, err
		}
		tls := map[string]any{
			"certificates": []map[string]any{
				{"certificateFile": certPath, "keyFile": keyPath},
			},
		}
		if sni := getString(in.Settings, "sni", ""); sni != "" {
			tls["serverName"] = sni
		}
		stream["tlsSettings"] = tls
	case "none":
		// plaintext xhttp (CDN front)
	default:
		return nil, fmt.Errorf("unsupported vless-xhttp security %q (want reality|tls|none)", security)
	}

	return map[string]any{
		"tag":      in.Tag,
		"listen":   orDefault(in.Listen, "::"),
		"port":     in.Port,
		"protocol": "vless",
		"settings": map[string]any{
			"clients":    clients,
			"decryption": "none",
		},
		"streamSettings": stream,
		"sniffing": map[string]any{
			"enabled":      true,
			"destOverride": []string{"http", "tls", "quic"},
		},
	}, nil
}

func renderOutbound(ob core.OutboundSpec) (map[string]any, error) {
	out := map[string]any{
		"protocol": ob.Type,
		"tag":      ob.Tag,
	}
	for k, v := range ob.Settings {
		out[k] = v
	}
	return out, nil
}

func renderWarpOutbound(w core.WarpSpec) map[string]any {
	addrs := []string{}
	if w.Address4 != "" {
		addrs = append(addrs, w.Address4)
	}
	if w.Address6 != "" {
		addrs = append(addrs, w.Address6)
	}
	host, port := splitWarpEndpoint(w.Endpoint)
	peer := map[string]any{
		"publicKey":  w.PublicKey,
		"endpoint":   fmt.Sprintf("%s:%d", host, port),
		"keepAlive":  25,
	}
	if len(w.Reserved) > 0 {
		// xray-core wireguard outbound takes reserved as []int.
		peer["reserved"] = w.Reserved
	}
	return map[string]any{
		"protocol": "wireguard",
		"tag":      "warp",
		"settings": map[string]any{
			"secretKey": w.PrivateKey,
			"address":   addrs,
			"peers":     []map[string]any{peer},
			"mtu":       1280,
		},
	}
}

// renderRoute maps EdgeNest's RouteSpec to xray's routing rule shape.
func renderRoute(r core.RouteSpec) (map[string]any, error) {
	rule := map[string]any{"type": "field", "outboundTag": r.Outbound}
	switch r.Type {
	case "domain":
		rule["domain"] = []string{"full:" + r.Value}
	case "domain_suffix":
		rule["domain"] = []string{"domain:" + r.Value}
	case "domain_keyword":
		rule["domain"] = []string{"keyword:" + r.Value}
	case "domain_regex":
		rule["domain"] = []string{"regexp:" + r.Value}
	case "geosite":
		rule["domain"] = []string{"geosite:" + r.Value}
	case "geoip":
		rule["ip"] = []string{"geoip:" + r.Value}
	case "ip_cidr":
		rule["ip"] = []string{r.Value}
	case "process_name":
		rule["process"] = map[string]any{"processes": []string{r.Value}}
	default:
		return nil, fmt.Errorf("unknown route type %q", r.Type)
	}
	return rule, nil
}

// splitWarpEndpoint returns (host, port). Falls back to the Cloudflare default.
func splitWarpEndpoint(hp string) (string, int) {
	if hp == "" {
		return "engage.cloudflareclient.com", 2408
	}
	if i := strings.LastIndex(hp, ":"); i > 0 {
		host := hp[:i]
		if p, err := strconv.Atoi(hp[i+1:]); err == nil {
			return host, p
		}
		return host, 2408
	}
	return hp, 2408
}

// renderClients maps each ClientSpec through f and enforces I1: unique non-empty Email.
func renderClients(clients []core.ClientSpec, f func(core.ClientSpec) (map[string]any, error)) ([]map[string]any, error) {
	if len(clients) == 0 {
		return nil, fmt.Errorf("at least one client required (xray stats key == Client.Email)")
	}
	seen := map[string]bool{}
	out := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		if c.Email == "" {
			return nil, fmt.Errorf("client missing email (Invariant I1)")
		}
		if seen[c.Email] {
			return nil, fmt.Errorf("duplicate client email %q", c.Email)
		}
		seen[c.Email] = true
		u, err := f(c)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// ---- settings helpers ----

func getString(m map[string]any, key, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func requireString(m map[string]any, key string) (string, error) {
	v := getString(m, key, "")
	if v == "" {
		return "", fmt.Errorf("missing required setting %q", key)
	}
	return v, nil
}

func getInt(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		case string:
			if i, err := strconv.Atoi(n); err == nil {
				return i
			}
		}
	}
	return def
}

func getStringSlice(m map[string]any, key string, def []string) []string {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return def
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
