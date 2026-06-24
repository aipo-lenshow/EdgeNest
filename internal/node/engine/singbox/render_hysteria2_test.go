package singbox

import (
	"encoding/json"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

func h2Inbound(extra map[string]any, clients ...core.ClientSpec) core.InboundSpec {
	s := map[string]any{
		"tls_cert_path": "/etc/edgenest/certs/wizard-fullchain.pem",
		"tls_key_path":  "/etc/edgenest/certs/wizard-privkey.pem",
		"sni":           "edgenest.local",
	}
	for k, v := range extra {
		s[k] = v
	}
	return core.InboundSpec{
		Tag:      "h2",
		Engine:   core.EngineSingbox,
		Type:     "hysteria2",
		Listen:   "::",
		Port:     41020,
		Settings: s,
		Clients:  clients,
	}
}

func renderH2(t *testing.T, in core.InboundSpec) map[string]any {
	t.Helper()
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{in}}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return doc["inbounds"].([]any)[0].(map[string]any)
}

func TestRender_Hysteria2_Basic(t *testing.T) {
	in := renderH2(t, h2Inbound(nil, core.ClientSpec{
		Email: "alice@example.com", Password: "secret",
	}))
	if in["type"] != "hysteria2" {
		t.Errorf("wrong type: %v", in["type"])
	}
	users := in["users"].([]any)
	u := users[0].(map[string]any)
	if u["name"] != "alice@example.com" {
		t.Errorf("invariant I1 broken: name=%v", u["name"])
	}
	if u["password"] != "secret" {
		t.Errorf("password mismatch: %v", u["password"])
	}
	tls := in["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Error("tls not enabled")
	}
	alpn := tls["alpn"].([]any)
	if len(alpn) != 1 || alpn[0] != "h3" {
		t.Errorf("alpn = %v, want [h3]", alpn)
	}
	if _, hasInbound := in["sniff"]; hasInbound {
		t.Errorf("sing-box v1.13 removed inbound-level sniff; expected absent, got %v", in["sniff"])
	}
}

func TestRender_Hysteria2_BandwidthAndIgnore(t *testing.T) {
	in := renderH2(t, h2Inbound(map[string]any{
		"up_mbps":                 100,
		"down_mbps":               500,
		"ignore_client_bandwidth": true,
	}, core.ClientSpec{Email: "a", Password: "p"}))
	if int(in["up_mbps"].(float64)) != 100 {
		t.Errorf("up_mbps = %v", in["up_mbps"])
	}
	if int(in["down_mbps"].(float64)) != 500 {
		t.Errorf("down_mbps = %v", in["down_mbps"])
	}
	if in["ignore_client_bandwidth"] != true {
		t.Error("ignore_client_bandwidth missing")
	}
}

func TestRender_Hysteria2_ObfsRequiresPassword(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{h2Inbound(map[string]any{
		"obfs": "salamander",
		// obfs_password intentionally missing
	}, core.ClientSpec{Email: "a", Password: "p"})}}
	if _, err := render(cfg); err == nil {
		t.Fatal("obfs without obfs_password should fail")
	}
}

func TestRender_Hysteria2_ObfsHappy(t *testing.T) {
	in := renderH2(t, h2Inbound(map[string]any{
		"obfs":          "salamander",
		"obfs_password": "obfs-sec",
	}, core.ClientSpec{Email: "a", Password: "p"}))
	obfs := in["obfs"].(map[string]any)
	if obfs["type"] != "salamander" || obfs["password"] != "obfs-sec" {
		t.Errorf("obfs = %+v", obfs)
	}
}

// String URL shorthand still works in v1.13 alongside the typed object form.
func TestRender_Hysteria2_MasqueradeShorthand(t *testing.T) {
	in := renderH2(t, h2Inbound(map[string]any{
		"masquerade": "https://example.org/",
	}, core.ClientSpec{Email: "a", Password: "p"}))
	if mq, _ := in["masquerade"].(string); mq != "https://example.org/" {
		t.Errorf("masquerade shorthand = %v", in["masquerade"])
	}
}

func TestRender_Hysteria2_MasqueradeProxyType(t *testing.T) {
	in := renderH2(t, h2Inbound(map[string]any{
		"masquerade_type": "proxy",
		"masquerade_url":  "https://upstream.example/",
	}, core.ClientSpec{Email: "a", Password: "p"}))
	mq, _ := in["masquerade"].(map[string]any)
	if mq["type"] != "proxy" || mq["url"] != "https://upstream.example/" || mq["rewrite_host"] != true {
		t.Errorf("masquerade proxy-type = %+v", in["masquerade"])
	}
}

func TestRender_Hysteria2_MasqueradeStringType(t *testing.T) {
	in := renderH2(t, h2Inbound(map[string]any{
		"masquerade_type":    "string",
		"masquerade_content": "<html>hi</html>",
	}, core.ClientSpec{Email: "a", Password: "p"}))
	mq, _ := in["masquerade"].(map[string]any)
	if mq["type"] != "string" || mq["content"] != "<html>hi</html>" {
		t.Errorf("masquerade string-type = %+v", in["masquerade"])
	}
	if int(mq["status_code"].(float64)) != 200 {
		t.Errorf("status_code default = %v, want 200", mq["status_code"])
	}
}

func TestRender_Hysteria2_MasqueradeFileType(t *testing.T) {
	in := renderH2(t, h2Inbound(map[string]any{
		"masquerade_type": "file",
		"masquerade_dir":  "/var/www/decoy",
	}, core.ClientSpec{Email: "a", Password: "p"}))
	mq, _ := in["masquerade"].(map[string]any)
	if mq["type"] != "file" || mq["directory"] != "/var/www/decoy" {
		t.Errorf("masquerade file-type = %+v", in["masquerade"])
	}
}

func TestRender_Hysteria2_RequiresCertPaths(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{{
		Tag: "h2", Engine: core.EngineSingbox, Type: "hysteria2",
		Listen: "::", Port: 41020,
		Settings: map[string]any{"sni": "x"},
		Clients:  []core.ClientSpec{{Email: "a", Password: "p"}},
	}}}
	if _, err := render(cfg); err == nil {
		t.Fatal("missing cert paths should error")
	}
}
