// Package share builds client-importable proxy URIs (vless://, hysteria2://, ...)
// from inbound + client rows. The URIs are what end users paste into V2RayN,
// sing-box, Clash etc. — and what the subscription endpoint base64-wraps.
//
// DISCIPLINE: control plane only — no engine imports. We read the same
// Settings JSON the engine reads, but parse it locally.
package share

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// BuildURIs returns one URI per client of an inbound, suitable for pasting
// into a proxy client. host is the FQDN or IP the client should dial.
// Unsupported protocols return an empty slice (no error — let the rest
// of the bundle through).
func BuildURIs(in *model.Inbound, host string) ([]string, error) {
	if host == "" {
		return nil, fmt.Errorf("share host empty (set 'share_host' in settings)")
	}
	var settings map[string]any
	if in.Settings != "" {
		if err := json.Unmarshal([]byte(in.Settings), &settings); err != nil {
			return nil, fmt.Errorf("parse inbound %s settings: %w", in.Tag, err)
		}
	}
	if settings == nil {
		settings = map[string]any{}
	}

	var out []string
	for _, c := range in.Clients {
		if !c.Enabled {
			continue
		}
		var (
			uri string
			err error
		)
		switch in.Type {
		case "vless":
			uri, err = buildVLESS(in, c, settings, host)
		case "vless-xhttp":
			uri, err = buildVLESSXHTTP(in, c, settings, host)
		case "vless-ws":
			uri, err = buildVLESSWS(in, c, settings, host)
		case "hysteria2":
			uri, err = buildHysteria2(in, c, settings, host)
		case "trojan":
			uri, err = buildTrojan(in, c, settings, host)
		case "shadowsocks":
			uri, err = buildShadowsocks(in, c, settings, host)
		case "anytls":
			uri, err = buildAnyTLS(in, c, settings, host)
		case "tuic":
			uri, err = buildTUIC(in, c, settings, host)
		case "vmess", "vmess-ws":
			uri, err = buildVMess(in, c, settings, host)
		case "socks":
			uri, err = buildSocks(in, c, settings, host)
		default:
			continue // unsupported — silent skip
		}
		if err != nil {
			return nil, fmt.Errorf("build %s uri: %w", in.Type, err)
		}
		if uri != "" {
			out = append(out, uri)
		}
	}
	return out, nil
}

// BuildURIForClient renders a single client's URI on a single inbound.
// Returns "" + nil if the protocol is not supported.
func BuildURIForClient(in *model.Inbound, c *model.Client, host string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("share host empty")
	}
	var settings map[string]any
	if in.Settings != "" {
		if err := json.Unmarshal([]byte(in.Settings), &settings); err != nil {
			return "", err
		}
	}
	if settings == nil {
		settings = map[string]any{}
	}
	switch in.Type {
	case "vless":
		return buildVLESS(in, *c, settings, host)
	case "vless-xhttp":
		return buildVLESSXHTTP(in, *c, settings, host)
	case "vless-ws":
		return buildVLESSWS(in, *c, settings, host)
	case "hysteria2":
		return buildHysteria2(in, *c, settings, host)
	case "trojan":
		return buildTrojan(in, *c, settings, host)
	case "shadowsocks":
		return buildShadowsocks(in, *c, settings, host)
	case "anytls":
		return buildAnyTLS(in, *c, settings, host)
	case "tuic":
		return buildTUIC(in, *c, settings, host)
	case "vmess", "vmess-ws":
		return buildVMess(in, *c, settings, host)
	case "socks":
		return buildSocks(in, *c, settings, host)
	default:
		return "", nil
	}
}

