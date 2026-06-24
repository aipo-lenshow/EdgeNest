package wizard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/system"
)

// hostFamily classifies a literal IP into "v4" or "v6" for the inbound-tag
// suffix + the per-family port-collision check. Returns "v4" for any input
// that fails to parse — single-stack ASCII tags stay backward compatible
// with the original "EdgeNest-<protocol>-<port>" shape, and the share
// resolver doesn't depend on the suffix.
// inboundArgoBound reports whether a stored inbound's settings JSON carries
// argo_bound=true. Used to enforce the one-Argo-inbound-per-node constraint.
func inboundArgoBound(settings string) bool {
	if settings == "" {
		return false
	}
	var s map[string]any
	if json.Unmarshal([]byte(settings), &s) != nil {
		return false
	}
	switch v := s["argo_bound"].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

func hostFamily(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed != nil && parsed.To4() == nil {
		return "v6"
	}
	return "v4"
}

// listenForHost decides what IP the inbound engine binds to. Four regimes:
//
//   - NAT'd VPS (Oracle Cloud, GCP, AWS with EIP): the operator-chosen host
//     is the upstream PUBLIC IP that lives on the gateway, NOT a local
//     interface. Binding to it fails "cannot assign requested address".
//     Detect by walking net.InterfaceAddrs() — host not local → wildcard,
//     NAT forwards the public-IP traffic in transparently. Wildcard family
//     follows the kernel's actual capability (see single-family note below).
//
//   - Single-family v4-only (v6 disabled): the operator-chosen host IS
//     local, but binding to "::" fails on a kernel where install.sh wrote
//     net.ipv6.conf.all.disable_ipv6=1 (v4-only nodes disable v6 to stop
//     dual-stack DNS hangs). Return "0.0.0.0"
//     so the inbound actually starts. The old "::" wildcard would silently
//     break every protocol's startup (SOCKS5 / Hy2 / VLESS all gone).
//
//   - Single-family v6-only: return "::" — the only available family.
//     install.sh writes Kasper public DNS64 to /etc/resolv.conf on this
//     branch so v4-only origins are reachable via NAT64.
//
//   - Dual-stack direct-attached: bind to the specific host so a v4 inbound
//     and a v6 inbound on the same port occupy distinct sockets (the only
//     scenario where specific binding is strictly necessary). The v4 socket
//     on `1.2.3.4:1080` and the v6 socket on `[2607::2]:1080` are
//     independent and the OS won't conflate them.
//
// host "" → "::" (caller didn't pick yet — shouldn't normally happen).
func listenForHost(host string) string {
	if host == "" {
		return "::"
	}
	target := net.ParseIP(host)
	if target == nil {
		return host
	}
	// Is host a local interface? Walk the kernel-visible address list:
	// disable_ipv6=1 strips v6 entries entirely, so hasV6Global reflects
	// what the kernel can actually bind, not what the network.json file
	// claims is available.
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		// Conservative fallback — if we can't tell, prefer the v6 wildcard
		// (works on every dual-stack / v6-only kernel) and let the caller's
		// retry on bind failure handle the v4-only-with-disabled-v6 edge.
		return "::"
	}
	local := false
	hasV4 := false
	hasV6Global := false
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipnet.IP.Equal(target) {
			local = true
		}
		if !ipnet.IP.IsLoopback() && !ipnet.IP.IsLinkLocalUnicast() {
			if ipnet.IP.To4() != nil {
				hasV4 = true
			} else if ipnet.IP.IsGlobalUnicast() {
				hasV6Global = true
			}
		}
	}
	// Pick the wildcard that matches the family the kernel can actually
	// bind. The NAT branch and the direct single-family branch share the
	// same logic: if there's no v6 on this kernel, "::" fails.
	wildcard := "::"
	if !hasV6Global {
		wildcard = "0.0.0.0"
	}
	if !local {
		return wildcard // NAT'd VPS — public IP lives on gateway, not iface
	}
	if hasV4 && hasV6Global {
		return host // dual-stack: specific bind unlocks v4+v6 same-port coexistence
	}
	return wildcard // single-family: wildcard matches the only available family
}

// FunnelRequest is the body for /api/v1/wizard/create-funnel. It carries the
// four pieces of state the 4-step inbound wizard collects: optional domain,
// client multi-select (used only as an autotuning hint — every inbound is
// available to every subscription regardless), per-protocol opt-ins, and a
// default client email.
type FunnelRequest struct {
	Domain      string        `json:"domain"`
	Clients     []string      `json:"clients"`
	Protocols   []FunnelProto `json:"protocols"`
	ClientEmail string        `json:"client_email"`
	// BundleName overrides the subscription's display name. Front-end emits the
	// localised mode tag ("快速套餐" / "完整套餐" / 场景名). Empty falls back to
	// a neutral "EdgeNest 套餐". The time-based label used to sit here but it
	// rendered in server UTC, so the displayed time looked off vs. the front-end's
	// "创建于" (which formats created_at in the user's local timezone) — moved
	// that responsibility to the localized created_at column instead.
	BundleName string `json:"bundle_name"`
	// Host is the literal IP this batch binds to and exposes in its subscription
	// URIs — picked by the wizard's HostChooser from NodeCapability.IPv4Addrs /
	// IPv6Addrs. All inbounds created in this batch share the same listen IP
	// (Quick / Scenario / Full all run "one wizard batch = one IP"
	// design directive). Different batches can use different IPs; a dual-stack
	// node runs the wizard twice (once per family). Empty falls through to the
	// capability default — first IPv4Addrs entry on dual-stack / single-stack.
	Host string `json:"host"`
	// CertsDir overrides where the wizard drops the Hy2 self-signed cert.
	// Defaults to /etc/edgenest/certs when empty.
	CertsDir string `json:"certs_dir"`
	// AcmeEmail is the ACME account contact used when the wizard issues a
	// real certificate for a grey-cloud ("ok") domain. Ignored on the
	// self-signed branches. Falls back to the stored acme_email setting.
	AcmeEmail string `json:"acme_email"`

	// AdvancedOverrides lets the wizard stash CDN / Argo configuration on the
	// /advanced row atomically with the inbound batch. Without this the
	// operator would have to jump out to the Advanced page, save the token,
	// then come back to the wizard — and risk losing the in-progress state.
	// Nil = no change to /advanced.
	AdvancedOverrides *AdvancedOverrides `json:"advanced_overrides,omitempty"`

	// PanelPort is filled by the handler from the running config so funnel
	// validation can refuse a port that would lock the operator out.
	PanelPort int `json:"-"`
}

