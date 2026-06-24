package share

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

func newStore(t *testing.T) (*store.Store, uint) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	n, err := st.EnsureLocalNode()
	if err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	return st, n.ID
}

// seedWizardStyle: one user "alice" with credentials on both a VLESS and a
// Hysteria2 inbound — mirrors what the first-run wizard creates.
func seedWizardStyle(t *testing.T, st *store.Store, nodeID uint, email string) (uint, uint) {
	t.Helper()
	vless := &model.Inbound{
		NodeID: nodeID, Tag: "vless-w", Engine: "singbox", Type: "vless",
		Listen: "::", Port: 8443, Network: "tcp", Enabled: true,
		Settings: `{"sni":"www.microsoft.com","reality_public_key":"PUB","short_ids":["sid"]}`,
	}
	if err := st.CreateInbound(vless); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateClient(&model.Client{
		InboundID: vless.ID, Email: email,
		UUID: "11111111-1111-1111-1111-111111111111",
		Flow: "xtls-rprx-vision", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	vlessClient, _ := st.ListClients(vless.ID)

	h2 := &model.Inbound{
		NodeID: nodeID, Tag: "h2-w", Engine: "singbox", Type: "hysteria2",
		Listen: "::", Port: 41020, Network: "udp", Enabled: true,
		Settings: `{"sni":"edgenest.local","tls_cert_path":"/x/y"}`,
	}
	if err := st.CreateInbound(h2); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateClient(&model.Client{
		InboundID: h2.ID, Email: email,
		Password: "secret", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	return vless.ID, vlessClient[0].ID
}

func TestResolve_HappyPath_TwoInboundsOneEmail(t *testing.T) {
	st, nodeID := newStore(t)
	_, vlessClientID := seedWizardStyle(t, st, nodeID, "alice@example.com")

	token := "raw-token-XYZ"
	if err := st.CreateSubscription(&model.Subscription{
		Name: "alice", TokenHash: store.HashToken(token), ClientID: vlessClientID,
	}); err != nil {
		t.Fatal(err)
	}

	r := NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{})
	uris, err := r.Resolve(token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(uris) != 2 {
		t.Fatalf("want 2 URIs (vless + hysteria2), got %d: %v", len(uris), uris)
	}
}

func TestResolve_UnknownToken_NotFound(t *testing.T) {
	st, _ := newStore(t)
	r := NewResolver(st, "h", nil, "", core.NodeCapability{})
	if _, err := r.Resolve("nope"); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestResolve_Revoked_NotFound(t *testing.T) {
	st, nodeID := newStore(t)
	_, clientID := seedWizardStyle(t, st, nodeID, "bob@example.com")
	token := "tok"
	sub := &model.Subscription{
		Name: "bob", TokenHash: store.HashToken(token), ClientID: clientID,
	}
	if err := st.CreateSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(st, "h", nil, "", core.NodeCapability{})
	if _, err := r.Resolve(token); err != ErrNotFound {
		t.Errorf("want ErrNotFound for revoked, got %v", err)
	}
}

func TestResolve_Expired_NotFound(t *testing.T) {
	st, nodeID := newStore(t)
	_, clientID := seedWizardStyle(t, st, nodeID, "carol@example.com")
	token := "tok"
	if err := st.CreateSubscription(&model.Subscription{
		Name: "carol", TokenHash: store.HashToken(token), ClientID: clientID,
		ExpiresAt: time.Now().Add(-1 * time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(st, "h", nil, "", core.NodeCapability{})
	if _, err := r.Resolve(token); err != ErrNotFound {
		t.Errorf("want ErrNotFound for expired, got %v", err)
	}
}

func TestResolve_AllowedInbounds_Filter(t *testing.T) {
	st, nodeID := newStore(t)
	_, clientID := seedWizardStyle(t, st, nodeID, "dave@example.com")
	token := "tok"
	if err := st.CreateSubscription(&model.Subscription{
		Name: "dave", TokenHash: store.HashToken(token), ClientID: clientID,
		AllowedInbounds: `["vless-w"]`,
	}); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(st, "h", nil, "", core.NodeCapability{})
	uris, err := r.Resolve(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 {
		t.Fatalf("filter to vless-w should give 1 uri, got %d", len(uris))
	}
}

func TestResolve_DisabledInbound_Skipped(t *testing.T) {
	st, nodeID := newStore(t)
	_, clientID := seedWizardStyle(t, st, nodeID, "eve@example.com")

	// Disable the hysteria2 inbound.
	ins, _ := st.ListInbounds(nodeID)
	for _, in := range ins {
		if in.Type == "hysteria2" {
			in.Enabled = false
			if err := st.UpdateInbound(&in); err != nil {
				t.Fatal(err)
			}
		}
	}

	token := "tok"
	if err := st.CreateSubscription(&model.Subscription{
		Name: "eve", TokenHash: store.HashToken(token), ClientID: clientID,
	}); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(st, "h", nil, "", core.NodeCapability{})
	uris, err := r.Resolve(token)
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 {
		t.Fatalf("disabled inbound should be skipped; got %d uris", len(uris))
	}
}

// TestBuildBundleForClient_DisabledUserReturnsEmptyNotNil locks the fix for the
// share-modal crash: when a user is fully disabled/expired (quota or expiry
// enforcement disabled every credential), the preview must return a non-nil
// empty slice so the JSON encoder emits [] not null — the panel calls
// data.uris.length and null would crash the share dialog.
func TestBuildBundleForClient_DisabledUserReturnsEmptyNotNil(t *testing.T) {
	st, nodeID := newStore(t)
	_, clientID := seedWizardStyle(t, st, nodeID, "frank@example.com")

	// Disable every credential of the user (what the enforcer does).
	for _, c := range mustClientsByEmail(t, st, "frank@example.com") {
		if err := st.SetClientEnabled(c.ID, false); err != nil {
			t.Fatal(err)
		}
	}

	seed, err := st.GetClient(clientID)
	if err != nil {
		t.Fatal(err)
	}
	r := NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{})
	links, err := r.BuildBundleForClient(seed)
	if err != nil {
		t.Fatalf("BuildBundleForClient: %v", err)
	}
	if links == nil {
		t.Fatal("links must be non-nil ([] not null) for a fully-disabled user")
	}
	if len(links) != 0 {
		t.Errorf("disabled user should yield 0 links, got %v", links)
	}
}

func mustClientsByEmail(t *testing.T, st *store.Store, email string) []model.Client {
	t.Helper()
	cs, err := st.ClientsByEmail(email)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}
