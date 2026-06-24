package xray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

func withCapability(t *testing.T, v4, v6 bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "network.json")
	body := []byte(`{"ipv4":` + boolJSON(v4) + `,"ipv6_global":` + boolJSON(v6) + `}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write capability file: %v", err)
	}
	orig := capabilityPath
	capabilityPath = path
	t.Cleanup(func() { capabilityPath = orig })
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func renderForCapabilityTest(t *testing.T) map[string]any {
	t.Helper()
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{{
			Engine: core.EngineXray, Type: "vless-xhttp", Tag: "vx",
			Listen: "::", Port: 443,
			Settings: map[string]any{
				"sni":                 "www.microsoft.com",
				"reality_private_key": "PRIV",
				"short_ids":           []string{""},
				"xhttp_path":          "/x",
			},
			Clients: []core.ClientSpec{{Email: "u@x", UUID: "uuid-1"}},
		}},
	}
	return mustRender(t, cfg)
}

func findOutbound(doc map[string]any, tag string) map[string]any {
	obs, _ := doc["outbounds"].([]any)
	for _, raw := range obs {
		ob, _ := raw.(map[string]any)
		if ob != nil && ob["tag"] == tag {
			return ob
		}
	}
	return nil
}

func freedomStrategy(doc map[string]any) string {
	d := findOutbound(doc, "direct")
	if d == nil {
		return "<no-direct-outbound>"
	}
	s, _ := d["settings"].(map[string]any)
	if s == nil {
		return ""
	}
	v, _ := s["domainStrategy"].(string)
	return v
}

// assertNoBlackholeRule fails if any routing rule sends a destination CIDR
// to the blackhole outbound — the xray-side counterpart we walked back.
func assertNoBlackholeRule(t *testing.T, doc map[string]any) {
	t.Helper()
	routing, _ := doc["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	for i, raw := range rules {
		rule, _ := raw.(map[string]any)
		if rule == nil {
			continue
		}
		if tag, _ := rule["outboundTag"].(string); tag == "block" {
			t.Errorf("routing.rules[%d] sends destination to blackhole: %v (the family rule forbids)", i, rule)
		}
	}
}

func TestRender_Capability_DualStack(t *testing.T) {
	withCapability(t, true, true)
	doc := renderForCapabilityTest(t)
	if got := freedomStrategy(doc); got != "" {
		t.Errorf("dual-stack: freedom.domainStrategy = %q, want empty (AsIs)", got)
	}
	if findOutbound(doc, "direct-v4") == nil {
		t.Error("dual-stack: direct-v4 outbound must be present")
	}
	if findOutbound(doc, "direct-v6") == nil {
		t.Error("dual-stack: direct-v6 outbound must be present")
	}
	assertNoBlackholeRule(t, doc)
}

func TestRender_Capability_V4Only(t *testing.T) {
	withCapability(t, true, false)
	doc := renderForCapabilityTest(t)
	if got := freedomStrategy(doc); got != "UseIPv4" {
		t.Errorf("v4-only: freedom.domainStrategy = %q, want UseIPv4", got)
	}
	if findOutbound(doc, "direct-v6") == nil {
		t.Error("v4-only: direct-v6 outbound must still be emitted")
	}
	assertNoBlackholeRule(t, doc)
	b, _ := json.Marshal(doc)
	if strings.Contains(string(b), `"2000::/3"`) {
		t.Errorf("v4-only: 2000::/3 blackhole rule must not appear (walkback)")
	}
}

func TestRender_Capability_V6Only(t *testing.T) {
	withCapability(t, false, true)
	doc := renderForCapabilityTest(t)
	if got := freedomStrategy(doc); got != "UseIPv6" {
		t.Errorf("v6-only: freedom.domainStrategy = %q, want UseIPv6", got)
	}
	if findOutbound(doc, "direct-v4") == nil {
		t.Error("v6-only: direct-v4 outbound must still be emitted")
	}
	assertNoBlackholeRule(t, doc)
	b, _ := json.Marshal(doc)
	if strings.Contains(string(b), `"0.0.0.0/0"`) {
		t.Errorf("v6-only: 0.0.0.0/0 blackhole rule must not appear (walkback)")
	}
}

