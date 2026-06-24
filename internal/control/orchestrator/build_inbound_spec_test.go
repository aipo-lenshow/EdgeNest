package orchestrator

import (
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// TestBuildInboundSpec_CopiesSubscriptionHost is the regression gate.
// The 14p family-pinning route rules in sing-box / xray render decide v4 vs
// v6 by reading InboundSpec.SubscriptionHost. If buildInboundSpec doesn't
// copy that field through from model.Inbound, render() will always see ""
// and the v6 / v4 pin rules silently never fire — exactly the bug a fresh
// double-batch deploy reproduced (sing-box.json route.rules contained only
// sniff + hijack-dns despite the dual-stack inbounds being there with
// distinct v4 / v6 listen IPs and the right tags).
//
// The test deliberately picks an IPv6 SubscriptionHost so the bug case is
// covered: a v4 host would still pass the "field not zero" check even if
// the field weren't actually copied (Listen happens to match).
func TestBuildInboundSpec_CopiesSubscriptionHost(t *testing.T) {
	ib := model.Inbound{
		ID:               42,
		NodeID:           1,
		Tag:              "EdgeNest-VLESS-Reality-v6-8443",
		Engine:           "singbox",
		Type:             "vless",
		Listen:           "2001:db8:5500:ccc4::2",
		Port:             8443,
		Network:          "tcp",
		Enabled:          true,
		Settings:         `{"sni":"www.microsoft.com"}`,
		SubscriptionHost: "2001:db8:5500:ccc4::2",
	}
	spec, err := buildInboundSpec(ib)
	if err != nil {
		t.Fatalf("buildInboundSpec: %v", err)
	}
	if spec.SubscriptionHost != "2001:db8:5500:ccc4::2" {
		t.Errorf("SubscriptionHost = %q, want %q (the bug: render's inboundFamily reads this; empty → no v6 pin rule)",
			spec.SubscriptionHost, "2001:db8:5500:ccc4::2")
	}
}

// TestBuildInboundSpec_ArgoBound_ListenForced ensures the long-standing
// argo_bound override on Listen still wins (the Argo tunnel pins to
// loopback regardless of the operator-set listen IP). Pairs with the
// SubscriptionHost copy above so a future refactor doesn't accidentally
// drop the loopback rewrite while patching the new field through.
func TestBuildInboundSpec_ArgoBound_ListenForced(t *testing.T) {
	ib := model.Inbound{
		Tag:              "EdgeNest-VLESS-WS-v4-2083",
		Engine:           "singbox",
		Type:             "vless-ws",
		Listen:           "203.0.113.10",
		Port:             2083,
		Network:          "tcp",
		Enabled:          true,
		Settings:         `{"argo_bound":"true","ws_path":"/x"}`,
		SubscriptionHost: "203.0.113.10",
	}
	spec, err := buildInboundSpec(ib)
	if err != nil {
		t.Fatalf("buildInboundSpec: %v", err)
	}
	if spec.Listen != "127.0.0.1" {
		t.Errorf("Argo-bound Listen = %q, want 127.0.0.1", spec.Listen)
	}
	// SubscriptionHost is intentionally preserved here even with argo_bound —
	// the share resolver layers the Argo hostname override on top at URI
	// build time, so the field still needs to flow through.
	if spec.SubscriptionHost != "203.0.113.10" {
		t.Errorf("SubscriptionHost = %q, want %q", spec.SubscriptionHost, "203.0.113.10")
	}
}
