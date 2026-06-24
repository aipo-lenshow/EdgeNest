// Package selfsigned writes an idempotent self-signed RSA-2048 cert pair.
// Shared by bootstrap (pre-mints the wizard pair so ad-hoc TLS inbound
// creation works without running the wizard) and the wizard itself (where
// it's the canonical first-run cert for Hysteria2 / TUIC / Trojan).
package selfsigned

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Options carries everything the SAN extension needs. CommonName goes in
// Subject (legacy clients still read it); DNSNames + IPAddresses populate
// the SAN extension that every modern TLS verifier (browsers, sing-box,
// xray, mihomo, openssl) actually checks.
//
// Either DNSNames or IPAddresses may be empty, but at least one of them
// must contain a verifier-matchable entry. CommonName falls back to the
// first DNSName / IPAddress if empty.
type Options struct {
	CommonName  string
	DNSNames    []string
	IPAddresses []string // string form ("203.0.113.10", "2001:db8::1"); parsed via net.ParseIP
}

// Write creates a 2-year self-signed RSA-2048 cert for the given CN and
// writes PEM-encoded cert + key to certPath / keyPath. Idempotent: if both
// files already exist and parse, it's a no-op. SAN contains DNS:<domain>.
// For multi-SAN certs (panel + protocol IPs + domain), use WriteMultiSAN.
func Write(domain, certPath, keyPath string) error {
	return WriteMultiSAN(Options{CommonName: domain, DNSNames: []string{domain}}, certPath, keyPath)
}

// WriteMultiSAN writes a self-signed cert with a full SAN list. Panel
// HTTPS, Hysteria2 / TUIC / Trojan, and Settings host edits all funnel
// through this so one cert covers every face of the server: panel host
// (FQDN or IP), the node's v4 / v6 addresses, and the wizard-supplied
// protocol domain — clients can verify regardless of which address they
// dialed.
//
// Idempotent on identical file contents; intentionally NOT idempotent on
// option changes — callers that need a re-issue should delete certPath
// first (the bootstrap / Settings host-change paths do this).
func WriteMultiSAN(opts Options, certPath, keyPath string) error {
	if opts.CommonName == "" {
		if len(opts.DNSNames) > 0 {
			opts.CommonName = opts.DNSNames[0]
		} else if len(opts.IPAddresses) > 0 {
			opts.CommonName = opts.IPAddresses[0]
		} else {
			return fmt.Errorf("selfsigned: at least one DNS name or IP address required")
		}
	}
	if exists(certPath, keyPath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return fmt.Errorf("mkdir cert dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return fmt.Errorf("mkdir key dir: %w", err)
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("rsa keygen: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("serial: %w", err)
	}

	ips := make([]net.IP, 0, len(opts.IPAddresses))
	for _, s := range opts.IPAddresses {
		if ip := net.ParseIP(s); ip != nil {
			ips = append(ips, ip)
		}
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: opts.CommonName},
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.Add(2 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     opts.DNSNames,
		IPAddresses:  ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("create cert: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	return nil
}

func exists(certPath, keyPath string) bool {
	cb, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	if _, err := os.Stat(keyPath); err != nil {
		return false
	}
	block, _ := pem.Decode(cb)
	if block == nil {
		return false
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return false
	}
	return true
}
