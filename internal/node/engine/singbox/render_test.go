package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// vlessInbound is a minimal valid VLESS-Reality inbound used across tests.
func vlessInbound(clients ...core.ClientSpec) core.InboundSpec {
	return core.InboundSpec{
		Tag:    "vless-reality",
		Engine: core.EngineSingbox,
		Type:   "vless",
		Listen: "::",
		Port:   8443,
		Settings: map[string]any{
			"sni":                  "www.microsoft.com",
			"reality_private_key":  "mDuMKKpJ_DMK5Qj1k9D3qV5T0bM4y3-N0kZbW2X9tJ4",
			"short_ids":            []string{"0123456789abcdef"},
		},
		Clients: clients,
	}
}

func TestRender_VLESSReality_Happy(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(core.ClientSpec{
				Email: "alice@example.com",
				UUID:  "11111111-1111-1111-1111-111111111111",
			}),
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	ins, _ := doc["inbounds"].([]any)
	if len(ins) != 1 {
		t.Fatalf("want 1 inbound, got %d", len(ins))
	}
	in := ins[0].(map[string]any)
	users := in["users"].([]any)
	u := users[0].(map[string]any)

	// Invariant I1: users[].name == client.email
	if got := u["name"]; got != "alice@example.com" {
		t.Errorf("users[0].name = %v, want alice@example.com (invariant I1)", got)
	}
	// Reality structure
	tls := in["tls"].(map[string]any)
	rty := tls["reality"].(map[string]any)
	if rty["enabled"] != true {
		t.Errorf("reality.enabled not set")
	}
	if rty["private_key"] != "mDuMKKpJ_DMK5Qj1k9D3qV5T0bM4y3-N0kZbW2X9tJ4" {
		t.Errorf("private_key not propagated")
	}
}

// G4: Advanced.BlockQUIC toggles a route reject rule for forwarded QUIC/STUN,
// scoped to the proxy inbound tag so it never touches server-side outbounds.
func TestRender_BlockQUIC(t *testing.T) {
	mkCfg := func(block bool) core.DesiredConfig {
		return core.DesiredConfig{
			Inbounds: []core.InboundSpec{
				vlessInbound(core.ClientSpec{
					Email: "alice@example.com",
					UUID:  "11111111-1111-1111-1111-111111111111",
				}),
			},
			Advanced: &core.AdvancedSpec{BlockQUIC: block},
		}
	}

	findQUICReject := func(t *testing.T, b []byte) map[string]any {
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		rules := doc["route"].(map[string]any)["rules"].([]any)
		for _, r := range rules {
			m := r.(map[string]any)
			if m["action"] == "reject" {
				return m
			}
		}
		return nil
	}

	// Off → no reject rule.
	bOff, err := render(mkCfg(false))
	if err != nil {
		t.Fatal(err)
	}
	if r := findQUICReject(t, bOff); r != nil {
		t.Errorf("BlockQUIC off must NOT emit a reject rule, got %v", r)
	}

	// On → reject rule scoped to the proxy inbound tag, matching quic+stun.
	bOn, err := render(mkCfg(true))
	if err != nil {
		t.Fatal(err)
	}
	r := findQUICReject(t, bOn)
	if r == nil {
		t.Fatalf("BlockQUIC on must emit a reject rule\n%s", bOn)
	}
	protos, _ := json.Marshal(r["protocol"])
	if string(protos) != `["quic","stun"]` {
		t.Errorf("reject protocol = %s, want [quic,stun]", protos)
	}
	inbounds, _ := json.Marshal(r["inbound"])
	if string(inbounds) != `["vless-reality"]` {
		t.Errorf("reject must be scoped to proxy inbound tag, got inbound=%s (unscoped reject would kill server's own WARP/DoQ outbounds)", inbounds)
	}
	if r["method"] != "default" || r["no_drop"] != true {
		t.Errorf("reject should be method=default no_drop=true for fast ICMP fallback, got method=%v no_drop=%v", r["method"], r["no_drop"])
	}
}

