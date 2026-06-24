package wizard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// stubChecker is a domainChecker that returns a fixed verdict, so funnel ACME
// tests don't depend on live DNS.
type stubChecker struct{ status DomainStatus }

func (s stubChecker) Validate(_ context.Context, domain string) (DomainResult, error) {
	return DomainResult{Domain: domain, Status: s.status}, nil
}

// fakeIssuer records the last Issue call and returns a canned cert or error.
type fakeIssuer struct {
	err       error
	lastEmail string
	lastDom   string
	calls     int
}

func (f *fakeIssuer) Issue(_ context.Context, nodeID uint, req cert.IssueRequest) (*model.Certificate, error) {
	f.calls++
	f.lastEmail = req.Email
	f.lastDom = req.Domain
	if f.err != nil {
		return nil, f.err
	}
	return &model.Certificate{
		Domain:   req.Domain,
		Email:    req.Email,
		CertPath: "/etc/edgenest/certs/" + req.Domain + "/fullchain.pem",
		KeyPath:  "/etc/edgenest/certs/" + req.Domain + "/privkey.pem",
	}, nil
}

// withGreyCloudACME builds a wizard whose domain check always says "ok" (grey
// cloud) and whose issuer is the supplied fake.
func withGreyCloudACME(t *testing.T, iss CertIssuer) (*Wizard, *store.Store, uint) {
	t.Helper()
	st, nodeID := newStore(t)
	w := New(st, nil, iss)
	w.validator = stubChecker{status: DomainStatusOK}
	return w, st, nodeID
}

// TestCreateFromFunnel_ACMEOnGreyCloud: a grey-cloud domain + successful
// issuance flips NeedsCert inbounds to the real cert with acme_managed=true
// (encoders go strict) and no self_signed residue.
func TestCreateFromFunnel_ACMEOnGreyCloud(t *testing.T) {
	iss := &fakeIssuer{}
	w, st, nodeID := withGreyCloudACME(t, iss)

	res, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		Domain:      "edge.example.com",
		AcmeEmail:   "owner@example.com",
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols:   []FunnelProto{{ID: "trojan-tls"}, {ID: "hysteria2"}},
	})
	if err != nil {
		t.Fatalf("grey-cloud ACME batch: %v", err)
	}
	if res.CertMode != "acme" {
		t.Errorf("CertMode = %q, want acme", res.CertMode)
	}
	if res.CertDomain != "edge.example.com" {
		t.Errorf("CertDomain = %q, want edge.example.com", res.CertDomain)
	}
	if res.CertError != "" {
		t.Errorf("CertError = %q, want empty on success", res.CertError)
	}
	// One cert for the whole batch, not one per NeedsCert protocol.
	if iss.calls != 1 {
		t.Errorf("issuer called %d times, want 1 (batch shares one cert)", iss.calls)
	}
	if iss.lastEmail != "owner@example.com" || iss.lastDom != "edge.example.com" {
		t.Errorf("issuer got email=%q dom=%q", iss.lastEmail, iss.lastDom)
	}
	ins, _ := st.ListInbounds(nodeID)
	for i := range ins {
		s := settingsOf(t, &ins[i])
		if s["acme_managed"] != "true" {
			t.Errorf("%s: acme_managed = %v, want \"true\"", ins[i].Tag, s["acme_managed"])
		}
		if _, ok := s["self_signed"]; ok {
			t.Errorf("%s: self_signed must be absent on the ACME branch", ins[i].Tag)
		}
		if cp, _ := s["tls_cert_path"].(string); !strings.HasSuffix(cp, "edge.example.com/fullchain.pem") {
			t.Errorf("%s: tls_cert_path = %q, want the ACME cert", ins[i].Tag, cp)
		}
	}
}

