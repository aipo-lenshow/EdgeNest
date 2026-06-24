package usersvc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func newTestStore(t *testing.T) (*store.Store, uint) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.EnsureLocalNode(); err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	ln, _ := st.GetLocalNode()
	return st, ln.ID
}

// seedInbound creates an enabled inbound with one wizard client to mirror.
func seedInbound(t *testing.T, st *store.Store, nodeID uint, tag, typ string, port int) *model.Inbound {
	t.Helper()
	in := &model.Inbound{
		NodeID: nodeID, Tag: tag, Engine: "singbox", Type: typ,
		Listen: "::", Port: port, Network: "tcp", Enabled: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	seed := &model.Client{InboundID: in.ID, Email: "seed@" + tag, UUID: "seed-uuid", Enabled: true}
	if err := st.CreateClient(seed); err != nil {
		t.Fatal(err)
	}
	return in
}

func newSvc(st *store.Store, applied, audited *int) *Service {
	return &Service{
		Store: st,
		Apply: func(ctx context.Context, n uint) error { *applied++; return nil },
		Audit: func(action, resource string, meta map[string]string) { *audited++ },
	}
}

func TestCreate_AcrossInboundsAndSubscription(t *testing.T) {
	st, nodeID := newTestStore(t)
	seedInbound(t, st, nodeID, "vless-1", "vless", 8443)
	seedInbound(t, st, nodeID, "ss-1", "shadowsocks", 8444) // must be skipped

	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)

	res, err := svc.Create(context.Background(), nodeID, CreateParams{Email: "alice@x", QuotaBytes: 1 << 30})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(res.InboundTags) != 1 || res.InboundTags[0] != "vless-1" {
		t.Errorf("inbound tags = %v, want [vless-1]", res.InboundTags)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "ss-1" {
		t.Errorf("skipped = %v, want [ss-1]", res.Skipped)
	}
	if res.SubToken == "" || res.SubID == 0 {
		t.Errorf("expected a bundle subscription, got token=%q id=%d", res.SubToken, res.SubID)
	}
	if applied != 1 || audited != 1 {
		t.Errorf("apply=%d audit=%d, want 1/1", applied, audited)
	}
	clients, _ := st.ClientsByEmail("alice@x")
	if len(clients) != 1 || clients[0].QuotaBytes != 1<<30 {
		t.Errorf("alice clients = %+v", clients)
	}
}

func TestCreate_DuplicateEmailRejected(t *testing.T) {
	st, nodeID := newTestStore(t)
	seedInbound(t, st, nodeID, "vless-1", "vless", 8443)
	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)
	if _, err := svc.Create(context.Background(), nodeID, CreateParams{Email: "bob@x"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := svc.Create(context.Background(), nodeID, CreateParams{Email: "bob@x"})
	e, ok := err.(*Error)
	if !ok || e.Code != "EMAIL_EXISTS" {
		t.Fatalf("want EMAIL_EXISTS, got %v", err)
	}
}

func TestCreate_NoEligibleInbound(t *testing.T) {
	st, nodeID := newTestStore(t)
	seedInbound(t, st, nodeID, "ss-1", "shadowsocks", 8444) // only SS → nothing eligible
	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)
	_, err := svc.Create(context.Background(), nodeID, CreateParams{Email: "carol@x"})
	e, ok := err.(*Error)
	if !ok || e.Code != "NO_INBOUND" {
		t.Fatalf("want NO_INBOUND, got %v", err)
	}
	if applied != 0 {
		t.Errorf("apply should not be called on failure, got %d", applied)
	}
}

func TestUpdate_QuotaExpiryEnabled(t *testing.T) {
	st, nodeID := newTestStore(t)
	seedInbound(t, st, nodeID, "vless-1", "vless", 8443)
	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)
	if _, err := svc.Create(context.Background(), nodeID, CreateParams{Email: "dave@x"}); err != nil {
		t.Fatal(err)
	}

	q := int64(5 << 30)
	exp := time.Now().Add(48 * time.Hour).Unix()
	dis := false
	if _, err := svc.Update(context.Background(), nodeID, "dave@x", UpdateParams{
		QuotaBytes: &q, ExpiryAt: &exp, Enabled: &dis,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	clients, _ := st.ClientsByEmail("dave@x")
	c := clients[0]
	if c.QuotaBytes != q || c.ExpiryAt != exp || c.Enabled {
		t.Errorf("after update: %+v (want quota=%d exp=%d enabled=false)", c, q, exp)
	}
}

func TestUpdate_EnableClearsStaleExpiry(t *testing.T) {
	st, nodeID := newTestStore(t)
	seedInbound(t, st, nodeID, "vless-1", "vless", 8443)
	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)
	svc.Create(context.Background(), nodeID, CreateParams{Email: "eve@x"})

	// Expire in the past + disable.
	past := time.Now().Add(-24 * time.Hour).Unix()
	off := false
	svc.Update(context.Background(), nodeID, "eve@x", UpdateParams{ExpiryAt: &past, Enabled: &off})

	// Re-enable without a new expiry → stale past expiry must be cleared to 0.
	on := true
	if _, err := svc.Update(context.Background(), nodeID, "eve@x", UpdateParams{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	clients, _ := st.ClientsByEmail("eve@x")
	if !clients[0].Enabled || clients[0].ExpiryAt != 0 {
		t.Errorf("re-enable: enabled=%v expiry=%d, want true/0", clients[0].Enabled, clients[0].ExpiryAt)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	st, nodeID := newTestStore(t)
	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)
	q := int64(1)
	_, err := svc.Update(context.Background(), nodeID, "ghost@x", UpdateParams{QuotaBytes: &q})
	e, ok := err.(*Error)
	if !ok || e.Code != "NOT_FOUND" {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
}

func TestDelete_RemovesClientsAndSubscription(t *testing.T) {
	st, nodeID := newTestStore(t)
	seedInbound(t, st, nodeID, "vless-1", "vless", 8443)
	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)
	res, _ := svc.Create(context.Background(), nodeID, CreateParams{Email: "frank@x"})
	subID := res.SubID

	del, err := svc.Delete(context.Background(), nodeID, "frank@x")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if del.DeletedClients != 1 {
		t.Errorf("deleted clients = %d, want 1", del.DeletedClients)
	}
	if clients, _ := st.ClientsByEmail("frank@x"); len(clients) != 0 {
		t.Errorf("clients should be gone, got %d", len(clients))
	}
	if subs, _ := st.ListSubscriptions(); len(subs) != 0 {
		t.Errorf("subscription %d should be gone, got %d subs", subID, len(subs))
	}
}

func TestDelete_NotFound(t *testing.T) {
	st, nodeID := newTestStore(t)
	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)
	_, err := svc.Delete(context.Background(), nodeID, "nobody@x")
	e, ok := err.(*Error)
	if !ok || e.Code != "NOT_FOUND" {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}
}
