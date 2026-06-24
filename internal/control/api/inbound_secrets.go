package api

import (
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
)

// Default paths for the wizard-generated self-signed certificate. Protocols
// that need TLS (trojan / tuic / hysteria2 / anytls) fall back to this pair
// when the operator hasn't supplied an explicit cert path — so a fresh
// install can stand up new inbounds without trips to the certs page.
const (
	defaultWizardCertPath = "/etc/edgenest/certs/wizard-fullchain.pem"
	defaultWizardKeyPath  = "/etc/edgenest/certs/wizard-privkey.pem"
)

// autofillInboundSettings fills in cryptographic material and operational
// defaults for the inbound Settings JSON before persistence. This is the
// hinge between "user pastes a template with `<base64 X25519 private key>`
// placeholders" and "engine receives a config it can actually render".
//
// existing is the previous settings on the row (Updates only); nil for
// Create. Used to preserve scrubbed-secrets when the panel UI round-trips
// a settings object without the private key.
func autofillInboundSettings(typ string, s, existing map[string]any) (map[string]any, error) {
	if s == nil {
		s = map[string]any{}
	}
	// Carry forward scrubbed secrets / values the request didn't include.
	carry := func(keys ...string) {
		for _, k := range keys {
			if !meaningful(s[k]) {
				if v, ok := existing[k]; ok && meaningful(v) {
					s[k] = v
				}
			}
		}
	}

	switch typ {
	case "vless":
		carry("reality_private_key", "reality_public_key", "short_ids")
		if !meaningful(s["reality_private_key"]) {
			priv, pub, err := auth.GenerateRealityKeypair()
			if err != nil {
				return nil, err
			}
			s["reality_private_key"] = priv
			s["reality_public_key"] = pub
		}
		if !meaningful(s["short_ids"]) {
			sid, err := auth.RandomHex(8)
			if err != nil {
				return nil, err
			}
			s["short_ids"] = []any{sid}
		}
		if !meaningful(s["sni"]) {
			s["sni"] = "www.microsoft.com"
		}
		if !meaningful(s["server_port_target"]) {
			s["server_port_target"] = 443
		}
		if !meaningful(s["flow"]) {
			s["flow"] = "xtls-rprx-vision"
		}

	case "vless-xhttp":
		if !meaningful(s["security"]) {
			s["security"] = "reality"
		}
		if toStr(s["security"]) == "reality" {
			carry("reality_private_key", "reality_public_key", "short_ids")
			if !meaningful(s["reality_private_key"]) {
				priv, pub, err := auth.GenerateRealityKeypair()
				if err != nil {
					return nil, err
				}
				s["reality_private_key"] = priv
				s["reality_public_key"] = pub
			}
			if !meaningful(s["short_ids"]) {
				sid, err := auth.RandomHex(8)
				if err != nil {
					return nil, err
				}
				s["short_ids"] = []any{sid}
			}
			if !meaningful(s["sni"]) {
				s["sni"] = "www.microsoft.com"
			}
		}
		if !meaningful(s["xhttp_path"]) {
			s["xhttp_path"] = "/xhttp"
		}
		if !meaningful(s["xhttp_host"]) {
			if sni := toStr(s["sni"]); sni != "" {
				s["xhttp_host"] = sni
			}
		}

	case "vless-ws":
		carry("reality_public_key")
		if !meaningful(s["ws_path"]) {
			s["ws_path"] = "/vless"
		}
		// ws_host is the HTTP Host header the client sends — every mainstream
		// client (Clash / Mihomo / Stash / Surge / sing-box / V2RayN) wires
		// the WS layer through this name. When operators leave it blank the
		// connection still works on plain TCP but breaks the moment a CDN /
		// reverse proxy sits in front. Default to the SNI when present so the
		// wizard / bundle output is immediately portable.
		if !meaningful(s["ws_host"]) {
			if sni := toStr(s["sni"]); sni != "" {
				s["ws_host"] = sni
			}
		}

	case "vmess", "vmess-ws":
		if !meaningful(s["ws_path"]) {
			s["ws_path"] = "/vmess"
		}
		if !meaningful(s["ws_host"]) {
			if sni := toStr(s["sni"]); sni != "" {
				s["ws_host"] = sni
			}
		}

	case "shadowsocks":
		carry("password")
		if !meaningful(s["method"]) {
			s["method"] = "2022-blake3-aes-128-gcm"
		}
		if !meaningful(s["password"]) {
			psk, err := auth.RandomBase64(ss2022PSKLen(toStr(s["method"])))
			if err != nil {
				return nil, err
			}
			s["password"] = psk
		}

	case "hysteria2":
		carry("obfs_password")
		fillTLSDefaults(s)
		// `obfs` is a boolean toggle in the structured form. When on we ensure
		// `obfs` = "salamander" and mint a password if needed; when off we
		// strip both keys so the engine renders without obfuscation.
		//
		// Legacy callers (older request shapes) only set obfs_password to a
		// `<placeholder>` sentinel — we still honour that as "obfs on, please
		// generate a password" for backward compat with existing tests / API
		// consumers.
		obfsOn := false
		switch v := s["obfs"].(type) {
		case bool:
			obfsOn = v
		case string:
			if v != "" && v != "false" && v != "off" {
				obfsOn = true
			}
		}
		if !obfsOn {
			if pw, ok := s["obfs_password"]; ok && (isPlaceholder(pw) || meaningful(pw)) {
				obfsOn = true
			}
		}
		if obfsOn {
			s["obfs"] = "salamander"
			if !meaningful(s["obfs_password"]) || isPlaceholder(s["obfs_password"]) {
				rh, err := auth.RandomHex(16)
				if err != nil {
					return nil, err
				}
				s["obfs_password"] = rh
			}
		} else {
			delete(s, "obfs")
			delete(s, "obfs_password")
		}
		if !meaningful(s["up_mbps"]) {
			s["up_mbps"] = 100
		}
		if !meaningful(s["down_mbps"]) {
			s["down_mbps"] = 500
		}
		if !meaningful(s["sni"]) {
			s["sni"] = "www.bing.com"
		}

	case "trojan", "tuic", "anytls":
		fillTLSDefaults(s)
		if !meaningful(s["sni"]) {
			s["sni"] = "www.bing.com"
		}
		if typ == "tuic" && !meaningful(s["congestion_control"]) {
			s["congestion_control"] = "bbr"
		}

	case "socks":
		// socks5 has no protocol-level secrets — credentials live on the
		// client row (email + password). The only inbound knob is whether
		// to require auth.
		if _, ok := s["require_auth"]; !ok {
			s["require_auth"] = true
		}
	}

	return s, nil
}

