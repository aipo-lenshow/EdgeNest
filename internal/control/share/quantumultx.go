package share

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// EncodeQuantumultX renders a subscription bundle as a Quantumult X server
// subscription body. QX's subscription endpoint expects **plain text**, one
// `<scheme>=host:port, kv, ...` entry per line — NO `[server_local]` section
// header and NO comment lines (they break the in-app parser, which then
// reports "subscription invalid" without specifics).
//
// Protocols QX doesn't speak natively (Hysteria2 / TUIC / SOCKS /
// VLESS-XHTTP) are silently dropped — emitting hint comments here breaks
// the import, so the visibility tradeoff loses to "subscription works at all".
// Per App Store version history: VLESS landed in QX 1.5.0, Reality TLS in
// 1.5.5 (2026-01-14), AnyTLS in 1.6.0 (2026-05-21) — all emitted below.
func EncodeQuantumultX(bundles []Bundle, host string) string {
	var sb strings.Builder
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
		case "trojan":
			if line, ok := qxTrojan(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "shadowsocks":
			if line, ok := qxShadowsocks(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "vmess", "vmess-ws":
			if line, ok := qxVMess(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "vless":
			if line, ok := qxVLESSReality(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "vless-ws":
			if line, ok := qxVLESSWS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "anytls":
			if line, ok := qxAnyTLS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		}
	}
	return sb.String()
}

func qxTrojan(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("trojan=%s:%d", hostForURI(host), in.Port),
		"password=" + c.Password,
		// Every TLS trojan example in the official sample.conf carries
		// `over-tls=true` (`trojan=...:443, password=..., over-tls=true,
		// tls-host=..., tls-verification=true`); `tls-host`/`tls-verification`
		// without it is an undocumented combination — and without it QX treats
		// the trojan as plaintext, so the TLS handshake never happens.
		"over-tls=true",
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "tls-host="+sni)
	}
	// Self-signed → tls-verification=false (QX expects this exact key).
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		parts = append(parts, "tls-verification=false")
	} else {
		parts = append(parts, "tls-verification=true")
	}
	parts = append(parts, "fast-open=false", "udp-relay=true", "tag="+tag)
	return strings.Join(parts, ", "), true
}

func qxShadowsocks(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	method := strDefault(str(s["method"]), "2022-blake3-aes-128-gcm")
	parts := []string{
		fmt.Sprintf("shadowsocks=%s:%d", hostForURI(host), in.Port),
		"method=" + method,
		"password=" + c.Password,
		"fast-open=false",
		"udp-relay=true",
		"tag=" + tag,
	}
	return strings.Join(parts, ", "), true
}

// QX vmess line: vmess=host:port, method=chacha20-ietf-poly1305, password=<uuid>,
//   obfs=wss, obfs-uri=<path>, obfs-host=<host>, tls-verification=true, tag=Foo
func qxVMess(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" {
		return "", false
	}
	obfs := "ws"
	if str(s["tls_cert_path"]) != "" {
		obfs = "wss"
	}
	parts := []string{
		fmt.Sprintf("vmess=%s:%d", hostForURI(host), in.Port),
		"method=chacha20-ietf-poly1305",
		"password=" + c.UUID,
		"obfs=" + obfs,
		"obfs-uri=" + strDefault(str(s["ws_path"]), "/"),
	}
	if h := str(s["ws_host"]); h != "" {
		parts = append(parts, "obfs-host="+h)
	} else if sni := str(s["sni"]); sni != "" && obfs == "wss" {
		parts = append(parts, "obfs-host="+sni)
	}
	parts = append(parts, "fast-open=false", "udp-relay=true", "tag="+tag)
	return strings.Join(parts, ", "), true
}