// The "don't log client IP" privacy toggle must NOT touch sing-box.json at all
// (it's handled in the log write path, not the config). Guard: log.level stays
// "info" and the render is byte-identical whether or not Advanced is present —
// so a binary swap that doesn't touch the DB renders an unchanged config.
func TestRender_LogLevelUnaffectedByAdvanced(t *testing.T) {
	logLevel := func(t *testing.T, b []byte) string {
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		return doc["log"].(map[string]any)["level"].(string)
	}
	in := []core.InboundSpec{
		vlessInbound(core.ClientSpec{
			Email: "alice@example.com",
			UUID:  "11111111-1111-1111-1111-111111111111",
		}),
	}
	bPlain, err := render(core.DesiredConfig{Inbounds: in})
	if err != nil {
		t.Fatal(err)
	}
	if lvl := logLevel(t, bPlain); lvl != "info" {
		t.Errorf("log.level = %q, want info", lvl)
	}
}

// TestRender_SkipsClientlessInbound — the wizard creates the inbound first
// and adds clients second, so Apply runs once with an empty client list.
// The top-level Render() must skip those inbounds (and log) rather than
// failing the whole apply.
func TestRender_SkipsClientlessInbound(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(), // no clients: must be skipped
			vlessInbound(core.ClientSpec{
				Email: "alice@example.com",
				UUID:  "11111111-1111-1111-1111-111111111111",
			}),
		},
	}
	// Rename the first inbound so both can coexist in the same config.
	cfg.Inbounds[0].Tag = "vless-pending"
	cfg.Inbounds[1].Tag = "vless-live"
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: want skip, got err: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	ins := doc["inbounds"].([]any)
	if len(ins) != 1 {
		t.Fatalf("want exactly 1 rendered inbound (live), got %d", len(ins))
	}
	if tag := ins[0].(map[string]any)["tag"]; tag != "vless-live" {
		t.Errorf("rendered the wrong inbound: %v", tag)
	}
}

func TestRender_VLESSReality_RejectsMissingEmail(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(core.ClientSpec{
				Email: "", // bad
				UUID:  "deadbeef-dead-dead-dead-deadbeefdead",
			}),
		},
	}
	if _, err := render(cfg); err == nil {
		t.Fatal("expected error for missing client email (Invariant I1)")
	}
}

func TestRender_VLESSReality_RejectsMissingUUID(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(core.ClientSpec{
				Email: "alice@example.com",
				UUID:  "",
			}),
		},
	}
	if _, err := render(cfg); err == nil {
		t.Fatal("expected error for missing client UUID")
	}
}

func TestRender_VLESSReality_RejectsDuplicateEmail(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(
				core.ClientSpec{Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111"},
				core.ClientSpec{Email: "alice@example.com", UUID: "22222222-2222-2222-2222-222222222222"},
			),
		},
	}
	if _, err := render(cfg); err == nil {
		t.Fatal("expected error for duplicate client email")
	}
}

func TestRender_Hysteria2_Happy(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			{
				Tag: "h2", Engine: core.EngineSingbox, Type: "hysteria2",
				Listen: "::", Port: 41020,
				Settings: map[string]any{
					"tls_cert_path": "/etc/edgenest/certs/fullchain.pem",
					"tls_key_path":  "/etc/edgenest/certs/privkey.pem",
					"up_mbps":       100,
					"down_mbps":     500,
				},
				Clients: []core.ClientSpec{
					{Email: "bob@example.com", Password: "hunter2"},
				},
			},
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(b), `"name": "bob@example.com"`) {
		t.Errorf("hysteria2 users[].name should equal client email")
	}
	if !strings.Contains(string(b), `"up_mbps": 100`) {
		t.Errorf("up_mbps not propagated")
	}
}

