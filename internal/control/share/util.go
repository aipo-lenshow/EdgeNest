package share

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// certPinSHA256 returns the lowercase-hex SHA-256 of the leaf certificate's
// raw DER, the exact value Hysteria2's `pinSHA256` URI parameter expects
// (apernet/hysteria app/cmd/cert.go certPinSHA256 = hex(sha256(certDER))).
// It reads the PEM cert at certPath and hashes the FIRST block's DER bytes
// (the end-entity / leaf cert — Hysteria pins only rawCerts[0]).
//
// On any failure (missing file in unit tests, unreadable path, malformed
// PEM) it returns "" so the caller degrades to plain insecure=1 — the
// pre-existing behaviour — instead of erroring the whole subscription.
//
// NOT the SPKI/base64 form: sing-box's certificate_public_key_sha256 hashes
// the public key and base64-encodes it; feeding that to Hysteria's pinSHA256
// makes the client reject the cert. Whole-DER lowercase-hex is correct for
// the hysteria2:// scheme across Hysteria core / sing-box / Shadowrocket.
// hopRange reads a Hysteria2 inbound's port-hopping range from settings
// (port_hop_start / port_hop_end). Returns ok=false unless both are present
// and form a valid ascending range. JSON-decoded numbers arrive as float64;
// the UI may also send strings.
func hopRange(s map[string]any) (start, end int, ok bool) {
	start = intOf(s["port_hop_start"])
	end = intOf(s["port_hop_end"])
	if start <= 0 || end < start {
		return 0, 0, false
	}
	return start, end, true
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	}
	return 0
}

func certPinSHA256(certPath string) string {
	if certPath == "" {
		return ""
	}
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

// hostForURI returns the host literal suitable for embedding into a URI's
// authority (or a `<scheme>, host, port` style line). RFC 3986 requires
// IPv6 literals to be wrapped in square brackets so the colons inside the
// address aren't confused with the `:port` delimiter:
//
//	vless://uuid@[2607:8700::2]:8443?...
//	trojan-tag = trojan, [2607:8700::2], 8443, ...
//
// Without the brackets, every URI / line parser (Shadowrocket, Stash,
// sing-box, Surge, Loon, Quantumult X, V2rayN) silently drops the node:
// some treat the last `:` as port separator and read a port from a hex
// digit chunk, others fail the whole URI parse.
//
// IPv4 literals and DNS names pass through untouched (already valid in
// authority position). The branch is shape-only — we don't talk to DNS,
// just check if the string is a non-nil IP that fails To4().
//
// IMPORTANT: this is for URI / Surge / Loon / QX line builders ONLY.
// Clash YAML, sing-box JSON, and VMess JSON ("add" field) all hand the
// raw host string to a downstream parser that does its own IPv6 dial-time
// bracketing; wrapping the JSON / YAML value here would produce double
// brackets and break those clients instead.
func hostForURI(host string) string {
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return "[" + host + "]"
	}
	return host
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func strSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// encodeUserInfoSS produces the userinfo segment of an `ss://` URI.
//
// SIP022 (AEAD-2022 ciphers, prefix `2022-`) explicitly FORBIDS base64-wrapping
// the userinfo: the canonical form is `method:percent-encoded-password`.
// Shadowrocket 2.2.80+, sing-box CLI, and shadowsocks-rust treat a
// base64-wrapped 2022 userinfo as a legacy AEAD URI, run the inner blob
// through the AEAD-128 keying path, and either crash on length mismatch or
// silently AEAD-decrypt with the wrong key (the classic "doesn't connect"
// failure the user reported on Shadowrocket).
//
// Legacy SIP002 ciphers (`aes-*-gcm`, `chacha20-ietf-poly1305`, `none`) still
// take the base64url-encoded form, which is what V2RayN historically required.
//
// See: https://shadowsocks.org/doc/sip022.html and the discussion in
// shadowsocks-org/wiki/SIP002-URI-Scheme.
func encodeUserInfoSS(method, password string) string {
	if strings.HasPrefix(method, "2022-") {
		// SS-2022: emit the server password *literal* (base64.StdEncoding of
		// the 16-byte PSK, including `==` padding) URL-encoded. Why not the
		// SIP022 base64url-no-padding form: Shadowrocket / Hiddify / V2RayN
		// all run the URL-decoded password through `base64.StdEncoding.Decode`
		// first; if that fails they fall back to "raw UTF-8 string as PSK",
		// which silently mismatches the server's 16-byte key and the AEAD
		// handshake fails with no client-visible error. StdEncoding's alphabet
		// doesn't contain `_`, so RawURLEncoding strings hit that fallback
		// path. Keeping the literal aligned with sing-box's config field is
		// the only form that round-trips cleanly across the v2ray-style URI
		// consumers (Shadowrocket / Hiddify / V2RayN / NekoBox).
		// QueryEscape (not PathEscape) handles `/`, `+` and `=`.
		return method + ":" + url.QueryEscape(password)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
}

// bundleHost returns the server host the encoder should emit for this bundle.
// Bundle.EffectiveHost overrides the global host when non-empty — used by the
// resolver to substitute a Cloudflare anycast IP for inbounds that opt into
// the CDN preferred-IP pool. Encoders that respect this helper keep SNI /
// Host header values from settings, so traffic still presents the user's
// domain at the TLS / HTTP layer.
func bundleHost(b Bundle, fallback string) string {
	if b.EffectiveHost != "" {
		return b.EffectiveHost
	}
	return fallback
}

// EncodeSubscriptionBody wraps a list of URIs as a V2RayN-style subscription:
// newline-joined URIs, then base64 (standard, with padding) the whole blob.
func EncodeSubscriptionBody(uris []string) string {
	joined := ""
	for i, u := range uris {
		if i > 0 {
			joined += "\n"
		}
		joined += u
	}
	return base64.StdEncoding.EncodeToString([]byte(joined))
}
