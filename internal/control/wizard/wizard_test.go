package wizard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
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

func TestStatus_BeforeAndAfter(t *testing.T) {
	st, nodeID := newStore(t)
	w := New(st, nil, nil)

	s, err := w.Status(nodeID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if s.Done {
		t.Error("Status.Done should be false before Complete")
	}
	if s.HasInbound {
		t.Error("Status.HasInbound should be false before Complete")
	}

	if err := st.SetSetting(bootstrap.KeyWizardDone, "true"); err != nil {
		t.Fatal(err)
	}
	s, _ = w.Status(nodeID)
	if !s.Done {
		t.Error("Status.Done should be true after marker set")
	}
}

func TestComplete_CreatesTwoInboundsAndOneClientEach(t *testing.T) {
	st, nodeID := newStore(t)
	certDir := t.TempDir()

	w := New(st, nil, nil) // orch=nil, skip Apply
	res, err := w.Complete(context.Background(), nodeID, CompleteRequest{
		ClientEmail: "owner@example.com",
		CertsDir:    certDir,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Defaults filled in.
	if res.ClientEmail != "owner@example.com" {
		t.Errorf("wrong client_email: %q", res.ClientEmail)
	}
	if res.ClientUUID == "" {
		t.Error("missing client uuid")
	}
	if res.ClientPassword == "" {
		t.Error("missing client password")
	}
	if res.RealityPublicKey == "" {
		t.Error("missing reality public key")
	}

	// Inbounds.
	ins, err := st.ListInbounds(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 2 {
		t.Fatalf("want 2 inbounds, got %d", len(ins))
	}
	byType := map[string]int{}
	for _, in := range ins {
		byType[in.Type]++
	}
	if byType["vless"] != 1 || byType["hysteria2"] != 1 {
		t.Errorf("want one vless and one hysteria2 inbound, got %v", byType)
	}

	// Both inbounds should carry the shared client email.
	for _, in := range ins {
		cs, err := st.ListClients(in.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 1 {
			t.Errorf("inbound %s: want 1 client, got %d", in.Type, len(cs))
		}
		if cs[0].Email != "owner@example.com" {
			t.Errorf("inbound %s: wrong client email %q", in.Type, cs[0].Email)
		}
	}

	// Wizard marker set.
	done, _ := st.GetSetting(bootstrap.KeyWizardDone)
	if done != "true" {
		t.Errorf("wizard_done = %q, want \"true\"", done)
	}

	// Reality public key persisted for the panel UI.
	pubFromDB, _ := st.GetSetting("wizard_reality_public_key")
	if pubFromDB != res.RealityPublicKey {
		t.Errorf("public key mismatch DB=%q result=%q", pubFromDB, res.RealityPublicKey)
	}

	// Cert files exist.
	if _, err := os.Stat(res.SelfSignedCertPath); err != nil {
		t.Errorf("cert missing: %v", err)
	}
	if _, err := os.Stat(res.SelfSignedKeyPath); err != nil {
		t.Errorf("key missing: %v", err)
	}

	// VLESS inbound settings must contain the reality private key + SNI.
	var vlessIn = ins[0]
	if vlessIn.Type != "vless" {
		vlessIn = ins[1]
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(vlessIn.Settings), &settings); err != nil {
		t.Fatalf("vless settings json: %v", err)
	}
	if settings["sni"] == "" {
		t.Error("vless settings missing sni")
	}
	if settings["reality_private_key"] == "" {
		t.Error("vless settings missing reality_private_key")
	}
}

func TestComplete_RefusesWhenAlreadyDone(t *testing.T) {
	st, nodeID := newStore(t)
	if err := st.SetSetting(bootstrap.KeyWizardDone, "true"); err != nil {
		t.Fatal(err)
	}
	w := New(st, nil, nil)
	_, err := w.Complete(context.Background(), nodeID, CompleteRequest{
		ClientEmail: "x@example.com",
		CertsDir:    t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error when wizard already completed")
	}
}

func TestComplete_RequiresClientEmail(t *testing.T) {
	st, nodeID := newStore(t)
	w := New(st, nil, nil)
	_, err := w.Complete(context.Background(), nodeID, CompleteRequest{
		CertsDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error when client_email empty (invariant I1)")
	}
}

func TestGenerateRealityKeypair_Distinct(t *testing.T) {
	priv1, pub1, err := generateRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	priv2, pub2, err := generateRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if priv1 == priv2 || pub1 == pub2 {
		t.Error("two consecutive keypairs should not match (entropy bug?)")
	}
	if priv1 == "" || pub1 == "" {
		t.Error("empty keypair")
	}
}