// TestCreateFromFunnel_ACMEFailFallsBackSelfSigned: issuance error must NOT
// abort the batch — inbounds still get created on the self-signed pair and the
// reason surfaces in CertError.
func TestCreateFromFunnel_ACMEFailFallsBackSelfSigned(t *testing.T) {
	iss := &fakeIssuer{err: errors.New("port 80 busy")}
	w, st, nodeID := withGreyCloudACME(t, iss)

	res, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		Domain:      "edge.example.com",
		AcmeEmail:   "owner@example.com",
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols:   []FunnelProto{{ID: "trojan-tls"}, {ID: "hysteria2"}},
	})
	if err != nil {
		t.Fatalf("ACME failure must not abort the batch: %v", err)
	}
	if res.CertMode != "self-signed" {
		t.Errorf("CertMode = %q, want self-signed on fallback", res.CertMode)
	}
	if res.CertError == "" || !strings.Contains(res.CertError, "port 80 busy") {
		t.Errorf("CertError = %q, want the issuance reason", res.CertError)
	}
	if len(res.Inbounds) != 2 {
		t.Fatalf("created %d inbounds, want 2 (batch not rolled back)", len(res.Inbounds))
	}
	ins, _ := st.ListInbounds(nodeID)
	for i := range ins {
		s := settingsOf(t, &ins[i])
		if s["self_signed"] != "true" {
			t.Errorf("%s: want self_signed on fallback", ins[i].Tag)
		}
		if _, ok := s["acme_managed"]; ok {
			t.Errorf("%s: acme_managed must be absent on fallback", ins[i].Tag)
		}
	}
}

// TestCreateFromFunnel_NoEmailFallsBackSelfSigned: a grey-cloud domain without
// any contact email can't issue — falls back to self-signed and says so.
func TestCreateFromFunnel_NoEmailFallsBackSelfSigned(t *testing.T) {
	iss := &fakeIssuer{}
	w, _, nodeID := withGreyCloudACME(t, iss)

	res, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		Domain:      "edge.example.com",
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols:   []FunnelProto{{ID: "trojan-tls"}},
	})
	if err != nil {
		t.Fatalf("no-email batch: %v", err)
	}
	if iss.calls != 0 {
		t.Errorf("issuer should not be called without an email; calls=%d", iss.calls)
	}
	if res.CertMode != "self-signed" {
		t.Errorf("CertMode = %q, want self-signed", res.CertMode)
	}
	if !strings.Contains(res.CertError, "email") {
		t.Errorf("CertError = %q, want it to name the missing email", res.CertError)
	}
}

// settingsOf unmarshals an inbound's settings JSON for assertions.
func settingsOf(t *testing.T, in *model.Inbound) map[string]any {
	t.Helper()
	s := map[string]any{}
	if err := json.Unmarshal([]byte(in.Settings), &s); err != nil {
		t.Fatalf("settings of %s: %v", in.Tag, err)
	}
	return s
}

