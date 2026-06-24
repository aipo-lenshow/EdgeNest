package share

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// EncodeSurge renders a subscription bundle as a Surge .conf [Proxy] section.
// Surge has its own KV-on-one-line proxy syntax (similar to QX). Reference:
// https://manual.nssurge.com/policy/proxy.html.
//
// Coverage (as of Surge iOS 5.14 / macOS 6.6):
//
//	Trojan / Hysteria2 / TUIC / Shadowsocks / VMess-WS / AnyTLS / SOCKS5 — supported
//	VLESS family (Reality / WS / XHTTP) — Surge has no plans to add VLESS;
//	  emit as a `# unsupported: ...` comment so operators see the gap.
//
// IPv6 host: Surge's `<tag> = <scheme>, <host>, <port>, ...` line is
// comma-separated, so the port is its own field and the host needs NO
// brackets — Surge rejects a bracketed literal ([2607:..]) with
// "字段 hostname 的值无效" / SGSettingsModelErrorDomain:0 (confirmed on
// Surge iOS against a real dual-stack node). So we emit the RAW v6 here,
// NOT hostForURI(host). This differs from QX (host:port colon form → needs
// brackets) and Loon (comma form → tolerates brackets); only Surge breaks
// on the bracketed shape, so the fix stays Surge-local.
func EncodeSurge(bundles []Bundle, host string) string {
	var sb strings.Builder
	// Surge .conf comment marker is `//` (per manual.nssurge.com). Lines
	// starting with `#` raise INVALID LINE and abort the whole import — the
	// user would see an unusable empty profile with no explanation.
	sb.WriteString("// EdgeNest subscription (Surge .conf format)\n")
	sb.WriteString("// Drop into Surge → Settings → Profile → External Resources.\n\n")

	// Surge 5.x requires a [General] section + a [Rule] section ending in
	// FINAL, otherwise the import dies with "规则必须以 FINAL 结尾"
	// (SGSettingsModelErrorDomain:0). [Proxy Group] is not strictly required
	// but Surge expects FINAL to point at a proxy or group; without a
	// selector the user has no node to switch to.
	sb.WriteString("[General]\n")
	sb.WriteString("loglevel = notify\n")
	sb.WriteString("dns-server = system, 8.8.8.8\n")
	sb.WriteString("skip-proxy = 127.0.0.1, 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, localhost, *.local\n\n")

	sb.WriteString("[Proxy]\n")
	proxyTags := make([]string, 0, len(bundles))
	for _, b := range bundles {
		var settings map[string]any
		if b.Inbound.Settings != "" {
			_ = json.Unmarshal([]byte(b.Inbound.Settings), &settings)
		}
		if settings == nil {
			settings = map[string]any{}
		}
		tag := remarkOf(b.Inbound, b.Client)
		emitted := false
		switch b.Inbound.Type {
		case "trojan":
			if line, ok := surgeTrojan(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
				emitted = true
			}
		case "hysteria2":
			if line, ok := surgeHysteria2(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
				emitted = true
			}
		case "tuic":
			if line, ok := surgeTUIC(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
				emitted = true
			}
		case "shadowsocks":
			if line, ok := surgeShadowsocks(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
				emitted = true
			}
		case "vmess", "vmess-ws":
			if line, ok := surgeVMess(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
				emitted = true
			}
		case "anytls":
			if line, ok := surgeAnyTLS(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
				emitted = true
			}
		case "socks":
			if line, ok := surgeSocks(b.Inbound, b.Client, settings, bundleHost(b, host), tag); ok {
				sb.WriteString(line + "\n")
				emitted = true
			}
		case "vless":
			sb.WriteString("// unsupported in Surge: " + tag + " (vless-reality — Surge has no VLESS support; use sing-box / Mihomo / Stash for this node)\n")
		case "vless-ws":
			sb.WriteString("// unsupported in Surge: " + tag + " (vless-ws — Surge has no VLESS support)\n")
		case "vless-xhttp":
			sb.WriteString("// unsupported in Surge: " + tag + " (vless-xhttp — Surge has no VLESS / XHTTP support)\n")
		}
		if emitted {
			proxyTags = append(proxyTags, tag)
		}
	}
	sb.WriteString("\n")

	sb.WriteString("[Proxy Group]\n")
	if len(proxyTags) > 0 {
		sb.WriteString("EdgeNest = select, " + strings.Join(proxyTags, ", ") + ", EdgeNest-auto\n")
		sb.WriteString("EdgeNest-auto = url-test, " + strings.Join(proxyTags, ", ") + ", url=http://www.gstatic.com/generate_204, interval=300\n")
	} else {
		// No usable Surge proxy in the bundle (e.g. VLESS-only). Use DIRECT
		// as the group target so [Rule] FINAL still resolves to something
		// valid and Surge doesn't reject the profile.
		sb.WriteString("EdgeNest = select, DIRECT\n")
	}
	sb.WriteString("\n")

	sb.WriteString("[Rule]\n")
	sb.WriteString("FINAL, EdgeNest\n")
	return sb.String()
}

// trojan: <tag> = trojan, <host>, <port>, password=<pw>, sni=<sni>, skip-cert-verify=true|false
func surgeTrojan(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = trojan, %s, %d", tag, host, in.Port),
		"password=" + c.Password,
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "sni="+sni)
	}
	parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	return strings.Join(parts, ", "), true
}