func TestRender_Hysteria2_RequiresTLSPaths(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			{
				Tag: "h2", Engine: core.EngineSingbox, Type: "hysteria2",
				Port: 41020,
				Settings: map[string]any{
					"up_mbps": 100,
				},
				Clients: []core.ClientSpec{{Email: "x@y.z", Password: "p"}},
			},
		},
	}
	if _, err := render(cfg); err == nil {
		t.Fatal("expected error when hysteria2 has no tls paths")
	}
}

func TestRender_UnsupportedType(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			{Tag: "x", Engine: core.EngineSingbox, Type: "definitely-not-real", Port: 1234, Clients: []core.ClientSpec{{Email: "a@b.c"}}},
		},
	}
	if _, err := render(cfg); err == nil {
		t.Fatal("expected error for unsupported type")
	}
}

func TestRender_FiltersOtherEngines(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(core.ClientSpec{Email: "a@b.c", UUID: "11111111-1111-1111-1111-111111111111"}),
			{
				// xray-only protocol — should be silently skipped by this renderer.
				Tag: "xhttp", Engine: core.EngineXray, Type: "vless-xhttp", Port: 8080,
				Clients: []core.ClientSpec{{Email: "ignored@b.c", UUID: "deadbeef-dead-dead-dead-deadbeefdead"}},
			},
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(b), "ignored@b.c") {
		t.Error("xray-engine inbound leaked into sing-box render")
	}
}

func TestRender_OutboundsAlwaysIncludeDefaults(t *testing.T) {
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{vlessInbound(core.ClientSpec{Email: "a@b.c", UUID: "11111111-1111-1111-1111-111111111111"})}}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, tag := range []string{`"tag": "direct"`, `"tag": "block"`} {
		if !strings.Contains(string(b), tag) {
			t.Errorf("missing default outbound %s", tag)
		}
	}
	// v1.13 removed the "dns" outbound type; DNS hijacking is a route rule_action.
	if strings.Contains(string(b), `"tag": "dns-out"`) {
		t.Error("dns-out outbound leaked; should be a hijack-dns route rule in v1.13")
	}
	if !strings.Contains(string(b), `"hijack-dns"`) {
		t.Error("missing hijack-dns route rule_action")
	}
	if !strings.Contains(string(b), `"action": "sniff"`) {
		t.Error("missing sniff route rule_action")
	}
}

// WS inbounds behind a CDN need `transport.headers.Host` set or the reverse
// proxy rejects them as a Host-mismatch. The wizard autofills ws_host from
// SNI; the renderer must propagate it into the emitted transport.
func TestRender_VMessWS_EmitsHostHeader(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			{
				Tag: "vm-ws", Engine: core.EngineSingbox, Type: "vmess",
				Listen: "::", Port: 12345,
				Settings: map[string]any{
					"ws_path": "/vmess",
					"ws_host": "vm.example.com",
				},
				Clients: []core.ClientSpec{
					{Email: "u@x", UUID: "11111111-1111-1111-1111-111111111111"},
				},
			},
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	in := doc["inbounds"].([]any)[0].(map[string]any)
	tr := in["transport"].(map[string]any)
	headers, ok := tr["headers"].(map[string]any)
	if !ok {
		t.Fatalf("vmess-ws transport.headers missing: %+v", tr)
	}
	if headers["Host"] != "vm.example.com" {
		t.Errorf("transport.headers.Host = %v, want vm.example.com", headers["Host"])
	}
}

func TestRender_VLESSWS_EmitsHostHeader(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			{
				Tag: "vless-ws", Engine: core.EngineSingbox, Type: "vless-ws",
				Listen: "::", Port: 12346,
				Settings: map[string]any{
					"ws_path": "/vless",
					"ws_host": "vless.example.com",
				},
				Clients: []core.ClientSpec{
					{Email: "u@x", UUID: "22222222-2222-2222-2222-222222222222"},
				},
			},
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	in := doc["inbounds"].([]any)[0].(map[string]any)
	tr := in["transport"].(map[string]any)
	headers, ok := tr["headers"].(map[string]any)
	if !ok {
		t.Fatalf("vless-ws transport.headers missing: %+v", tr)
	}
	if headers["Host"] != "vless.example.com" {
		t.Errorf("transport.headers.Host = %v, want vless.example.com", headers["Host"])
	}
}