// AdvancedOverrides matches the subset of /advanced fields the wizard can
// write inline. Set ArgoMode="temp" for the temporary tunnel (no token), or
// ArgoMode="named" with ArgoToken populated. CDN fields are optional — both
// blank means "use whatever's already configured".
type AdvancedOverrides struct {
	CDNPreferredIPs []string `json:"cdn_preferred_ips,omitempty"`
	ArgoMode        string   `json:"argo_mode,omitempty"`  // "temp" | "named"
	ArgoToken       string   `json:"argo_token,omitempty"` // only for "named"
	ArgoDomain      string   `json:"argo_domain,omitempty"`
}

// FunnelProto is one row from Step 4. id is the UI-level protocol identifier
// (matches PROTO_IDS in web/src/lib/protocolMeta.ts). cdn / argoNamed track
// the per-row accel toggles; both are no-ops for non-CDN-eligible protocols.
type FunnelProto struct {
	ID        string `json:"id"`
	CDN       bool   `json:"cdn"`
	ArgoNamed bool   `json:"argo_named"`
	Port      int    `json:"port"`
	// Hy2Obfs enables salamander obfs on this Hysteria2 inbound. Default OFF
	// (see Hy2Defaults branch comment); advanced users can opt in from the
	// wizard's protocol card. No effect on non-Hy2 protocols.
	Hy2Obfs bool `json:"hy2_obfs"`
	// Hy2PortHopStart / Hy2PortHopEnd enable Hysteria2 port hopping when both
	// are set and form a valid range. 0/0 = off. Written to the inbound
	// settings as port_hop_start / port_hop_end; the orchestrator turns them
	// into nat REDIRECT rules and the encoders into the URI range / server_ports.
	Hy2PortHopStart int `json:"hy2_port_hop_start"`
	Hy2PortHopEnd   int `json:"hy2_port_hop_end"`
}

// FunnelResult enumerates what was created. Each created inbound surfaces its
// backend type so the wizard front-end can render per-protocol UI (e.g. show
// the WS host).
type FunnelResult struct {
	Inbounds          []FunnelInbound `json:"inbounds"`
	ClientEmail       string          `json:"client_email"`
	SubscriptionID    uint            `json:"subscription_id"`
	SubscriptionToken string          `json:"subscription_token"`
	SubscriptionURL   string          `json:"subscription_url"`
	// Host is the literal IP the operator picked in Step1. The front-end
	// builds the absolute subscription URL as `http://<Host>:<PanelPort>/sub/<Token>`
	// so the host segment matches the family the operator chose (window.
	// location.origin would always echo the panel-access IP, which is wrong
	// when the operator picked a different family from the one they used to
	// log in to the panel).
	Host         string       `json:"host"`
	DomainStatus DomainStatus `json:"domain_status"`
	// CertMode reports how the batch's TLS protocols were certified:
	// "none" (no TLS-cert protocol in the batch), "self-signed" (bootstrap
	// pair, clients skip verification), or "acme" (real cert issued, strict
	// verification). CertError carries the human-readable reason when an
	// ACME attempt fell back to self-signed — the Result panel surfaces it
	// instead of failing the batch.
	CertMode   string `json:"cert_mode"`
	CertDomain string `json:"cert_domain,omitempty"`
	CertError  string `json:"cert_error,omitempty"`
}

type FunnelInbound struct {
	ID      uint   `json:"id"`
	UIType  string `json:"ui_type"` // UI-level proto id ("vless-reality" etc.)
	Backend string `json:"backend"` // backend Inbound.Type
	Port    int    `json:"port"`
	Tag     string `json:"tag"`
	Remark  string `json:"remark"`
}

