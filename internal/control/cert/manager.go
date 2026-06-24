// Package cert handles ACME certificate issuance and renewal for proxy
// inbounds that need real TLS (Hysteria2, Trojan, TUIC, VLESS-WS-TLS).
//
// The Manager hides the ACME library behind an Issuer interface so tests can
// substitute a fake issuer without contacting Let's Encrypt. The production
// issuer is the lego adapter in lego_issuer.go.
//
// DISCIPLINE: control plane only — never imports node/engine.
package cert

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// Issuer is the seam between the cert manager and an ACME library. Real impl
// lives in lego_issuer.go; tests swap in a stub.
type Issuer interface {
	// Obtain runs a full order/challenge/finalize/download cycle for `domain`.
	// `mode` is "http-01" or "dns-01". `dnsProvider` and `dnsConfig` are
	// consulted only for DNS-01. Returns PEM cert chain + PEM private key.
	Obtain(ctx context.Context, req IssueRequest) (*IssueResult, error)
}

// IssueRequest carries everything the issuer needs.
type IssueRequest struct {
	Domain      string
	Email       string
	Mode        string            // "http-01" | "dns-01"
	DNSProvider string            // "cloudflare" | "aliyun" | ...
	DNSConfig   map[string]string // provider-specific creds (API token, etc.)
	HTTPPort    int               // HTTP-01 listener port (default 80)
}

// IssueResult is what the issuer hands back.
type IssueResult struct {
	CertPEM   []byte
	KeyPEM    []byte
	IssuerPEM []byte // intermediate(s); may be empty
	NotBefore time.Time
	NotAfter  time.Time
}

// Manager orchestrates the cert lifecycle: issue, persist, renew on schedule.
type Manager struct {
	store     *store.Store
	certsDir  string
	issuer    Issuer
	renewSoon time.Duration // renew when this much (or less) is left
}

// NewManager constructs a Manager. certsDir is where PEM files land; one
// subdirectory per domain.
func NewManager(s *store.Store, certsDir string, issuer Issuer) *Manager {
	return &Manager{
		store:     s,
		certsDir:  certsDir,
		issuer:    issuer,
		renewSoon: 30 * 24 * time.Hour,
	}
}

// Issue obtains a new certificate and persists it. If a Certificate row for
// (nodeID, domain) already exists, it is updated in place.
func (m *Manager) Issue(ctx context.Context, nodeID uint, req IssueRequest) (*model.Certificate, error) {
	if req.Domain == "" {
		return nil, errors.New("domain required")
	}
	if req.Email == "" {
		return nil, errors.New("email required (ACME account contact)")
	}
	if req.Mode == "" {
		req.Mode = "http-01"
	}
	if req.HTTPPort == 0 {
		req.HTTPPort = 80
	}

	res, err := m.issuer.Obtain(ctx, req)
	if err != nil {
		// Persist a row noting the failure so the operator sees it in /certs.
		m.recordFailure(nodeID, req, err)
		return nil, fmt.Errorf("acme obtain: %w", err)
	}

	certPath, keyPath, err := m.writePEM(req.Domain, res)
	if err != nil {
		return nil, fmt.Errorf("write pem: %w", err)
	}

	row, err := m.upsertCert(nodeID, req, certPath, keyPath, res)
	if err != nil {
		return nil, fmt.Errorf("persist cert: %w", err)
	}
	if req.Mode == "dns-01" {
		m.persistDNSConfig(req.DNSProvider, req.DNSConfig)
	}
	return row, nil
}

// Renew re-issues a cert by ID. Falls back to a fresh Issue under the hood.
func (m *Manager) Renew(ctx context.Context, certID uint) (*model.Certificate, error) {
	existing, err := m.getCert(certID)
	if err != nil {
		return nil, err
	}
	// Email comes from the cert row (captured at issue time). Rows issued before
	// the Email column existed have it empty — fall back to the acme_email
	// setting so they can still renew.
	email := existing.Email
	if email == "" {
		email = m.acmeEmail()
	}
	req := IssueRequest{
		Domain:      existing.Domain,
		Email:       email,
		Mode:        existing.Mode,
		DNSProvider: existing.DNSProvider,
		// DNS creds are kept in Settings, not on the cert row; load on demand.
		DNSConfig: m.loadDNSConfig(existing.DNSProvider),
	}
	return m.Issue(ctx, existing.NodeID, req)
}

// CheckAndRenew runs one pass: any cert with NotAfter within `renewSoon` is
// renewed. Errors per-cert are logged via the Certificate.LastError field
// rather than aborting the whole pass.
func (m *Manager) CheckAndRenew(ctx context.Context) int {
	all, err := m.listAllCerts()
	if err != nil {
		return 0
	}
	now := time.Now()
	renewed := 0
	for _, c := range all {
		if !c.AutoRenew {
			continue
		}
		if c.ExpiresAt == 0 {
			continue
		}
		if time.Unix(c.ExpiresAt, 0).Sub(now) > m.renewSoon {
			continue
		}
		if _, err := m.Renew(ctx, c.ID); err == nil {
			renewed++
		}
	}
	return renewed
}