// TestCreateFromFunnel_NoDomain_TLSProtocolsSelfSigned locks the unified
// certificate model's no-domain branch: every TLS-cert protocol — including
// the four that the retired AcmeOnly gate used to reject (trojan-tls,
// vmess-ws-cdn, vless-ws-cdn, vless-xhttp-tls-cdn) — is creatable without a
// domain, lands on the bootstrap self-signed pair, and carries no domain
// residue in ws_host / xhttp_host.
func TestCreateFromFunnel_NoDomain_TLSProtocolsSelfSigned(t *testing.T) {
	st, nodeID := newStore(t)
	w := New(st, nil, nil)
	w.xrayInstalled = func() bool { return true } // host has xray for this batch

	res, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols: []FunnelProto{
			{ID: "trojan-tls"},
			{ID: "vmess-ws-cdn"},
			{ID: "vless-ws-cdn"},
			{ID: "vless-xhttp-tls-cdn"},
			{ID: "hysteria2"},
		},
	})
	if err != nil {
		t.Fatalf("no-domain batch must not be rejected (old AcmeOnly gate regression): %v", err)
	}
	if res.CertMode != "self-signed" {
		t.Errorf("CertMode = %q, want self-signed", res.CertMode)
	}
	if len(res.Inbounds) != 5 {
		t.Fatalf("created %d inbounds, want 5", len(res.Inbounds))
	}

	ins, err := st.ListInbounds(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ins {
		in := &ins[i]
		s := settingsOf(t, in)
		if s["self_signed"] != "true" {
			t.Errorf("%s: self_signed = %v, want \"true\"", in.Tag, s["self_signed"])
		}
		cp, _ := s["tls_cert_path"].(string)
		if !strings.HasSuffix(cp, "wizard-fullchain.pem") {
			t.Errorf("%s: tls_cert_path = %q, want bootstrap pair", in.Tag, cp)
		}
		if _, ok := s["acme_managed"]; ok {
			t.Errorf("%s: acme_managed must not be set on the self-signed branch", in.Tag)
		}
		// No domain → no Host-header residue.
		if v, ok := s["ws_host"]; ok {
			t.Errorf("%s: ws_host = %v, want absent without a domain", in.Tag, v)
		}
		if v, ok := s["xhttp_host"]; ok {
			t.Errorf("%s: xhttp_host = %v, want absent without a domain", in.Tag, v)
		}
		// Family-pin invariant: SubscriptionHost is the literal Step-1 IP in
		// every domain state — the v4/v6 outbound pinning (14p/14q) keys on it.
		if in.SubscriptionHost != "127.0.0.1" {
			t.Errorf("%s: SubscriptionHost = %q, want the Step-1 IP", in.Tag, in.SubscriptionHost)
		}
	}
}

// TestCreateFromFunnel_XrayMissing_RejectsXHTTP locks the xray gate: when the
// host has no xray-core engine, an XHTTP protocol must be refused up front with
// an actionable error, and the whole batch (including the sing-box protocols
// that precede it) must roll back so the panel never shows a dead inbound.
func TestCreateFromFunnel_XrayMissing_RejectsXHTTP(t *testing.T) {
	st, nodeID := newStore(t)
	w := New(st, nil, nil)
	w.xrayInstalled = func() bool { return false } // host without xray-core

	_, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols: []FunnelProto{
			{ID: "vless-reality"},          // sing-box, created first
			{ID: "vless-xhttp-reality"},    // xray — must trip the gate
		},
	})
	if err == nil {
		t.Fatal("expected rejection when xray-core is not installed")
	}
	if !strings.Contains(err.Error(), "xray-core") {
		t.Errorf("error %q should mention xray-core", err.Error())
	}
	// Rollback invariant: nothing persisted, not even the sing-box inbound that
	// was created before the gate tripped.
	ins, _ := st.ListInbounds(nodeID)
	if len(ins) != 0 {
		t.Errorf("after rejection %d inbounds persisted, want 0 (rollback)", len(ins))
	}
}

// TestCreateFromFunnel_MismatchDomain_TreatedAsNoDomain locks the effDomain
// derivation: a domain that resolves to neither the VPS nor Cloudflare
// (status mismatch — here simulated by NXDOMAIN → none, the same effDomain
// branch) must not leak into sni / ws_host.
func TestCreateFromFunnel_UnresolvableDomain_NoResidue(t *testing.T) {
	st, nodeID := newStore(t)
	w := New(st, nil, nil)

	// .invalid TLD is RFC 6761-reserved: guaranteed NXDOMAIN without
	// depending on the test host's resolver contents.
	res, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		Domain:      "wizard-test.invalid",
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols:   []FunnelProto{{ID: "vmess-ws-cdn"}, {ID: "trojan-tls"}},
	})
	if err != nil {
		t.Fatalf("unresolvable domain must fall back to the no-domain branch: %v", err)
	}
	if res.DomainStatus != DomainStatusNone {
		t.Fatalf("DomainStatus = %q, want none (NXDOMAIN)", res.DomainStatus)
	}
	ins, _ := st.ListInbounds(nodeID)
	for i := range ins {
		s := settingsOf(t, &ins[i])
		if v, ok := s["ws_host"]; ok {
			t.Errorf("%s: ws_host = %v leaked from an unusable domain", ins[i].Tag, v)
		}
		if sni, _ := s["sni"].(string); sni == "wizard-test.invalid" {
			t.Errorf("%s: sni picked up the unusable domain", ins[i].Tag)
		}
		if s["self_signed"] != "true" {
			t.Errorf("%s: want self-signed on the unusable-domain branch", ins[i].Tag)
		}
	}
}

