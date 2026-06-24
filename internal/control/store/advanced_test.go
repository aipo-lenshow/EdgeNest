package store

import (
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

func TestUpsertAdvanced_InsertAndUpdate(t *testing.T) {
	st := openTestStore(t)

	a := &model.AdvancedConfig{
		NodeID:          1,
		CDNEnabled:      true,
		CDNPreferredIPs: `["104.16.0.1"]`,
		ArgoEnabled:     true,
		ArgoMode:        "fixed",
		ArgoDomain:      "tunnel.example.com",
		ArgoToken:       "tok-1",
	}
	if err := st.UpsertAdvanced(a); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if a.ID == 0 {
		t.Fatal("ID not set after insert")
	}
	got, err := st.GetAdvanced(1)
	if err != nil || got == nil {
		t.Fatalf("get after insert: err=%v nil=%v", err, got == nil)
	}
	if !got.CDNEnabled || got.CDNPreferredIPs != `["104.16.0.1"]` {
		t.Errorf("loaded cdn fields: %+v", got)
	}
	if !got.ArgoEnabled || got.ArgoMode != "fixed" || got.ArgoToken != "tok-1" {
		t.Errorf("loaded argo fields: %+v", got)
	}

	// Update.
	a.CDNEnabled = false
	a.ArgoToken = "tok-2"
	if err := st.UpsertAdvanced(a); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := st.GetAdvanced(1)
	if got2.CDNEnabled {
		t.Error("CDNEnabled should now be false")
	}
	if got2.ArgoToken != "tok-2" {
		t.Errorf("ArgoToken not updated: %q", got2.ArgoToken)
	}
	// Same row (no duplicate)
	if got2.ID != got.ID {
		t.Errorf("ID changed after update: was %d now %d", got.ID, got2.ID)
	}
}

func TestUpsertAdvanced_RejectsZeroNodeID(t *testing.T) {
	st := openTestStore(t)
	if err := st.UpsertAdvanced(&model.AdvancedConfig{}); err == nil {
		t.Error("want error when node_id is 0")
	}
}

func TestDeleteAdvanced(t *testing.T) {
	st := openTestStore(t)
	if err := st.UpsertAdvanced(&model.AdvancedConfig{NodeID: 7, CDNEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteAdvanced(7); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := st.GetAdvanced(7)
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
	// Idempotent: delete again is fine.
	if err := st.DeleteAdvanced(7); err != nil {
		t.Errorf("second delete: %v", err)
	}
}
