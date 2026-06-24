package singbox

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sort"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// ensureExperimental lazily allocates the experimental map so multiple telemetry
// surfaces (clash_api, v2ray_api) can be attached without clobbering each other.
func ensureExperimental(doc *singboxRoot) {
	if doc.Experimental == nil {
		doc.Experimental = map[string]any{}
	}
}

// isLocalIP reports whether host (string IP literal) matches any IP currently
// configured on a local interface — used by render() to decide if it can
// inet6_bind_address to that host. NAT'd cases (capability reports the
// upstream public IP, which is not on any local iface) return false so the
// bind attempt isn't issued. Mirrors the funnel.go::listenForHost local-probe
// so the inbound side and outbound side agree about which family is
// direct-attached.
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

// inboundFamily classifies an InboundSpec as v4 / v6 / "" (unknown / Argo /
// CDN). The classification drives the per-inbound route rule that pins v6
// inbounds to direct-v6 and v4 inbounds to direct-v4 — so a client that
// dialed in over v6 also egresses via v6 (test-ipv6.com sees a v6 IP,
// websites that geo-locate by source IP get a consistent family).
//
// Source of truth: SubscriptionHost (the literal IP the wizard's Step1
// HostChooser picked — user-visible "this inbound is on which IP"). Listen
// gets degraded to "0.0.0.0" / "::" / "127.0.0.1" by listenForHost on
// single-family / NAT / Argo nodes and is the wrong field to read here.
//
// Returns "" when SubscriptionHost is empty (Argo named tunnel) or doesn't
// parse as an IP literal — the route default ("direct") then handles those
// inbounds without a pinned family.
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

// capabilityPath is overridable from tests so render() can be exercised
// against synthetic v4-only / dual-stack / v6-only capability files.
var capabilityPath = core.DefaultCapabilityPath

// effectiveStrategy resolves the direct outbound's domain_resolver strategy
// from the node capability. Dual-stack returns "" (as-is, sing-box uses OS
// happy-eyeballs); single-stack pins to the available family so a destination
// domain doesn't try to dial a family that has no egress.
//
// The previous user-level Advanced.PreferIPVersion override was removed in
// per-inbound subscription_host_family on the URI side gives
// operators finer control without an extra global toggle.
func effectiveStrategy(cap core.NodeCapability) string {
	switch {
	case cap.IPv4 && !cap.IPv6Global:
		return "prefer_ipv4"
	case !cap.IPv4 && cap.IPv6Global:
		return "prefer_ipv6"
	default:
		return ""
	}
}