// ---- helpers ----

func (m *Manager) writePEM(domain string, res *IssueResult) (certPath, keyPath string, err error) {
	dir := filepath.Join(m.certsDir, domain)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	certPath = filepath.Join(dir, "fullchain.pem")
	keyPath = filepath.Join(dir, "privkey.pem")

	full := append([]byte{}, res.CertPEM...)
	if len(res.IssuerPEM) > 0 {
		full = append(full, '\n')
		full = append(full, res.IssuerPEM...)
	}
	if err := os.WriteFile(certPath, full, 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, res.KeyPEM, 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func (m *Manager) upsertCert(nodeID uint, req IssueRequest, certPath, keyPath string, res *IssueResult) (*model.Certificate, error) {
	existing, _ := m.findCert(nodeID, req.Domain)
	now := time.Now().Unix()
	if existing != nil {
		existing.CertPath = certPath
		existing.KeyPath = keyPath
		existing.IssuedAt = res.NotBefore.Unix()
		existing.ExpiresAt = res.NotAfter.Unix()
		existing.LastError = ""
		existing.Mode = req.Mode
		existing.DNSProvider = req.DNSProvider
		if req.Email != "" {
			existing.Email = req.Email
		}
		existing.UpdatedAt = now
		if err := m.store.DB().Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}
	row := &model.Certificate{
		NodeID:      nodeID,
		Domain:      req.Domain,
		Mode:        req.Mode,
		DNSProvider: req.DNSProvider,
		Email:       req.Email,
		CertPath:    certPath,
		KeyPath:     keyPath,
		IssuedAt:    res.NotBefore.Unix(),
		ExpiresAt:   res.NotAfter.Unix(),
		AutoRenew:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := m.store.DB().Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func (m *Manager) recordFailure(nodeID uint, req IssueRequest, err error) {
	existing, _ := m.findCert(nodeID, req.Domain)
	now := time.Now().Unix()
	if existing != nil {
		existing.LastError = err.Error()
		existing.UpdatedAt = now
		_ = m.store.DB().Save(existing).Error
		return
	}
	_ = m.store.DB().Create(&model.Certificate{
		NodeID:      nodeID,
		Domain:      req.Domain,
		Mode:        req.Mode,
		DNSProvider: req.DNSProvider,
		Email:       req.Email,
		LastError:   err.Error(),
		AutoRenew:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}).Error
}

func (m *Manager) findCert(nodeID uint, domain string) (*model.Certificate, error) {
	var c model.Certificate
	if err := m.store.DB().First(&c, "node_id = ? AND domain = ?", nodeID, domain).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (m *Manager) getCert(id uint) (*model.Certificate, error) {
	var c model.Certificate
	if err := m.store.DB().First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (m *Manager) listAllCerts() ([]model.Certificate, error) {
	var cs []model.Certificate
	err := m.store.DB().Order("id asc").Find(&cs).Error
	return cs, err
}

// acmeEmail reads the contact email from settings; falls back to a placeholder
// that LE rejects, surfacing the misconfiguration loudly.
func (m *Manager) acmeEmail() string {
	v, _ := m.store.GetSetting("acme_email")
	if v != "" {
		return v
	}
	return ""
}

// loadDNSConfig pulls the provider's credentials from settings. Stored as
// "dns_<provider>_<key>" (see persistDNSConfig) so renewal can reload the same
// creds the operator entered at issue time, and so they can be rotated without
// a restart. Keys come from the curated dnsRegistry.
func (m *Manager) loadDNSConfig(provider string) map[string]string {
	spec, ok := dnsRegistry[provider]
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, f := range spec.Fields {
		if v, _ := m.store.GetSetting("dns_" + provider + "_" + f.Key); v != "" {
			out[f.Key] = v
		}
	}
	return out
}

// persistDNSConfig saves the DNS-01 credentials supplied at issue time so the
// renewal scheduler (which reads from settings, not the request) can reuse them.
// Without this, a dns-01 cert would issue fine but silently fail to auto-renew.
func (m *Manager) persistDNSConfig(provider string, cfg map[string]string) {
	spec, ok := dnsRegistry[provider]
	if !ok {
		return
	}
	for _, f := range spec.Fields {
		if v := cfg[f.Key]; v != "" {
			_ = m.store.SetSetting("dns_"+provider+"_"+f.Key, v)
		}
	}
}

// ParseExpiry extracts NotAfter from a PEM cert chain. Returned for the
// scheduler / tests to verify lifecycle without round-tripping the issuer.
func ParseExpiry(certPEM []byte) (time.Time, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return time.Time{}, errors.New("no PEM block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}