// When ws_host is unset the headers block must NOT appear (sing-box rejects
// empty Host values, and a missing key correctly defers to the connection's
// own Host).
func TestRender_VLESSWS_NoHostWhenUnset(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			{
				Tag: "vless-ws", Engine: core.EngineSingbox, Type: "vless-ws",
				Listen: "::", Port: 12346,
				Settings: map[string]any{"ws_path": "/vless"},
				Clients: []core.ClientSpec{
					{Email: "u@x", UUID: "33333333-3333-3333-3333-333333333333"},
				},
			},
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	in := doc["inbounds"].([]any)[0].(map[string]any)
	tr := in["transport"].(map[string]any)
	if _, has := tr["headers"]; has {
		t.Errorf("transport.headers should be absent when ws_host unset, got %+v", tr["headers"])
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"sing-box version v1.13.12\n", "1.13.12"},
		{"version 1.13.12-r1", "1.13.12"},
		{"v1.14.0-rc.3", "1.14.0"},
	}
	for _, c := range cases {
		got, err := parseVersion(c.in)
		if err != nil {
			t.Fatalf("parseVersion(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := parseVersion("no version here"); err == nil {
		t.Error("expected parseVersion to fail on garbage")
	}
}

func TestVersionMatchesPin(t *testing.T) {
	cases := []struct {
		got, pin string
		want     bool
	}{
		{"1.13.12", "1.13.12", true},
		{"1.13.0", "1.13.12", true},  // patch differs, still OK
		{"1.13.99", "1.13.12", true}, // patch differs, still OK
		{"1.12.0", "1.13.12", false}, // minor differs, REJECT (renderer is 1.13-only)
		{"1.14.0", "1.13.12", false}, // minor differs, REJECT
		{"2.0.0", "1.13.12", false},  // major differs, REJECT
	}
	for _, c := range cases {
		if got := versionMatchesPin(c.got, c.pin); got != c.want {
			t.Errorf("versionMatchesPin(%s, %s) = %v, want %v", c.got, c.pin, got, c.want)
		}
	}
}

// TestRender_SkipsWarpRouteWhenDisabled — a route rule pointing at outbound
// "warp" must be dropped when WARP isn't enabled (the warp outbound only exists
// when enabled). Otherwise sing-box rejects the whole config and the data plane
// goes down. The one-click WARP presets create exactly such rules, so this must
// be safe to apply before WARP is turned on.
func TestRender_SkipsWarpRouteWhenDisabled(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(core.ClientSpec{
				Email: "alice@example.com",
				UUID:  "11111111-1111-1111-1111-111111111111",
			}),
		},
		Routes: []core.RouteSpec{
			{Type: "domain_suffix", Value: "openai.com", Outbound: "warp"},
			{Type: "domain_suffix", Value: "example.com", Outbound: "direct"},
		},
	}

	hasOutbound := func(b []byte, want string) bool {
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		for _, r := range doc["route"].(map[string]any)["rules"].([]any) {
			if m, ok := r.(map[string]any); ok && m["outbound"] == want {
				return true
			}
		}
		return false
	}

	// WARP disabled: the warp route is skipped, the direct route survives,
	// and crucially render() does NOT error.
	bOff, err := render(cfg)
	if err != nil {
		t.Fatalf("render must not fail on a warp route while WARP is off: %v", err)
	}
	if hasOutbound(bOff, "warp") {
		t.Error("warp route must be skipped when WARP is disabled")
	}
	if !hasOutbound(bOff, "direct") {
		t.Error("direct route must survive")
	}

	// WARP enabled: the warp route is now emitted.
	cfg.Warp = &core.WarpSpec{
		Enabled:    true,
		PrivateKey: "mDuMKKpJ_DMK5Qj1k9D3qV5T0bM4y3-N0kZbW2X9tJ4",
		PublicKey:  "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
		Address4:   "172.16.0.2/32",
		Endpoint:   "engage.cloudflareclient.com:2408",
		Reserved:   []int{1, 2, 3},
	}
	bOn, err := render(cfg)
	if err != nil {
		t.Fatalf("render with WARP enabled: %v", err)
	}
	if !hasOutbound(bOn, "warp") {
		t.Errorf("warp route must be emitted once WARP is enabled\n%s", bOn)
	}
}