// render maps DesiredConfig → sing-box v1.13.x JSON bytes (indented).
//
// Only inbounds with Engine == EngineSingbox are emitted. Outbounds + routes
// are shared across engines, so we always emit them.
//
// Invariant I1: every users[].name MUST equal Client.Email. The
// per-protocol renderers below enforce this; a missing email returns an error.
//
// v1.13 migration notes (vs. the pre-rewrite v1.10 renderer):
//   - inbound-level "sniff" / "sniff_override_destination" fields are removed;
//     sniffing is now a route rule_action.
//   - the "dns" outbound type is removed; DNS hijacking is now a route
//     rule_action (action="hijack-dns" matched on protocol=dns).
//   - Hysteria2 masquerade typed form ({type, url, ...}) is now supported.
func render(cfg core.DesiredConfig) ([]byte, error) {
	doc := singboxRoot{
		Log:       defaultLog(),
		Inbounds:  []map[string]any{},
		Outbounds: []map[string]any{},
		Route:     map[string]any{"rules": []map[string]any{}},
	}

	// userEmails accumulates every rendered sing-box client's email (== its
	// users[].name, Invariant I1). It feeds v2ray_api.stats.users below so the
	// stats service tracks exactly the users we actually serve.
	userEmails := map[string]struct{}{}

	for _, in := range cfg.Inbounds {
		if in.Engine != "" && in.Engine != core.EngineSingbox {
			continue
		}
		// Skip half-configured inbounds: an inbound that has no clients yet would
		// fail Invariant I1 in renderClientsAsUsers. The user has likely just
		// created the inbound and not yet added a client (this is the normal
		// wizard order: POST /inbounds → POST /clients → re-Apply). Render only
		// what the engine can actually serve; the next client POST will trigger
		// another Apply and pick this inbound up.
		if len(in.Clients) == 0 {
			log.Printf("singbox: skip inbound %q (%s): no clients yet, will render on next client add",
				in.Tag, in.Type)
			continue
		}
		rendered, err := renderInbound(in)
		if err != nil {
			return nil, fmt.Errorf("inbound %q (%s): %w", in.Tag, in.Type, err)
		}
		doc.Inbounds = append(doc.Inbounds, rendered)
		for _, c := range in.Clients {
			if c.Email != "" {
				userEmails[c.Email] = struct{}{}
			}
		}
	}

	// DNS block: declares a "local" resolver (uses /etc/resolv.conf or
	// systemd-resolved on Linux), referenced by route.default_domain_resolver
	// and by direct-v4 / direct-v6 outbounds that pin a per-outbound strategy.
	// Mandatory in sing-box 1.12+ — the deprecated outbound.domain_strategy was
	// rejected with FATAL by 1.13.13. Migration spec:
	// https://sing-box.sagernet.org/migration/#migrate-outbound-domain-strategy-option-to-domain-resolver
	doc.DNS = map[string]any{
		"servers": []map[string]any{
			{"type": "local", "tag": "local"},
		},
	}

	// Per-node capability (filled by install.sh's curl-probe detect) gives the
	// default v4/v6 preference for the direct outbound's resolver. We never
	// reject the other family — both direct-v4 and direct-v6 are always
	// emitted so user routes / WARP / Argo can pick either, and the
	// operator's "don't ban one family" directive is honored (ygkkk sb.sh
	// model, see [[ygkkk-dual-ip-design]]).
	cap := core.ReadNodeCapability(capabilityPath)
	strategy := effectiveStrategy(cap)

	// "direct" — the routing default. Its strategy honors user toggle, falling
	// back to capability default. On dual-stack with no user pref the field is
	// omitted so sing-box uses the OS resolver as-is (preserves v6 capacity).
	directOutbound := map[string]any{
		"type": "direct",
		"tag":  "direct",
	}
	if strategy != "" {
		directOutbound["domain_resolver"] = map[string]any{
			"server": "local", "strategy": strategy,
		}
	}

	// direct-v4 and direct-v6 are always emitted so route rules can pin a
	// family explicitly (e.g. "send openai.com via v6 only"). On a single-stack
	// host the unreachable family's outbound still exists — it's just unused
	// unless a route rule selects it; we don't ban it.
	directV4 := map[string]any{
		"type": "direct",
		"tag":  "direct-v4",
		"domain_resolver": map[string]any{
			"server": "local", "strategy": "prefer_ipv4",
		},
	}
	directV6 := map[string]any{
		"type": "direct",
		"tag":  "direct-v6",
		"domain_resolver": map[string]any{
			"server": "local", "strategy": "prefer_ipv6",
		},
	}

	// IPv6 outbound source-bind fix:
	//
	// route.auto_detect_interface=true picks the egress interface from the OS
	// routing table per-dial. On hosts whose IPv6 default route lives on a
	// non-conventional interface (Vultr / RamNode "ipv6net" 6in4 tunnel:
	// `default dev ipv6net metric 1024 pref medium`, no explicit nexthop)
	// sing-box 1.13's interface-pick logic picks the eth0 / ens3 family-
	// wrong interface and the dial returns ENETUNREACH:
	//
	//   outbound/direct[direct-v6]: dial tcp [2600:3c0d::...]:443:
	//                                connect: network is unreachable
	//
	// curl's libcurl picks the egress correctly from the same routing table,
	// confirming the kernel knows the answer — sing-box's helper just doesn't.
	//
	// Setting inet6_bind_address forces the source address; the kernel then
	// resolves the egress interface from `ip -6 route get <dst> from <src>`
	// which always matches the ipv6net default route. Equivalent to
	// `curl --interface 2607:8700:...::2`.
	//
	// Gate on "is the capability-reported v6 IP actually a local interface IP"
	// — NAT'd v6 (rare but exists on some Hurricane Electric edge cases) would
	// have capability.IPv6Addr == upstream public IP not on any local iface,
	// and binding to a non-local address fails. The wizard's listenForHost
	// in funnel.go uses the same probe; we mirror it here so the inbound
	// listen side and the outbound dial side stay in agreement about which
	// family is "direct-attached" vs. "NAT'd".
	//
	// IPv4 is INTENTIONALLY UNTOUCHED on the direct-v4 outbound: the
	// v0.10.0609-ipv4-baseline tag locks 3 VPS × 4 protocols × 19 clients
	// green via that path, so we never change direct-v4. The v4 source-bind
	// added below sits ONLY on direct-v6 — see the next comment block.
	if cap.IPv6Global && cap.IPv6Addr != "" && isLocalIP(cap.IPv6Addr) {
		directV6["inet6_bind_address"] = cap.IPv6Addr
	}

	// IPv4 fallback source. prefer_ipv6 falls back to A records when
	// the destination is IPv4-only (百度 s.bdstatic.com / 网易 / 大多数国内
	// 站, anything without an AAAA record). sing-box then dials a v4 socket;
	// without inet4_bind_address it picks an unconstrained source and on the
	// 6in4 tunnel hosts that source picks the wrong interface and times out:
	//
	//   ERROR connection: open connection to s.bdstatic.com:443 using
	//         outbound/direct[direct-v6]: dial tcp 104.193.88.109:443:
	//         i/o timeout
	//
	// Symptom for the user: "SOCKS5 doesn't work" (every v4-only destination
	// hangs 5s then errors), and "the v6 node breaks v4-only websites".
	//
	// Setting inet4_bind_address on direct-v6 binds the v4 fallback source to
	// the capability-reported v4 IP. sing-box picks v4 source when dialing
	// v4 destinations and v6 source when dialing v6 destinations, all from
	// the same direct-v6 outbound — exactly matching test-ipv6.com's
	// "dual-stack-correctness" expectation (ipv4.test-ipv6.com sees v4,
	// ipv6.test-ipv6.com sees v6, ds.test-ipv6.com prefers v6).
	//
	// This does NOT touch direct-v4 outbound — the baseline path. It only
	// adds a fallback source to the v6 outbound so its prefer_ipv6 fallback
	// works correctly. Same isLocalIP gate as v6: NAT'd v4 (Oracle Cloud,
	// where capability.IPv4Addr is the upstream public IP not on the iface)
	// skips the bind, since binding to a non-local address fails outright.
	if cap.IPv4 && cap.IPv4Addr != "" && isLocalIP(cap.IPv4Addr) {
		directV6["inet4_bind_address"] = cap.IPv4Addr
	}

	doc.Outbounds = append(doc.Outbounds,
		directOutbound,
		directV4,
		directV6,
		map[string]any{"type": "block", "tag": "block"},
	)
	for _, ob := range cfg.Outbounds {
		rendered, err := renderOutbound(ob)
		if err != nil {
			return nil, fmt.Errorf("outbound %q: %w", ob.Tag, err)
		}
		doc.Outbounds = append(doc.Outbounds, rendered)
	}
	if cfg.Warp != nil && cfg.Warp.Enabled {
		doc.Endpoints = append(doc.Endpoints, renderWarpEndpoint(*cfg.Warp))
	}

	// v1.13 route rules: sniff first (so subsequent rules can match by sniffed
	// destination), then hijack DNS into the in-process resolver, then the
	// family-pinning rules (v4 inbounds → direct-v4 / v6 inbounds → direct-v6),
	// then user routes. No reject rule for the "other family" — that violates
	// the operator's "don't ban the other family" directive; clients run RFC
	// 6555 happy-eyeballs themselves and fall back via the natural connect-
	// refused path when a family is unreachable.
	rules := make([]map[string]any, 0, len(cfg.Routes)+5)
	rules = append(rules, map[string]any{"action": "sniff"})
	rules = append(rules, map[string]any{"protocol": "dns", "action": "hijack-dns"})

	// Opt-in QUIC/STUN block (Advanced.BlockQUIC, default off). Rejects
	// forwarded QUIC/STUN so a client's browser HTTP/3 probe gets an ICMP
	// port-unreachable and falls back to TCP/443 through the tunnel (anti
	// QUIC-direct-leak + anti UDP-QoS). Scoped to proxy inbound tags via
	// `inbound` so it NEVER touches the server's own outbounds (WARP wireguard
	// dial, DoQ resolver) — those originate from no inbound and so can't match.
	// method:default → ICMP reject (fastest browser fallback); no_drop:true
	// keeps it from auto-degrading to silent drop under burst. Depends on the
	// sniff rule above being present and first. Sourced from sing-box
	// route/rule_action + route/sniff docs (protocol quic/stun, reject action).
	if cfg.Advanced != nil && cfg.Advanced.BlockQUIC {
		proxyInboundTags := make([]string, 0, len(cfg.Inbounds))
		for _, in := range cfg.Inbounds {
			if in.Engine != "" && in.Engine != core.EngineSingbox {
				continue
			}
			if len(in.Clients) == 0 {
				continue
			}
			proxyInboundTags = append(proxyInboundTags, in.Tag)
		}
		if len(proxyInboundTags) > 0 {
			rules = append(rules, map[string]any{
				"inbound":  proxyInboundTags,
				"protocol": []string{"quic", "stun"},
				"action":   "reject",
				"method":   "default",
				"no_drop":  true,
			})
		}
	}

	// User domain routes (e.g. the WARP presets: claude.ai → warp) MUST come
	// before family-pinning. sing-box is first-match-wins, and family-pinning
	// matches by inbound tag (broad — every v4/v6 inbound). If it ran first it
	// would route ALL proxied traffic to direct-v4/v6 and the specific domain
	// rules below would never fire (WARP routing silently dead). Specific
	// domain rules first, broad family catch-all after.
	//
	// A route rule that points at an outbound we didn't emit (most commonly an
	// `outbound=warp` rule while WARP is disabled — line ~267 only emits the warp
	// outbound when enabled) would make sing-box refuse the whole config
	// ("outbound not found"), taking the data plane down. Skip such rules so the
	// config is always valid; they reactivate automatically once the outbound is
	// enabled. This keeps the one-click WARP presets safe to apply before the
	// operator flips WARP on.
	validOutbounds := make(map[string]bool, len(doc.Outbounds)+len(doc.Endpoints))
	for _, ob := range doc.Outbounds {
		if tag, ok := ob["tag"].(string); ok {
			validOutbounds[tag] = true
		}
	}
	// Endpoint tags (e.g. the WARP wireguard endpoint) are valid route targets
	// too — a route rule's outbound can name an endpoint.
	for _, ep := range doc.Endpoints {
		if tag, ok := ep["tag"].(string); ok {
			validOutbounds[tag] = true
		}
	}
	for _, r := range cfg.Routes {
		if !validOutbounds[r.Outbound] {
			log.Printf("singbox: skip route %s=%s → outbound %q not active (e.g. WARP not enabled)",
				r.Type, r.Value, r.Outbound)
			continue
		}
		rule, err := renderRoute(r)
		if err != nil {
			return nil, fmt.Errorf("route %s=%s: %w", r.Type, r.Value, err)
		}
		rules = append(rules, rule)
	}

	// Family pinning — the fix for "v6 inbound but test-ipv6.com sees
	// v4". Without this, the default outbound's domain_resolver on dual-stack
	// has strategy="" and the OS happy-eyeballs path silently prefers v4
	// (kernel default for AF_UNSPEC dials when both families return). A user
	// who deliberately picked a v6 IP in Step1 — and is paying for the v6
	// transit on the VPS provider — expects egress to be v6 too, not v4. By
	// matching on the inbound's wizard-picked family rather than the listen
	// IP we also cover NAT'd Oracle nodes where listen is "::" but the
	// SubscriptionHost is a specific v4 or v6 public IP. Runs AFTER the domain
	// routes above so a claude.ai→warp rule still wins for those domains; all
	// other traffic from the inbound falls through to its family's direct.
	var v4Tags, v6Tags []string
	for _, in := range cfg.Inbounds {
		if in.Engine != "" && in.Engine != core.EngineSingbox {
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
			"inbound":  v6Tags,
			"outbound": "direct-v6",
		})
	}
	if len(v4Tags) > 0 {
		rules = append(rules, map[string]any{
			"inbound":  v4Tags,
			"outbound": "direct-v4",
		})
	}
	doc.Route = map[string]any{
		"rules":                 rules,
		"final":                 "direct",
		"auto_detect_interface": true,
		// sing-box 1.13 recommendation: declare the default resolver once at
		// the route level so we don't have to repeat domain_resolver on every
		// user-supplied outbound (ygkkk pattern).
		"default_domain_resolver": map[string]any{"server": "local"},
	}

	// experimental: read-only telemetry surfaces, both loopback, both orthogonal
	// to routing/outbounds so the data path is unchanged whether present or not.
	//   - clash_api: the panel polls /connections for live domain capture.
	//   - v2ray_api: per-user traffic counters (user>>>{email}>>>traffic>>>...)
	//     that the quota enforcer reads. Its stats.users list is the set of
	//     emails we just rendered, so the service tracks exactly our clients.
	if cfg.ClashAPI != nil && cfg.ClashAPI.Controller != "" {
		ensureExperimental(&doc)
		doc.Experimental["clash_api"] = map[string]any{
			"external_controller": cfg.ClashAPI.Controller,
			"secret":              cfg.ClashAPI.Secret,
		}
	}
	if cfg.V2RayAPI != nil && cfg.V2RayAPI.Controller != "" {
		ensureExperimental(&doc)
		users := make([]string, 0, len(userEmails))
		for e := range userEmails {
			users = append(users, e)
		}
		// Deterministic order so identical desired state renders byte-identical
		// (baseline invariant; the map iteration order is otherwise random).
		sort.Strings(users)
		doc.Experimental["v2ray_api"] = map[string]any{
			"listen": cfg.V2RayAPI.Controller,
			"stats": map[string]any{
				"enabled": true,
				"users":   users,
			},
		}
	}

	return json.MarshalIndent(doc, "", "  ")
}