// CreateFromFunnel runs the 4-step wizard's "Create" action. It is independent
// of the legacy Complete() flow — that one provisions a fixed pair of inbounds
// for first-run; this one is a repeatable "add more inbounds with sane
// per-protocol defaults" path.
//
// Auto-tuning rules baked in here (the "后端协议自动调优" task):
//   - VLESS-Reality:    server_addr = VPS IP regardless of domain state
//     (Reality is IP-direct; orange-cloud DNS is irrelevant).
//   - Hysteria2:        obfs OFF by default (salamander cross-impl decrypt
//     fails silently against Stash / Surge / Xray-core; see
//     the comment block on the Hy2Defaults branch below for
//     the upstream issue links).
//   - VMess/VLESS-WS+CDN:  ws_host = sni = domain; cdn_mode mirrors the toggle.
//   - VLESS-XHTTP-Reality: no domain; server_addr = VPS IP.
//   - VLESS-XHTTP-TLS+CDN: ws_host equivalent (xhttp_host) = domain.
//   - All TLS-cert protocols follow the unified certificate model: cert mode
//     tracks the domain verdict (no domain / mismatch → self-signed +
//     skip-cert-verify; grey-cloud "ok" → ACME real cert), never the protocol.
//   - All TLS-cert protocols on the "proxied" branch ride the same `cdn_mode`
//     toggle so the engine resolver knows to use the CDN preferred-IP pool.
func (w *Wizard) CreateFromFunnel(ctx context.Context, nodeID uint, req FunnelRequest) (FunnelResult, error) {
	if len(req.Protocols) == 0 {
		return FunnelResult{}, fmt.Errorf("at least one protocol must be selected")
	}
	if req.ClientEmail == "" {
		// Default user identity: sequential NNN@EdgeNest.Local (001, 002, …).
		if seq, err := w.store.NextSeqEmail(); err == nil && seq != "" {
			req.ClientEmail = seq
		} else {
			req.ClientEmail = "001@EdgeNest.Local"
		}
	}
	if req.CertsDir == "" {
		req.CertsDir = "/etc/edgenest/certs"
	}

	// Per-(family, protocol) uniqueness check — design directive:
	// each protocol type can only live on one IP per family. Multiple ports
	// on the same IP are fine (the (Listen, Port) composite index allows it).
	// Multiple IPs in the same family for the same protocol get rejected so
	// the wizard's "one wizard batch = one IP" model has a single canonical
	// home per protocol; users who want to spread protocols across IPs run
	// the wizard once per IP and pick the protocols that family doesn't have
	// yet. See [[edgenest-multi-ip-constraint]].
	existingForCheck, _ := w.store.ListInbounds(nodeID)
	batchFamily := hostFamily(req.Host)
	for _, p := range req.Protocols {
		meta, ok := uiProtoMeta[p.ID]
		if !ok {
			continue // unknown id — handled in the port pre-flight below
		}
		for _, ex := range existingForCheck {
			if ex.Type != meta.Backend {
				continue
			}
			if hostFamily(ex.Listen) != batchFamily {
				continue
			}
			if req.Host != "" && ex.Listen == req.Host {
				// Same protocol, same family, SAME IP: that's fine — the user
				// just wants another port on the same IP (the multi-instance
				// SOCKS5 case), the (Listen, Port) composite index takes care
				// of port uniqueness.
				continue
			}
			return FunnelResult{}, fmt.Errorf(
				"%s already exists in the %s family on a different IP (%s) — each protocol can only live on one IP per family; delete the existing one or pick a different protocol to spread across IPs",
				p.ID, batchFamily, ex.Listen)
		}
	}

	// Argo singleton constraint (1a): a node runs at most ONE cloudflared
	// tunnel (the supervisor is a singleton), and one quick/temp tunnel points
	// at exactly one loopback port = one origin = one protocol. So at most one
	// argo_bound inbound can ever carry traffic. Reject a second one up front
	// instead of letting the operator build an inbound that can never work —
	// the subscription would silently omit it. The matured community scripts
	// (yonggekkk/argosbx) make the same one-protocol-per-tunnel choice. See
	// [[edgenest-cdn-argo-ingress-design]].
	argoInBatch := 0
	for _, p := range req.Protocols {
		if meta, ok := uiProtoMeta[p.ID]; ok && p.ArgoNamed && meta.ArgoEligible {
			argoInBatch++
		}
	}
	if argoInBatch > 1 {
		return FunnelResult{}, fmt.Errorf("一条 Argo 隧道只能服务一个协议, 本次选了多个协议套 Argo。请只给一个 WebSocket 协议(推荐 VLESS-WS)选 Argo, 其余协议走直连/CDN")
	}
	if argoInBatch > 0 {
		for _, ex := range existingForCheck {
			if inboundArgoBound(ex.Settings) {
				return FunnelResult{}, fmt.Errorf("本节点已存在 Argo 入站(%s), 一条隧道只能服务一个协议。请先删除它或把它改为不走 Argo, 再新建", ex.Tag)
			}
		}
	}

	// Resolve the domain verdict up front — the unified certificate model
	// keys everything on it: cert mode (self-signed vs ACME), sni / ws_host
	// defaults, and the CDN port gate below. effDomain is non-empty only for
	// a domain that actually points somewhere usable (grey-cloud "ok" or
	// orange-cloud "proxied"); a mismatch / unresolved domain must not leak
	// into sni or Host headers — clients would send an SNI the cert and the
	// CDN both refuse.
	domainRes, _ := w.validator.Validate(ctx, req.Domain)
	effDomain := ""
	if domainRes.Status == DomainStatusOK || domainRes.Status == DomainStatusProxied {
		effDomain = req.Domain
	}

	// Port pre-flight — refuse the batch before we touch the DB if any
	// operator-supplied port would land us in an impossible spot. The wizard
	// front-end does the same checks, but a hand-rolled HTTP request can
	// bypass the UI; this is the authoritative gate.
	for _, p := range req.Protocols {
		meta, ok := uiProtoMeta[p.ID]
		if !ok {
			return FunnelResult{}, fmt.Errorf("unknown protocol id: %q", p.ID)
		}
		// Port == 0 means "let pickFreePort decide" — only validate operator-
		// supplied non-zero values here.
		if p.Port == 0 {
			continue
		}
		if p.Port < system.MinAllowedPort || p.Port > system.MaxAllowedPort {
			return FunnelResult{}, fmt.Errorf(
				"protocol %s: port %d out of allowed range %d-%d",
				p.ID, p.Port, system.MinAllowedPort, system.MaxAllowedPort)
		}
		if system.IsReserved(p.Port, req.PanelPort) {
			return FunnelResult{}, fmt.Errorf(
				"protocol %s: port %d is reserved (panel/SSH/DNS)", p.ID, p.Port)
		}
		// The Cloudflare port whitelist only matters when the row actually
		// rides the CDN. A bare-IP vmess-ws / vless-ws (CDN toggle off) is a
		// plain direct inbound and may sit on any allowed port.
		if p.CDN && meta.CDNEligible && !system.IsCFWhitelisted(p.Port) {
			return FunnelResult{}, fmt.Errorf(
				"protocol %s: port %d is not on the Cloudflare HTTPS whitelist %v — CDN will not proxy",
				p.ID, p.Port, system.CFHTTPSWhitelist)
		}
	}

	// Stash any current /advanced row before we overwrite it with the
	// wizard's overrides so that a rollback later in the batch can restore
	// the operator's previous state. The orchestrator notices the change on
	// the next Apply.
	var advancedBefore *model.AdvancedConfig
	if req.AdvancedOverrides != nil {
		cur, _ := w.store.GetAdvanced(nodeID)
		advancedBefore = cur
		next := mergeAdvancedOverrides(cur, nodeID, req.AdvancedOverrides)
		if err := w.store.UpsertAdvanced(next); err != nil {
			return FunnelResult{}, fmt.Errorf("apply advanced overrides: %w", err)
		}
	}

	// Reality keypair is shared across any Reality-using inbounds in this run,
	// so a single short_id pool covers them.
	priv, pub, err := generateRealityKeypair()
	if err != nil {
		return FunnelResult{}, fmt.Errorf("reality keypair: %w", err)
	}
	shortIDs := []string{randomHex(8)}

	vpsIP := ""
	if n, _ := w.store.GetLocalNode(); n != nil {
		vpsIP = n.PublicIP
	}

	clientUUID := uuid.NewString()
	clientPassword := randomHex(16)
	// SS-2022 PSK must be the base64 encoding of a 16-byte raw key for
	// `2022-blake3-aes-128-gcm`, not an arbitrary string — sing-box rejects
	// other lengths at startup and Shadowrocket / Stash decode AEAD with the
	// wrong key (silent fail, no error surfaced). Generate it here so the SS
	// branch below can pick it up; non-SS protocols keep the hex password.
	ssPassword := genSS2022Key128()
	// SOCKS5 needs an auth identifier that has no "@" (Shadowrocket / Surge
	// URI parsers see URL-encoded "%40" and mis-locate the host boundary, →
	// "no route"). Earlier this was solved by setting client.Email = socksUser,
	// but that broke subscription aggregation: resolver.ListEnabledClientsByEmail
	// uses the *first* client's email as the seed, so a SOCKS5 with a different
	// email got silently dropped from the bundle and never appeared in the
	// final Step-3 page or any client subscription.
	// New shape: keep client.Email = req.ClientEmail across all four protocols
	// (so resolver aggregates them) and stash the SOCKS5-specific username +
	// password in inbound settings. The encoder (uri.go) and the sing-box
	// renderer both read settings first and only fall back to client fields.
	socksUser := "socks-" + randomHex(3)

	result := FunnelResult{
		ClientEmail:  req.ClientEmail,
		DomainStatus: domainRes.Status,
		Host:         req.Host,
		CertMode:     "none", // upgraded to "self-signed" / "acme" by the cert branch below
	}

	// Issue a real ACME cert once for the whole batch when the operator gave a
	// usable domain and at least one TLS-cert protocol is selected. The cert is
	// keyed on effDomain, so all NeedsCert inbounds in this batch share it. On
	// failure we DON'T abort: the batch falls back to the self-signed bootstrap
	// pair and surfaces the reason in CertError, so the operator keeps a working
	// (if camouflage-less) setup and can retry issuance from the Certs page.
	// http-01 needs port 80 reachable; if it's busy/firewalled the error lands
	// here and we degrade gracefully.
	acmeCertPath, acmeKeyPath := "", ""
	if effDomain != "" && w.certMgr != nil {
		needsCertInBatch := false
		for _, p := range req.Protocols {
			// Argo-bound inbounds are plaintext WebSocket on loopback —
			// cloudflared speaks plain HTTP to them and Cloudflare's edge
			// supplies TLS to the client. They need no origin cert, so an
			// argo-only batch must not trigger ACME issuance (which would
			// fail with no domain, or waste a Let's Encrypt slot on an
			// unused cert).
			if m, ok := uiProtoMeta[p.ID]; ok && m.NeedsCert && !(p.ArgoNamed && m.ArgoEligible) {
				needsCertInBatch = true
				break
			}
		}
		if needsCertInBatch {
			// Reuse an existing valid cert for this domain instead of re-issuing.
			// Re-issuing burns Let's Encrypt's "5 duplicate certs/week" limit and,
			// when that 429s, drops the whole batch to self-signed even though a
			// perfectly good cert is already on disk. Reuse only when a row exists
			// for the domain, isn't within a day of expiry, and the cert file is
			// actually present (a stale DB row whose files were removed must still
			// re-issue).
			if certs, lerr := w.store.ListCertificates(nodeID); lerr == nil {
				cutoff := time.Now().Unix() + 24*3600
				for i := range certs {
					c := certs[i]
					if c.Domain != effDomain || c.CertPath == "" || c.ExpiresAt <= cutoff {
						continue
					}
					if _, statErr := os.Stat(c.CertPath); statErr != nil {
						continue
					}
					acmeCertPath = c.CertPath
					acmeKeyPath = c.KeyPath
					result.CertDomain = effDomain
					break
				}
			}

			if acmeCertPath == "" { // no reusable cert → issue one
				email := req.AcmeEmail
				if email == "" {
					email, _ = w.store.GetSetting("acme_email")
				}
				if email == "" {
					result.CertError = "no ACME contact email (set one in the wizard or the acme_email setting); fell back to self-signed"
				} else if crt, err := w.certMgr.Issue(ctx, nodeID, cert.IssueRequest{
					Domain:   effDomain,
					Email:    email,
					Mode:     "http-01",
					HTTPPort: 80,
				}); err != nil {
					result.CertError = fmt.Sprintf("ACME issuance failed (%v); fell back to self-signed", err)
				} else {
					acmeCertPath = crt.CertPath
					acmeKeyPath = crt.KeyPath
					result.CertDomain = effDomain
				}
			}
		}
	}
	// pickAClient lets the resolver aggregate the wizard's nodes under one
	// subscription. We bind the subscription to the first created client.
	var firstClientID uint

	// Build the "ports already taken on this node *for this family*" set so
	// the wizard can shift any wizard default that already collides on the
	// chosen IP (e.g. operator manually created an inbound on 8443 last week
	// on the same family). Per the design, the DB's (Listen, Port) composite
	// uniqueIndex lets a v4 inbound and a v6 inbound share the same port —
	// only same-family rows can collide. Also tracks ports allocated in
	// *this* batch so concurrent same-default protocols auto-spread instead
	// of failing at CreateInbound.
	existing, _ := w.store.ListInbounds(nodeID)
	usedPorts := make(map[int]bool, len(existing)+len(req.Protocols))
	for _, in := range existing {
		if hostFamily(in.Listen) == batchFamily {
			usedPorts[in.Port] = true
		}
	}

	// Track every inbound + client we create so we can roll the batch back if
	// any later step fails (a half-created wizard is much worse than a clean
	// error — the operator would have to manually hunt for orphans). The
	// rollback also restores /advanced if the wizard overwrote it, so a
	// failed Argo-named batch does not leave a stale token sitting around.
	var createdInbounds []uint
	var createdClients []uint
	rollback := func() {
		for _, cid := range createdClients {
			_ = w.store.DeleteClient(cid)
		}
		for _, iid := range createdInbounds {
			_ = w.store.DeleteInbound(iid)
		}
		if req.AdvancedOverrides != nil {
			if advancedBefore != nil {
				_ = w.store.UpsertAdvanced(advancedBefore)
			} else {
				_ = w.store.DeleteAdvanced(nodeID)
			}
		}
	}

	for _, p := range req.Protocols {
		meta, ok := uiProtoMeta[p.ID]
		if !ok {
			return result, fmt.Errorf("unknown protocol id: %q", p.ID)
		}
		preferred := p.Port
		if preferred == 0 {
			preferred = meta.DefaultPort
		}
		port := pickFreePort(preferred, usedPorts, req.PanelPort, req.Host, meta.Network)
		usedPorts[port] = true
		settings := map[string]any{}

		// Common defaults derived from the request. effDomain (not the raw
		// req.Domain) feeds sni / Host headers — a mismatch / unresolved
		// domain behaves exactly like the no-domain branch.
		domain := effDomain
		if domain == "" {
			domain = vpsIP
		}
		if meta.NeedsSNI {
			settings["sni"] = sniFor(p.ID, domain, vpsIP)
		}
		if meta.NeedsRealityKeys {
			settings["reality_private_key"] = priv
			settings["reality_public_key"] = pub
			settings["short_ids"] = shortIDs
			settings["server_port_target"] = 443
			settings["flow"] = "xtls-rprx-vision"
		}
		if meta.HasWS {
			settings["ws_path"] = "/" + randomHex(4)
			if effDomain != "" {
				settings["ws_host"] = effDomain
			}
		}
		if meta.HasXHTTP {
			settings["xhttp_path"] = "/" + randomHex(4)
			if effDomain != "" {
				settings["xhttp_host"] = effDomain
			}
			if meta.Security != "" {
				settings["security"] = meta.Security
			}
		}
		if meta.Hy2Defaults {
			settings["up_mbps"] = 100
			settings["down_mbps"] = 500
			if p.Hy2Obfs {
				// Advanced user explicitly opted in via the wizard toggle.
				// Generate the obfs password; clients (Mihomo / SFI / SFA /
				// Shadowrocket / Hiddify) work, but Stash / Surge / Karing
				// will silently drop the node — the wizard UI carries a red
				// warning to that effect when the toggle is on.
				settings["obfs"] = "salamander"
				settings["obfs_password"] = randomHex(8)
			}
			// salamander obfs is intentionally OFF by default. Cross-impl
			// compatibility between sing-box server and several mainstream
			// clients (Stash, Surge, Xray-core, v2rayN) is broken — the
			// server-side obfs decrypt fails silently with no log entry
			// even at debug level, and the client just sees infinite QUIC
			// Initial retries. Public reports of the same symptom:
			//   Xray-core   https://github.com/XTLS/Xray-core/issues/5712
			//   sing-box    https://github.com/SagerNet/sing-box/issues/3422
			//   v2rayN      https://github.com/2dust/v2rayN/issues/8803
			//   Surge       https://community.nssurge.com/d/3854
			// Hysteria upstream (v2.hysteria.network) and mainstream
			// one-click scripts (fscarmen/sing-box, etc.) all ship Hy2
			// without obfs by default. Advanced users who specifically
			// want active-probing resistance can opt in via the inbound
			// edit page after creation.
			settings["masquerade_type"] = "string"
			settings["masquerade_content"] = "<!doctype html><title>EdgeNest</title>\n"
			settings["masquerade_status_code"] = 200
			// Port hopping (opt-in): persist the range so the orchestrator can
			// emit nat REDIRECT rules and the share encoders the URI range /
			// server_ports. Only when both ends form a valid ascending range.
			if p.Hy2PortHopStart > 0 && p.Hy2PortHopEnd >= p.Hy2PortHopStart {
				settings["port_hop_start"] = p.Hy2PortHopStart
				settings["port_hop_end"] = p.Hy2PortHopEnd
			}
		}
		// Argo binding makes this a plaintext-WebSocket origin on loopback:
		// cloudflared connects with plain HTTP and Cloudflare terminates TLS
		// at its edge, so the origin carries NO certificate and Argo is
		// mutually exclusive with the CDN preferred-IP path (which needs a
		// real TLS origin). The wizard step constrains argo to ws protocols.
		isArgo := p.ArgoNamed && meta.ArgoEligible
		if meta.NeedsCert && !isArgo {
			// Unified certificate model: cert mode follows the domain verdict,
			// not the protocol. Every TLS protocol (Trojan / WS+TLS / XHTTP-TLS
			// / Hy2 / TUIC / AnyTLS) runs fine on the bootstrap self-signed
			// pair with skip-cert-verify on the client side — the operator
			// trades camouflage for zero-domain setup, same deal for all of
			// them. A grey-cloud domain upgrades the whole batch to an ACME
			// cert (issued synchronously above; acme_managed flips the encoders
			// to strict verification). The path is stable so the engine config
			// upgrades transparently when the cert is replaced.
			if acmeCertPath != "" {
				settings["tls_cert_path"] = acmeCertPath
				settings["tls_key_path"] = acmeKeyPath
				settings["acme_managed"] = "true" // encoders drop insecure/skip-verify
				result.CertMode = "acme"
			} else {
				certPath := filepath.Join(req.CertsDir, "wizard-fullchain.pem")
				keyPath := filepath.Join(req.CertsDir, "wizard-privkey.pem")
				settings["tls_cert_path"] = certPath
				settings["tls_key_path"] = keyPath
				settings["self_signed"] = "true"
				result.CertMode = "self-signed"
			}
		}
		if p.CDN && meta.CDNEligible && !isArgo {
			settings["cdn_mode"] = "true"
		}
		if isArgo {
			settings["argo_bound"] = "true"
		}
		if meta.SSDefaultMethod != "" {
			settings["method"] = meta.SSDefaultMethod
		}
		if p.ID == "socks5" {
			// SOCKS5 keeps client.Email = req.ClientEmail so resolver aggregates
			// the bundle, but the actual auth username goes here (URI-safe ASCII,
			// no "@"). Both the encoder and the sing-box renderer read these
			// fields first and only fall back to client.{Email,Password}.
			settings["socks_user"] = socksUser
			settings["socks_password"] = clientPassword
		}

		settingsBytes, _ := json.Marshal(settings)
		// Family suffix in the tag — lets a dual-stack node host the same
		// protocol+port on a v4 IP and a v6 IP without colliding on the
		// sing-box / xray tag uniqueness constraint.
		family := hostFamily(req.Host)
		tag := fmt.Sprintf("%s-%s-%d", meta.Remark, family, port)
		listen := listenForHost(req.Host)
		subHost := req.Host
		if isArgo {
			// Argo pins the inbound to loopback (the cloudflared process is the
			// only thing that should reach it). The URI host comes from the
			// Argo tunnel domain via the share resolver, so leave
			// SubscriptionHost empty to let the resolver chain pick it up.
			listen = "127.0.0.1"
			subHost = ""
		}
		in := &model.Inbound{
			NodeID: nodeID, Tag: tag, Engine: meta.Engine, Type: meta.Backend,
			Listen: listen, Port: port, Network: meta.Network, Enabled: true,
			Settings:         string(settingsBytes),
			Remark:           meta.Remark,
			SubscriptionHost: subHost,
		}
		// XHTTP inbounds run on the optional xray-core engine, which is opt-in at
		// install time. Refuse here with an actionable error rather than persist
		// an inbound the engine can never serve — otherwise the panel shows a
		// dead, non-listening inbound (engine Apply fails silently downstream).
		if meta.Engine == "xray" && !w.xrayInstalled() {
			rollback()
			return FunnelResult{}, fmt.Errorf("%s 需要 xray-core 引擎, 但本节点尚未安装 — 请先在「总览 → 系统信息」中一键安装后再创建", meta.Remark)
		}
		if err := w.store.CreateInbound(in); err != nil {
			rollback()
			return FunnelResult{}, fmt.Errorf("create %s inbound on port %d: %w", p.ID, port, err)
		}
		createdInbounds = append(createdInbounds, in.ID)
		client := &model.Client{
			InboundID: in.ID, Email: req.ClientEmail, Enabled: true,
		}
		if meta.NeedsUUID {
			client.UUID = clientUUID
		}
		if meta.NeedsPassword {
			// SS-2022 needs the base64-encoded raw key; every other
			// password-bearing protocol takes the shared hex password.
			if p.ID == "shadowsocks-2022" {
				client.Password = ssPassword
			} else {
				client.Password = clientPassword
			}
		}
		if meta.ClientFlow != "" {
			client.Flow = meta.ClientFlow
		}
		if err := w.store.CreateClient(client); err != nil {
			rollback()
			return FunnelResult{}, fmt.Errorf("create %s client: %w", p.ID, err)
		}
		createdClients = append(createdClients, client.ID)
		if firstClientID == 0 {
			firstClientID = client.ID
		}
		result.Inbounds = append(result.Inbounds, FunnelInbound{
			ID: in.ID, UIType: p.ID, Backend: meta.Backend,
			Port: port, Tag: tag, Remark: meta.Remark,
		})
	}

	subToken := randomHex(24)
	// Pack the batch's inbound IDs (not tags) into AllowedInbounds so renames
	// of an inbound's tag/remark don't break the subscription. The resolver
	// (decodeAllowedInbounds) still accepts the legacy tag-string shape for
	// pre-migration rows.
	inboundIDs := make([]uint, 0, len(result.Inbounds))
	for _, fi := range result.Inbounds {
		inboundIDs = append(inboundIDs, fi.ID)
	}
	allowedInboundsJSON, _ := json.Marshal(inboundIDs)
	// Name: prefer the front-end-supplied BundleName (already localised + tagged
	// with the wizard mode — "EdgeNest 快速套餐" / "EdgeNest 完整套餐" / "EdgeNest
	// 场景-<name>"). Append a 4-hex suffix so the same mode created twice doesn't
	// look identical in the list (created_at column already disambiguates by
	// time, in the user's local TZ).
	bundleName := req.BundleName
	if bundleName == "" {
		bundleName = "EdgeNest 套餐"
	}
	bundleName = fmt.Sprintf("%s · %s", bundleName, randomHex(2))
	sub := &model.Subscription{
		Name:            bundleName,
		Token:           subToken,
		TokenHash:       store.HashToken(subToken),
		ClientID:        firstClientID,
		AllowedNodes:    "[]",
		AllowedInbounds: string(allowedInboundsJSON),
	}
	if err := w.store.CreateSubscription(sub); err != nil {
		rollback()
		return FunnelResult{}, fmt.Errorf("create subscription: %w", err)
	}
	result.SubscriptionID = sub.ID
	result.SubscriptionToken = subToken
	result.SubscriptionURL = "/sub/" + subToken

	// Persist the Reality keypair public for quick lookup by the panel UI.
	if pub != "" {
		_ = w.store.SetSetting("wizard_reality_public_key", pub)
		_ = w.store.SetSetting("wizard_reality_short_id", shortIDs[0])
	}

	if w.orch != nil {
		_, _ = w.orch.Apply(ctx, nodeID)
	}
	return result, nil
}

