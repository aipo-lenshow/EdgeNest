package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// fakeIssuer hands back a freshly-minted self-signed cert. notAfter is
// configurable so tests can simulate near-expiry rows.
type fakeIssuer struct {
	notAfter  time.Time
	calls     int
	failOn    string // domain to fail (empty = never)
	lastEmail string // records the email of the most recent Obtain call
}

func (f *fakeIssuer) Obtain(_ context.Context, req IssueRequest) (*IssueResult, error) {
	f.calls++
	f.lastEmail = req.Email
	if req.Domain == f.failOn {
		return nil, errors.New("simulated CA failure")
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	notAfter := f.notAfter
	if notAfter.IsZero() {
		notAfter = time.Now().Add(90 * 24 * time.Hour)
	}
	notBefore := time.Now().Add(-1 * time.Hour)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: req.Domain},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{req.Domain},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(priv)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return &IssueResult{
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		IssuerPEM: nil,
		NotBefore: notBefore,
		NotAfter:  notAfter,
	}, nil
}

func newMgr(t *testing.T, issuer Issuer) (*Manager, *store.Store, uint) {
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
	certsDir := filepath.Join(dir, "certs")
	return NewManager(st, certsDir, issuer), st, n.ID
}

func TestIssue_WritesPEMAndPersists(t *testing.T) {
	mgr, _, nodeID := newMgr(t, &fakeIssuer{})

	row, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "edge.example.com",
		Email:  "ops@example.com",
		Mode:   "http-01",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if row.CertPath == "" || row.KeyPath == "" {
		t.Fatal("cert/key path empty")
	}
	if _, err := os.Stat(row.CertPath); err != nil {
		t.Errorf("cert file missing: %v", err)
	}
	if _, err := os.Stat(row.KeyPath); err != nil {
		t.Errorf("key file missing: %v", err)
	}
	if row.Domain != "edge.example.com" {
		t.Errorf("wrong domain: %s", row.Domain)
	}
	if row.ExpiresAt == 0 {
		t.Error("expires_at not set")
	}
}

func TestIssue_UpdatesExistingRowOnReissue(t *testing.T) {
	mgr, _, nodeID := newMgr(t, &fakeIssuer{})

	row1, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "edge.example.com",
		Email:  "ops@example.com",
		Mode:   "http-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	row2, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "edge.example.com",
		Email:  "ops@example.com",
		Mode:   "http-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if row1.ID != row2.ID {
		t.Errorf("re-issue should update existing row; got new id %d (was %d)", row2.ID, row1.ID)
	}
}

func TestIssue_FailureRecordsLastError(t *testing.T) {
	mgr, st, nodeID := newMgr(t, &fakeIssuer{failOn: "broken.example.com"})

	_, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "broken.example.com",
		Email:  "ops@example.com",
		Mode:   "http-01",
	})
	if err == nil {
		t.Fatal("expected issue error")
	}
	cs, _ := st.ListCertificates(nodeID)
	if len(cs) != 1 {
		t.Fatalf("want 1 failure row, got %d", len(cs))
	}
	if cs[0].LastError == "" {
		t.Error("LastError not recorded")
	}
}

func TestCheckAndRenew_OnlyRenewsExpiring(t *testing.T) {
	issuer := &fakeIssuer{notAfter: time.Now().Add(10 * 24 * time.Hour)} // near expiry
	mgr, st, nodeID := newMgr(t, issuer)

	// acme_email must be stored or Renew() fails (Issuer requires non-empty email).
	if err := st.SetSetting("acme_email", "ops@example.com"); err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "soon.example.com",
		Email:  "ops@example.com",
		Mode:   "http-01",
	}); err != nil {
		t.Fatal(err)
	}
	// And one far-from-expiry.
	issuer.notAfter = time.Now().Add(80 * 24 * time.Hour)
	if _, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "fresh.example.com",
		Email:  "ops@example.com",
		Mode:   "http-01",
	}); err != nil {
		t.Fatal(err)
	}
	beforeCalls := issuer.calls

	// CheckAndRenew should re-issue only soon.example.com.
	n := mgr.CheckAndRenew(context.Background())
	if n != 1 {
		t.Errorf("want 1 renewal, got %d", n)
	}
	if issuer.calls != beforeCalls+1 {
		t.Errorf("issuer.calls delta = %d, want 1", issuer.calls-beforeCalls)
	}

	cs, _ := st.ListCertificates(nodeID)
	if len(cs) != 2 {
		t.Fatalf("want 2 certs, got %d", len(cs))
	}
}

