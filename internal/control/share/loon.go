package share

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// EncodeLoon renders a subscription bundle as a Loon .conf [Proxy] section.
// Loon's proxy syntax is close to Surge's but distinct: it uses different
// keys (e.g. `transport=ws` instead of `ws=true`) and supports VLESS via
// its own engine. Reference: https://nsloon.app/docs/Profile.
//
// Coverage (as of Loon 3.2 / 2026-Q2):
//
//	VLESS-Reality / Hysteria2 / Trojan / SS / TUIC / VMess-WS / AnyTLS / SOCKS5 — supported
//	VLESS-WS — supported via `transport=ws`
//	VLESS-XHTTP — partial (Loon 3.3+ has experimental support; older builds skip)
func EncodeLoon(bundles []Bundle, host string) string {
	var sb strings.Builder
	sb.WriteString("# EdgeNest subscription (Loon .conf format)\n")
	sb.WriteString("# Drop into Loon → Configuration → Profile.\n")
	sb.WriteString("[Proxy]\n")
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
			if line, ok := loonVLESS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "vless-ws":
			if line, ok := loonVLESSWS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "vless-xhttp":
			sb.WriteString("# unsupported in Loon (< 3.3): " + tag + " (vless-xhttp — upgrade Loon for experimental support)\n")
		case "trojan":
			if line, ok := loonTrojan(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "hysteria2":
			if line, ok := loonHysteria2(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "tuic":
			if line, ok := loonTUIC(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "shadowsocks":
			if line, ok := loonShadowsocks(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "vmess", "vmess-ws":
			if line, ok := loonVMess(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "anytls":
			if line, ok := loonAnyTLS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		case "socks":
			if line, ok := loonSocks(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
			}
		}
	}
	return sb.String()
}

// vless-reality: <tag> = VLESS, host, port, "<uuid>", transport=tcp,
//
//	over-tls=true, sni=<sni>, public-key="<pbk>", short-id=<sid>, flow=<flow>
//
// UUID must be the 4th positional parameter wrapped in double quotes per Loon's
// .conf grammar (nsloon.app/docs/Node). Emitting `uuid=<UUID>` as a key=value
// makes Loon use the literal string `uuid=<UUID>` (45 chars including the
// prefix) as the node's UUID — vless authentication then fails and Loon shows
// "test failed" + proxy-down. Verified empirically against Loon 3.4.0(962).
func loonVLESS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf(`%s = VLESS, %s, %d, "%s"`, tag, hostForURI(host), in.Port, c.UUID),
		"transport=tcp",
	}
	// Loon docs (nsloon.app/docs/Node) use `over-tls=true` for the TLS gate.
	// `tls=true` was an alias in some 3.x builds but the documented stable
	// key is `over-tls`; sticking with the spec keeps the line working
	// across Loon 3.2 / 3.3 / future 3.4 without surprises.
	if pub := str(s["reality_public_key"]); pub != "" {
		parts = append(parts, "over-tls=true", "public-key="+pub)
		if sni := str(s["sni"]); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		if sids := strSlice(s["short_ids"]); len(sids) > 0 {
			parts = append(parts, "short-id="+sids[0])
		}
		parts = append(parts, "client-fingerprint=chrome")
	} else if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "over-tls=true", "sni="+sni)
	}
	flow := strDefault(c.Flow, str(s["flow"]))
	if flow == "" && str(s["reality_public_key"]) != "" {
		flow = "xtls-rprx-vision"
	}
	if flow != "" {
		parts = append(parts, "flow="+flow)
	}
	parts = append(parts, "udp=true")
	return strings.Join(parts, ", "), true
}

// vless-ws: VLESS over WebSocket. transport=ws + path/host.
//
// UUID must be the 4th POSITIONAL parameter wrapped in double quotes (same
// Loon grammar as loonVLESS reality) — `uuid=<UUID>` key=value makes Loon
// treat the literal string as the credential and auth fails (imports clean,
// proxy down). And the TLS server-name key for a plain (non-Reality) WS+TLS
// node is `tls-name=`, NOT `sni=` (Sub-Store loon.js only emits `sni=` on the
// Reality branch). Source: Loon0x00/LoonExampleConfig example.conf `vless2`
// line + Sub-Store loon.js producer.
func loonVLESSWS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf(`%s = VLESS, %s, %d, "%s"`, tag, hostForURI(host), in.Port, c.UUID),
		"transport=ws",
		"path=" + strDefault(str(s["ws_path"]), "/"),
	}
	if h := str(s["ws_host"]); h != "" {
		parts = append(parts, "host="+h)
	}
	if str(s["tls_cert_path"]) != "" {
		parts = append(parts, "over-tls=true")
		if sni := str(s["sni"]); sni != "" {
			parts = append(parts, "tls-name="+sni)
		}
		parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	}
	parts = append(parts, "udp=true")
	return strings.Join(parts, ", "), true
}

