package xray

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// TestEmitSamples writes one sample JSON per supported xray-core protocol into
// $EDGENEST_SAMPLES_DIR/xray/ so the operator can run `xray test -c <file>`
// against each. Only runs when EDGENEST_EMIT_SAMPLES=1.
func TestEmitSamples(t *testing.T) {
	if os.Getenv("EDGENEST_EMIT_SAMPLES") != "1" {
		t.Skip("set EDGENEST_EMIT_SAMPLES=1 to emit sample configs")
	}
	dir := os.Getenv("EDGENEST_SAMPLES_DIR")
	if dir == "" {
		dir = "/tmp/edgenest-samples"
	}
	dir = filepath.Join(dir, "xray")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		cfg  core.DesiredConfig
	}{
		{"vless-xhttp-reality", one("vless-xhttp", "vless-xhttp", 8443, map[string]any{
			"sni":                 "www.microsoft.com",
			"reality_private_key": "cNN6M7kmXcxtkreoDfuY7W0u6SQeTwaJ5uZ98R3AbXc",
			"short_ids":           []string{"0123456789abcdef"},
			"xhttp_path":          "/xhttp",
		}, core.ClientSpec{Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111"})},

		{"vless-xhttp-tls", one("vless-xhttp-tls", "vless-xhttp", 8444, map[string]any{
			"security":      "tls",
			"tls_cert_path": "/tmp/fake-cert.pem",
			"tls_key_path":  "/tmp/fake-key.pem",
			"sni":           "example.com",
			"xhttp_path":    "/xhttp",
		}, core.ClientSpec{Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111"})},

		{"vless-xhttp-none", one("vless-xhttp-cdn", "vless-xhttp", 8080, map[string]any{
			"security":   "none",
			"xhttp_path": "/xhttp",
		}, core.ClientSpec{Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111"})},
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
			Tag: tag, Engine: core.EngineXray, Type: typ,
			Listen: "::", Port: port, Settings: settings, Clients: clients,
		}},
	}
}