// vless://<uuid>@<host>:<port>?encryption=none&security=reality&sni=<sni>&fp=chrome
//
//	&pbk=<pub>&sid=<sid>&type=tcp&flow=<flow>#<remark>
func buildVLESS(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.UUID == "" {
		return "", fmt.Errorf("vless client %q has no uuid", c.Email)
	}
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("type", "tcp")
	// Raw-TCP VLESS carries no HTTP obfuscation header; headerType=none is the
	// default every client assumes, but emitting it explicitly completes the
	// Reality field set (type=tcp&headerType=none) the way reference configs
	// do, so a client that reads headerType strictly doesn't fall back to a
	// mismatched default.
	q.Set("headerType", "none")

	if pub := str(s["reality_public_key"]); pub != "" {
		q.Set("security", "reality")
		q.Set("pbk", pub)
		q.Set("fp", "chrome")
		if sni := str(s["sni"]); sni != "" {
			q.Set("sni", sni)
		}
		if sids := strSlice(s["short_ids"]); len(sids) > 0 {
			q.Set("sid", sids[0])
		}
	} else if sni := str(s["sni"]); sni != "" {
		q.Set("security", "tls")
		q.Set("sni", sni)
	}
	// Reality + raw TCP defaults to Vision flow if the client row didn't
	// override it. Without `flow=xtls-rprx-vision` the client connects but
	// loses Vision masking, which is the entire point of pairing Reality
	// with VLESS-TCP. Only force the default for the Reality path so plain
	// VLESS-TCP without security stays untouched.
	flow := strDefault(c.Flow, str(s["flow"]))
	if flow == "" && q.Get("security") == "reality" {
		flow = "xtls-rprx-vision"
	}
	if flow != "" {
		q.Set("flow", flow)
	}

	// XUDP packet encoding: makes Shadowrocket display "XUDP" (Full Cone UDP
	// mux) instead of plain "UDP", matching what our sing-box subscription
	// already sets (singbox.go packet_encoding). Non-standard query key (not
	// in XTLS sharing standard #716) but read by Shadowrocket and mihomo;
	// silently ignored by v2rayNG / sing-box / NekoBox. Verbatim-confirmed
	// against Sub-Store producer (uri.js: udp→&packetEncoding=xudp) and
	// mihomo convert/v.go (query.Get("packetEncoding")). Our VLESS inbounds
	// all relay UDP, so xudp is always the right hint.
	q.Set("packetEncoding", "xudp")

	remark := url.PathEscape(remarkOf(in, c))
	u := fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		c.UUID, hostForURI(host), in.Port, q.Encode(), remark)
	return u, nil
}

// hysteria2://<password>@<host>:<port>?sni=<sni>&insecure=1&obfs=salamander&obfs-password=<p>#<remark>
//
// Shadowrocket / NekoBox / Hiddify all require the obfs parameters when the
// server has obfs enabled — without them the client never completes the
// handshake (no error surfaced, just "connection failed"). Param names
// follow the Hysteria2 URI scheme: `obfs` (the obfuscator name) and
// `obfs-password` (the shared password). The sing-box server expects the
// password under settings["obfs_password"] (underscore), so translate.
func buildHysteria2(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.Password == "" {
		return "", fmt.Errorf("hysteria2 client %q has no password", c.Email)
	}
	q := url.Values{}
	if sni := str(s["sni"]); sni != "" {
		q.Set("sni", sni)
	} else if dom := str(s["domain"]); dom != "" {
		q.Set("sni", dom)
	}
	if obfsName := str(s["obfs"]); obfsName != "" {
		q.Set("obfs", obfsName)
		if pw := str(s["obfs_password"]); pw != "" {
			q.Set("obfs-password", pw)
		}
	}
	// Self-signed cert → insecure=1; an ACME-managed cert (acme_managed=true,
	// set by the wizard when it issues a real cert) is browser-trusted, so the
	// client verifies normally. Same gate shape as every other encoder here.
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		q.Set("insecure", "1")
		// Cert pinning upgrade: insecure=1 alone disables ALL cert validation
		// (MITM-able). Hysteria2's VerifyPeerCertificate callback still runs
		// under insecure, so adding pinSHA256 makes the client trust ONLY this
		// exact self-signed cert — MITM-proof while still bypassing the CA-chain
		// check a self-signed cert can't pass. Both fields coexist
		// (apernet/hysteria cert.go warns insecure is "only MITM-resistant when
		// paired with pinSHA256"). Degrades silently to bare insecure=1 if the
		// cert isn't readable.
		if pin := certPinSHA256(str(s["tls_cert_path"])); pin != "" {
			q.Set("pinSHA256", pin)
		}
	}
	// Port hopping: when a range is configured, the authority port slot carries
	// the RANGE (host:START-END) — Hysteria2's official URI scheme reads the
	// range there (apernet/hysteria portunion), so Hysteria core / sing-box /
	// Mihomo all honour it. mport=START-END is the Shadowrocket / NekoBox
	// dialect for the same thing; emit both for maximum client coverage. The
	// server-side nat REDIRECT (firewall.ApplyPortHops) maps the range back to
	// in.Port. Without a range, the authority is the single listen port.
	portPart := strconv.Itoa(in.Port)
	if start, end, ok := hopRange(s); ok {
		portPart = fmt.Sprintf("%d-%d", start, end)
		q.Set("mport", portPart)
	}
	remark := url.PathEscape(remarkOf(in, c))
	u := fmt.Sprintf("hysteria2://%s@%s:%s?%s#%s",
		url.QueryEscape(c.Password), hostForURI(host), portPart, q.Encode(), remark)
	return u, nil
}