// QX VLESS-Reality (1.5.5+):
//   vless=host:port, method=none, password=<uuid>, obfs=over-tls,
//     tls-host=<sni>, public-key=<pbk>, short-id=<sid>,
//     tls-verification=true, fast-open=false, udp-relay=true, tag=Foo
//
// Reality is signalled by obfs=over-tls + public-key (there is no separate
// "reality" scheme in QX). VLESS-XHTTP is not supported by QX (no xhttp
// transport option), so only the Reality-TCP shape is emitted here.
func qxVLESSReality(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" {
		return "", false
	}
	pub := str(s["reality_public_key"])
	if pub == "" {
		// Plain VLESS-TCP without Reality is uninteresting on QX; skip.
		return "", false
	}
	parts := []string{
		fmt.Sprintf("vless=%s:%d", hostForURI(host), in.Port),
		"method=none",
		"password=" + c.UUID,
		"obfs=over-tls",
	}
	if sni := str(s["sni"]); sni != "" {
		// In Reality mode QX reads the camouflage SNI from `obfs-host=`,
		// not `tls-host=`. `tls-host=` only applies when the inbound is
		// plain `obfs=over-tls` (no Reality) — leaving it here for the
		// Reality branch silently drops the SNI, so the handshake hits the
		// server with the bare host IP and the Reality check fails.
		parts = append(parts, "obfs-host="+sni)
	}
	// QX 1.5.5 canonical Reality keys are `reality-base64-pubkey` and
	// `reality-hex-shortid`. The pre-1.7.0 naming (`public-key` / `short-id`)
	// is silently rejected — the subscription line still parses but the
	// proxy never establishes a Reality handshake. Likewise QX reads
	// `vless-flow=`, not the bare `flow=`. Without it Vision masking is off.
	parts = append(parts, "reality-base64-pubkey="+pub)
	if sids := strSlice(s["short_ids"]); len(sids) > 0 {
		parts = append(parts, "reality-hex-shortid="+sids[0])
	}
	parts = append(parts, "vless-flow=xtls-rprx-vision")
	// NO `tls-verification` here: it is NOT a valid key on QX's `vless=`
	// scheme (key set: method/password/obfs/obfs-host/obfs-uri/over-tls/
	// vless-flow/reality-*/fast-open/udp-relay/tls13/tag — per official
	// sample.conf). An unknown key on one line makes QX reject the ENTIRE
	// server-subscription resource with "Response invalid" (confirmed
	// on-device 2026-06-12). Reality needs no verification flag anyway — the
	// pubkey drives the handshake.
	parts = append(parts, "fast-open=false", "udp-relay=true", "tag="+tag)
	return strings.Join(parts, ", "), true
}

// QX AnyTLS (1.6.0+, App Store release 2026-05-21):
//
//	anytls=host:port, password=<pw>, over-tls=true, tls-host=<sni>,
//	  tls-verification=true|false, udp-relay=true, tag=Foo
//
// Official sample.conf: `anytls=example.com:443, password=pwd, over-tls=true,
// tls-host=apple.com, udp-relay=true, tag=anytls-standard-tls-01` (+ a Reality
// variant with reality-base64-pubkey/reality-hex-shortid). `over-tls=true` is
// mandatory; `tls-verification` is a generic TLS key valid here (it is on the
// trojan examples that share the over-tls=true shape), so a self-signed node
// gets tls-verification=false like trojan does.
//
// CAUTION: on QX < 1.6.0 `anytls=` is an unknown scheme and QX rejects the
// ENTIRE remote server resource with "Response invalid" — there is no
// per-line skip. If that bites, the operator must update QX; we deliberately
// don't silently drop anytls for everyone to protect pre-1.6.0 installs.
func qxAnyTLS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("anytls=%s:%d", hostForURI(host), in.Port),
		"password=" + c.Password,
		"over-tls=true",
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "tls-host="+sni)
	}
	if str(s["self_signed"]) == "true" || (str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true") {
		parts = append(parts, "tls-verification=false")
	} else {
		parts = append(parts, "tls-verification=true")
	}
	parts = append(parts, "udp-relay=true", "tag="+tag)
	return strings.Join(parts, ", "), true
}

// QX VLESS-WS:
//   vless=host:port, method=none, password=<uuid>, obfs=wss|ws,
//     obfs-uri=<path>, obfs-host=<host>, tls-verification=true,
//     fast-open=false, udp-relay=true, tag=Foo
//
// TLS upstream (cert configured) → obfs=wss; plaintext WS → obfs=ws.
func qxVLESSWS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" {
		return "", false
	}
	obfs := "ws"
	if str(s["tls_cert_path"]) != "" {
		obfs = "wss"
	}
	parts := []string{
		fmt.Sprintf("vless=%s:%d", hostForURI(host), in.Port),
		"method=none",
		"password=" + c.UUID,
		"obfs=" + obfs,
		"obfs-uri=" + strDefault(str(s["ws_path"]), "/"),
	}
	if h := str(s["ws_host"]); h != "" {
		parts = append(parts, "obfs-host="+h)
	} else if sni := str(s["sni"]); sni != "" && obfs == "wss" {
		parts = append(parts, "obfs-host="+sni)
	}
	// NO `tls-verification`: invalid key on QX's `vless=` scheme — it poisons
	// the whole resource ("Response invalid"). See qxVLESSReality. QX's vless
	// has no skip-cert-verify key, so a self-signed (non-Reality) VLESS-WS node
	// cannot disable verification here — it will fail to connect on QX until an
	// ACME cert is in place (P2). Other clients still get it via their encoders.
	parts = append(parts, "fast-open=false", "udp-relay=true", "tag="+tag)
	return strings.Join(parts, ", "), true
}
