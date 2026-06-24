package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// withCapability temporarily points the package's capabilityPath at a JSON
// file the test writes into t.TempDir(). The default (file missing) is
// dual-stack; the helper covers v4-only / dual-stack / v6-only by writing
// the explicit shape install.sh's detect_node_capability would.
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
		t.Fatalf("invalid JSON: %v", err)
	}
	return doc
}

// findOutbound returns the outbound block with the given tag (or nil).
func findOutbound(doc map[string]any, tag string) map[string]any {
	obs, _ := doc["outbounds"].([]any)
	for _, raw := range obs {
		ob, _ := raw.(map[string]any)
		if ob == nil {
			continue
		}
		if ob["tag"] == tag {
			return ob
		}
	}
	return nil
}

// directStrategy returns the direct outbound's domain_resolver.strategy
// (empty string when the field is omitted, which is the as_is case).
func directStrategy(doc map[string]any) string {
	d := findOutbound(doc, "direct")
	if d == nil {
		return "<no-direct-outbound>"
	}
	dr, _ := d["domain_resolver"].(map[string]any)
	if dr == nil {
		return ""
	}
	s, _ := dr["strategy"].(string)
	return s
}

// assertNoRejectRule fails if any route rule asserts {action:reject} against
// a destination CIDR — the anti-pattern we're explicitly walking back. The
// "don't ban the other family" directive means render() must never emit such
// a rule, in any capability case.
func assertNoRejectRule(t *testing.T, doc map[string]any) {
	t.Helper()
	route, _ := doc["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	for i, raw := range rules {
		rule, _ := raw.(map[string]any)
		if rule == nil {
			continue
		}
		if act, _ := rule["action"].(string); act == "reject" {
			t.Errorf("route.rules[%d] has action=reject: %v (the family rule forbids banning the other family)", i, rule)
		}
	}
}

func TestRender_Capability_DualStack(t *testing.T) {
	withCapability(t, true, true)
	doc := renderForCapabilityTest(t)
	if got := directStrategy(doc); got != "" {
		t.Errorf("dual-stack: direct.strategy = %q, want empty (as_is)", got)
	}
	if findOutbound(doc, "direct-v4") == nil {
		t.Error("dual-stack: direct-v4 outbound must be present")
	}
	if findOutbound(doc, "direct-v6") == nil {
		t.Error("dual-stack: direct-v6 outbound must be present")
	}
	assertNoRejectRule(t, doc)
}

func TestRender_Capability_V4Only(t *testing.T) {
	withCapability(t, true, false)
	doc := renderForCapabilityTest(t)
	if got := directStrategy(doc); got != "prefer_ipv4" {
		t.Errorf("v4-only: direct.strategy = %q, want prefer_ipv4", got)
	}
	// direct-v6 is still emitted — never banned even on a v4-only host.
	if findOutbound(doc, "direct-v6") == nil {
		t.Error("v4-only: direct-v6 outbound must still be emitted (no banning)")
	}
	assertNoRejectRule(t, doc)
	// Belt-and-suspenders: assert the literal 2000::/3 reject CIDR from B6
	// is not anywhere in the document.
	b, _ := json.Marshal(doc)
	if strings.Contains(string(b), `"2000::/3"`) {
		t.Errorf("v4-only: 2000::/3 reject rule must not appear (walkback)")
	}
}

func TestRender_Capability_V6Only(t *testing.T) {
	withCapability(t, false, true)
	doc := renderForCapabilityTest(t)
	if got := directStrategy(doc); got != "prefer_ipv6" {
		t.Errorf("v6-only: direct.strategy = %q, want prefer_ipv6", got)
	}
	if findOutbound(doc, "direct-v4") == nil {
		t.Error("v6-only: direct-v4 outbound must still be emitted (no banning)")
	}
	assertNoRejectRule(t, doc)
	b, _ := json.Marshal(doc)
	if strings.Contains(string(b), `"0.0.0.0/0"`) {
		// We never emit a route rule rejecting all v4 — DNS64 / Kasper
		// NAT64 handles v4-only destinations on a v6-only host instead.
		t.Errorf("v6-only: 0.0.0.0/0 reject rule must not appear (walkback)")
	}
}

func TestRender_DefaultDomainResolver_Present(t *testing.T) {
	withCapability(t, true, true)
	doc := renderForCapabilityTest(t)
	route, _ := doc["route"].(map[string]any)
	ddr, _ := route["default_domain_resolver"].(map[string]any)
	if ddr == nil {
		t.Fatal("route.default_domain_resolver missing (sing-box 1.13 recommended)")
	}
	if ddr["server"] != "local" {
		t.Errorf("default_domain_resolver.server = %v, want local", ddr["server"])
	}
}