type singboxRoot struct {
	Log          map[string]any   `json:"log"`
	DNS          map[string]any   `json:"dns,omitempty"`
	Inbounds     []map[string]any `json:"inbounds"`
	Outbounds    []map[string]any `json:"outbounds"`
	Endpoints    []map[string]any `json:"endpoints,omitempty"`
	Route        map[string]any   `json:"route"`
	Experimental map[string]any   `json:"experimental,omitempty"`
}

func defaultLog() map[string]any {
	// EDGENEST_SINGBOX_LOG_LEVEL overrides the default `info` level so an
	// operator can flip to `debug` / `trace` from systemd without rebuilding
	// the binary — handy when chasing a silent QUIC handshake failure on a
	// specific client (Stash Hy2, Karing SS) where info-level logs go quiet.
	// Valid sing-box levels: trace, debug, info, warn, error, fatal, panic.
	//
	// NOTE: the "don't log client IP" privacy toggle is deliberately NOT done by
	// lowering this level — real captures showed source IPs still leak at ERROR
	// (REALITY "process connection from <ip>" handshake failures). It's handled
	// instead by redacting IPs in the log write path (internal/logredact), which
	// keeps full log fidelity AND never touches sing-box.json (no baseline drift).
	level := os.Getenv("EDGENEST_SINGBOX_LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	return map[string]any{
		"level":     level,
		"timestamp": true,
	}
}

