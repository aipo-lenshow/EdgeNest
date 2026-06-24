package share

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// cdnInbound returns a wizard-style CDN-mode VLESS-WS inbound. The settings
// flag mirrors what the API layer writes when the operator opts the inbound
// into Cloudflare anycast routing.
func cdnInbound(typ string) *model.Inbound {
	// Realistic CDN-mode inbound: TLS cert paths set (the wizard places real
	// or ACME-issued certs there once CDN routing is configured), `cdn_mode`
	// opt-in. Without tls_cert_path the singbox encoder skips the TLS block,
	// which is fine for non-CDN deployments but defeats the substitution
	// assertions below.
	return &model.Inbound{
		Tag: "cdn-" + typ, Type: typ, Port: 443,
		Settings: `{"ws_path":"/x","ws_host":"app.example.com","sni":"app.example.com","tls_cert_path":"/etc/edgenest/certs/wizard-fullchain.pem","cdn_mode":"true"}`,
	}
}

func TestPickCDNHost_DeterministicPerUser(t *testing.T) {
	in := cdnInbound("vless-ws")
	pool := []string{"162.159.193.1", "162.159.193.2", "162.159.193.3"}
	c1 := model.Client{Email: "alice@example.com"}
	c2 := model.Client{Email: "bob@example.com"}
	var s map[string]any
	_ = unmarshalJSONFromString(in.Settings, &s)

	a1 := PickCDNHost(in, c1, s, pool)
	a2 := PickCDNHost(in, c1, s, pool) // same user → same IP
	if a1 != a2 {
		t.Errorf("same user resolved to different IPs: %q vs %q", a1, a2)
	}
	if !inSlice(a1, pool) {
		t.Errorf("picked IP %q not in pool %v", a1, pool)
	}
	b1 := PickCDNHost(in, c2, s, pool)
	if !inSlice(b1, pool) {
		t.Errorf("user b picked IP %q not in pool %v", b1, pool)
	}
}

func TestPickCDNHost_RejectsNonCDNProtocols(t *testing.T) {
	pool := []string{"162.159.193.1"}
	c := model.Client{Email: "a@x"}
	for _, typ := range []string{"vless", "hysteria2", "trojan", "tuic", "shadowsocks", "anytls", "socks"} {
		in := &model.Inbound{Tag: "x", Type: typ, Port: 443,
			Settings: `{"cdn_mode":"true"}`}
		var s map[string]any
		_ = unmarshalJSONFromString(in.Settings, &s)
		if got := PickCDNHost(in, c, s, pool); got != "" {
			t.Errorf("%s should not pick CDN host (CF cannot proxy it), got %q", typ, got)
		}
	}
}

func TestPickCDNHost_RequiresOptIn(t *testing.T) {
	in := &model.Inbound{Tag: "v", Type: "vless-ws", Port: 443, Settings: `{}`}
	var s map[string]any
	if got := PickCDNHost(in, model.Client{Email: "a@x"}, s, []string{"1.2.3.4"}); got != "" {
		t.Errorf("inbound without cdn_mode=true should fall through, got %q", got)
	}
}

func TestPickCDNHost_EmptyPool(t *testing.T) {
	in := cdnInbound("vless-ws")
	var s map[string]any
	_ = unmarshalJSONFromString(in.Settings, &s)
	if got := PickCDNHost(in, model.Client{Email: "a@x"}, s, nil); got != "" {
		t.Errorf("empty pool should fall through, got %q", got)
	}
}

// TestEncodeClash_CDNModeSubstitutesServer wires it end-to-end: when a bundle
// has EffectiveHost set (the resolver fills this from PickCDNHost), Clash
// YAML must emit the IP in `server:` while keeping the original domain in
// `servername:` so TLS / Host stays anchored to the operator's CDN domain.
func TestEncodeClash_CDNModeSubstitutesServer(t *testing.T) {
	bundle := Bundle{
		Inbound: cdnInbound("vless-ws"),
		Client: model.Client{
			Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111",
			Enabled: true,
		},
		EffectiveHost: "162.159.193.42",
	}
	out := EncodeClash([]Bundle{bundle}, "app.example.com")
	if !strings.Contains(out, "server: 162.159.193.42") {
		t.Errorf("server should be CDN IP, got:\n%s", out)
	}
	if !strings.Contains(out, "servername: app.example.com") {
		t.Errorf("servername should stay as domain, got:\n%s", out)
	}
	if strings.Contains(out, "server: app.example.com") {
		t.Error("server should not be domain when EffectiveHost is set")
	}
}

func TestEncodeSingbox_CDNModeSubstitutesServer(t *testing.T) {
	bundle := Bundle{
		Inbound: cdnInbound("vless-ws"),
		Client: model.Client{
			Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111",
			Enabled: true,
		},
		EffectiveHost: "162.159.193.42",
	}
	out := EncodeSingbox([]Bundle{bundle}, "app.example.com")
	if !strings.Contains(out, `"server": "162.159.193.42"`) {
		t.Errorf("singbox server should be CDN IP, got:\n%s", out)
	}
	if !strings.Contains(out, `"server_name": "app.example.com"`) {
		t.Errorf("singbox server_name should stay as domain, got:\n%s", out)
	}
}

func unmarshalJSONFromString(raw string, out *map[string]any) error {
	return json.Unmarshal([]byte(raw), out)
}

func inSlice(s string, ss []string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
