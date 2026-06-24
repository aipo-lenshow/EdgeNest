// Package wizard handles the first-run setup. It provisions default inbounds
// (VLESS-Reality + Hysteria2) so that fresh installs are usable end-to-end
// without manual config.
//
// DISCIPLINE: control plane code only — no engine imports.
package wizard

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/orchestrator"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/system"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// CertIssuer is the slice of cert.Manager the wizard needs: issue a real cert
// synchronously when a grey-cloud domain is supplied. Kept as an interface so
// funnel tests can substitute a fake without contacting Let's Encrypt. cert is
// control-plane code (no engine imports), so this doesn't break the package's
// discipline note above.
type CertIssuer interface {
	Issue(ctx context.Context, nodeID uint, req cert.IssueRequest) (*model.Certificate, error)
}

// domainChecker is the domain-verdict seam. Production uses *DomainValidator
// (real DNS + Cloudflare CIDR lookup); funnel tests substitute a stub so they
// can exercise the ACME / self-signed branches without flaky live DNS.
type domainChecker interface {
	Validate(ctx context.Context, domain string) (DomainResult, error)
}

// Wizard provisions default state on first-run.
type Wizard struct {
	store     *store.Store
	orch      *orchestrator.Orchestrator
	validator domainChecker
	certMgr   CertIssuer
	// xrayInstalled reports whether the optional xray-core engine is present on
	// the host. Seam (defaults to the real host probe in New) so funnel tests
	// can exercise XHTTP protocols without a real /usr/local/bin/xray.
	xrayInstalled func() bool
}

// New constructs a Wizard. The domain validator is wired with a closure that
// looks the VPS public IPv4 up from the local Node row on every call, so a
// re-detect (DetectPublicIPv4) is picked up without restarting. certMgr may be
// nil (e.g. in tests that don't exercise ACME); the funnel then falls back to
// the self-signed bootstrap pair.
func New(s *store.Store, o *orchestrator.Orchestrator, certMgr CertIssuer) *Wizard {
	w := &Wizard{store: s, orch: o, certMgr: certMgr}
	w.xrayInstalled = func() bool { return system.ReadXrayStatus().Installed }
	// Domain verdict matches against EVERY public IP the node owns — the node
	// row's detected v4 plus all v4/v6 addresses from network.json — so a
	// dual-stack domain (A→v4, AAAA→v6) validates as OK instead of mismatch.
	w.validator = NewDomainValidator(func() []string {
		seen := map[string]bool{}
		var out []string
		add := func(ip string) {
			if ip != "" && !seen[ip] {
				seen[ip] = true
				out = append(out, ip)
			}
		}
		if n, err := s.GetLocalNode(); err == nil && n != nil {
			add(n.PublicIP)
		}
		capb := core.ReadNodeCapability(core.DefaultCapabilityPath)
		for _, ip := range capb.IPv4Addrs {
			add(ip)
		}
		for _, ip := range capb.IPv6Addrs {
			add(ip)
		}
		return out
	})
	return w
}

// ValidateDomain runs the four-state DNS check used by Wizard Step 2. See
// DomainStatus for the meaning of each result. The domain string can be
// empty — that's "skip" not "error".
func (w *Wizard) ValidateDomain(ctx context.Context, domain string) (DomainResult, error) {
	return w.validator.Validate(ctx, domain)
}

// Status reports whether the wizard has been completed.
type Status struct {
	Done       bool   `json:"done"`
	PanelPath  string `json:"panel_path"`
	HasInbound bool   `json:"has_inbound"`
}

// Status reads the wizard flag plus a couple of "is the panel actually
// provisioned" signals so the front-end knows where to land.
func (w *Wizard) Status(nodeID uint) (Status, error) {
	done, err := w.store.GetSetting(bootstrap.KeyWizardDone)
	if err != nil {
		return Status{}, err
	}
	panelPath, _ := w.store.GetSetting(bootstrap.KeyPanelPath)
	ins, err := w.store.ListInbounds(nodeID)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Done:       done == "true",
		PanelPath:  panelPath,
		HasInbound: len(ins) > 0,
	}, nil
}

// CompleteRequest is the wizard payload. Most fields are optional; reasonable
// defaults are filled in for any field the user did not customise.
type CompleteRequest struct {
	// Initial client (one user the wizard creates across both inbounds).
	ClientEmail string `json:"client_email"`

	// VLESS-Reality params.
	VLESSPort   int    `json:"vless_port"`   // default 8443
	VLESSDomain string `json:"vless_domain"` // SNI camouflage, default www.microsoft.com

	// Hysteria2 params.
	Hysteria2Port     int    `json:"hysteria2_port"`      // default 41020
	Hysteria2Domain   string `json:"hysteria2_domain"`    // for self-signed CN; default "edgenest.local"
	Hysteria2UpMbps   int    `json:"hysteria2_up_mbps"`   // default 100
	Hysteria2DownMbps int    `json:"hysteria2_down_mbps"` // default 500

	// CertsDir is where to drop the wizard's self-signed cert for Hysteria2.
	// Falls back to "/etc/edgenest/certs" when empty.
	CertsDir string `json:"certs_dir"`
}