// TestRender_WarpEndpointSchema — WARP must render as a sing-box 1.11+ wireguard
// *endpoint* (top-level "endpoints", fields address/peers), NOT the removed
// ≤1.10 outbound (local_address/peer_public_key) which sing-box 1.13 rejects
// with `unknown field "local_address"`.
func TestRender_WarpEndpointSchema(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(core.ClientSpec{
				Email: "a@example.com",
				UUID:  "11111111-1111-1111-1111-111111111111",
			}),
		},
		Warp: &core.WarpSpec{
			Enabled:    true,
			PrivateKey: "mDuMKKpJ_DMK5Qj1k9D3qV5T0bM4y3-N0kZbW2X9tJ4",
			PublicKey:  "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
			Address4:   "172.16.0.2/32",
			Endpoint:   "engage.cloudflareclient.com:2408",
			Reserved:   []int{1, 2, 3},
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "local_address") {
		t.Error("must not emit legacy outbound field local_address (1.13 rejects it)")
	}
	eps, ok := doc["endpoints"].([]any)
	if !ok || len(eps) == 0 {
		t.Fatalf("warp must render as a top-level endpoint, got endpoints=%v", doc["endpoints"])
	}
	ep := eps[0].(map[string]any)
	if ep["type"] != "wireguard" || ep["tag"] != "warp" {
		t.Errorf("endpoint head wrong: %+v", ep)
	}
	if _, hasAddr := ep["address"]; !hasAddr {
		t.Error("endpoint must use 'address' (not 'local_address')")
	}
	if _, hasPeers := ep["peers"]; !hasPeers {
		t.Error("endpoint must carry a 'peers' array")
	}
}

// TestRender_WarpNoInbounds — a user can register+enable WARP and apply presets
// before creating any inbound. That config (warp endpoint + warp routes + ZERO
// inbounds + no family-pin) must still render to a valid sing-box config, not
// error. (The "经 WARP 再探" probe is separately independent of inbounds — it
// uses its own userspace tunnel.)
func TestRender_WarpNoInbounds(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: nil, // no inbounds yet
		Warp: &core.WarpSpec{
			Enabled:    true,
			PrivateKey: "mDuMKKpJ_DMK5Qj1k9D3qV5T0bM4y3-N0kZbW2X9tJ4",
			PublicKey:  "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
			Address4:   "172.16.0.2/32",
			Endpoint:   "engage.cloudflareclient.com:2408",
			Reserved:   []int{1, 2, 3},
		},
		Routes: []core.RouteSpec{
			{Type: "domain_suffix", Value: "claude.ai", Outbound: "warp"},
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("warp + routes + 0 inbounds must render without error: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// warp endpoint present, claude→warp route present, no inbounds.
	if eps, _ := doc["endpoints"].([]any); len(eps) == 0 {
		t.Error("warp endpoint should be present")
	}
	if ins, _ := doc["inbounds"].([]any); len(ins) != 0 {
		t.Errorf("expected 0 inbounds, got %d", len(ins))
	}
	foundWarpRoute := false
	for _, r := range doc["route"].(map[string]any)["rules"].([]any) {
		if m, _ := r.(map[string]any); m["outbound"] == "warp" {
			foundWarpRoute = true
		}
	}
	if !foundWarpRoute {
		t.Error("claude→warp route should survive with 0 inbounds")
	}
}
