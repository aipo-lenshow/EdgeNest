package quota

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.EnsureLocalNode(); err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	return st
}

func TestEnforceAll_DisablesOverQuotaAndCallsApply(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	in := &model.Inbound{
		NodeID: nodeID, Tag: "vless-1", Engine: "singbox", Type: "vless",
		Listen: "::", Port: 8443, Network: "tcp", Enabled: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	good := &model.Client{InboundID: in.ID, Email: "good@x", UUID: "u1",
		Enabled: true, TrafficUp: 100, QuotaBytes: 1_000_000}
	bad := &model.Client{InboundID: in.ID, Email: "bad@x", UUID: "u2",
		Enabled: true, TrafficUp: 999_999, TrafficDown: 2, QuotaBytes: 1_000_000}
	for _, c := range []*model.Client{good, bad} {
		if err := st.CreateClient(c); err != nil {
			t.Fatal(err)
		}
	}

	applied := 0
	auditCalls := 0
	enf := &Enforcer{
		Store: st,
		Apply: func(ctx context.Context, n uint) error {
			applied++
			if n != nodeID {
				t.Errorf("apply got wrong node id %d", n)
			}
			return nil
		},
		Audit:   func(action, resource string, meta map[string]string) { auditCalls++ },
		NowFunc: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	res, err := enf.EnforceAll(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if len(res.Disabled) != 1 || res.Disabled[0].Email != "bad@x" {
		t.Errorf("want [bad@x] disabled, got %+v", res.Disabled)
	}
	if applied != 1 {
		t.Errorf("Apply called %d times, want 1", applied)
	}
	if auditCalls != 1 {
		t.Errorf("Audit called %d times, want 1", auditCalls)
	}
	// Verify DB state: bad disabled, good still enabled.
	gotBad, _ := st.GetClient(bad.ID)
	gotGood, _ := st.GetClient(good.ID)
	if gotBad.Enabled {
		t.Error("bad client should be Enabled=false in DB")
	}
	if !gotGood.Enabled {
		t.Error("good client should still be Enabled=true")
	}
}

func TestEnforceAll_NoDecisionsNoApply(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	in := &model.Inbound{
		NodeID: nodeID, Tag: "vless-1", Engine: "singbox", Type: "vless",
		Listen: "::", Port: 8443, Network: "tcp", Enabled: true,
	}
	_ = st.CreateInbound(in)
	_ = st.CreateClient(&model.Client{
		InboundID: in.ID, Email: "ok@x", UUID: "u", Enabled: true,
		TrafficUp: 10, QuotaBytes: 0, // unlimited
	})

	applied := 0
	enf := &Enforcer{
		Store: st,
		Apply: func(ctx context.Context, n uint) error { applied++; return nil },
	}
	res, err := enf.EnforceAll(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if len(res.Disabled) != 0 {
		t.Errorf("nothing should be disabled, got %+v", res.Disabled)
	}
	if applied != 0 {
		t.Error("Apply should not be called when no clients changed")
	}
}