// hysteria2: <tag> = hysteria2, <host>, <port>, password=<pw>, sni=<sni>,
//
//	skip-cert-verify=..., obfs=salamander, obfs-password=...
//
// Surge 5.7+ accepts `obfs` + `obfs-password` keys directly in the proxy line.
func surgeHysteria2(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = hysteria2, %s, %d", tag, host, in.Port),
		"password=" + c.Password,
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "sni="+sni)
	}
	// Surge 5.18 official manual (manual.nssurge.com/policy/proxy.html) uses
	// the dedicated `salamander-password` key on the Hy2 line — `obfs=` plus
	// `obfs-password=` does NOT trigger Hy2 obfs in Surge (the parser only
	// recognises `obfs` for SS / VMess). Without salamander-password Surge
	// connects on the plain Hy2 path and the server-side obfs filter drops
	// the handshake silently.
	if obfs := str(s["obfs"]); obfs == "salamander" {
		if pw := str(s["obfs_password"]); pw != "" {
			parts = append(parts, "salamander-password="+pw)
		}
	}
	parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	return strings.Join(parts, ", "), true
}

// tuic: <tag> = tuic-v5, <host>, <port>, uuid=<uuid>, password=<password>, alpn=h3, sni=<sni>, skip-cert-verify=...
// Surge's `tuic` policy type is TUIC v4 and REQUIRES `token=` — official manual
// (manual.nssurge.com/policy/proxy.html): "Parameter for TUIC — token: Required."
// A v5 node must use the SEPARATE `tuic-v5` type with `uuid=` + `password=`
// (Surge iOS release notes 5.5.1: "Added support for TUIC v5 protocol";
// Sub-Store surge.js producer emits `tuic-v5` whenever the node has no token,
// and its surge peggy parser only accepts uuid=/password= under `tuic-v5`).
// A `version=` parameter does not exist in Surge's dialect at all — it came
// from a stale May-2023 community tutorial predating 5.5.1. Emitting
// `tuic, ..., version=5` fails on-device with "字段 `token` 必须被提供" /
// SGSettingsModelErrorDomain:0 (confirmed 2026-06-12).
func surgeTUIC(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" || c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = tuic-v5, %s, %d", tag, host, in.Port),
		"uuid=" + c.UUID,
		"password=" + c.Password,
		"alpn=h3",
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "sni="+sni)
	}
	parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	return strings.Join(parts, ", "), true
}

// shadowsocks: <tag> = ss, <host>, <port>, encrypt-method=<m>, password=<pw>
func surgeShadowsocks(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	method := strDefault(str(s["method"]), "2022-blake3-aes-128-gcm")
	parts := []string{
		fmt.Sprintf("%s = ss, %s, %d", tag, host, in.Port),
		"encrypt-method=" + method,
		"password=" + c.Password,
	}
	return strings.Join(parts, ", "), true
}

// vmess: <tag> = vmess, <host>, <port>, username=<uuid>, vmess-aead=true,
//
//	ws=true, ws-path=/..., tls=true|false, sni=<sni>, skip-cert-verify=...
//
// vmess-aead=true is mandatory: our sing-box VMess inbound is AEAD-only
// (alterId removed upstream, so credentials are always AEAD). Without this
// flag Surge falls back to legacy MD5 auth → handshake mismatch → "imports
// fine but won't proxy". Sub-Store's surge producer emits `vmess-aead` for
// every alterId=0 node for the same reason (vmess-security.js).
func surgeVMess(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.UUID == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = vmess, %s, %d", tag, host, in.Port),
		"username=" + c.UUID,
		"vmess-aead=true",
		"ws=true",
		"ws-path=" + strDefault(str(s["ws_path"]), "/"),
	}
	if h := str(s["ws_host"]); h != "" {
		parts = append(parts, `ws-headers="Host: `+h+`"`)
	}
	if str(s["tls_cert_path"]) != "" {
		parts = append(parts, "tls=true")
		if sni := str(s["sni"]); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	} else {
		parts = append(parts, "tls=false")
	}
	return strings.Join(parts, ", "), true
}

// anytls: <tag> = anytls, <host>, <port>, password=<pw>, sni=<sni>, skip-cert-verify=...
// AnyTLS support in Surge is recent (Surge 5.12+). Older builds will mark the
// line as invalid and skip it — that's acceptable since the comment hints at
// upgrade.
func surgeAnyTLS(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	if c.Password == "" {
		return "", false
	}
	parts := []string{
		fmt.Sprintf("%s = anytls, %s, %d", tag, host, in.Port),
		"password=" + c.Password,
	}
	if sni := str(s["sni"]); sni != "" {
		parts = append(parts, "sni="+sni)
	}
	parts = append(parts, "skip-cert-verify="+selfSignedStr(s))
	return strings.Join(parts, ", "), true
}

// socks: <tag> = socks5, <host>, <port>, username=<u>, password=<p>
func surgeSocks(in *model.Inbound, c model.Client, s map[string]any, host, tag string) (string, bool) {
	parts := []string{
		fmt.Sprintf("%s = socks5, %s, %d", tag, host, in.Port),
	}
	// Wizard-shape inbounds: settings socks_user/socks_password override
	// client.Email/Password so server auth aligns. See clashSocks.
	user := strDefault(str(s["socks_user"]), strDefault(c.Email, str(s["username"])))
	pass := strDefault(str(s["socks_password"]), strDefault(c.Password, str(s["password"])))
	if user != "" && pass != "" {
		parts = append(parts, "username="+user, "password="+pass)
	}
	return strings.Join(parts, ", "), true
}

// selfSignedStr returns "true" when the inbound is using EdgeNest's
// wizard-generated self-signed cert (autofill marks `self_signed: true`),
// "false" otherwise — the convention Surge / Stash / Loon all expect.
func selfSignedStr(s map[string]any) string {
	if str(s["self_signed"]) == "true" {
		return "true"
	}
	if str(s["tls_cert_path"]) != "" && str(s["acme_managed"]) != "true" {
		return "true"
	}
	return "false"
}
