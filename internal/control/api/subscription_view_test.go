package api

import (
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// TestSubscriptionView_FamilyFallbackDiscipline locks the BUG-1 view fix:
// the v4-preferring global fallback host may only stand in when its family
// cannot be wrong — never for a subscription whose every referenced inbound
// has been deleted (those show an empty host + orphaned=true instead of
// masquerading a v6 bundle as v4).
func TestSubscriptionView_FamilyFallbackDiscipline(t *testing.T) {
	const fallback = "203.0.113.7" // node default — IPv4 on a dual-stack box

	lookup := inboundLookup{
		tag: map[uint]string{
			21: "EdgeNest-VLESS-Reality-v6-8443",
			22: "EdgeNest-Trojan-v4-8444", // legacy row: live but no SubscriptionHost
		},
		host: map[uint]string{
			21: "2001:db8::21",
			22: "",
		},
	}

	cases := []struct {
		name         string
		allowed      string
		wantHost     string
		wantOrphaned bool
	}{
		{
			name:         "live v6 inbound wins",
			allowed:      "[21]",
			wantHost:     "2001:db8::21",
			wantOrphaned: false,
		},
		{
			name:         "all refs deleted: no fallback, orphaned",
			allowed:      "[11,12]",
			wantHost:     "",
			wantOrphaned: true,
		},
		{
			name:         "live legacy inbound without host: fallback ok",
			allowed:      "[22]",
			wantHost:     fallback,
			wantOrphaned: false,
		},
		{
			name:         "open allow-list: fallback ok",
			allowed:      "[]",
			wantHost:     fallback,
			wantOrphaned: false,
		},
		{
			name:         "mixed dangling + live v6: v6 wins, not orphaned",
			allowed:      "[11,21]",
			wantHost:     "2001:db8::21",
			wantOrphaned: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := &model.Subscription{
				Name: "t", Token: "tok", ClientID: 1,
				AllowedNodes: "[]", AllowedInbounds: tc.allowed,
			}
			resp := subscriptionView(sub, lookup, 2087, fallback)
			if got := resp["subscription_host"]; got != tc.wantHost {
				t.Errorf("subscription_host = %v, want %q", got, tc.wantHost)
			}
			if got := resp["orphaned"]; got != tc.wantOrphaned {
				t.Errorf("orphaned = %v, want %v", got, tc.wantOrphaned)
			}
		})
	}
}

// TestSubscriptionView_AbsoluteURLBracketsV6 keeps the RFC 3986 bracketing
// behavior pinned alongside the new fallback discipline.
func TestSubscriptionView_AbsoluteURLBracketsV6(t *testing.T) {
	lookup := inboundLookup{
		tag:  map[uint]string{31: "x"},
		host: map[uint]string{31: "2001:db8::31"},
	}
	sub := &model.Subscription{
		Name: "t", Token: "tok6", ClientID: 1,
		AllowedNodes: "[]", AllowedInbounds: "[31]",
	}
	resp := subscriptionView(sub, lookup, 2087, "203.0.113.7")
	want := "http://[2001:db8::31]:2087/sub/tok6"
	if got := resp["absolute_url"]; got != want {
		t.Errorf("absolute_url = %v, want %q", got, want)
	}
}