// trojan://<password>@<host>:<port>?sni=<sni>&type=tcp&security=tls&allowInsecure=1#<remark>
//
// Self-signed cert → allowInsecure=1 (Shadowrocket / Hiddify / V2RayN all
// honour this), letting the client skip CA validation. ACME-issued cert
// clears the flag so real-CA chains are validated normally.
func buildTrojan(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.Password == "" {
		return "", fmt.Errorf("trojan client %q has no password", c.Email)
	}
	q := url.Values{}
	q.Set("type", "tcp")
	q.Set("security", "tls")
	// Most clients are happy with no ALPN here, but Shadowrocket and Stash
	// default to h2-only when the URI omits it, and an sing-box server with
	// only `http/1.1` in its ALPN list will reject the handshake. Listing
	// both keeps the negotiation flexible regardless of server config.
	q.Set("alpn", "h2,http/1.1")
	if sni := str(s["sni"]); sni != "" {
		q.Set("sni", sni)
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		q.Set("allowInsecure", "1")
	}
	remark := url.PathEscape(remarkOf(in, c))
	u := fmt.Sprintf("trojan://%s@%s:%d?%s#%s",
		url.QueryEscape(c.Password), hostForURI(host), in.Port, q.Encode(), remark)
	return u, nil
}

// vless://<uuid>@<host>:<port>?encryption=none&type=xhttp&path=/x&host=...
//
//	&security=reality&pbk=<pub>&sni=<sni>&sid=<sid>&fp=chrome#<remark>
//
// vless-xhttp shares the vless:// scheme but the transport is xhttp (HTTP/2-style
// duplex over TLS / Reality). Clients (sing-box-core / v2rayN ≥ 7.x) look at
// `type=xhttp` to switch transports.
func buildVLESSXHTTP(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.UUID == "" {
		return "", fmt.Errorf("vless-xhttp client %q has no uuid", c.Email)
	}
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("type", "xhttp")
	// `mode=auto` makes xray-core / Mihomo / v2rayN pick stream-up for
	// bidirectional traffic and packet-up for short bursts. Without it,
	// hiddify-sing-box (the engine behind Hiddify) silently picks
	// packet-up for everything, breaking large uploads (Telegram media,
	// Cloud Drive, RDP). Settings can override via `xhttp_mode`.
	q.Set("mode", strDefault(str(s["xhttp_mode"]), "auto"))
	if path := str(s["xhttp_path"]); path != "" {
		q.Set("path", path)
	} else {
		q.Set("path", "/xhttp")
	}
	if xh := str(s["xhttp_host"]); xh != "" {
		q.Set("host", xh)
	}

	security := strDefault(str(s["security"]), "reality")
	switch security {
	case "reality":
		if pub := str(s["reality_public_key"]); pub != "" {
			q.Set("security", "reality")
			q.Set("pbk", pub)
			q.Set("fp", "chrome")
			if sni := str(s["sni"]); sni != "" {
				q.Set("sni", sni)
			}
			if sids := strSlice(s["short_ids"]); len(sids) > 0 {
				q.Set("sid", sids[0])
			}
		}
	case "tls":
		q.Set("security", "tls")
		q.Set("fp", "chrome")
		if sni := str(s["sni"]); sni != "" {
			q.Set("sni", sni)
		}
		// Same self-signed fallback as VLESS-WS / VMess-WS: until ACME runs,
		// the wizard ships a CN=edgenest.local cert and the client must skip
		// validation or it fails the TLS handshake silently.
		if str(s["acme_managed"]) != "true" {
			q.Set("allowInsecure", "1")
		}
	}
	// flow (xtls-rprx-vision) is incompatible with xhttp transport — xray-core
	// rejects the outbound at startup. Strip it unconditionally for vless-xhttp,
	// even if the user / wizard set it on the client row (Reality-TCP defaults
	// often leak in here).
	remark := url.PathEscape(remarkOf(in, c))
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		c.UUID, hostForURI(host), in.Port, q.Encode(), remark), nil
}

