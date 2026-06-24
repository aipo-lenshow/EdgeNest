package xray

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

func mustRender(t *testing.T, cfg core.DesiredConfig) map[string]any {
	t.Helper()
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestRender_OnlyXrayInbounds(t *testing.T) {
	// sing-box inbound must be filtered out, xray inbound included.
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{
		{Engine: core.EngineSingbox, Type: "vless", Tag: "skipme", Port: 443,
			Clients: []core.ClientSpec{{Email: "x", UUID: "u"}}},
		{Engine: core.EngineXray, Type: "vless-xhttp", Tag: "keepme", Port: 444,
			Settings: map[string]any{
				"sni":                  "www.microsoft.com",
				"reality_private_key":  "PRIV",
				"xhttp_path":           "/x",
			},
			Clients: []core.ClientSpec{{Email: "a@x", UUID: "u-1"}}},
	}}
	out := mustRender(t, cfg)
	inbs := out["inbounds"].([]any)
	if len(inbs) != 1 {
		t.Fatalf("want 1 inbound, got %d", len(inbs))
	}
	if inbs[0].(map[string]any)["tag"] != "keepme" {
		t.Errorf("wrong tag kept: %v", inbs[0])
	}
}

func TestRender_VLESSXHTTP_RealityShape(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{{
		Engine: core.EngineXray, Type: "vless-xhttp", Tag: "vx",
		Listen: "::", Port: 443,
		Settings: map[string]any{
			"sni":                 "www.microsoft.com",
			"reality_private_key": "PRIV",
			"short_ids":           []string{"ab"},
			"xhttp_path":          "/path",
		},
		Clients: []core.ClientSpec{{Email: "u@x", UUID: "uuid-1", Flow: "xtls-rprx-vision"}},
	}}}
	out := mustRender(t, cfg)
	inb := out["inbounds"].([]any)[0].(map[string]any)
	if inb["protocol"] != "vless" {
		t.Errorf("protocol = %v, want vless", inb["protocol"])
	}
	stream := inb["streamSettings"].(map[string]any)
	if stream["network"] != "xhttp" || stream["security"] != "reality" {
		t.Errorf("stream wrong: %+v", stream)
	}
	reality := stream["realitySettings"].(map[string]any)
	if reality["dest"] != "www.microsoft.com:443" {
		t.Errorf("dest = %v", reality["dest"])
	}
	if reality["privateKey"] != "PRIV" {
		t.Errorf("privateKey lost")
	}
	xh := stream["xhttpSettings"].(map[string]any)
	if xh["path"] != "/path" {
		t.Errorf("xhttp path = %v", xh["path"])
	}
	clients := inb["settings"].(map[string]any)["clients"].([]any)
	c0 := clients[0].(map[string]any)
	// flow=xtls-rprx-vision is incompatible with xhttp transport — xray-core
	// refuses to boot the inbound when both are set. The renderer must drop
	// the inbound's flow on xhttp regardless of what the client row carried
	// (Reality-TCP defaults often leak in here).
	if c0["email"] != "u@x" || c0["id"] != "uuid-1" || c0["flow"] != "" {
		t.Errorf("client wrong: %+v", c0)
	}
}

func TestRender_VLESSXHTTP_TLSMode(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{{
		Engine: core.EngineXray, Type: "vless-xhttp", Tag: "vx",
		Port: 443,
		Settings: map[string]any{
			"security":      "tls",
			"tls_cert_path": "/c.pem",
			"tls_key_path":  "/k.pem",
		},
		Clients: []core.ClientSpec{{Email: "u@x", UUID: "u-1"}},
	}}}
	out := mustRender(t, cfg)
	stream := out["inbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)
	tls := stream["tlsSettings"].(map[string]any)
	certs := tls["certificates"].([]any)
	c := certs[0].(map[string]any)
	if c["certificateFile"] != "/c.pem" || c["keyFile"] != "/k.pem" {
		t.Errorf("tls cert wrong: %+v", c)
	}
	if _, has := stream["realitySettings"]; has {
		t.Error("tls mode must not emit realitySettings")
	}
}