// TestCreateFromFunnel_CDNPortGate_OnlyWhenToggled locks the narrowed CF
// whitelist gate: a CDN-eligible protocol off the whitelist port is fine as
// long as the CDN toggle is OFF (bare-IP direct mode), and still refused
// when the toggle is ON.
func TestCreateFromFunnel_CDNPortGate_OnlyWhenToggled(t *testing.T) {
	t.Run("cdn off, off-whitelist port accepted", func(t *testing.T) {
		st, nodeID := newStore(t)
		w := New(st, nil, nil)
		_, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
			ClientEmail: "owner@example.com",
			CertsDir:    t.TempDir(),
			Host:        "127.0.0.1",
			Protocols:   []FunnelProto{{ID: "vmess-ws-cdn", Port: 18080, CDN: false}},
		})
		if err != nil {
			t.Fatalf("bare-IP vmess-ws on a non-CF port must be accepted: %v", err)
		}
		ins, _ := st.ListInbounds(nodeID)
		if len(ins) != 1 {
			t.Fatalf("created %d inbounds, want 1", len(ins))
		}
		s := settingsOf(t, &ins[0])
		if _, ok := s["cdn_mode"]; ok {
			t.Error("cdn_mode must not be set when the toggle is off")
		}
	})

	t.Run("cdn on, off-whitelist port refused", func(t *testing.T) {
		st, nodeID := newStore(t)
		w := New(st, nil, nil)
		_, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
			ClientEmail: "owner@example.com",
			CertsDir:    t.TempDir(),
			Host:        "127.0.0.1",
			Protocols:   []FunnelProto{{ID: "vmess-ws-cdn", Port: 18080, CDN: true}},
		})
		if err == nil {
			t.Fatal("CDN-toggled vmess-ws on a non-CF port must be refused")
		}
		if !strings.Contains(err.Error(), "whitelist") {
			t.Errorf("error should name the CF whitelist, got: %v", err)
		}
	})
}

// TestCreateFromFunnel_Argo_PlaintextLoopback: an Argo-selected ws protocol
// must be built as a PLAINTEXT WebSocket origin on 127.0.0.1 (no cert) marked
// argo_bound — cloudflared reaches it over plain HTTP and Cloudflare supplies
// TLS at its edge. A cert on the origin would break the http→TLS handshake.
func TestCreateFromFunnel_Argo_PlaintextLoopback(t *testing.T) {
	st, nodeID := newStore(t)
	w := New(st, nil, nil)
	res, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "203.0.113.9",
		// No domain → temp-argo shape; ArgoNamed flags the binding for both
		// temp and named (the wizard maps argoTemp||argoNamed → argo_named).
		Protocols: []FunnelProto{{ID: "vmess-ws-cdn", Port: 12345, ArgoNamed: true}},
	})
	if err != nil {
		t.Fatalf("argo funnel must succeed without a domain/cert: %v", err)
	}
	if res.CertMode == "self-signed" || res.CertMode == "acme" {
		t.Errorf("argo origin must carry no cert, got CertMode=%q", res.CertMode)
	}
	ins, _ := st.ListInbounds(nodeID)
	if len(ins) != 1 {
		t.Fatalf("created %d inbounds, want 1", len(ins))
	}
	in := ins[0]
	if in.Listen != "127.0.0.1" {
		t.Errorf("listen = %q, want 127.0.0.1 (loopback origin)", in.Listen)
	}
	if in.SubscriptionHost != "" {
		t.Errorf("subscription_host = %q, want empty (resolver supplies tunnel host)", in.SubscriptionHost)
	}
	s := settingsOf(t, &in)
	if s["argo_bound"] != "true" {
		t.Errorf("argo_bound = %v, want \"true\"", s["argo_bound"])
	}
	if _, ok := s["tls_cert_path"]; ok {
		t.Error("argo origin must be plaintext — tls_cert_path must be absent")
	}
	if _, ok := s["cdn_mode"]; ok {
		t.Error("argo and cdn_mode are mutually exclusive — cdn_mode must be absent")
	}
}