// anytls://<password>@<host>:<port>?sni=<sni>&insecure=1#<remark>
// AnyTLS is a newer protocol; the URI shape mirrors trojan's and is what
// shadowrocket / hiddify use at the time of writing. Self-signed → insecure=1.
func buildAnyTLS(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.Password == "" {
		return "", fmt.Errorf("anytls client %q has no password", c.Email)
	}
	q := url.Values{}
	if sni := str(s["sni"]); sni != "" {
		q.Set("sni", sni)
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		q.Set("insecure", "1")
	}
	remark := url.PathEscape(remarkOf(in, c))
	// The path slash before `?` matches the canonical AnyTLS URI scheme
	// example (anytls-go/docs/uri_scheme.md): `anytls://pw@host:port/?...`.
	// Strict RFC 3986 parsers (Hiddify, sing-box CLI) reject host:port?query
	// without the path delimiter.
	return fmt.Sprintf("anytls://%s@%s:%d/?%s#%s",
		url.QueryEscape(c.Password), hostForURI(host), in.Port, q.Encode(), remark), nil
}

// ss://base64(method:password)@host:port#remark
// Method is read from Settings["method"], defaults to 2022-blake3-aes-128-gcm
// (matches the inbound autofill — keep them in sync or Shadowrocket decodes
// the URI with the wrong cipher).
func buildShadowsocks(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.Password == "" {
		return "", fmt.Errorf("ss client %q has no password", c.Email)
	}
	method := strDefault(str(s["method"]), "2022-blake3-aes-128-gcm")
	userinfo := encodeUserInfoSS(method, c.Password)
	remark := url.PathEscape(remarkOf(in, c))
	u := fmt.Sprintf("ss://%s@%s:%s#%s",
		userinfo, hostForURI(host), strconv.Itoa(in.Port), remark)
	return u, nil
}

// vless://<uuid>@<host>:<port>?encryption=none&type=ws&path=<path>&security=tls&sni=<sni>#<remark>
// Plain VLESS over WebSocket. TLS is optional; we flip it on when the inbound
// has a tls cert configured (operator runs WS behind a CDN with TLS upstream).
func buildVLESSWS(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.UUID == "" {
		return "", fmt.Errorf("vless-ws client %q has no uuid", c.Email)
	}
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("type", "ws")
	q.Set("path", strDefault(str(s["ws_path"]), "/"))
	if wsHost := str(s["ws_host"]); wsHost != "" {
		q.Set("host", wsHost)
	}
	if str(s["tls_cert_path"]) != "" {
		q.Set("security", "tls")
		if sni := str(s["sni"]); sni != "" {
			q.Set("sni", sni)
		}
		q.Set("fp", "chrome")
		// Wizard inbounds default to a bootstrap self-signed cert (CN=edgenest.local)
		// until the operator issues an ACME cert from the Certs page. Without
		// allowInsecure=1 the client rejects the CN mismatch on first connect
		// (silent "no route" in Shadowrocket / sing-box) and the user can't tell
		// why. acme_managed=true clears the bypass once a real cert lands.
		if str(s["acme_managed"]) != "true" {
			q.Set("allowInsecure", "1")
		}
	}
	// XUDP packet encoding — see buildVLESS. Keeps Shadowrocket on Full Cone
	// UDP mux; ignored by clients that don't read the key.
	q.Set("packetEncoding", "xudp")
	remark := url.PathEscape(remarkOf(in, c))
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		c.UUID, hostForURI(host), in.Port, q.Encode(), remark), nil
}

