package singbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// TestEmitSamples writes one sample JSON per supported sing-box protocol into
// $EDGENEST_SAMPLES_DIR/singbox/ so the operator can run `sing-box check` /
// `sing-box format` against each file with whatever version they are
// validating. Only runs when EDGENEST_EMIT_SAMPLES=1 is set so normal `go test`
// doesn't litter the filesystem.
func TestEmitSamples(t *testing.T) {
	if os.Getenv("EDGENEST_EMIT_SAMPLES") != "1" {
		t.Skip("set EDGENEST_EMIT_SAMPLES=1 to emit sample configs")
	}
	dir := os.Getenv("EDGENEST_SAMPLES_DIR")
	if dir == "" {
		dir = "/tmp/edgenest-samples"
	}
	dir = filepath.Join(dir, "singbox")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		cfg  core.DesiredConfig
	}{
		{"vless-reality", one("vless-reality", "vless", 8443, map[string]any{
			"sni":                 "www.microsoft.com",
			"reality_private_key": "WCSkDdXghTPRajTno6gLuQ8FoKGnex-Oqap8eUNVnlk",
			"short_ids":           []string{"0123456789abcdef"},
		}, core.ClientSpec{Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111"})},

		{"hysteria2", one("h2", "hysteria2", 41020, map[string]any{
			"tls_cert_path": "/tmp/fake-cert.pem",
			"tls_key_path":  "/tmp/fake-key.pem",
			"up_mbps":       100,
			"down_mbps":     500,
			"obfs":          "salamander",
			"obfs_password": "obfs-secret",
			"masquerade":    "https://www.bing.com/",
		}, core.ClientSpec{Email: "bob@example.com", Password: "hunter2"})},

		{"trojan", one("trojan", "trojan", 41030, map[string]any{
			"tls_cert_path": "/tmp/fake-cert.pem",
			"tls_key_path":  "/tmp/fake-key.pem",
		}, core.ClientSpec{Email: "carol@example.com", Password: "trojan-pass"})},

		{"shadowsocks", one("ss", "shadowsocks", 41040, map[string]any{
			"method":   "2022-blake3-aes-128-gcm",
			"password": "MTIzNDU2Nzg5MGFiY2RlZg==",
		}, core.ClientSpec{Email: "dave@example.com", Password: "YWJjZGVmMTIzNDU2Nzg5MA=="})},

		{"tuic", one("tuic", "tuic", 41050, map[string]any{
			"tls_cert_path": "/tmp/fake-cert.pem",
			"tls_key_path":  "/tmp/fake-key.pem",
		}, core.ClientSpec{Email: "eve@example.com", UUID: "22222222-2222-2222-2222-222222222222", Password: "tuic-pw"})},

		{"vmess-ws", one("vmess", "vmess", 41060, map[string]any{
			"ws_path": "/vmess",
		}, core.ClientSpec{Email: "frank@example.com", UUID: "33333333-3333-3333-3333-333333333333"})},

		{"vless-ws", one("vless-ws", "vless-ws", 41070, map[string]any{
			"ws_path": "/vless",
		}, core.ClientSpec{Email: "grace@example.com", UUID: "44444444-4444-4444-4444-444444444444"})},

		{"socks-auth", one("socks", "socks", 1080, nil,
			core.ClientSpec{Email: "henry@example.com", Password: "socks-pw"})},

		{"anytls", one("anytls", "anytls", 41080, map[string]any{
			"tls_cert_path": "/tmp/fake-cert.pem",
			"tls_key_path":  "/tmp/fake-key.pem",
		}, core.ClientSpec{Email: "iris@example.com", Password: "anytls-pw"})},
	}

	for _, c := range cases {
		b, err := render(c.cfg)
		if err != nil {
			t.Errorf("render %s: %v", c.name, err)
			continue
		}
		p := filepath.Join(dir, c.name+".json")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", p, len(b))
	}
}

func one(tag, typ string, port int, settings map[string]any, clients ...core.ClientSpec) core.DesiredConfig {
	return core.DesiredConfig{
		Inbounds: []core.InboundSpec{{
			Tag: tag, Engine: core.EngineSingbox, Type: typ,
			Listen: "::", Port: port, Settings: settings, Clients: clients,
		}},
	}
}