// TestCreateFromFunnel_CertModeNone_WithoutTLSProtocols: a batch with no
// TLS-cert protocol reports cert_mode "none".
func TestCreateFromFunnel_CertModeNone_WithoutTLSProtocols(t *testing.T) {
	st, nodeID := newStore(t)
	w := New(st, nil, nil)
	res, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols:   []FunnelProto{{ID: "shadowsocks-2022"}, {ID: "socks5"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CertMode != "none" {
		t.Errorf("CertMode = %q, want none", res.CertMode)
	}
}

// TestCreateFromFunnel_ReusesExistingCert (0-46): when a valid cert for the
// domain already exists on disk, the wizard must reuse it rather than re-issue.
// Re-issuing burns Let's Encrypt's duplicate-cert rate limit and, on the 429,
// drops the batch to self-signed despite a perfectly good cert being present.
func TestCreateFromFunnel_ReusesExistingCert(t *testing.T) {
	iss := &fakeIssuer{}
	w, st, nodeID := withGreyCloudACME(t, iss)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.pem")
	keyPath := filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(certPath, []byte("cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&model.Certificate{
		NodeID:    nodeID,
		Domain:    "edge.example.com",
		Mode:      "http-01",
		CertPath:  certPath,
		KeyPath:   keyPath,
		ExpiresAt: time.Now().Add(60 * 24 * time.Hour).Unix(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	res, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		Domain:      "edge.example.com",
		AcmeEmail:   "owner@example.com",
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols:   []FunnelProto{{ID: "trojan-tls"}},
	})
	if err != nil {
		t.Fatalf("reuse path: %v", err)
	}
	if iss.calls != 0 {
		t.Errorf("issuer called %d times, want 0 (existing valid cert must be reused)", iss.calls)
	}
	if res.CertMode != "acme" {
		t.Errorf("CertMode = %q, want acme (reused real cert)", res.CertMode)
	}
	ins, _ := st.ListInbounds(nodeID)
	for i := range ins {
		s := settingsOf(t, &ins[i])
		if s["acme_managed"] != "true" {
			t.Errorf("%s: acme_managed = %v, want true (reused cert is real)", ins[i].Tag, s["acme_managed"])
		}
		if cp, _ := s["tls_cert_path"].(string); cp != certPath {
			t.Errorf("%s: tls_cert_path = %q, want reused %q", ins[i].Tag, cp, certPath)
		}
	}
}

// TestCreateFromFunnel_StaleCertRowReissues: a cert row whose file is gone must
// NOT be reused — the wizard re-issues so it never points at a missing file.
func TestCreateFromFunnel_StaleCertRowReissues(t *testing.T) {
	iss := &fakeIssuer{}
	w, st, nodeID := withGreyCloudACME(t, iss)

	if err := st.DB().Create(&model.Certificate{
		NodeID:    nodeID,
		Domain:    "edge.example.com",
		Mode:      "http-01",
		CertPath:  "/nonexistent/fullchain.pem", // file gone
		KeyPath:   "/nonexistent/privkey.pem",
		ExpiresAt: time.Now().Add(60 * 24 * time.Hour).Unix(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	_, err := w.CreateFromFunnel(context.Background(), nodeID, FunnelRequest{
		Domain:      "edge.example.com",
		AcmeEmail:   "owner@example.com",
		ClientEmail: "owner@example.com",
		CertsDir:    t.TempDir(),
		Host:        "127.0.0.1",
		Protocols:   []FunnelProto{{ID: "trojan-tls"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if iss.calls != 1 {
		t.Errorf("issuer called %d times, want 1 (stale row with missing file must re-issue)", iss.calls)
	}
}