// uiProtoMetaEntry is the backend twin of web/src/lib/protocolMeta.ts —
// kept in sync by hand. The frontend mapping decides which UI controls show
// up; this table decides how the backend turns the UI-level id into engine
// settings.
type uiProtoMetaEntry struct {
	Backend          string
	Engine           string
	Network          string
	DefaultPort      int
	Remark           string
	NeedsUUID        bool
	NeedsPassword    bool
	ClientFlow       string
	NeedsSNI         bool
	NeedsRealityKeys bool
	HasWS            bool
	HasXHTTP         bool
	Security         string // "reality" | "tls"  (XHTTP only)
	Hy2Defaults      bool
	NeedsCert        bool
	CDNEligible      bool
	ArgoEligible     bool
	SSDefaultMethod  string
}

// Default ports are mutually unique across the 11 protocols. Inbound.Port
// has a global uniqueIndex on the DB, so if two wizard protocols shared the
// same default the second CreateInbound call would 400 and tear the wizard
// down mid-batch. When a wizard run hits a collision (e.g. the operator
// already created an inbound on 8443 manually), pickFreePort walks +1 until
// an unused port is found.
var uiProtoMeta = map[string]uiProtoMetaEntry{
	"vless-reality": {
		Backend: "vless", Engine: "singbox", Network: "tcp",
		DefaultPort: 8443, Remark: "EdgeNest-VLESS-Reality",
		NeedsUUID: true, ClientFlow: "xtls-rprx-vision",
		NeedsSNI: true, NeedsRealityKeys: true,
	},
	"hysteria2": {
		Backend: "hysteria2", Engine: "singbox", Network: "udp",
		DefaultPort: 41020, Remark: "EdgeNest-Hysteria2",
		NeedsPassword: true, Hy2Defaults: true,
		NeedsSNI: true, NeedsCert: true,
	},
	"shadowsocks-2022": {
		Backend: "shadowsocks", Engine: "singbox", Network: "tcp",
		// Avoid the "SS" substring because Shadowrocket's flag heuristic treats
		// ISO 3166-1 alpha-2 codes inside the remark as a country hint, and
		// "SS" maps to South Sudan — every Shadowsocks-2022 node would show a
		// South Sudan flag regardless of where the VPS lived.
		DefaultPort: 8388, Remark: "EdgeNest-Shadowsocks2022",
		NeedsPassword:   true,
		SSDefaultMethod: "2022-blake3-aes-128-gcm",
	},
	"vmess-ws-cdn": {
		// 2053 / 2083 / 2096 are inside Cloudflare's free-tier HTTPS port
		// whitelist (443, 2053, 2083, 2087, 2096, 8443). v0.05/v0.06 shipped
		// 8080/8081/8448 for these protocols — outside the whitelist, so the
		// CDN never actually proxied them. Wizard v0.07 corrects to whitelist
		// defaults; 2087 is reserved for the panel, 8443 for vless-reality,
		// 443 left for the operator to opt into (see edgenest-port-rules).
		Backend: "vmess-ws", Engine: "singbox", Network: "tcp",
		DefaultPort: 2053, Remark: "EdgeNest-VMess-WS",
		NeedsUUID: true, HasWS: true,
		NeedsCert: true,
		NeedsSNI:  true, CDNEligible: true, ArgoEligible: true,
	},
	"trojan-tls": {
		Backend: "trojan", Engine: "singbox", Network: "tcp",
		DefaultPort: 8444, Remark: "EdgeNest-Trojan",
		NeedsPassword: true,
		NeedsSNI:      true, NeedsCert: true,
	},
	"vless-ws-cdn": {
		Backend: "vless-ws", Engine: "singbox", Network: "tcp",
		DefaultPort: 2083, Remark: "EdgeNest-VLESS-WS",
		NeedsUUID: true, HasWS: true,
		NeedsSNI: true, NeedsCert: true,
		CDNEligible: true, ArgoEligible: true,
	},
	"vless-xhttp-reality": {
		// XHTTP transport is xray-core native; sing-box has no renderer for
		// `vless-xhttp`. Routing it to sing-box would make every Apply fail
		// with "unsupported sing-box inbound type" and tear the wizard run
		// down before any inbound persists.
		Backend: "vless-xhttp", Engine: "xray", Network: "tcp",
		DefaultPort: 8447, Remark: "EdgeNest-VLESS-XHTTP-Reality",
		NeedsUUID: true, HasXHTTP: true, Security: "reality",
		NeedsSNI: true, NeedsRealityKeys: true,
	},
	"vless-xhttp-tls-cdn": {
		Backend: "vless-xhttp", Engine: "xray", Network: "tcp",
		DefaultPort: 2096, Remark: "EdgeNest-VLESS-XHTTP-TLS",
		NeedsUUID: true, HasXHTTP: true, Security: "tls",
		NeedsSNI: true, NeedsCert: true,
		CDNEligible: true, ArgoEligible: true,
	},
	"tuic-v5": {
		Backend: "tuic", Engine: "singbox", Network: "udp",
		DefaultPort: 50000, Remark: "EdgeNest-TUIC",
		NeedsUUID: true, NeedsPassword: true,
		NeedsSNI: true, NeedsCert: true,
	},
	"anytls": {
		Backend: "anytls", Engine: "singbox", Network: "tcp",
		DefaultPort: 8445, Remark: "EdgeNest-AnyTLS",
		NeedsPassword: true,
		NeedsSNI:      true, NeedsCert: true,
	},
	"socks5": {
		// SOCKS5 needs auth on a public VPS — an anonymous SOCKS5 on the open
		// internet is a free abuse-relay for anyone scanning port 1080. The
		// wizard now generates a random password and emits user:pass auth in
		// the URI; users who explicitly want unauthenticated LAN-only SOCKS5
		// can flip the bit on the inbound edit page after creation.
		Backend: "socks", Engine: "singbox", Network: "tcp",
		DefaultPort: 1080, Remark: "EdgeNest-SOCKS5",
		NeedsPassword: true,
	},
}

