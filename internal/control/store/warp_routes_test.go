package store

import (
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return st
}

func TestUpsertWarp_InsertAndUpdate(t *testing.T) {
	st := openTestStore(t)
	w := &model.WarpConfig{
		NodeID: 1, Enabled: true,
		PrivateKey: "a", PublicKey: "B",
		Address4: "172.16.0.2", Endpoint: "engage.cloudflareclient.com:2408",
	}
	if err := st.UpsertWarp(w); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if w.ID == 0 {
		t.Fatal("ID not set after insert")
	}
	got, err := st.GetWarp(1)
	if err != nil || got == nil {
		t.Fatalf("get after insert: err=%v nil=%v", err, got == nil)
	}
	if got.PublicKey != "B" || !got.Enabled {
		t.Errorf("loaded mismatch: %+v", got)
	}

	// Update.
	w.PublicKey = "C"
	w.Enabled = false
	if err := st.UpsertWarp(w); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := st.GetWarp(1)
	if got2.PublicKey != "C" || got2.Enabled {
		t.Errorf("update did not stick: %+v", got2)
	}

	// Same record (only one row per node).
	var count int64
	st.DB().Model(&model.WarpConfig{}).Where("node_id = ?", 1).Count(&count)
	if count != 1 {
		t.Errorf("want 1 warp row per node, got %d", count)
	}
}

func TestDeleteWarp(t *testing.T) {
	st := openTestStore(t)
	_ = st.UpsertWarp(&model.WarpConfig{NodeID: 1, Enabled: true, PublicKey: "X"})
	if err := st.DeleteWarp(1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	w, _ := st.GetWarp(1)
	if w != nil {
		t.Errorf("warp still present after delete: %+v", w)
	}
}

func TestRouteCRUD_AndReorder(t *testing.T) {
	st := openTestStore(t)
	mk := func(value string, order int) *model.RouteRule {
		return &model.RouteRule{
			NodeID: 1, Type: "domain_suffix", Value: value,
			Outbound: "warp", Enabled: true, Order: order,
		}
	}
	rules := []*model.RouteRule{mk("a.com", 0), mk("b.com", 1), mk("c.com", 2)}
	for _, r := range rules {
		if err := st.CreateRouteRule(r); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	got, err := st.ListRouteRules(1)
	if err != nil || len(got) != 3 {
		t.Fatalf("list: err=%v len=%d", err, len(got))
	}
	if got[0].Value != "a.com" || got[2].Value != "c.com" {
		t.Errorf("initial order wrong: %+v", got)
	}

	// Reverse via reorder.
	if err := st.ReorderRouteRules(1, []uint{rules[2].ID, rules[1].ID, rules[0].ID}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	got, _ = st.ListRouteRules(1)
	if got[0].Value != "c.com" || got[2].Value != "a.com" {
		t.Errorf("reorder didn't stick: %+v", got)
	}

	// Update one rule.
	got[0].Value = "z.com"
	if err := st.UpdateRouteRule(&got[0]); err != nil {
		t.Fatalf("update: %v", err)
	}
	gotOne, _ := st.GetRouteRule(got[0].ID)
	if gotOne.Value != "z.com" {
		t.Errorf("update missed: %+v", gotOne)
	}

	// Delete.
	if err := st.DeleteRouteRule(got[1].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	after, _ := st.ListRouteRules(1)
	if len(after) != 2 {
		t.Errorf("delete didn't remove row, list=%+v", after)
	}
}

func TestBulkRouteOps(t *testing.T) {
	st := openTestStore(t)
	mk := func(node uint, value string) *model.RouteRule {
		r := &model.RouteRule{
			NodeID: node, Type: "domain_suffix", Value: value,
			Outbound: "warp", Enabled: true,
		}
		if err := st.CreateRouteRule(r); err != nil {
			t.Fatalf("create: %v", err)
		}
		return r
	}
	a, b, c := mk(1, "a.com"), mk(1, "b.com"), mk(1, "c.com")
	other := mk(2, "other.com") // different node — must never be touched

	// Bulk disable a + b.
	if err := st.BulkSetRouteEnabled(1, []uint{a.ID, b.ID}, false); err != nil {
		t.Fatalf("bulk disable: %v", err)
	}
	got, _ := st.ListRouteRules(1)
	for _, r := range got {
		want := r.ID == c.ID // only c stays enabled
		if r.Enabled != want {
			t.Errorf("rule %s enabled=%v want %v", r.Value, r.Enabled, want)
		}
	}

	// node scoping: passing another node's id must not flip it.
	if err := st.BulkSetRouteEnabled(1, []uint{other.ID}, false); err != nil {
		t.Fatalf("bulk disable cross-node: %v", err)
	}
	o, _ := st.GetRouteRule(other.ID)
	if !o.Enabled {
		t.Error("cross-node id flipped another node's rule")
	}

	// Bulk delete a + b, scoped to node 1.
	if err := st.BulkDeleteRouteRules(1, []uint{a.ID, b.ID}); err != nil {
		t.Fatalf("bulk delete: %v", err)
	}
	got, _ = st.ListRouteRules(1)
	if len(got) != 1 || got[0].ID != c.ID {
		t.Errorf("bulk delete wrong survivors: %+v", got)
	}
	if got2, _ := st.ListRouteRules(2); len(got2) != 1 {
		t.Errorf("bulk delete leaked across nodes: %+v", got2)
	}
}
