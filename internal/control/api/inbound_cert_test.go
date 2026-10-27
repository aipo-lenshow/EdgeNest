package api

import (
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// Switching a TLS inbound to an issued certificate points it at that
// certificate's files and SNI; switching back restores the self-signed pair.
func TestApplyCertDomain(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := st.EnsureLocalNode()
	_ = st.DB().Create(&model.Certificate{NodeID: n.ID, Domain: "n1.example.com",
		CertPath: "/c/n1/fullchain.pem", KeyPath: "/c/n1/privkey.pem"}).Error
	h := &Handler{store: st}

	s := map[string]any{"tls_cert_path": defaultWizardCertPath, "self_signed": "true", "sni": "old.example.com"}
	if msg := h.applyCertDomain(n.ID, s, "n1.example.com"); msg != "" {
		t.Fatal(msg)
	}
	if s["tls_cert_path"] != "/c/n1/fullchain.pem" || s["sni"] != "n1.example.com" || s["acme_managed"] != "true" || s["self_signed"] != nil {
		t.Fatalf("issued: %v", s)
	}
	raw := `{"tls_cert_path":"/c/n1/fullchain.pem"}`
	if d, ok := h.inboundCertDomain(n.ID, raw); !ok || d != "n1.example.com" {
		t.Fatalf("detect: %q %v", d, ok)
	}
	if msg := h.applyCertDomain(n.ID, s, ""); msg != "" || s["tls_cert_path"] != defaultWizardCertPath || s["self_signed"] != "true" || s["acme_managed"] != nil {
		t.Fatalf("self-signed: %v %q", s, msg)
	}
	if msg := h.applyCertDomain(n.ID, s, "missing.example.com"); msg == "" {
		t.Fatal("missing cert should be refused")
	}
}