// genSS2022Key128 returns a fresh base64-encoded 16-byte PSK suitable for the
// `2022-blake3-aes-128-gcm` cipher. sing-box server reject passwords that do
// not decode to exactly 16 bytes; Shadowrocket / Stash / Mihomo expect the
// same encoding to derive the AEAD key. Using anything else (a hex string, a
// 20-char nonce) silently breaks all four clients.
func genSS2022Key128() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	// MUST be StdEncoding (with `=` padding) — sing-box 1.13 rejects
	// unpadded PSKs with `decode psk: illegal base64 data at input byte 20`,
	// which makes the whole sing-box.json fail atomic check and drops every
	// other inbound in the bundle with it (not just SS). The `==` tail
	// gets URL-encoded to `%3D%3D` in the SIP022 URL, which mainstream
	// clients (Shadowrocket / sing-box / Stash) parse fine; do not "fix"
	// it without re-running `sing-box check` against the produced config.
	return base64.StdEncoding.EncodeToString(b)
}

// pickFreePort returns a port that passes all three layers of conflict
// checks, walking upward from `preferred`. design directive: don't
// settle for the first "+1" — verify every candidate against
//
//  1. DB layer:        `used` map (same-family inbounds already assigned)
//  2. Reserved layer:  panel port, SSH (22), DNS (53), CDN whitelist ports
//     reserved for CDN-eligible protocols only
//  3. OS layer:        net.Listen on (host, port) — catches occupiers
//     outside EdgeNest (apt, monitoring, another tenant
//     on the same VPS) that the DB doesn't know about.
//
// Each "+1" re-runs the full chain; we don't blindly trust that the next
// integer is free just because the previous one wasn't (the OS check is
// especially worth re-running — system services cluster around well-known
// ports). Bounded by 1000 attempts so a fully saturated host surfaces a
// clean error instead of looping for hours.
//
// host is the literal IP the inbound will listen on ("" → fall back to
// wildcard probe). network is "tcp" / "udp" / "both" to match the OS probe
// to the actual socket the renderer will open.
func pickFreePort(preferred int, used map[int]bool, panelPort int, host, network string) int {
	port := preferred
	for i := 0; i < 1000; i++ {
		if port < system.MinAllowedPort || port > system.MaxAllowedPort {
			return preferred // out of range — surface as the original error
		}
		if used[port] {
			port++
			continue
		}
		if system.IsReserved(port, panelPort) {
			port++
			continue
		}
		if isPortBusyOnHost(host, port, network) {
			port++
			continue
		}
		return port
	}
	return preferred
}

