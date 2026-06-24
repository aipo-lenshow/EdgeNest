package share

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// PickCDNHost returns the effective host string for the bundle's `server`
// field when CDN preferred IPs are in play; returns "" when the inbound
// hasn't opted in or the pool is empty (callers fall back to the default
// share host in that case).
//
// Selection is deterministic per (client email, inbound tag) so the same
// user always resolves to the same anycast IP across reloads (avoids
// connection churn), while different users spread across the pool.
//
// Only protocols that ride HTTP/HTTPS through Cloudflare can use the pool
// (vmess-ws / vless-ws / vless-xhttp-tls). The opt-in is per inbound:
// `settings.cdn_mode = true`. Other protocols ignore the pool even if the
// flag is on, because raw TLS / QUIC / Reality cannot be CDN-fronted.
func PickCDNHost(in *model.Inbound, c model.Client, settings map[string]any, pool []string) string {
	if len(pool) == 0 {
		return ""
	}
	if !isCDNCompatibleType(in.Type) {
		return ""
	}
	if !truthyBool(settings["cdn_mode"]) {
		return ""
	}
	idx := bundleHash(c.Email, in.Tag) % uint32(len(pool))
	return pool[idx]
}

// isCDNCompatibleType reports whether the protocol can be proxied through a
// Cloudflare anycast IP. Real protocol restriction (CF only proxies HTTP)
// rather than a panel-level limit.
func isCDNCompatibleType(t string) bool {
	switch t {
	case "vmess", "vmess-ws", "vless-ws", "vless-xhttp":
		return true
	}
	return false
}

func bundleHash(email, tag string) uint32 {
	h := sha256.Sum256([]byte(email + "\x00" + tag))
	return binary.BigEndian.Uint32(h[:4])
}

// truthyBool accepts both the bool form the JSON-decoded settings map carries
// straight from the API and the string form ("true" / "false") the autofill
// path normalises into. Mirrors how inbound_secrets handles obfs / self_signed.
func truthyBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "yes"
	}
	return false
}