// tuic://<uuid>:<password>@<host>:<port>?congestion_control=bbr&alpn=h3&sni=<sni>#<remark>
// TUIC v5 — both UUID and password are required.
func buildTUIC(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.UUID == "" {
		return "", fmt.Errorf("tuic client %q has no uuid", c.Email)
	}
	if c.Password == "" {
		return "", fmt.Errorf("tuic client %q has no password", c.Email)
	}
	q := url.Values{}
	cc := strDefault(str(s["congestion_control"]), "bbr")
	// sing-box parses `congestion_control` (underscore); Mihomo / Clash Meta
	// parse `congestion-controller` (hyphen). Emit both so a single URI
	// imports cleanly in either family without falling back to cubic.
	q.Set("congestion_control", cc)
	q.Set("congestion-controller", cc)
	q.Set("alpn", "h3")
	// Same snake/kebab split for udp relay mode — sing-box family reads
	// `udp_relay_mode`, Mihomo / Stash / Clash Verge Rev read
	// `udp-relay-mode`. Emit both so a single subscription works.
	q.Set("udp_relay_mode", "native")
	q.Set("udp-relay-mode", "native")
	if sni := str(s["sni"]); sni != "" {
		q.Set("sni", sni)
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		// Dialect split, same as the snake/kebab params above:
		// `allow_insecure` is what Hiddify / NekoBox parse, `insecure` is
		// the only spelling Shadowrocket's TUIC importer credits (Xboard's
		// Shadowrocket emitter uses it; verified on-device — allow_insecure
		// alone imports fine but leaves cert verification on, and the
		// self-signed handshake then fails silently).
		q.Set("allow_insecure", "1")
		q.Set("insecure", "1")
	}
	remark := url.PathEscape(remarkOf(in, c))
	return fmt.Sprintf("tuic://%s:%s@%s:%d?%s#%s",
		c.UUID, url.QueryEscape(c.Password), hostForURI(host), in.Port, q.Encode(), remark), nil
}