// renderInbound dispatches to the protocol-specific renderer.
func renderInbound(in core.InboundSpec) (map[string]any, error) {
	switch in.Type {
	case "vless":
		return renderVLESSReality(in)
	case "hysteria2":
		return renderHysteria2(in)
	case "trojan":
		return renderTrojan(in)
	case "shadowsocks":
		return renderShadowsocks(in)
	case "tuic":
		return renderTUIC(in)
	case "vmess", "vmess-ws":
		return renderVMessWS(in)
	case "vless-ws":
		return renderVLESSWS(in)
	case "socks":
		return renderSocks(in)
	case "anytls":
		return renderAnyTLS(in)
	default:
		return nil, fmt.Errorf("unsupported sing-box inbound type %q", in.Type)
	}
}

func renderOutbound(ob core.OutboundSpec) (map[string]any, error) {
	out := map[string]any{
		"type": ob.Type,
		"tag":  ob.Tag,
	}
	for k, v := range ob.Settings {
		out[k] = v
	}
	return out, nil
}

// renderWarpEndpoint emits the WARP tunnel as a sing-box wireguard *endpoint*
// (top-level "endpoints" array), the form sing-box 1.11+ requires. The legacy
// wireguard *outbound* (server/server_port/local_address/peer_public_key) was
// removed in 1.11 — emitting it makes 1.13 reject the whole config with
// `unknown field "local_address"`. Schema verified against sing-box 1.13.13
// `sing-box check`. The endpoint's tag ("warp") is referenced by route rules as
// their outbound, exactly like an outbound tag.
func renderWarpEndpoint(w core.WarpSpec) map[string]any {
	return map[string]any{
		"type":        "wireguard",
		"tag":         "warp",
		"mtu":         1280,
		"address":     nonEmptyStrings(w.Address4, w.Address6),
		"private_key": w.PrivateKey,
		"peers": []map[string]any{
			{
				"address":     splitHostPort(w.Endpoint, "engage.cloudflareclient.com"),
				"port":        splitHostPortPort(w.Endpoint, 2408),
				"public_key":  w.PublicKey,
				"reserved":    w.Reserved,
				"allowed_ips": []string{"0.0.0.0/0", "::/0"},
			},
		},
	}
}

func renderRoute(r core.RouteSpec) (map[string]any, error) {
	rule := map[string]any{"outbound": r.Outbound}
	switch r.Type {
	case "domain", "domain_suffix", "domain_keyword", "domain_regex",
		"geosite", "geoip", "ip_cidr", "process_name":
		rule[r.Type] = []string{r.Value}
		return rule, nil
	default:
		return nil, fmt.Errorf("unknown route type %q", r.Type)
	}
}

// nonEmptyStrings returns ss with empty strings removed.
func nonEmptyStrings(ss ...string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