func loonTrojan(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = trojan, %s, %d", tag, hostForURI(host), in.Port),
		"password=" + c.Password,
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "tls-name="+sni)
	}
	parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	return strings.Join(parts, ", "), true
}

// Loon 3.2+ accepts `obfs=salamander, obfs-password=...` directly on the
// Hysteria2 line; without them the client never completes the handshake when
// the server has obfs enabled (which the wizard does by default).
func loonHysteria2(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = Hysteria2, %s, %d", tag, hostForURI(host), in.Port),
		"password=" + c.Password,
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "sni="+sni)
	}
	if obfs := str(s["obfs"]); obfs != "" {
		parts = append(parts, "obfs="+obfs)
		if pw := str(s["obfs_password"]); pw != "" {
			parts = append(parts, "obfs-password="+pw)
		}
	}
	parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	return strings.Join(parts, ", "), true
}

func loonTUIC(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" || c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = TUIC, %s, %d", tag, hostForURI(host), in.Port),
		"uuid=" + c.UUID,
		"alpn=h3",
		"password=" + c.Password,
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "sni="+sni)
	}
	parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	return strings.Join(parts, ", "), true
}

// shadowsocks: <tag> = Shadowsocks, host, port, <cipher>, "<password>"
//
// cipher must be the 4th positional (bare, no quotes), password the 5th
// positional wrapped in double quotes per Loon's .conf grammar
// (nsloon.app/docs/Node). Emitting `method=<cipher>` makes Loon look up the
// literal `method=<cipher>` in its supported-ciphers table, fail to match,
// and silently drop the node. Verified empirically against Loon 3.4.0(962).
func loonShadowsocks(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	method := strDefault(str(s["method"]), "2022-blake3-aes-128-gcm")
	parts := []string{
		fmt.Sprintf(`%s = Shadowsocks, %s, %d, %s, "%s"`, tag, hostForURI(host), in.Port, method, c.Password),
	}
	return strings.Join(parts, ", "), true
}

// vmess: <tag> = vmess, host, port, <cipher>, "<uuid>", transport=ws, ...
//
// Like loonVLESS / loonShadowsocks, Loon needs the cipher as the 4th POSITIONAL
// field and the UUID as the 5th, double-quoted. The `username=<UUID>` key=value
// form makes Loon use the literal string as the credential and auth fails
// (imports clean, proxy down). `auto` lets Loon negotiate the AEAD cipher
// (server runs security=auto). Source: Loon0x00/LoonExampleConfig example.conf
// `vmess4` line (`vmess, host, port, aes-128-gcm, "<uuid>", transport=ws, ...`)
// + Sub-Store loon.js producer.
func loonVMess(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf(`%s = vmess, %s, %d, auto, "%s"`, tag, hostForURI(host), in.Port, c.UUID),
		"transport=ws",
		"path=" + strDefault(str(s["ws_path"]), "/"),
	}
	if h := str(s["ws_host"]); h != "" {
		parts = append(parts, "host="+h)
	}
	if str(s["tls_cert_path"]) != "" {
		parts = append(parts, "over-tls=true")
		if sni := str(s["sni"]); sni != "" {
			parts = append(parts, "tls-name="+sni)
		}
		parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	}
	return strings.Join(parts, ", "), true
}

func loonAnyTLS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = anytls, %s, %d", tag, hostForURI(host), in.Port),
		"password=" + c.Password,
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "sni="+sni)
	}
	parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	return strings.Join(parts, ", "), true
}

func loonSocks(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	parts := []string{
		fmt.Sprintf("%s = socks5, %s, %d", tag, hostForURI(host), in.Port),
	}
	// Same as clashSocks: settings override for wizard-shape inbounds.
	user := strDefault(str(s["socks_user"]), strDefault(c.Email, str(s["username"])))
	pass := strDefault(str(s["socks_password"]), strDefault(c.Password, str(s["password"])))
	if user != "" && pass != "" {
		parts = append(parts, user, pass)
	}
	return strings.Join(parts, ", "), true
}