// TestCheckAndRenew_SkipsAutoRenewOff locks the auto-renew toggle: a near-expiry
// cert with auto_renew flipped off (via SetCertAutoRenew) must NOT be renewed by
// the scheduler pass, even though its expiry is well inside the renew window.
func TestCheckAndRenew_SkipsAutoRenewOff(t *testing.T) {
	issuer := &fakeIssuer{notAfter: time.Now().Add(10 * 24 * time.Hour)} // near expiry
	mgr, st, nodeID := newMgr(t, issuer)

	row, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "off.example.com",
		Email:  "ops@example.com",
		Mode:   "http-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetCertAutoRenew(row.ID, false); err != nil {
		t.Fatal(err)
	}
	beforeCalls := issuer.calls

	if n := mgr.CheckAndRenew(context.Background()); n != 0 {
		t.Errorf("want 0 renewals (auto-renew off), got %d", n)
	}
	if issuer.calls != beforeCalls {
		t.Errorf("issuer.calls changed by %d, want 0", issuer.calls-beforeCalls)
	}

	// Flipping it back on lets the same pass renew it.
	if _, err := st.SetCertAutoRenew(row.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := mgr.CheckAndRenew(context.Background()); n != 1 {
		t.Errorf("want 1 renewal after re-enabling, got %d", n)
	}
}

// TestIssue_PersistsDNSConfigForRenewal locks the dns-01 auto-renew chain: the
// credentials supplied at issue time must land in settings so loadDNSConfig (the
// renewal path, which reads settings not the request) can reload them.
func TestIssue_PersistsDNSConfigForRenewal(t *testing.T) {
	mgr, st, nodeID := newMgr(t, &fakeIssuer{})

	if _, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain:      "dns.example.com",
		Email:       "ops@example.com",
		Mode:        "dns-01",
		DNSProvider: "cloudflare",
		DNSConfig:   map[string]string{"api_token": "tok-123"},
	}); err != nil {
		t.Fatal(err)
	}

	if v, _ := st.GetSetting("dns_cloudflare_api_token"); v != "tok-123" {
		t.Errorf("setting dns_cloudflare_api_token = %q, want tok-123", v)
	}
	got := mgr.loadDNSConfig("cloudflare")
	if got["api_token"] != "tok-123" {
		t.Errorf("loadDNSConfig api_token = %q, want tok-123", got["api_token"])
	}
}

// TestIssue_PersistsEmailAndIssuedAt locks 0-22 + 0-23: a fresh issue stores
// the contact email on the row and a non-zero IssuedAt (from the cert's
// NotBefore — the old bug left it at epoch 0).
func TestIssue_PersistsEmailAndIssuedAt(t *testing.T) {
	mgr, st, nodeID := newMgr(t, &fakeIssuer{})
	row, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "x.example.com",
		Email:  "owner@example.com",
		Mode:   "http-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.Email != "owner@example.com" {
		t.Errorf("row.Email = %q, want owner@example.com", row.Email)
	}
	if row.IssuedAt <= 0 {
		t.Errorf("row.IssuedAt = %d, want > 0 (0-22 regression)", row.IssuedAt)
	}
	// And it's durable.
	cs, _ := st.ListCertificates(nodeID)
	if len(cs) != 1 || cs[0].Email != "owner@example.com" || cs[0].IssuedAt <= 0 {
		t.Errorf("persisted row wrong: %+v", cs)
	}
}

// TestRenew_UsesCertRowEmail locks 0-23: renewal reads the email from the cert
// row, so a cleared acme_email setting can't break it.
func TestRenew_UsesCertRowEmail(t *testing.T) {
	issuer := &fakeIssuer{}
	mgr, _, nodeID := newMgr(t, issuer)
	row, err := mgr.Issue(context.Background(), nodeID, IssueRequest{
		Domain: "x.example.com",
		Email:  "row@example.com",
		Mode:   "http-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	// No acme_email setting at all — the row's email must carry the renewal.
	if _, err := mgr.Renew(context.Background(), row.ID); err != nil {
		t.Fatalf("renew should succeed off the row email: %v", err)
	}
	if issuer.lastEmail != "row@example.com" {
		t.Errorf("renew used email %q, want the row's row@example.com", issuer.lastEmail)
	}
}

// TestRenew_FallsBackToSettingEmail: a row with no Email (issued before the
// column existed, e.g. the live id=1) renews off the acme_email setting.
func TestRenew_FallsBackToSettingEmail(t *testing.T) {
	issuer := &fakeIssuer{}
	mgr, st, nodeID := newMgr(t, issuer)
	if err := st.SetSetting("acme_email", "fallback@example.com"); err != nil {
		t.Fatal(err)
	}
	// Insert a legacy-style row directly (Email empty).
	legacy := &model.Certificate{
		NodeID:    nodeID,
		Domain:    "legacy.example.com",
		Mode:      "http-01",
		ExpiresAt: time.Now().Add(48 * time.Hour).Unix(),
		AutoRenew: true,
	}
	if err := st.DB().Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Renew(context.Background(), legacy.ID); err != nil {
		t.Fatalf("legacy-row renew should fall back to the setting: %v", err)
	}
	if issuer.lastEmail != "fallback@example.com" {
		t.Errorf("renew used email %q, want the setting fallback@example.com", issuer.lastEmail)
	}
}

func TestParseExpiry(t *testing.T) {
	issuer := &fakeIssuer{notAfter: time.Now().Add(48 * time.Hour).Truncate(time.Second)}
	res, err := issuer.Obtain(context.Background(), IssueRequest{Domain: "x.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseExpiry(res.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(issuer.notAfter) {
		t.Errorf("ParseExpiry = %v, want %v", got, issuer.notAfter)
	}
}
