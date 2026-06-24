package singbox

import (
	"encoding/json"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// vlessInboundWithHost is vlessInbound + SubscriptionHost set, so the new
// family-pinning rules can be exercised without changing every
// existing test fixture (which leaves SubscriptionHost empty and falls
// through to the default direct outbound, the earlier behaviour).
func vlessInboundWithHost(tag, host string, clients ...core.ClientSpec) core.InboundSpec {
	in := vlessInbound(clients...)
	in.Tag = tag
	in.SubscriptionHost = host
	return in
}

// findRouteRules extracts route.rules from the rendered doc as []map[string]any.
func findRouteRules(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	route, _ := doc["route"].(map[string]any)
	rulesRaw, _ := route["rules"].([]any)
	out := make([]map[string]any, 0, len(rulesRaw))
	for _, raw := range rulesRaw {
		if r, ok := raw.(map[string]any); ok {
			out = append(out, r)
		}
	}
	return out
}

// findRuleWithOutbound returns the rule whose `outbound` field matches tag.
// nil when no such rule exists. Lets each test assert "the v6 pin rule is
// there" without caring about its position relative to the sniff / hijack-dns
// rules that come before it.
func findRuleWithOutbound(rules []map[string]any, outbound string) map[string]any {
	for _, r := range rules {
		if ob, _ := r["outbound"].(string); ob == outbound {
			return r
		}
	}
	return nil
}

func TestRender_FamilyPinning_V6InboundRoutedToDirectV6(t *testing.T) {
	withCapability(t, true, true)
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInboundWithHost("vless-v6-8443", "2001:db8:5500:ccc4::2",
				core.ClientSpec{
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
		t.Fatalf("invalid JSON: %v", err)
	}
	rules := findRouteRules(t, doc)
	v6Rule := findRuleWithOutbound(rules, "direct-v6")
	if v6Rule == nil {
		t.Fatal("expected a route rule pinning v6 inbounds to direct-v6, got none")
	}
	tags, _ := v6Rule["inbound"].([]any)
	if len(tags) != 1 || tags[0] != "vless-v6-8443" {
		t.Errorf("v6 rule inbound list = %v, want [vless-v6-8443]", tags)
	}
	if findRuleWithOutbound(rules, "direct-v4") != nil {
		t.Error("no v4 inbounds in cfg, but a direct-v4 pin rule was emitted")
	}
}

func TestRender_FamilyPinning_V4InboundRoutedToDirectV4(t *testing.T) {
	withCapability(t, true, true)
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInboundWithHost("vless-v4-8443", "203.0.113.10",
				core.ClientSpec{
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
		t.Fatalf("invalid JSON: %v", err)
	}
	rules := findRouteRules(t, doc)
	v4Rule := findRuleWithOutbound(rules, "direct-v4")
	if v4Rule == nil {
		t.Fatal("expected a route rule pinning v4 inbounds to direct-v4, got none")
	}
	tags, _ := v4Rule["inbound"].([]any)
	if len(tags) != 1 || tags[0] != "vless-v4-8443" {
		t.Errorf("v4 rule inbound list = %v, want [vless-v4-8443]", tags)
	}
}

func TestRender_FamilyPinning_DualStackBatch(t *testing.T) {
	withCapability(t, true, true)
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInboundWithHost("vless-v4-8443", "203.0.113.10",
				core.ClientSpec{Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111"}),
			vlessInboundWithHost("vless-v6-8443", "2001:db8:5500:ccc4::2",
				core.ClientSpec{Email: "alice@example.com", UUID: "22222222-2222-2222-2222-222222222222"}),
		},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	rules := findRouteRules(t, doc)
	v4Rule := findRuleWithOutbound(rules, "direct-v4")
	v6Rule := findRuleWithOutbound(rules, "direct-v6")
	if v4Rule == nil || v6Rule == nil {
		t.Fatalf("dual-stack: want both v4 + v6 pin rules, got v4=%v v6=%v", v4Rule, v6Rule)
	}
	v4Tags, _ := v4Rule["inbound"].([]any)
	v6Tags, _ := v6Rule["inbound"].([]any)
	if len(v4Tags) != 1 || v4Tags[0] != "vless-v4-8443" {
		t.Errorf("v4 pin rule inbound = %v, want [vless-v4-8443]", v4Tags)
	}
	if len(v6Tags) != 1 || v6Tags[0] != "vless-v6-8443" {
		t.Errorf("v6 pin rule inbound = %v, want [vless-v6-8443]", v6Tags)
	}
}

// TestRender_FamilyPinning_EmptyHostNoRule guards the Argo / earlier
// migration path: SubscriptionHost empty → fall through to default direct
// outbound, no pin rule emitted (otherwise the rule would have an empty
// `inbound` field and sing-box would FATAL on parse).
func TestRender_FamilyPinning_EmptyHostNoRule(t *testing.T) {
	withCapability(t, true, true)
	in := vlessInbound(core.ClientSpec{
		Email: "alice@example.com",
		UUID:  "11111111-1111-1111-1111-111111111111",
	})
	// in.SubscriptionHost is intentionally empty.
	cfg := core.DesiredConfig{Inbounds: []core.InboundSpec{in}}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	rules := findRouteRules(t, doc)
	if findRuleWithOutbound(rules, "direct-v6") != nil {
		t.Error("SubscriptionHost empty: must not emit a direct-v6 pin rule")
	}
	if findRuleWithOutbound(rules, "direct-v4") != nil {
		t.Error("SubscriptionHost empty: must not emit a direct-v4 pin rule")
	}
}

// TestRender_RouteOutbounds proves the user-facing routing semantics the
// operator asked us to verify: a direct/block/warp route rule maps to a real
// sing-box outbound tag (so the rule actually takes effect), and a warp-targeted
// route is dropped while WARP is disabled (the 0-30 guard) but appears once WARP
// is enabled. Disabled rules are filtered upstream in the orchestrator, so they
// never reach render — see orchestrator.go `if !r.Enabled { continue }`.
func TestRender_RouteOutbounds(t *testing.T) {
	withCapability(t, true, true)
	mkcfg := func(warpEnabled bool) core.DesiredConfig {
		cfg := core.DesiredConfig{
			Inbounds: []core.InboundSpec{
				vlessInbound(core.ClientSpec{
					Email: "a@b.c", UUID: "11111111-1111-1111-1111-111111111111",
				}),
			},
			Routes: []core.RouteSpec{
				{Type: "domain_suffix", Value: "direct.example", Outbound: "direct"},
				{Type: "domain_suffix", Value: "block.example", Outbound: "block"},
				{Type: "domain_suffix", Value: "warp.example", Outbound: "warp"},
			},
		}
		if warpEnabled {
			cfg.Warp = &core.WarpSpec{
				Enabled: true, PrivateKey: "k", PublicKey: "K",
				Address4: "172.16.0.2", Reserved: []int{0, 0, 0},
				Endpoint: "162.159.192.1:2408",
			}
		}
		return cfg
	}

	ruleFor := func(rules []map[string]any, domain string) map[string]any {
		for _, r := range rules {
			if ds, ok := r["domain_suffix"].([]any); ok {
				for _, d := range ds {
					if d == domain {
						return r
					}
				}
			}
		}
		return nil
	}

	// WARP disabled: direct + block present, warp route skipped by the guard.
	b, err := render(mkcfg(false))
	if err != nil {
		t.Fatalf("render (warp off): %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	rules := findRouteRules(t, doc)
	if r := ruleFor(rules, "direct.example"); r == nil || r["outbound"] != "direct" {
		t.Errorf("direct route missing/mismapped: %v", r)
	}
	if r := ruleFor(rules, "block.example"); r == nil || r["outbound"] != "block" {
		t.Errorf("block route missing/mismapped: %v", r)
	}
	if ruleFor(rules, "warp.example") != nil {
		t.Error("warp route should be skipped while WARP is disabled (0-30 guard)")
	}

	// WARP enabled: the warp route now renders against the warp endpoint.
	b, err = render(mkcfg(true))
	if err != nil {
		t.Fatalf("render (warp on): %v", err)
	}
	doc = map[string]any{}
	_ = json.Unmarshal(b, &doc)
	rules = findRouteRules(t, doc)
	if r := ruleFor(rules, "warp.example"); r == nil || r["outbound"] != "warp" {
		t.Errorf("warp route missing/mismapped when WARP enabled: %v", r)
	}
}
