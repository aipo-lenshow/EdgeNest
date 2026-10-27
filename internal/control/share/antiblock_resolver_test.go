package share

import (
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// seedEdge adds an edge_bound VLESS-WS inbound (plaintext, loopback) for email
// next to the wizard-style Reality + Hy2 pair, and returns a subscription token.
func seedEdge(t *testing.T, st *store.Store, nodeID uint) string {
	t.Helper()
	_, clientID := seedWizardStyle(t, st, nodeID, "alice@example.com")
	ws := &model.Inbound{
		NodeID: nodeID, Tag: "ws-edge", Engine: "singbox", Type: "vless-ws",
		Listen: "127.0.0.1", Port: 20001, Network: "tcp", Enabled: true,
		Settings: `{"ws_path":"/a1b2","edge_bound":"true","sni":"1.2.3.4"}`,
	}
	if err := st.CreateInbound(ws); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateClient(&model.Client{
		InboundID: ws.ID, Email: "alice@example.com",
		UUID: "22222222-2222-2222-2222-222222222222", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	token := "tok-edge"
	if err := st.CreateSubscription(&model.Subscription{
		Name: "alice", TokenHash: store.HashToken(token), ClientID: clientID,
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

func joined(t *testing.T, r *Resolver, token string) string {
	t.Helper()
	uris, err := r.Resolve(token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return strings.Join(uris, "\n")
}

func TestAntiBlock_EdgeInboundHiddenWithoutDomain(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	got := joined(t, NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{}), token)
	if strings.Contains(got, "a1b2") || strings.Contains(got, "20001") {
		t.Fatalf("edge inbound leaked as a loopback link with no edge domain:\n%s", got)
	}
}

func TestAntiBlock_EdgeInboundDialsDomainOverTLS(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	r := NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com"})
	var edge string
	for _, u := range strings.Split(joined(t, r, token), "\n") {
		if strings.Contains(u, "a1b2") {
			edge = u
		}
	}
	if edge == "" {
		t.Fatal("edge inbound missing from subscription")
	}
	for _, want := range []string{"@cdn.example.com:443", "security=tls", "sni=cdn.example.com", "host=cdn.example.com"} {
		if !strings.Contains(edge, want) {
			t.Errorf("edge URI missing %q:\n%s", want, edge)
		}
	}
	if strings.Contains(edge, "1.2.3.4") || strings.Contains(edge, "insecure=1") {
		t.Errorf("edge URI exposes the VPS IP or skips verification:\n%s", edge)
	}
}

func TestAntiBlock_DirectDomainReplacesIP(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	r := NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com", DirectDomain: "n1.example.com"})
	got := joined(t, r, token)
	if strings.Contains(got, "1.2.3.4") {
		t.Fatalf("raw IP still in subscription:\n%s", got)
	}
	if !strings.Contains(got, "@n1.example.com:8443") || !strings.Contains(got, "@n1.example.com:41020") {
		t.Fatalf("direct nodes not on the direct domain:\n%s", got)
	}
}

func TestAntiBlock_DirectDomainV6OnlyForV6Inbounds(t *testing.T) {
	st, nodeID := newStore(t)
	in := &model.Inbound{
		NodeID: nodeID, Tag: "vless-v6", Engine: "singbox", Type: "vless",
		Listen: "2001:db8::1", Port: 8443, Network: "tcp", Enabled: true,
		SubscriptionHost: "2001:db8::1",
		Settings:         `{"sni":"www.microsoft.com","reality_public_key":"PUB","short_ids":["sid"]}`,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{DirectDomain: "n1.example.com"})
	if got := r.effectiveHost(in, model.Client{}); got != "2001:db8::1" {
		t.Fatalf("v6 inbound took the v4 name: %q", got)
	}
	r.WithAntiBlock(antiblock.Config{DirectDomain: "n1.example.com", DirectDomainV6: "n1v6.example.com"})
	if got := r.effectiveHost(in, model.Client{}); got != "n1v6.example.com" {
		t.Fatalf("v6 inbound: got %q, want the v6 name", got)
	}
}

func TestAntiBlock_StealthKeepsOnlyShielded(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	r := NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com", Stealth: true})
	uris, err := r.Resolve(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 || !strings.Contains(uris[0], "cdn.example.com") {
		t.Fatalf("low-profile mode should leave only the edge node, got:\n%s", strings.Join(uris, "\n"))
	}
	bundles, err := r.ResolveBundles(token)
	if err != nil || len(bundles) != 1 {
		t.Fatalf("bundles: %v %d", err, len(bundles))
	}
}
