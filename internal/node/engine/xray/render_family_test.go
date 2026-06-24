package xray

import (
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// xhttpInboundWithHost builds a VLESS-XHTTP-Reality inbound (the only inbound
// type xray handles in this project) with a specified Tag + SubscriptionHost,
// so the family-pinning rules can be exercised. Reality keys are stub strings
// — render doesn't validate them, they only have to be non-empty.
func xhttpInboundWithHost(tag, host string, client core.ClientSpec) core.InboundSpec {
	return core.InboundSpec{
		Engine: core.EngineXray,
		Type:   "vless-xhttp",
		Tag:    tag,
		Listen: "::",
		Port:   8447,
		Settings: map[string]any{
			"sni":                 "www.microsoft.com",
			"reality_private_key": "PRIV",
			"short_ids":           []string{"0123456789abcdef"},
			"xhttp_path":          "/x",
			"security":            "reality",
		},
		Clients:          []core.ClientSpec{client},
		SubscriptionHost: host,
	}
}

func findRoutingRules(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	routing, _ := doc["routing"].(map[string]any)
	rulesRaw, _ := routing["rules"].([]any)
	out := make([]map[string]any, 0, len(rulesRaw))
	for _, raw := range rulesRaw {
		if r, ok := raw.(map[string]any); ok {
			out = append(out, r)
		}
	}
	return out
}

func findRuleWithOutboundTag(rules []map[string]any, tag string) map[string]any {
	for _, r := range rules {
		if ob, _ := r["outboundTag"].(string); ob == tag {
			return r
		}
	}
	return nil
}

func TestRender_FamilyPinning_V6InboundRoutedToDirectV6(t *testing.T) {
	withCapability(t, true, true)
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			xhttpInboundWithHost("xhttp-v6-8447", "2001:db8:5500:ccc4::2",
				core.ClientSpec{Email: "alice@example.com", UUID: "uuid-1"}),
		},
	}
	doc := mustRender(t, cfg)
	rules := findRoutingRules(t, doc)
	v6Rule := findRuleWithOutboundTag(rules, "direct-v6")
	if v6Rule == nil {
		t.Fatal("expected a routing rule pinning v6 inbounds to direct-v6")
	}
	tags, _ := v6Rule["inboundTag"].([]any)
	if len(tags) != 1 || tags[0] != "xhttp-v6-8447" {
		t.Errorf("v6 rule inboundTag = %v, want [xhttp-v6-8447]", tags)
	}
	if findRuleWithOutboundTag(rules, "direct-v4") != nil {
		t.Error("no v4 inbounds in cfg, but a direct-v4 pin rule was emitted")
	}
}

func TestRender_FamilyPinning_DualStackBatch(t *testing.T) {
	withCapability(t, true, true)
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			xhttpInboundWithHost("xhttp-v4-8447", "203.0.113.10",
				core.ClientSpec{Email: "alice@example.com", UUID: "uuid-1"}),
			xhttpInboundWithHost("xhttp-v6-8447", "2001:db8:5500:ccc4::2",
				core.ClientSpec{Email: "alice@example.com", UUID: "uuid-2"}),
		},
	}
	doc := mustRender(t, cfg)
	rules := findRoutingRules(t, doc)
	if findRuleWithOutboundTag(rules, "direct-v4") == nil {
		t.Error("dual-stack: missing direct-v4 pin rule for v4 inbounds")
	}
	if findRuleWithOutboundTag(rules, "direct-v6") == nil {
		t.Error("dual-stack: missing direct-v6 pin rule for v6 inbounds")
	}
}

func TestRender_FamilyPinning_EmptyHostNoRule(t *testing.T) {
	withCapability(t, true, true)
	doc := renderForCapabilityTest(t) // helper leaves SubscriptionHost empty
	rules := findRoutingRules(t, doc)
	if findRuleWithOutboundTag(rules, "direct-v6") != nil {
		t.Error("SubscriptionHost empty: must not emit a direct-v6 pin rule")
	}
	if findRuleWithOutboundTag(rules, "direct-v4") != nil {
		t.Error("SubscriptionHost empty: must not emit a direct-v4 pin rule")
	}
}