// vmess://base64({...JSON...}) — V2RayN format. Transport is always WS for us
// (the inbound type is "vmess" which our engine renders as VMess-over-WS).
func buildVMess(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	if c.UUID == "" {
		return "", fmt.Errorf("vmess client %q has no uuid", c.Email)
	}
	// `port` as a JSON number (not string) — sing-box mobile / SFA / SFI use
	// strict JSON parsing and reject string-typed ports. V2RayN 7.x and
	// NekoBox accept either, but int is what the V2Ray URI scheme actually
	// specifies.
	cfg := map[string]any{
		"v":    "2",
		"ps":   remarkOf(in, c),
		"add":  host,
		"port": in.Port,
		"id":   c.UUID,
		"aid":  0,
		"scy":  "auto",
		"net":  "ws",
		"type": "none",
		"host": str(s["ws_host"]),
		"path": strDefault(str(s["ws_path"]), "/"),
		"tls":  "",
	}
	if str(s["tls_cert_path"]) != "" {
		cfg["tls"] = "tls"
		// Empty ALPN keeps the client (especially Shadowrocket 2.2.80) from
		// hard-advertising h2 only. Some sing-box WS-over-TLS servers do not
		// advertise h2 in their ALPN list, so an h2-only client fails the
		// handshake silently. The empty string tells the parser to skip ALPN.
		cfg["alpn"] = ""
		if sni := str(s["sni"]); sni != "" {
			cfg["sni"] = sni
		}
		// Match the VLESS-WS / Trojan / TUIC fallback: self-signed cert →
		// instruct client to skip CN verification. The vmess base64-JSON
		// spec has NO standard insecure field, so we emit every alias with
		// real-world parsers behind it: `verify_cert` (V2RayN / NekoBox),
		// `skip-cert-verify` (Clash-dialect importers), `allowInsecure`
		// (3x-ui v2 wire form), and lowercase `allowinsecure` for Hiddify.
		// Hiddify delegates vmess:// parsing to github.com/hiddify/ray2sing,
		// whose getTLSOptions reads ONLY the exact lowercase keys `insecure`
		// / `allowinsecure` via a case-sensitive raw map lookup — the vmess
		// path keeps raw JSON keys (no lowercasing), so camelCase
		// `allowInsecure` is silently dropped and self-signed VMess-WS fails
		// the TLS handshake (x509 unknown authority), confirmed by reading
		// ray2sing/common.go (2026-06-12). Shadowrocket honours none of these
		// — its importer only reads allowInsecure from the query-param vmess
		// flavour, so self-signed VMess there still needs the node's
		// "Allow Insecure" toggle until an ACME cert removes the need.
		if str(s["acme_managed"]) != "true" {
			cfg["verify_cert"] = false
			cfg["skip-cert-verify"] = true
			cfg["allowInsecure"] = true
			cfg["allowinsecure"] = "true"
			// v2rayNG (Android, com.v2ray.ang) ignores ALL of the above on the
			// base64-JSON path: VmessQRCode.kt has a plain `var insecure: String`
			// (no @SerializedName) and VmessFmt.kt maps only string "1" -> true.
			// Without this key v2rayNG defaults verification ON and self-signed
			// VMess-WS fails the TLS handshake (confirmed: full-mode bare-IP node,
			// vmess-ws the only protocol that couldn't proxy on v2rayNG while
			// vless/trojan worked via their query-param &allowInsecure=1).
			// Source: 2dust/v2rayNG VmessQRCode.kt + VmessFmt.kt, commit c0141225
			// (2025-11-09), shipped v1.10.28. NOTE string "1", not bool — and only
			// honoured by v2rayNG >= 1.10.28; older builds have no link field.
			cfg["insecure"] = "1"
		}
	}
	raw, _ := json.Marshal(cfg)
	return "vmess://" + base64.StdEncoding.EncodeToString(raw), nil
}

// socks://<base64(user:pass)>@<host>:<port>#<remark> when authenticated,
// otherwise socks://<host>:<port>#<remark>.
//
// Base64 userinfo + bare `socks://` scheme is the wire form every generator
// that targets URI-list importers actually ships — v2rayN's own ToUri,
// Sub-Store's uri producer and Xboard's Shadowrocket emitter all agree.
// The previous plain `socks5://user:pass@` flavour imports in Shadowrocket
// but the credentials don't survive the import (auth then fails on every
// connect, verified on-device); v2rayN only tolerates plain userinfo as a
// parse fallback. Clash-family clients never see this builder — they get
// the YAML encoder — so the base64 form has no downside on our matrix.
func buildSocks(in *model.Inbound, c model.Client, s map[string]any, host string) (string, error) {
	remark := url.PathEscape(remarkOf(in, c))
	// Wizard-shape inbounds stash the SOCKS5 auth handle in settings so
	// client.Email can stay aligned with the rest of the bundle (resolver
	// uses client.Email to aggregate the subscription). Manually-created
	// inbounds fall back to client.Email / client.Password.
	user := strDefault(str(s["socks_user"]), strDefault(c.Email, str(s["username"])))
	pass := strDefault(str(s["socks_password"]), strDefault(c.Password, str(s["password"])))
	if user != "" && pass != "" {
		userinfo := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		return fmt.Sprintf("socks://%s@%s:%d#%s",
			url.QueryEscape(userinfo), hostForURI(host), in.Port, remark), nil
	}
	return fmt.Sprintf("socks://%s:%d#%s", hostForURI(host), in.Port, remark), nil
}

func remarkOf(in *model.Inbound, c model.Client) string {
	switch {
	case in.Remark != "":
		// Operator-set label is canonical — don't trail it with the client
		// email, that's noise in the proxy client's node list.
		return in.Remark
	case c.Email != "":
		return in.Tag + "-" + c.Email
	default:
		return in.Tag
	}
}
