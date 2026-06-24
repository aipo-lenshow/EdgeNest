package share

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// EncodeSingbox renders a subscription bundle as a sing-box client config
// (the JSON the sing-box CLI / Android app / SFI eats directly). We emit a
// minimal config: outbounds for each proxy + a selector group + a direct +
// dns block. Routing stays default — picking rules is the user's call.
func EncodeSingbox(bundles []Bundle, host string) string {
	type outbound = map[string]any
	outbounds := make([]outbound, 0, len(bundles)+3)
	tags := make([]string, 0, len(bundles))

	for _, b := range bundles {
		var settings map[string]any
		if b.Inbound.Settings != "" {
			_ = json.Unmarshal([]byte(b.Inbound.Settings), &settings)
		}
		if settings == nil {
			settings = map[string]any{}
		}
		tag := remarkOf(b.Inbound, b.Client)
		switch b.Inbound.Type {
		case "vless":
			if o := singboxVLESS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "vless-ws":
			if o := singboxVLESSWS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "vless-xhttp":
			if o := singboxVLESSXHTTP(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "hysteria2":
			if o := singboxHysteria2(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "trojan":
			if o := singboxTrojan(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "shadowsocks":
			if o := singboxShadowsocks(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "tuic":
			if o := singboxTUIC(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "vmess", "vmess-ws":
			if o := singboxVMess(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "socks":
			if o := singboxSocks(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		case "anytls":
			if o := singboxAnyTLS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); o != nil {
				outbounds = append(outbounds, o)
				tags = append(tags, tag)
			}
		}
	}

	selectorTags := append([]string{"auto"}, tags...)
	urltestTags := append([]string{}, tags...)
	if len(urltestTags) == 0 {
		urltestTags = []string{"direct"}
	}

	outbounds = append(outbounds,
		outbound{"type": "selector", "tag": "EdgeNest", "outbounds": selectorTags, "default": firstNonEmpty(tags, "auto")},
		outbound{"type": "urltest", "tag": "auto", "outbounds": urltestTags, "url": "https://www.gstatic.com/generate_204", "interval": "5m"},
		outbound{"type": "direct", "tag": "direct"},
	)
	// `block` and `dns` outbounds intentionally omitted. They are legacy
	// special outbounds; sing-box 1.11+ migrated them to rule_action: reject
	// and rule_action: hijack-dns respectively. SFI / SFA fire a
	// "legacy special outbounds will be removed" deprecation warning whenever
	// either appears, even with no rule referencing it. We use the modern
	// rule_action form below (no `reject` rule because every byte is meant
	// to ride EdgeNest), so neither outbound is needed.
	// https://sing-box.sagernet.org/migration/#migrate-legacy-special-outbounds-to-rule-actions

	// Full mobile sing-box profile. SFI/SFA/SFM/SFT/Karing/NekoBox/Hiddify
	// mobile all run as a bare NetworkExtension / VpnService provider — they
	// do not auto-inject tun/dns/route. A subscription that only declares
	// outbounds imports cleanly but the resulting VPN session has no inbound
	// to capture system traffic, so every protocol "shows green" and
	// silently relays nothing.
	//
	// Pieces required for a working mobile profile:
	//   • inbounds.tun         — captures OS traffic; we use the 1.10+
	//     `address: [...]` array (the old `inet4_address: "..."` string
	//     field is deprecated and triggers a warning on SFI 1.11.x).
	//   • dns.servers + detour — encrypts the resolver (TLS to 8.8.8.8) and
	//     forces it through the proxy.
	//   • route.rules          — `action: sniff` for protocol/hostname
	//     sniffing (replaces inbound `sniff: true`), then 2x
	//     `action: hijack-dns` for protocol:dns + port:53 (replaces the
	//     legacy `dns-out` outbound + outbound-style rule).
	//   • route.auto_detect_interface — REQUIRED on Android sing-box clients
	//     (SFA, hiddify-sing-box, etc.) for TUN-mode outbound NIC binding.
	//     Without it Android 1.13.x routes outbound packets back through
	//     TUN and the kernel kills the connection with ECONNABORTED.
	//     iOS NEPacketTunnelProvider does system-level routing and ignores
	//     the field; it's been in sing-box since v1.0 (commit 638f8a5,
	//     2022-07-10), never deprecated, and is the documented mobile
	//     profile standard. (Removing this field was a v0.08.0606 mistake
	//     that broke Android proxying — re-added 2026-06-06.)
	//     https://sing-box.sagernet.org/configuration/route/
	//     https://sing-box.sagernet.org/manual/proxy/client/
	//
	// DNS server shape: legacy `{"address":"tls://8.8.8.8"}`. The 1.12+
	// typed shape (`type:"tls", server:"8.8.8.8"`) fatally rejects on SFI
	// 1.11.x App Store builds with `unknown field "server"`. Until Apple
	// reviews a newer SFI we stay legacy and eat a cosmetic deprecation
	// warning on SFA 1.13.x (will fail when 1.14.0 GA removes legacy —
	// switch to a UA-sniffed dual emit at that point).
	cfg := map[string]any{
		"log": map[string]any{"level": "info"},
		"dns": map[string]any{
			"servers": []map[string]any{
				{"tag": "google", "address": "tls://8.8.8.8", "detour": "EdgeNest"},
			},
		},
		"inbounds": []map[string]any{
			{
				"type":         "tun",
				"tag":          "tun-in",
				"address":      []string{"172.19.0.1/30"},
				"auto_route":   true,
				"strict_route": true,
				"stack":        "gvisor",
			},
		},
		"outbounds": outbounds,
		"route": map[string]any{
			"auto_detect_interface": true,
			"rules": []map[string]any{
				{"action": "sniff"},
				{"protocol": "dns", "action": "hijack-dns"},
				{"port": 53, "action": "hijack-dns"},
				// Private / LAN / loopback go DIRECT, not through EdgeNest.
				// With tun capturing all traffic and final=EdgeNest, a request
				// to 192.168.x / 10.x (the user's own router / NAS / printer)
				// would otherwise be relayed to the VPS, which can't reach the
				// client's LAN → silent failure. ip_is_private (sing-box 1.8+)
				// is the geo-free private matcher; no mmdb, no region logic.
				{"ip_is_private": true, "action": "route", "outbound": "direct"},
			},
			"final": "EdgeNest",
		},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return string(b) + "\n"
}

func firstNonEmpty(tags []string, fallback string) string {
	if len(tags) > 0 {
		return tags[0]
	}
	return fallback
}

func singboxVLESS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	if c.UUID == "" {
		return nil
	}
	o := map[string]any{
		"type":            "vless",
		"tag":             tag,
		"server":          host,
		"server_port":     in.Port,
		"uuid":            c.UUID,
		"packet_encoding": "xudp",
	}
	if flow := strDefault(c.Flow, str(s["flow"])); flow != "" {
		o["flow"] = flow
	}
	if pub := str(s["reality_public_key"]); pub != "" {
		tls := map[string]any{
			"enabled":     true,
			"server_name": str(s["sni"]),
			"reality": map[string]any{
				"enabled":    true,
				"public_key": pub,
			},
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": "chrome",
			},
		}
		if sids := strSlice(s["short_ids"]); len(sids) > 0 {
			tls["reality"].(map[string]any)["short_id"] = sids[0]
		}
		o["tls"] = tls
	} else if sni := str(s["sni"]); sni != "" {
		o["tls"] = map[string]any{"enabled": true, "server_name": sni}
	}
	return o
}

func singboxHysteria2(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	if c.Password == "" {
		return nil
	}
	// Mirror the clash Hy2 server_name policy: SFI mobile / sing-box CLI both
	// place the SNI in the QUIC ClientHello; per RFC 6066 IP literals are
	// invalid in the SNI extension. Some clients (Stash, recent SFI) silently
	// drop the QUIC Initial when sni is an IP. Fall back to the self-signed
	// cert's CN placeholder; insecure=true keeps cert verification off.
	sni := strDefault(str(s["sni"]), str(s["domain"]))
	if sni != "" && net.ParseIP(sni) != nil {
		sni = "edgenest.local"
	}
	tls := map[string]any{
		"enabled":     true,
		"server_name": sni,
		"alpn":        []string{"h3"},
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		tls["insecure"] = true
	}
	o := map[string]any{
		"type":        "hysteria2",
		"tag":         tag,
		"server":      host,
		"server_port": in.Port,
		"password":    c.Password,
		"tls":         tls,
	}
	// Port hopping: sing-box clients read the range from server_ports, NOT from
	// a URI mport — the JSON-native field. Format is "start:end" with a COLON
	// (sing-box docs: `server_ports` example ["2080:3000"]); a hyphen makes
	// sing-box reject the outbound with "bad port range" and the whole config
	// fails to load. server_port stays as the canonical single port; server_ports
	// adds the spray range.
	if start, end, ok := hopRange(s); ok {
		o["server_ports"] = []string{fmt.Sprintf("%d:%d", start, end)}
	}
	// Wizard autofill enables salamander obfs by default; sing-box clients
	// need the obfs object or the handshake fails without a clear error.
	if obfs := str(s["obfs"]); obfs != "" {
		o["obfs"] = map[string]any{
			"type":     obfs,
			"password": str(s["obfs_password"]),
		}
	}
	return o
}

func singboxTrojan(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	if c.Password == "" {
		return nil
	}
	o := map[string]any{
		"type":        "trojan",
		"tag":         tag,
		"server":      host,
		"server_port": in.Port,
		"password":    c.Password,
	}
	tls := map[string]any{
		"enabled":     true,
		"server_name": str(s["sni"]),
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		tls["insecure"] = true
	}
	o["tls"] = tls
	return o
}

func singboxShadowsocks(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	if c.Password == "" {
		return nil
	}
	method := strDefault(str(s["method"]), "2022-blake3-aes-128-gcm")
	return map[string]any{
		"type":        "shadowsocks",
		"tag":         tag,
		"server":      host,
		"server_port": in.Port,
		"method":      method,
		"password":    c.Password,
	}
}

func singboxVLESSWS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	if c.UUID == "" {
		return nil
	}
	o := map[string]any{
		"type":            "vless",
		"tag":             tag,
		"server":          host,
		"server_port":     in.Port,
		"uuid":            c.UUID,
		"packet_encoding": "xudp",
		"transport":       buildSingboxWSTransport(s),
	}
	if str(s["tls_cert_path"]) != "" {
		tls := map[string]any{"enabled": true, "server_name": str(s["sni"])}
		// Until ACME provisions a real cert, the wizard's bootstrap
		// self-signed pair (CN=edgenest.local) is what's on disk.
		// sing-box outbound otherwise rejects the handshake silently.
		if str(s["acme_managed"]) != "true" {
			tls["insecure"] = true
		}
		o["tls"] = tls
	}
	return o
}

// buildSingboxWSTransport returns the transport map for VLESS-WS / VMess-WS
// outbounds, omitting the Host header entry when the inbound has no ws_host
// configured. Emitting `headers: {Host: ""}` makes CDN front-ends 421 the
// upgrade request (Host mismatch on an empty string), which surfaces as
// "connection refused" without any obvious clue in the client log.
func buildSingboxWSTransport(s map[string]any) map[string]any {
	t := map[string]any{
		"type": "ws",
		"path": strDefault(str(s["ws_path"]), "/"),
	}
	if h := str(s["ws_host"]); h != "" {
		t["headers"] = map[string]any{"Host": h}
	}
	return t
}

// singboxVLESSXHTTP intentionally returns nil: sing-box core does not implement
// the xhttp transport (it is an Xray-bespoke variant). Emitting `type: xhttp`
// makes Hiddify / sing-box GUI / SFI / Karing / NekoBox reject the whole
// config. Users on those clients fall back to other inbounds in the same
// subscription (Reality TCP, WS, Hy2, etc.). xray-core clients (v2rayN,
// Shadowrocket) still get the protocol via the URI encoder.
func singboxVLESSXHTTP(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	return nil
}

func singboxTUIC(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	if c.UUID == "" || c.Password == "" {
		return nil
	}
	tls := map[string]any{
		"enabled":     true,
		"server_name": str(s["sni"]),
		"alpn":        []string{"h3"},
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		tls["insecure"] = true
	}
	return map[string]any{
		"type":               "tuic",
		"tag":                tag,
		"server":             host,
		"server_port":        in.Port,
		"uuid":               c.UUID,
		"password":           c.Password,
		"congestion_control": strDefault(str(s["congestion_control"]), "bbr"),
		"udp_relay_mode":     "native",
		"zero_rtt_handshake": false,
		"tls":                tls,
	}
}

func singboxVMess(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	if c.UUID == "" {
		return nil
	}
	o := map[string]any{
		"type":            "vmess",
		"tag":             tag,
		"server":          host,
		"server_port":     in.Port,
		"uuid":            c.UUID,
		"security":        "auto",
		"alter_id":        0,
		"packet_encoding": "xudp",
		"transport":       buildSingboxWSTransport(s),
	}
	if str(s["tls_cert_path"]) != "" {
		tls := map[string]any{"enabled": true, "server_name": str(s["sni"])}
		// Until ACME provisions a real cert, the wizard's bootstrap
		// self-signed pair (CN=edgenest.local) is what's on disk.
		// sing-box outbound otherwise rejects the handshake silently.
		if str(s["acme_managed"]) != "true" {
			tls["insecure"] = true
		}
		o["tls"] = tls
	}
	return o
}

func singboxSocks(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	o := map[string]any{
		"type":        "socks",
		"tag":         tag,
		"server":      host,
		"server_port": in.Port,
		"version":     "5",
	}
	// See clashSocks for the same wizard settings-priority rationale.
	user := strDefault(str(s["socks_user"]), strDefault(c.Email, str(s["username"])))
	pass := strDefault(str(s["socks_password"]), strDefault(c.Password, str(s["password"])))
	if user != "" && pass != "" {
		o["username"] = user
		o["password"] = pass
	}
	return o
}

// singboxAnyTLS intentionally returns nil. AnyTLS only landed as a sing-box
// outbound type in v1.12.0; clients on older cores (standalone SFI/SFA still
// shipping ≤1.11 via the App Store / F-Droid) hard-reject the WHOLE config
// with `decode config: outbounds[N]: unknown outbound type: anytls` — one
// unknown type nukes every other node in the subscription (confirmed on-device
// 2026-06-12). Same blast radius as the xhttp case (singboxVLESSXHTTP), so we
// take the same escape hatch: omit anytls from the sing-box JSON and let those
// clients ride the other inbounds. Clients that DO support anytls reach it via
// the v2ray URI / clash / loon / surge / qx encoders (none of which gate it on
// a core version). Revisit if/when we UA-sniff the client core version.
func singboxAnyTLS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) map[string]any {
	return nil
}