func TestRender_RejectsBadSecurity(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{{
		Engine: core.EngineXray, Type: "vless-xhttp", Tag: "vx", Port: 443,
		Settings: map[string]any{"security": "bogus"},
		Clients:  []core.ClientSpec{{Email: "x@x", UUID: "u"}},
	}}}
	if _, err := render(cfg); err == nil || !strings.Contains(err.Error(), "security") {
		t.Errorf("want security error, got %v", err)
	}
}

func TestRender_VLESSXHTTP_RequiresUUID(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{{
		Engine: core.EngineXray, Type: "vless-xhttp", Tag: "vx", Port: 443,
		Settings: map[string]any{"sni": "x", "reality_private_key": "p"},
		Clients:  []core.ClientSpec{{Email: "x@x"}},
	}}}
	if _, err := render(cfg); err == nil || !strings.Contains(err.Error(), "UUID") {
		t.Errorf("want UUID error, got %v", err)
	}
}

func TestRender_EnforcesEmailUnique(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{{
		Engine: core.EngineXray, Type: "vless-xhttp", Tag: "vx", Port: 443,
		Settings: map[string]any{"sni": "x", "reality_private_key": "p"},
		Clients: []core.ClientSpec{
			{Email: "dup@x", UUID: "u1"},
			{Email: "dup@x", UUID: "u2"},
		},
	}}}
	if _, err := render(cfg); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("want duplicate error, got %v", err)
	}
}

func TestRender_EnforcesEmailNonEmpty(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{{
		Engine: core.EngineXray, Type: "vless-xhttp", Tag: "vx", Port: 443,
		Settings: map[string]any{"sni": "x", "reality_private_key": "p"},
		Clients: []core.ClientSpec{{UUID: "u1"}}, // no Email
	}}}
	if _, err := render(cfg); err == nil || !strings.Contains(err.Error(), "Invariant I1") {
		t.Errorf("want I1 error, got %v", err)
	}
}

// anytls is NOT supported by xray-core mainline (verified against v26.3.27 —
// "unknown config id: anytls"). The protocol now belongs to the sing-box
// engine. If someone forces an anytls inbound onto xray we must reject it
// at render time so it surfaces as a user error, not a runtime crash.
func TestRender_RejectsAnyTLS(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{{
		Engine: core.EngineXray, Type: "anytls", Tag: "atls", Port: 8443,
		Settings: map[string]any{
			"tls_cert_path": "/c.pem",
			"tls_key_path":  "/k.pem",
		},
		Clients: []core.ClientSpec{{Email: "u@x", Password: "p"}},
	}}}
	if _, err := render(cfg); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("want unsupported error, got %v", err)
	}
}

func TestRender_DefaultOutbounds(t *testing.T) {
	cfg := core.DesiredConfig{}
	out := mustRender(t, cfg)
	obs := out["outbounds"].([]any)
	// design: direct + direct-v4 + direct-v6 + blackhole. The per-family
	// freedom outbounds are always emitted so route rules can pin v4 or v6
	// explicitly without us banning the other family on a single-stack host.
	if len(obs) != 4 {
		t.Fatalf("want 4 default outbounds (direct + direct-v4 + direct-v6 + block), got %d", len(obs))
	}
	if obs[0].(map[string]any)["protocol"] != "freedom" || obs[0].(map[string]any)["tag"] != "direct" {
		t.Errorf("outbounds[0] = %+v, want freedom/direct", obs[0])
	}
	if obs[1].(map[string]any)["tag"] != "direct-v4" {
		t.Errorf("outbounds[1] = %+v, want direct-v4", obs[1])
	}
	if obs[2].(map[string]any)["tag"] != "direct-v6" {
		t.Errorf("outbounds[2] = %+v, want direct-v6", obs[2])
	}
	if obs[3].(map[string]any)["protocol"] != "blackhole" {
		t.Errorf("outbounds[3] = %+v, want blackhole", obs[3])
	}
}