// fillTLSDefaults seeds tls_cert_path / tls_key_path to the wizard's
// self-signed pair when the operator hasn't filled them in. Acceptable for
// v1 standalone (Reality / Hy2 / TUIC don't validate against a CA on the
// client side anyway, and the warning surfaces in the UI when the user
// hits "Issue cert"). The pair itself is provisioned at bootstrap time so
// ad-hoc creation (advanced modal, quick-bundle) finds it ready.
//
// Also marks `self_signed: true` on autofilled paths so subscription
// encoders (clash skip-cert-verify / singbox insecure / uri allowInsecure /
// qx tls-verification=false) emit the right flag for clients to bypass CA
// validation. ACME-managed certs MUST set `acme_managed: true` to override.
func fillTLSDefaults(s map[string]any) {
	autofilled := false
	if !meaningful(s["tls_cert_path"]) {
		s["tls_cert_path"] = defaultWizardCertPath
		autofilled = true
	}
	if !meaningful(s["tls_key_path"]) {
		s["tls_key_path"] = defaultWizardKeyPath
		autofilled = true
	}
	if autofilled && !meaningful(s["self_signed"]) && !meaningful(s["acme_managed"]) {
		s["self_signed"] = "true"
	}
}

// ss2022PSKLen returns the byte length of the PSK required by a Shadowsocks
// 2022 method. Non-2022 methods (legacy AEAD) accept any string, so we still
// hand them a 16-byte secret.
func ss2022PSKLen(method string) int {
	switch method {
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32
	default:
		return 16
	}
}

// meaningful is the "is this value something the engine will accept" check —
// not nil, not empty, not a `<placeholder>` string, not an array of empties.
func meaningful(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		if x == "" {
			return false
		}
		if strings.HasPrefix(x, "<") && strings.HasSuffix(x, ">") {
			return false
		}
		return true
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				return true
			}
			if _, ok := e.(string); !ok && e != nil {
				return true
			}
		}
		return false
	case []string:
		for _, s := range x {
			if s != "" {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func toStr(v any) string {
	s, _ := v.(string)
	return s
}

// isPlaceholder reports whether v is a `<…>` placeholder string the panel
// template ships with (e.g. `<random>`). Real values never start with `<`.
func isPlaceholder(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	return strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">")
}