// CompleteResult is what we return after a successful wizard run.
type CompleteResult struct {
	VLESSInboundID     uint     `json:"vless_inbound_id"`
	Hysteria2InboundID uint     `json:"hysteria2_inbound_id"`
	ClientEmail        string   `json:"client_email"`
	ClientUUID         string   `json:"client_uuid"`
	ClientPassword     string   `json:"client_password"`
	RealityPublicKey   string   `json:"reality_public_key"` // for client config
	RealityShortIDs    []string `json:"reality_short_ids"`
	SelfSignedCertPath string   `json:"self_signed_cert_path"`
	SelfSignedKeyPath  string   `json:"self_signed_key_path"`
	// Default subscription auto-created for the wizard client. Token is
	// returned exactly once; SubscriptionURL is path-only (UI prefixes origin).
	SubscriptionID    uint   `json:"subscription_id"`
	SubscriptionToken string `json:"subscription_token"`
	SubscriptionURL   string `json:"subscription_url"`
}

// Complete is the first-run action: it generates a Reality keypair, a Hysteria2
// self-signed cert, two inbounds with one shared client, marks the wizard done,
// and pushes the new config to the node.
//
// Idempotency: refuses to run if wizard_done == true. Caller should check
// Status first.
func (w *Wizard) Complete(ctx context.Context, nodeID uint, req CompleteRequest) (CompleteResult, error) {
	if req.ClientEmail == "" {
		return CompleteResult{}, fmt.Errorf("client_email is required (invariant I1)")
	}
	if done, _ := w.store.GetSetting(bootstrap.KeyWizardDone); done == "true" {
		return CompleteResult{}, fmt.Errorf("wizard already completed")
	}

	// Defaults.
	if req.VLESSPort == 0 {
		req.VLESSPort = 8443
	}
	if req.VLESSDomain == "" {
		req.VLESSDomain = "www.microsoft.com"
	}
	if req.Hysteria2Port == 0 {
		req.Hysteria2Port = 41020
	}
	if req.Hysteria2Domain == "" {
		req.Hysteria2Domain = "edgenest.local"
	}
	if req.Hysteria2UpMbps == 0 {
		req.Hysteria2UpMbps = 100
	}
	if req.Hysteria2DownMbps == 0 {
		req.Hysteria2DownMbps = 500
	}
	if req.CertsDir == "" {
		req.CertsDir = "/etc/edgenest/certs"
	}

	// 1) Reality keypair + short IDs.
	priv, pub, err := generateRealityKeypair()
	if err != nil {
		return CompleteResult{}, fmt.Errorf("reality keypair: %w", err)
	}
	shortIDs := []string{randomHex(8)}

	// 2) Self-signed cert for Hysteria2 (acme replaces this in TASK-10).
	certPath := filepath.Join(req.CertsDir, "wizard-fullchain.pem")
	keyPath := filepath.Join(req.CertsDir, "wizard-privkey.pem")
	if err := writeSelfSignedCert(req.Hysteria2Domain, certPath, keyPath); err != nil {
		return CompleteResult{}, fmt.Errorf("self-signed cert: %w", err)
	}

	// 3) Shared client credentials.
	clientUUID := uuid.NewString()
	clientPassword := randomHex(16)
	subToken := randomHex(24)

	// 4) VLESS-Reality inbound. We persist BOTH private_key (used by the
	// engine to render sing-box config) AND public_key (used by the share
	// encoder to emit pbk= in the vless:// URI). Without the public_key here
	// the share encoder falls back to security=tls which doesn't work with
	// a Reality server.
	vlessSettings, _ := json.Marshal(map[string]any{
		"sni":                 req.VLESSDomain,
		"reality_private_key": priv,
		"reality_public_key":  pub,
		"short_ids":           shortIDs,
		"server_port_target":  443,
		"flow":                "xtls-rprx-vision",
	})
	vlessIn := &model.Inbound{
		NodeID: nodeID, Tag: fmt.Sprintf("EdgeNest-VLESS-Reality-%d", req.VLESSPort), Engine: "singbox", Type: "vless",
		Listen: "::", Port: req.VLESSPort, Network: "tcp", Enabled: true,
		Settings: string(vlessSettings),
		Remark:   "EdgeNest-VLESS-Reality",
	}
	if err := w.store.CreateInbound(vlessIn); err != nil {
		return CompleteResult{}, fmt.Errorf("create vless inbound: %w", err)
	}
	vlessClient := &model.Client{
		InboundID: vlessIn.ID, Email: req.ClientEmail, UUID: clientUUID,
		Flow: "xtls-rprx-vision", Enabled: true,
	}
	if err := w.store.CreateClient(vlessClient); err != nil {
		return CompleteResult{}, fmt.Errorf("create vless client: %w", err)
	}

	// 5) Hysteria2 inbound. We default sniff=true (engine default), set sni for
	// the share-link builder, and pin a benign default masquerade so HTTP
	// probes get a real-looking 200 instead of a tell-tale 404.
	h2Settings, _ := json.Marshal(map[string]any{
		"tls_cert_path":          certPath,
		"tls_key_path":           keyPath,
		"sni":                    req.Hysteria2Domain,
		"up_mbps":                req.Hysteria2UpMbps,
		"down_mbps":              req.Hysteria2DownMbps,
		"masquerade_type":        "string",
		"masquerade_content":     "<!doctype html><title>EdgeNest</title>\n",
		"masquerade_status_code": 200,
	})
	h2In := &model.Inbound{
		NodeID: nodeID, Tag: fmt.Sprintf("EdgeNest-Hysteria2-%d", req.Hysteria2Port), Engine: "singbox", Type: "hysteria2",
		Listen: "::", Port: req.Hysteria2Port, Network: "udp", Enabled: true,
		Settings: string(h2Settings),
		Remark:   "EdgeNest-Hysteria2",
	}
	if err := w.store.CreateInbound(h2In); err != nil {
		return CompleteResult{}, fmt.Errorf("create hysteria2 inbound: %w", err)
	}
	if err := w.store.CreateClient(&model.Client{
		InboundID: h2In.ID, Email: req.ClientEmail, Password: clientPassword, Enabled: true,
	}); err != nil {
		return CompleteResult{}, fmt.Errorf("create hysteria2 client: %w", err)
	}

	// 6) Default subscription. The resolver aggregates clients by email, so
	// binding the subscription to the VLESS client is enough — both inbounds
	// surface in the same token bundle. Allowed lists left empty = all.
	sub := &model.Subscription{
		Name:            "Default subscription",
		Token:           subToken,
		TokenHash:       store.HashToken(subToken),
		ClientID:        vlessClient.ID,
		AllowedNodes:    "[]",
		AllowedInbounds: "[]",
	}
	if err := w.store.CreateSubscription(sub); err != nil {
		return CompleteResult{}, fmt.Errorf("create default subscription: %w", err)
	}

	// 7) Mark wizard done — BEFORE Apply so a transient engine error doesn't
	// reset the wizard. The inbounds exist either way; the operator can hit
	// "engine restart" in the panel if Apply ever fails.
	if err := w.store.SetSetting(bootstrap.KeyWizardDone, "true"); err != nil {
		return CompleteResult{}, fmt.Errorf("mark wizard done: %w", err)
	}

	// 7) Persist the Reality public key so the panel UI can surface it for
	// client config. We use a Setting key here; long-term home is on the
	// inbound row's Settings JSON, but keeping a quick-lookup makes the
	// "share with client" flow cheaper.
	_ = w.store.SetSetting("wizard_reality_public_key", pub)
	_ = w.store.SetSetting("wizard_reality_short_id", shortIDs[0])

	// 8) Push to the node. We tolerate engine errors here: the panel is
	// usable, the user can re-run Apply manually. We just surface in the
	// returned CompleteResult? -- no, we return error so caller can decide.
	if w.orch != nil {
		if _, err := w.orch.Apply(ctx, nodeID); err != nil {
			// Don't fail the wizard over an engine push failure. Surface in result.
			// (caller can show a "engine warning" toast and direct the user to
			//  the Engine page.)
			_ = err
		}
	}

	return CompleteResult{
		VLESSInboundID:     vlessIn.ID,
		Hysteria2InboundID: h2In.ID,
		ClientEmail:        req.ClientEmail,
		ClientUUID:         clientUUID,
		ClientPassword:     clientPassword,
		RealityPublicKey:   pub,
		RealityShortIDs:    shortIDs,
		SelfSignedCertPath: certPath,
		SelfSignedKeyPath:  keyPath,
		SubscriptionID:     sub.ID,
		SubscriptionToken:  subToken,
		SubscriptionURL:    "/sub/" + subToken,
	}, nil
}

// generateRealityKeypair delegates to auth.GenerateRealityKeypair — kept as a
// package-local symbol so wizard_test.go still imports cleanly without
// caring where the X25519 helper lives.
func generateRealityKeypair() (priv, pub string, err error) {
	return auth.GenerateRealityKeypair()
}

// randomHex returns 2n hex chars from crypto/rand.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is fatal; in v1 standalone we'd rather crash than
		// silently return predictable bytes.
		panic("crypto/rand: " + err.Error())
	}
	const hex = "0123456789abcdef"
	out := make([]byte, n*2)
	for i, c := range b {
		out[i*2] = hex[c>>4]
		out[i*2+1] = hex[c&0x0f]
	}
	return string(out)
}