func TestRender_WarpOutboundShape(t *testing.T) {
	cfg := core.DesiredConfig{Warp: &core.WarpSpec{
		Enabled: true, PrivateKey: "PRIV", PublicKey: "PUB",
		Address4: "172.16.0.2/32", Address6: "fd00::2/128",
		Endpoint: "engage.cloudflareclient.com:2408",
		Reserved: []int{1, 2, 3},
	}}
	out := mustRender(t, cfg)
	obs := out["outbounds"].([]any)
	// Layout: direct, direct-v4, direct-v6, block, then warp (cfg outbounds
	// are appended after the four defaults; warp is appended last when enabled).
	warp := obs[4].(map[string]any)
	if warp["protocol"] != "wireguard" || warp["tag"] != "warp" {
		t.Errorf("warp shape wrong: %+v", warp)
	}
	settings := warp["settings"].(map[string]any)
	if settings["secretKey"] != "PRIV" {
		t.Errorf("secretKey wrong")
	}
	peer := settings["peers"].([]any)[0].(map[string]any)
	if peer["publicKey"] != "PUB" || peer["endpoint"] != "engage.cloudflareclient.com:2408" {
		t.Errorf("peer wrong: %+v", peer)
	}
}

func TestRenderRoute_AllShapes(t *testing.T) {
	cases := []struct {
		name string
		spec core.RouteSpec
		want map[string]any
	}{
		{"domain", core.RouteSpec{Type: "domain", Value: "example.com", Outbound: "direct"},
			map[string]any{"type": "field", "outboundTag": "direct", "domain": []any{"full:example.com"}}},
		{"suffix", core.RouteSpec{Type: "domain_suffix", Value: "openai.com", Outbound: "warp"},
			map[string]any{"type": "field", "outboundTag": "warp", "domain": []any{"domain:openai.com"}}},
		{"keyword", core.RouteSpec{Type: "domain_keyword", Value: "google", Outbound: "warp"},
			map[string]any{"type": "field", "outboundTag": "warp", "domain": []any{"keyword:google"}}},
		{"regex", core.RouteSpec{Type: "domain_regex", Value: ".*ai$", Outbound: "warp"},
			map[string]any{"type": "field", "outboundTag": "warp", "domain": []any{"regexp:.*ai$"}}},
		{"geosite", core.RouteSpec{Type: "geosite", Value: "cn", Outbound: "direct"},
			map[string]any{"type": "field", "outboundTag": "direct", "domain": []any{"geosite:cn"}}},
		{"geoip", core.RouteSpec{Type: "geoip", Value: "cn", Outbound: "direct"},
			map[string]any{"type": "field", "outboundTag": "direct", "ip": []any{"geoip:cn"}}},
		{"ipcidr", core.RouteSpec{Type: "ip_cidr", Value: "10.0.0.0/8", Outbound: "block"},
			map[string]any{"type": "field", "outboundTag": "block", "ip": []any{"10.0.0.0/8"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := core.DesiredConfig{Routes: []core.RouteSpec{c.spec}}
			// Routes pointing at "warp" only survive when the warp outbound is
			// emitted (render skips dangling outbounds, see render.go guard), so
			// enable WARP for those shape cases.
			if c.spec.Outbound == "warp" {
				cfg.Warp = &core.WarpSpec{
					Enabled:    true,
					PrivateKey: "mDuMKKpJ_DMK5Qj1k9D3qV5T0bM4y3-N0kZbW2X9tJ4",
					PublicKey:  "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
					Address4:   "172.16.0.2/32",
					Endpoint:   "engage.cloudflareclient.com:2408",
					Reserved:   []int{1, 2, 3},
				}
			}
			out := mustRender(t, cfg)
			rules := out["routing"].(map[string]any)["rules"].([]any)
			if len(rules) != 1 {
				t.Fatalf("want 1 rule, got %d", len(rules))
			}
			got := rules[0].(map[string]any)
			if got["type"] != c.want["type"] || got["outboundTag"] != c.want["outboundTag"] {
				t.Errorf("rule head: %+v", got)
			}
			for k, v := range c.want {
				if k == "type" || k == "outboundTag" {
					continue
				}
				if !sliceEq(got[k], v) {
					t.Errorf("rule[%s] = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func sliceEq(a, b any) bool {
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if !aok || !bok || len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func TestVersionMatchesMajor(t *testing.T) {
	if !versionMatchesMajor("26.3.27", "26") {
		t.Error("26.3.27 should match major 26")
	}
	if !versionMatchesMajor("26.9.0", "26") {
		t.Error("26.9.0 should match major 26")
	}
	if versionMatchesMajor("25.12.31", "26") {
		t.Error("25.12.31 should NOT match major 26")
	}
	if versionMatchesMajor("27.1.0", "26") {
		t.Error("27.1.0 should NOT match major 26")
	}
}