// isPortBusyOnHost probes the OS to see if a (host, port) pair can be bound
// for both TCP and UDP (or whichever protocol matches `network`). Listens
// momentarily then closes, so the actual inbound's eventual Bind doesn't
// inherit any state. host "" or "::" probes the wildcard.
func isPortBusyOnHost(host string, port int, network string) bool {
	bindAddr := host
	if bindAddr == "" {
		bindAddr = "::"
	}
	// IPv6 literals need brackets in net.Listen syntax.
	if ip := net.ParseIP(bindAddr); ip != nil && ip.To4() == nil {
		bindAddr = "[" + bindAddr + "]"
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	probe := func(proto string) bool {
		if proto == "tcp" {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return true
			}
			_ = ln.Close()
			return false
		}
		// udp
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return true
		}
		_ = pc.Close()
		return false
	}
	switch network {
	case "udp":
		return probe("udp")
	case "tcp":
		return probe("tcp")
	default: // "both" / empty — be conservative, fail if either is busy
		return probe("tcp") || probe("udp")
	}
}

// mergeAdvancedOverrides folds the wizard's AdvancedOverrides into the
// existing /advanced row (or a new one if the operator hasn't configured
// anything yet). Fields the wizard did not set on the override struct pass
// through from `cur` so the panel never loses a value the operator had
// already saved on another page.
//
// CDN: presence of CDNPreferredIPs in the override flips cdn_enabled on
// (cdn_mode setting still lives per-inbound).
// Argo: ArgoMode="temp"|"fixed" flips argo_enabled + writes the matching
// fields; empty ArgoMode means "don't touch Argo settings".
func mergeAdvancedOverrides(cur *model.AdvancedConfig, nodeID uint, ov *AdvancedOverrides) *model.AdvancedConfig {
	next := &model.AdvancedConfig{NodeID: nodeID}
	if cur != nil {
		*next = *cur
		next.NodeID = nodeID
	}
	if len(ov.CDNPreferredIPs) > 0 {
		raw, _ := json.Marshal(ov.CDNPreferredIPs)
		next.CDNPreferredIPs = string(raw)
		next.CDNEnabled = true
	}
	switch ov.ArgoMode {
	case "temp":
		next.ArgoEnabled = true
		next.ArgoMode = "temp"
		// Temporary tunnels do not consume a token or a domain.
		next.ArgoToken = ""
		next.ArgoDomain = ""
	case "fixed":
		next.ArgoEnabled = true
		next.ArgoMode = "fixed"
		if ov.ArgoToken != "" {
			next.ArgoToken = ov.ArgoToken
		}
		if ov.ArgoDomain != "" {
			next.ArgoDomain = ov.ArgoDomain
		}
	}
	return next
}

// sniFor centralises the "what does this protocol's SNI default to?" rule.
// VLESS-Reality + VLESS-XHTTP-Reality use a benign-looking SNI camouflage
// regardless of whether the operator has a real domain — that is the point
// of Reality. Everything else uses the operator's domain (or VPS IP for the
// no-domain branch, where the wizard falls back to a self-signed cert with
// the IP in the SAN field).
func sniFor(protoID, domain, vpsIP string) string {
	switch protoID {
	case "vless-reality", "vless-xhttp-reality":
		return "www.microsoft.com"
	}
	if domain != "" {
		return domain
	}
	if vpsIP != "" {
		return vpsIP
	}
	return "edgenest.local"
}
