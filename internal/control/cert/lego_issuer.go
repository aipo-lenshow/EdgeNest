package cert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"os"
	"sync"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/azuredns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/digitalocean"
	"github.com/go-acme/lego/v4/providers/dns/gandiv5"
	"github.com/go-acme/lego/v4/providers/dns/gcloud"
	"github.com/go-acme/lego/v4/providers/dns/godaddy"
	"github.com/go-acme/lego/v4/providers/dns/huaweicloud"
	"github.com/go-acme/lego/v4/providers/dns/namecheap"
	"github.com/go-acme/lego/v4/providers/dns/namesilo"
	"github.com/go-acme/lego/v4/providers/dns/porkbun"
	"github.com/go-acme/lego/v4/providers/dns/route53"
	"github.com/go-acme/lego/v4/providers/dns/tencentcloud"
	"github.com/go-acme/lego/v4/registration"
)

// dnsConstructors maps a curated provider name to its lego constructor. Each
// reads credentials from env vars (set by dnsProviderFor under dnsEnvMu just
// before the call). Importing only these keeps the binary far smaller than the
// full lego provider aggregator (which pulls every cloud SDK).
var dnsConstructors = map[string]func() (challenge.Provider, error){
	"cloudflare":   func() (challenge.Provider, error) { return cloudflare.NewDNSProvider() },
	"route53":      func() (challenge.Provider, error) { return route53.NewDNSProvider() },
	"gcloud":       func() (challenge.Provider, error) { return gcloud.NewDNSProvider() },
	"azuredns":     func() (challenge.Provider, error) { return azuredns.NewDNSProvider() },
	"alidns":       func() (challenge.Provider, error) { return alidns.NewDNSProvider() },
	"tencentcloud": func() (challenge.Provider, error) { return tencentcloud.NewDNSProvider() },
	"huaweicloud":  func() (challenge.Provider, error) { return huaweicloud.NewDNSProvider() },
	"digitalocean": func() (challenge.Provider, error) { return digitalocean.NewDNSProvider() },
	"godaddy":      func() (challenge.Provider, error) { return godaddy.NewDNSProvider() },
	"namecheap":    func() (challenge.Provider, error) { return namecheap.NewDNSProvider() },
	"namesilo":     func() (challenge.Provider, error) { return namesilo.NewDNSProvider() },
	"gandiv5":      func() (challenge.Provider, error) { return gandiv5.NewDNSProvider() },
	"porkbun":      func() (challenge.Provider, error) { return porkbun.NewDNSProvider() },
}

// LEDirectoryProduction is the default ACME directory URL.
const LEDirectoryProduction = "https://acme-v02.api.letsencrypt.org/directory"

// LEDirectoryStaging is the staging server (use during development / tests so
// you don't burn through real LE rate limits).
const LEDirectoryStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"

// LegoIssuer is the production ACME issuer (wraps go-acme/lego).
type LegoIssuer struct {
	// DirectoryURL overrides the CA; defaults to LE production.
	DirectoryURL string
}

// NewLegoIssuer constructs a production-mode issuer.
func NewLegoIssuer() *LegoIssuer {
	return &LegoIssuer{DirectoryURL: LEDirectoryProduction}
}

// acmeAccount is the in-memory ACME account; we don't persist private keys to
// the panel DB (one keypair per run is fine since registrations are durable
// against repeat-with-same-email + EAB-free LE).
type acmeAccount struct {
	email string
	key   crypto.PrivateKey
	reg   *registration.Resource
}

func (a *acmeAccount) GetEmail() string                        { return a.email }
func (a *acmeAccount) GetRegistration() *registration.Resource { return a.reg }
func (a *acmeAccount) GetPrivateKey() crypto.PrivateKey        { return a.key }

// Obtain runs the full ACME flow.
func (l *LegoIssuer) Obtain(ctx context.Context, req IssueRequest) (*IssueResult, error) {
	if req.Mode == "" {
		req.Mode = "http-01"
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("account key: %w", err)
	}
	acct := &acmeAccount{email: req.Email, key: priv}

	cfg := lego.NewConfig(acct)
	cfg.CADirURL = l.directory()
	cfg.Certificate.KeyType = certcrypto.RSA2048

	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("lego client: %w", err)
	}

	switch req.Mode {
	case "http-01":
		port := req.HTTPPort
		if port == 0 {
			port = 80
		}
		provider := http01.NewProviderServer("", fmt.Sprintf("%d", port))
		if err := client.Challenge.SetHTTP01Provider(provider); err != nil {
			return nil, fmt.Errorf("http-01 provider: %w", err)
		}
	case "dns-01":
		provider, err := dnsProviderFor(req.DNSProvider, req.DNSConfig)
		if err != nil {
			return nil, err
		}
		if err := client.Challenge.SetDNS01Provider(provider); err != nil {
			return nil, fmt.Errorf("dns-01 provider: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown acme mode %q", req.Mode)
	}

	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("register account: %w", err)
	}
	acct.reg = reg

	obtain := certificate.ObtainRequest{
		Domains: []string{req.Domain},
		Bundle:  true,
	}
	out, err := client.Certificate.Obtain(obtain)
	if err != nil {
		return nil, fmt.Errorf("obtain: %w", err)
	}

	// Parse the leaf so we capture both NotBefore and NotAfter — the cert row's
	// IssuedAt comes from NotBefore (ParseExpiry alone only yields NotAfter,
	// which left IssuedAt at the zero time = unix epoch 0).
	block, _ := pem.Decode(out.Certificate)
	if block == nil {
		return nil, fmt.Errorf("parse leaf: no PEM block")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse leaf: %w", err)
	}

	return &IssueResult{
		CertPEM:   out.Certificate,
		KeyPEM:    out.PrivateKey,
		IssuerPEM: out.IssuerCertificate,
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
	}, nil
}

// dnsEnvMu serializes the setenv→construct→unsetenv dance below. lego's
// provider constructors read credentials from process environment variables, so
// we must not have two concurrent issuances clobbering each other's env.
var dnsEnvMu sync.Mutex

// dnsProviderFor builds a lego DNS-01 challenge provider from the operator's
// credentials. Curated providers (dnsRegistry) map friendly keys → lego env
// vars; any other lego provider works too — its cfg keys are taken as literal
// env var names. DNS-01 is the path for domains where port 80 is unreachable
// (orange-cloud CDN, firewalled :80) or for wildcard certs.
func dnsProviderFor(name string, cfg map[string]string) (challenge.Provider, error) {
	if name == "" {
		return nil, errors.New("dns-01: provider name required")
	}

	dnsEnvMu.Lock()
	defer dnsEnvMu.Unlock()

	ctor, ok := dnsConstructors[name]
	if !ok {
		return nil, fmt.Errorf("dns-01: provider %q not supported", name)
	}

	var toClear []string
	if spec, ok := dnsRegistry[name]; ok {
		for _, f := range spec.Fields {
			if v := cfg[f.Key]; v != "" {
				_ = os.Setenv(f.Env, v)
				toClear = append(toClear, f.Env)
			}
		}
	}

	provider, err := ctor()
	// Credentials are copied into the provider's own config at construction, so
	// it's safe (and tidier) to drop them from the environment immediately.
	for _, e := range toClear {
		_ = os.Unsetenv(e)
	}
	if err != nil {
		return nil, fmt.Errorf("dns-01 provider %q: %w", name, err)
	}
	return provider, nil
}

func (l *LegoIssuer) directory() string {
	if l.DirectoryURL == "" {
		return LEDirectoryProduction
	}
	return l.DirectoryURL
}
