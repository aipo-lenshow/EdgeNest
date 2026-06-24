package share

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// EncodeClash renders a subscription bundle as a Clash / Mihomo YAML body. The
// shape is intentionally minimal: a `proxies:` list plus a couple of pre-baked
// proxy-groups so the user gets a working profile on import without needing to
// craft groups by hand.
//
// Only protocols Clash/Mihomo actually understand make it in (vless, hysteria2,
// trojan, shadowsocks). Unsupported protocols are silently skipped — same
// policy as BuildURIs.
// EncodeClash renders a Mihomo-compatible YAML body (Clash Verge Rev, ClashX
// Pro, openClash). Use EncodeStash for the Stash-specific dialect — the two
// differ only in the Hysteria2 proxy entry (Stash 3.x reads `auth:` while
// Mihomo reads `password:`), so they share every other helper.
func EncodeClash(bundles []Bundle, host string) string {
	return encodeClashLike(bundles, host, false)
}

// EncodeStash renders the same dispatch table as EncodeClash, but routes
// hysteria2 nodes through stashHysteria2 so the `auth:` field reaches
// Stash's strict YAML schema validator.
func EncodeStash(bundles []Bundle, host string) string {
	return encodeClashLike(bundles, host, true)
}

func encodeClashLike(bundles []Bundle, host string, stashMode bool) string {
	type proxy struct {
		name string
		body []string // already-indented "  key: value" lines
	}
	var proxies []proxy
	for _, b := range bundles {
		var settings map[string]any
		if b.Inbound.Settings != "" {
			_ = json.Unmarshal([]byte(b.Inbound.Settings), &settings)
		}
		if settings == nil {
			settings = map[string]any{}
		}
		name := remarkOf(b.Inbound, b.Client)
		switch b.Inbound.Type {
		case "vless":
			if body, ok := clashVLESS(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "vless-ws":
			if body, ok := clashVLESSWS(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "vless-xhttp":
			// Stash does NOT support the xhttp transport. Stash's official
			// proxy-types schema (stash.wiki/en/proxy-protocols/proxy-types)
			// documents only ws / grpc / h2 as VLESS `network:` values — no
			// xhttp / splithttp, and no version has added it through the 3.x
			// line (verified 2026-06-14, real-machine: Stash iOS fails BOTH
			// vless-xhttp-reality and vless-xhttp-tls; the transport is
			// unparsed, independent of the Reality/TLS security layer).
			// Mihomo / ClashMi DO support it, so only skip in stashMode —
			// emitting a dead node Stash can't dial just confuses the user
			// (same "a fragile core is better off one node short than shipping
			// a broken one" rule singbox.go applies to anytls/xhttp).
			if stashMode {
				break
			}
			if body, ok := clashVLESSXHTTP(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "hysteria2":
			var (
				body []string
				ok   bool
			)
			if stashMode {
				body, ok = stashHysteria2(b.Inbound, b.Client, settings, bundleHost(b, host))
			} else {
				body, ok = clashHysteria2(b.Inbound, b.Client, settings, bundleHost(b, host))
			}
			if ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "trojan":
			if body, ok := clashTrojan(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "shadowsocks":
			if body, ok := clashShadowsocks(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "tuic":
			if body, ok := clashTUIC(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "vmess", "vmess-ws":
			if body, ok := clashVMess(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "socks":
			if body, ok := clashSocks(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		case "anytls":
			if body, ok := clashAnyTLS(b.Inbound, b.Client, settings, bundleHost(b, host)); ok {
				proxies = append(proxies, proxy{name: name, body: body})
			}
		}
	}

	var sb strings.Builder
	if stashMode {
		sb.WriteString("# EdgeNest subscription (Stash format)\n")
	} else {
		sb.WriteString("# EdgeNest subscription (Clash / Mihomo format)\n")
	}
	sb.WriteString("mixed-port: 7890\n")
	sb.WriteString("allow-lan: false\n")
	sb.WriteString("mode: rule\n")
	sb.WriteString("log-level: info\n")
	// Mihomo (Clash.Meta) gets an anti-leak DNS block: fake-ip + DoH that
	// egresses through the tunnel (respect-rules), so resolution never hits
	// the local ISP. Region-free by design — NO geosite/geoip rules (project
	// rule: never assume user geography). Stash is intentionally left minimal:
	// respect-rules / proxy-server-nameserver are Mihomo-only keys and Stash's
	// strict schema validator can reject the whole profile, so upgrading Stash
	// needs its own dialect research first.
	if !stashMode {
		sb.WriteString(mihomoDNSBlock)
	}
	sb.WriteString("\nproxies:\n")
	if len(proxies) == 0 {
		sb.WriteString("  []\n")
	}
	names := make([]string, 0, len(proxies))
	for _, p := range proxies {
		sb.WriteString("  - name: ")
		sb.WriteString(yamlString(p.name))
		sb.WriteString("\n")
		for _, line := range p.body {
			sb.WriteString("    ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		names = append(names, p.name)
	}

	sb.WriteString("\nproxy-groups:\n")
	sb.WriteString("  - name: EdgeNest\n    type: select\n    proxies:\n      - auto\n")
	for _, n := range names {
		sb.WriteString("      - ")
		sb.WriteString(yamlString(n))
		sb.WriteString("\n")
	}
	sb.WriteString("  - name: auto\n    type: url-test\n    url: http://www.gstatic.com/generate_204\n    interval: 300\n    proxies:\n")
	if len(names) == 0 {
		sb.WriteString("      - DIRECT\n")
	}
	for _, n := range names {
		sb.WriteString("      - ")
		sb.WriteString(yamlString(n))
		sb.WriteString("\n")
	}

	// Rules: private/LAN/loopback direct (network topology, NOT geography —
	// explicit IP-CIDR so no geoip database download is needed; GEOIP,private
	// would force an mmdb fetch), everything else to the proxy. Zero region
	// branching. Stash keeps the bare MATCH it always shipped.
	sb.WriteString("\nrules:\n")
	if !stashMode {
		sb.WriteString(mihomoPrivateDirectRules)
	}
	sb.WriteString("  - MATCH,EdgeNest\n")
	return sb.String()
}

// mihomoDNSBlock is a region-free anti-leak DNS section for Clash.Meta /
// Mihomo. fake-ip prevents DNS-based leakage of real resolution; respect-rules
// routes the DoH queries through the tunnel (requires proxy-server-nameserver);
// fake-ip-filter holds ONLY network-nature exclusions (captive-portal probes,
// NTP, *.lan) — never geosite. fake-ip-range is Mihomo's documented default
// 198.18.0.1/16 (sing-box uses /15 — do not cross-copy). Verified against
// wiki.metacubex.one (dns + rules pages).
const mihomoDNSBlock = `ipv6: true
dns:
  enable: true
  ipv6: true
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  fake-ip-filter-mode: blacklist
  fake-ip-filter:
    - '*.lan'
    - '*.local'
    - '+.local'
    - localhost
    - '+.msftncsi.com'
    - '+.msftconnecttest.com'
    - 'captive.apple.com'
    - '+.pool.ntp.org'
  respect-rules: true
  default-nameserver:
    - 1.1.1.1
    - 8.8.8.8
  proxy-server-nameserver:
    - https://1.1.1.1/dns-query
  nameserver:
    - https://1.1.1.1/dns-query
    - https://dns.google/dns-query
`

// mihomoPrivateDirectRules sends RFC1918 / loopback / link-local straight to
// DIRECT via explicit CIDR (no geo database). no-resolve avoids forcing a DNS
// lookup just to evaluate an IP rule.
const mihomoPrivateDirectRules = `  - IP-CIDR,192.168.0.0/16,DIRECT,no-resolve
  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR,172.16.0.0/12,DIRECT,no-resolve
  - IP-CIDR,127.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR6,fc00::/7,DIRECT,no-resolve
  - IP-CIDR6,::1/128,DIRECT,no-resolve
  - DOMAIN-SUFFIX,lan,DIRECT
  - DOMAIN,localhost,DIRECT
`

func clashVLESS(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.UUID == "" {
		return nil, false
	}
	out := []string{
		"type: vless",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"uuid: " + yamlString(c.UUID),
		"udp: true",
	}
	if flow := strDefault(c.Flow, str(s["flow"])); flow != "" {
		out = append(out, "flow: "+yamlString(flow))
	}
	if pub := str(s["reality_public_key"]); pub != "" {
		out = append(out, "tls: true")
		out = append(out, "network: tcp")
		out = append(out, "client-fingerprint: chrome")
		if sni := str(s["sni"]); sni != "" {
			out = append(out, "servername: "+yamlString(sni))
		}
		out = append(out, "reality-opts:")
		out = append(out, "  public-key: "+yamlString(pub))
		if sids := strSlice(s["short_ids"]); len(sids) > 0 {
			out = append(out, "  short-id: "+yamlString(sids[0]))
		}
	} else if sni := str(s["sni"]); sni != "" {
		out = append(out, "tls: true")
		out = append(out, "servername: "+yamlString(sni))
	}
	return out, true
}

func clashHysteria2(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.Password == "" {
		return nil, false
	}
	// Mihomo / Clash Verge Rev reads `password:`, Stash 3.x reads `auth:`.
	// Emit both — Stash prefers `auth`, Mihomo falls through to `password`,
	// and neither complains about the extra field. Without `auth:` Stash
	// silently ignores the password and the connection drops at handshake.
	// Stash 3.3 spec (docs.stash.ws) lists only `password` for Hy2.
	// An extra `auth` field is treated as an unknown key by Stash's strict
	// YAML schema validator and the entire proxy entry is silently dropped
	// from the in-memory proxy table — Stash UI still shows the node but no
	// outbound traffic is ever generated. Mihomo / sing-box ignore unknown
	// keys, so we lose nothing by dropping it.
	out := []string{
		"type: hysteria2",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"password: " + yamlString(c.Password),
	}
	// Port hopping (Mihomo only): `ports` carries the spray range, `hop-interval`
	// the rotation period (seconds). The server nat-redirects the range back to
	// `port`. Stash's strict schema is uncertain on these keys, so the Stash
	// path (stashHysteria2) intentionally omits them.
	if start, end, ok := hopRange(s); ok {
		out = append(out, fmt.Sprintf("ports: %d-%d", start, end))
		out = append(out, "hop-interval: 30")
	}
	// Stash 3.x silently drops Hy2 QUIC Initial when SNI is an IP literal
	// (RFC 6066 forbids IP in SNI extension). Mihomo / Shadowrocket / sing-box
	// tolerate IP-SNI but Stash enforces the spec — QUIC ClientHello goes
	// out, server can't match a vhost, no TLS alert is returned. Substitute
	// the self-signed cert's CN placeholder; skip-cert-verify=true still
	// pacifies the cert chain check, but Stash needs a syntactically valid
	// hostname to construct the SNI extension.
	if sni := str(s["sni"]); sni != "" {
		if net.ParseIP(sni) != nil {
			sni = "edgenest.local"
		}
		out = append(out, "sni: "+yamlString(sni))
	}
	// Inline flow-style list (`alpn: [h3]`) — Stash's YAML parser handles both
	// block and flow style, but reusing the flow form sidesteps an
	// indentation pitfall where the block list got attached to whichever
	// field appeared next in the proxy entry.
	out = append(out, "alpn: [h3]")
	// Wizard autofill enables salamander obfs by default; clients need both
	// fields or the connection silently fails.
	if obfs := str(s["obfs"]); obfs != "" {
		out = append(out, "obfs: "+yamlString(obfs))
		if pw := str(s["obfs_password"]); pw != "" {
			out = append(out, "obfs-password: "+yamlString(pw))
		}
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		out = append(out, "skip-cert-verify: true")
	}
	return out, true
}

// stashHysteria2 renders a Hysteria2 proxy entry against Stash's documented
// schema (stash.wiki/en/proxy-protocols/proxy-types).
//
// The official sample (verified by webfetch) lists exactly:
//
//	name, type, server, port, auth, fast-open, obfs, obfs-password,
//	sni, skip-cert-verify, up-speed, down-speed
//
// Any field outside that set is treated as an unknown key by Stash's strict
// YAML schema and the proxy entry is silently dropped from the in-memory
// proxy table — the UI still lists the node but no outbound traffic is ever
// generated, and the server-side hysteria2 inbound only sees QUIC Initial
// retries with no follow-on stream multiplex.
//
// Differences vs clashHysteria2 (Mihomo dialect):
//   - `auth:` instead of `password:` (Mihomo reads password, Stash reads auth)
//   - `up-speed:` / `down-speed:` instead of Mihomo's `up:` / `down:`
//   - No `alpn:` (Stash hard-codes h3 internally; an explicit `alpn:` is
//     an unknown key and triggers the silent drop above)
//   - `fast-open: true` (Stash-only optimisation, halves the first-byte RTT)
func stashHysteria2(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.Password == "" {
		return nil, false
	}
	out := []string{
		"type: hysteria2",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"auth: " + yamlString(c.Password),
		"fast-open: true",
	}
	if obfs := str(s["obfs"]); obfs != "" {
		out = append(out, "obfs: "+yamlString(obfs))
		if pw := str(s["obfs_password"]); pw != "" {
			out = append(out, "obfs-password: "+yamlString(pw))
		}
	}
	if sni := str(s["sni"]); sni != "" {
		if net.ParseIP(sni) != nil {
			sni = "edgenest.local"
		}
		out = append(out, "sni: "+yamlString(sni))
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		out = append(out, "skip-cert-verify: true")
	}
	out = append(out, "up-speed: 100", "down-speed: 100")
	return out, true
}

func clashTrojan(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.Password == "" {
		return nil, false
	}
	out := []string{
		"type: trojan",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"password: " + yamlString(c.Password),
		"udp: true",
	}
	if sni := str(s["sni"]); sni != "" {
		out = append(out, "sni: "+yamlString(sni))
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		out = append(out, "skip-cert-verify: true")
	}
	return out, true
}

func clashShadowsocks(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.Password == "" {
		return nil, false
	}
	method := strDefault(str(s["method"]), "2022-blake3-aes-128-gcm")
	return []string{
		"type: ss",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"cipher: " + yamlString(method),
		"password: " + yamlString(c.Password),
		"udp: true",
	}, true
}

// Mihomo (Clash Meta) ships first-class support for the protocols below; older
// stock Clash will ignore unknown `type:` values. Tested shapes are taken from
// https://wiki.metacubex.one/config/proxies/.

func clashVLESSWS(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.UUID == "" {
		return nil, false
	}
	out := []string{
		"type: vless",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"uuid: " + yamlString(c.UUID),
		"udp: true",
		"network: ws",
	}
	if str(s["tls_cert_path"]) != "" {
		out = append(out, "tls: true")
		if sni := str(s["sni"]); sni != "" {
			out = append(out, "servername: "+yamlString(sni))
		}
		out = append(out, "client-fingerprint: chrome")
		// Mirror the URI emitter's self-signed bypass: until ACME provisions
		// a real cert, the bootstrap CN=edgenest.local cert is on disk and
		// Mihomo / Stash will reject the handshake without skip-cert-verify.
		if str(s["acme_managed"]) != "true" {
			out = append(out, "skip-cert-verify: true")
		}
	}
	out = append(out, "ws-opts:")
	out = append(out, "  path: "+yamlString(strDefault(str(s["ws_path"]), "/")))
	if h := str(s["ws_host"]); h != "" {
		out = append(out, "  headers:")
		out = append(out, "    Host: "+yamlString(h))
	}
	return out, true
}

// VLESS-XHTTP rides Mihomo's `network: h2`-style xhttp transport. Reality and
// TLS are both representable.
func clashVLESSXHTTP(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.UUID == "" {
		return nil, false
	}
	out := []string{
		"type: vless",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"uuid: " + yamlString(c.UUID),
		"udp: true",
		"network: xhttp",
	}
	security := strDefault(str(s["security"]), "reality")
	switch security {
	case "reality":
		if pub := str(s["reality_public_key"]); pub != "" {
			out = append(out, "tls: true")
			if sni := str(s["sni"]); sni != "" {
				out = append(out, "servername: "+yamlString(sni))
			}
			out = append(out, "client-fingerprint: chrome")
			out = append(out, "reality-opts:")
			out = append(out, "  public-key: "+yamlString(pub))
			if sids := strSlice(s["short_ids"]); len(sids) > 0 {
				out = append(out, "  short-id: "+yamlString(sids[0]))
			}
		}
	case "tls":
		out = append(out, "tls: true")
		out = append(out, "client-fingerprint: chrome")
		if sni := str(s["sni"]); sni != "" {
			out = append(out, "servername: "+yamlString(sni))
		}
		if str(s["acme_managed"]) != "true" {
			out = append(out, "skip-cert-verify: true")
		}
	}
	out = append(out, "xhttp-opts:")
	// `mode: auto` lets the client negotiate stream-up vs packet-up. Without
	// it Hiddify's xhttp parser silently picks packet-up which breaks
	// bidirectional uploads (Telegram, large form posts).
	out = append(out, "  mode: auto")
	out = append(out, "  path: "+yamlString(strDefault(str(s["xhttp_path"]), "/xhttp")))
	if h := str(s["xhttp_host"]); h != "" {
		out = append(out, "  host: "+yamlString(h))
	}
	return out, true
}

func clashTUIC(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.UUID == "" || c.Password == "" {
		return nil, false
	}
	// `version: 5` is required by Stash 3.x — without it Stash assumes TUIC
	// v4 (token-based auth) and the uuid+password combo fails the handshake
	// silently. Mihomo treats `version` as optional and defaults to v5, so
	// emitting it everywhere is safe.
	out := []string{
		"type: tuic",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"version: 5",
		"uuid: " + yamlString(c.UUID),
		"password: " + yamlString(c.Password),
		"congestion-controller: " + yamlString(strDefault(str(s["congestion_control"]), "bbr")),
		"alpn:",
		"  - h3",
		"udp-relay-mode: native",
	}
	if sni := str(s["sni"]); sni != "" {
		out = append(out, "sni: "+yamlString(sni))
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		out = append(out, "skip-cert-verify: true")
	}
	return out, true
}

func clashVMess(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.UUID == "" {
		return nil, false
	}
	out := []string{
		"type: vmess",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"uuid: " + yamlString(c.UUID),
		"alterId: 0",
		"cipher: auto",
		"udp: true",
		"network: ws",
	}
	if str(s["tls_cert_path"]) != "" {
		out = append(out, "tls: true")
		if sni := str(s["sni"]); sni != "" {
			out = append(out, "servername: "+yamlString(sni))
		}
		if str(s["acme_managed"]) != "true" {
			out = append(out, "skip-cert-verify: true")
		}
	}
	out = append(out, "ws-opts:")
	out = append(out, "  path: "+yamlString(strDefault(str(s["ws_path"]), "/")))
	if h := str(s["ws_host"]); h != "" {
		out = append(out, "  headers:")
		out = append(out, "    Host: "+yamlString(h))
	}
	return out, true
}

func clashSocks(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	out := []string{
		"type: socks5",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"udp: true",
	}
	// Wizard-shape inbounds stash the SOCKS5 auth handle in settings (because
	// client.Email is kept = req.ClientEmail so the resolver aggregates the
	// bundle). Encoders must read settings first or the client sends
	// `wizard@local` while sing-box server expects `socks-<hex>` → auth fails.
	user := strDefault(str(s["socks_user"]), strDefault(c.Email, str(s["username"])))
	pass := strDefault(str(s["socks_password"]), strDefault(c.Password, str(s["password"])))
	if user != "" && pass != "" {
		out = append(out, "username: "+yamlString(user))
		out = append(out, "password: "+yamlString(pass))
	}
	return out, true
}

func clashAnyTLS(in *model.Inbound, c model.Client, s map[string]any, host string) ([]string, bool) {
	if c.Password == "" {
		return nil, false
	}
	out := []string{
		"type: anytls",
		"server: " + yamlString(host),
		fmt.Sprintf("port: %d", in.Port),
		"password: " + yamlString(c.Password),
		"udp: true",
	}
	if sni := str(s["sni"]); sni != "" {
		out = append(out, "sni: "+yamlString(sni))
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		out = append(out, "skip-cert-verify: true")
	}
	return out, true
}

// yamlString quotes a YAML scalar conservatively — anything with a colon,
// special char or leading symbol gets double-quoted with JSON-style escaping,
// which YAML 1.1 accepts as a valid double-quoted scalar.
func yamlString(s string) string {
	if s == "" {
		return `""`
	}
	needsQuote := false
	for _, r := range s {
		if r == ':' || r == '#' || r == '\'' || r == '"' || r == '\\' || r == '\n' ||
			r == '{' || r == '}' || r == '[' || r == ']' || r == ',' || r == '&' ||
			r == '*' || r == '?' || r == '|' || r == '>' || r == '!' || r == '%' ||
			r == '@' || r == '`' {
			needsQuote = true
			break
		}
	}
	switch s {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		needsQuote = true
	}
	if !needsQuote && (s[0] == '-' || s[0] == ' ') {
		needsQuote = true
	}
	if !needsQuote {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}
