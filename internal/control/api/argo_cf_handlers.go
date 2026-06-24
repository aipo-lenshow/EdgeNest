package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/cfapi"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// Setting keys for the CF-API tunnel flow. The cert manager already persists a
// Cloudflare DNS token under dns_cloudflare_api_token (DNS-01 issuance); the
// "reuse cert token" option reads that. A token pasted just for tunnels is kept
// separately so the two never clobber each other.
const (
	settingDNSCloudflareToken = "dns_cloudflare_api_token"
	settingArgoCFToken        = "argo_cf_api_token"
)

// cfCode maps a Cloudflare API error to the response code: an auth/permission
// failure (token lacks scope) becomes CF_TOKEN_SCOPE — the common case when the
// operator reuses the DNS-01 cert token, which only carries Zone:DNS:Edit and
// can't create account-level tunnels. Everything else keeps the call-site code.
func cfCode(err error, fallback string) string {
	if cfapi.IsAuthError(err) {
		return "CF_TOKEN_SCOPE"
	}
	return fallback
}

// resolveCFToken picks the Cloudflare API token for a tunnel call: an explicit
// token in the request wins (and is remembered for next time); otherwise
// reuse_cert_token pulls the DNS-01 token; otherwise the previously-saved
// tunnel token. Returns "" when nothing is available.
func (h *Handler) resolveCFToken(provided string, reuseCert bool) string {
	if t := strings.TrimSpace(provided); t != "" {
		_ = h.store.SetSetting(settingArgoCFToken, t)
		return t
	}
	if reuseCert {
		if t, _ := h.store.GetSetting(settingDNSCloudflareToken); t != "" {
			return t
		}
	}
	t, _ := h.store.GetSetting(settingArgoCFToken)
	return t
}

// cdnInboundDomains returns the set of domains used by CDN-fronted inbounds
// (settings.cdn_mode == "true"), lowercased. The tunnel picker hides any tunnel
// already serving one of these so the operator can't reuse a tunnel that would
// collide with a CDN hostname.
func (h *Handler) cdnInboundDomains(nodeID uint) map[string]bool {
	out := map[string]bool{}
	ibs, err := h.store.ListInbounds(nodeID)
	if err != nil {
		return out
	}
	for _, ib := range ibs {
		var s map[string]any
		if json.Unmarshal([]byte(ib.Settings), &s) != nil {
			continue
		}
		if fmt.Sprint(s["cdn_mode"]) != "true" {
			continue
		}
		for _, k := range []string{"sni", "ws_host", "xhttp_host"} {
			if v, ok := s[k].(string); ok && v != "" {
				out[strings.ToLower(strings.TrimSpace(v))] = true
			}
		}
	}
	return out
}

// tunnelHitsCDN reports whether a tunnel serves any domain claimed for CDN.
// Short-circuits (no API call) when there are no CDN domains; on a lookup error
// it returns false (can't confirm a collision → don't hide the tunnel).
func (h *Handler) tunnelHitsCDN(ctx context.Context, cf *cfapi.Client, accountID, tunnelID string, cdnDoms map[string]bool) bool {
	if len(cdnDoms) == 0 {
		return false
	}
	hosts, err := cf.TunnelHostnames(ctx, accountID, tunnelID)
	if err != nil {
		return false
	}
	for _, hn := range hosts {
		if cdnDoms[hn] {
			return true
		}
	}
	return false
}

// argoRecordConflictMsg returns a non-empty Chinese error message when the domain
// already has an A/AAAA DNS record (orange-cloud CDN front or grey-cloud direct
// origin) — which conflicts with using it as an Argo tunnel hostname. A tunnel
// needs a CNAME → <id>.cfargotunnel.com; an existing A/AAAA would be overwritten
// (breaking CDN/direct) or leave the tunnel hostname unreachable. An existing
// CNAME (e.g. re-provisioning the same tunnel) is fine and not flagged.
func argoRecordConflictMsg(recs []cfapi.Record, domain string) string {
	for _, r := range recs {
		if r.Type == "A" || r.Type == "AAAA" {
			kind := "直连"
			if r.Proxied {
				kind = "橙云代理(CDN 前置/直连)"
			}
			return fmt.Sprintf(
				"域名 %s 已存在 %s 记录(%s → %s)。Argo 隧道需要把该域名设为指向隧道的 CNAME,"+
					"与现有 A/AAAA 记录冲突,无法用作隧道域名。"+
					"请改用一个专用子域名(例如 argo.<你的域名>),不要和 CDN 前置/直连共用同一个域名。",
				domain, r.Type, kind, r.Content)
		}
	}
	return ""
}

// argoDomainConflict performs a best-effort orange-cloud check for the manual
// fixed-mode save path (PutAdvancedArgo), which — unlike the API-token provision
// flow — has no verified CF token in hand. It reuses whatever CF API token is
// stored (DNS-01 cert token or a previously-saved tunnel-flow token). Returns a
// conflict message, or "" when there's no conflict OR we can't check (no token /
// zone not found / lookup failed) — we never block a save just because the check
// was unavailable; the provision flow remains the primary guard.
func (h *Handler) argoDomainConflict(ctx context.Context, domain string) string {
	token := h.resolveCFToken("", true)
	if token == "" {
		return ""
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cf := cfapi.New(token)
	zone, err := cf.ZoneForDomain(cctx, domain)
	if err != nil {
		return ""
	}
	recs, err := cf.LookupRecords(cctx, zone.ID, domain)
	if err != nil {
		return ""
	}
	return argoRecordConflictMsg(recs, domain)
}

type argoCFListReq struct {
	Token          string `json:"token"`
	ReuseCertToken bool   `json:"reuse_cert_token"`
	Domain         string `json:"domain"`
}

// ArgoCFListTunnels verifies the token, resolves the domain's zone/account, and
// returns the account's existing tunnels so the UI can offer "reuse an existing
// tunnel" alongside "create a new one".
//
// POST /api/v1/argo/cf/tunnels
func (h *Handler) ArgoCFListTunnels(c *gin.Context) {
	var body argoCFListReq
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	token := h.resolveCFToken(body.Token, body.ReuseCertToken)
	if token == "" {
		core.Fail(c, http.StatusBadRequest, "NO_CF_TOKEN",
			"提供 Cloudflare API token,或勾选复用证书 token(需 DNS-01 已配置 Cloudflare)")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(body.Domain))
	if domain == "" {
		core.Fail(c, http.StatusBadRequest, "NO_DOMAIN", "域名必填")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()

	cf := cfapi.New(token)
	if err := cf.VerifyToken(ctx); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_CF_TOKEN", err.Error())
		return
	}
	zone, err := cf.ZoneForDomain(ctx, domain)
	if err != nil {
		core.Fail(c, http.StatusBadRequest, cfCode(err, "NO_ZONE"), err.Error())
		return
	}
	tunnels, err := cf.ListTunnels(ctx, zone.Account.ID)
	if err != nil {
		core.Fail(c, http.StatusBadGateway, cfCode(err, "CF_ERROR"), err.Error())
		return
	}
	// Show ALL tunnels (any status), but hide ones already serving a domain the
	// operator uses for CDN — reusing such a tunnel would collide (a hostname
	// can't be both a CDN A-record and a tunnel CNAME). The per-tunnel ingress
	// lookup only runs when there ARE CDN domains to collide with.
	cdnDoms := h.cdnInboundDomains(h.parseLocalNodeID())
	out := make([]gin.H, 0, len(tunnels))
	for _, t := range tunnels {
		if h.tunnelHitsCDN(ctx, cf, zone.Account.ID, t.ID, cdnDoms) {
			continue
		}
		out = append(out, gin.H{"id": t.ID, "name": t.Name, "status": t.Status})
	}
	core.OK(c, gin.H{
		"zone":         zone.Name,
		"account_name": zone.Account.Name,
		"tunnels":      out,
	})
}

type argoCFProbeReq struct {
	Token          string `json:"token"`
	ReuseCertToken bool   `json:"reuse_cert_token"`
}

// ArgoCFProbe is the entry point of the API-token tunnel path: it verifies the
// token, lists the zones it can read (the domains the operator can hang a tunnel
// under), and probes whether the token ALSO has account-level tunnel permission.
// The probe is a read of the account's tunnels — a DNS-only token (the reused
// DNS-01 cert token) reads its zones fine but gets an auth error here, so the UI
// can tell the operator up front to widen the token instead of failing only at
// "create tunnel" time.
//
// POST /api/v1/argo/cf/probe
func (h *Handler) ArgoCFProbe(c *gin.Context) {
	var body argoCFProbeReq
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	token := h.resolveCFToken(body.Token, body.ReuseCertToken)
	if token == "" {
		core.Fail(c, http.StatusBadRequest, "NO_CF_TOKEN",
			"提供 Cloudflare API token,或勾选复用证书 token(需 DNS-01 已配置 Cloudflare)")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()

	cf := cfapi.New(token)
	if err := cf.VerifyToken(ctx); err != nil {
		core.Fail(c, http.StatusBadRequest, cfCode(err, "BAD_CF_TOKEN"), err.Error())
		return
	}
	zones, err := cf.ListZones(ctx)
	if err != nil {
		core.Fail(c, http.StatusBadRequest, cfCode(err, "NO_ZONE"), err.Error())
		return
	}
	if len(zones) == 0 {
		core.Fail(c, http.StatusBadRequest, "NO_ZONE",
			"这个 token 读不到任何域名(Zone)。请确认它有 Zone:DNS:Read/Edit 权限。")
		return
	}

	zoneNames := make([]string, 0, len(zones))
	for i := range zones {
		zoneNames = append(zoneNames, zones[i].Name)
	}

	// Probe tunnel permission by reading the account's tunnels. Account id comes
	// from any readable zone (single-account is the common case). An auth error
	// means the token lacks Cloudflare Tunnel access — report has_tunnel_perm
	// false but still hand back the zones so the UI keeps context.
	accountID := zones[0].Account.ID
	accountName := zones[0].Account.Name
	tunnels, terr := cf.ListTunnels(ctx, accountID)
	if terr != nil {
		if cfapi.IsAuthError(terr) {
			core.OK(c, gin.H{
				"verified":        true,
				"has_tunnel_perm": false,
				"account_name":    accountName,
				"zones":           zoneNames,
				"tunnels":         []gin.H{},
			})
			return
		}
		core.Fail(c, http.StatusBadGateway, cfCode(terr, "CF_ERROR"), terr.Error())
		return
	}

	cdnDoms := h.cdnInboundDomains(h.parseLocalNodeID())
	outTunnels := make([]gin.H, 0, len(tunnels))
	for _, t := range tunnels {
		if h.tunnelHitsCDN(ctx, cf, accountID, t.ID, cdnDoms) {
			continue // hide tunnels serving a domain already claimed for CDN
		}
		outTunnels = append(outTunnels, gin.H{"id": t.ID, "name": t.Name, "status": t.Status})
	}
	core.OK(c, gin.H{
		"verified":        true,
		"has_tunnel_perm": true,
		"account_name":    accountName,
		"zones":           zoneNames,
		"tunnels":         outTunnels,
	})
}

type argoCFProvisionReq struct {
	Token          string `json:"token"`
	ReuseCertToken bool   `json:"reuse_cert_token"`
	Domain         string `json:"domain"`
	// TunnelID selects an existing tunnel to reuse; empty creates a new one
	// named TunnelName (or a default derived from the domain).
	TunnelID   string `json:"tunnel_id"`
	TunnelName string `json:"tunnel_name"`
}

// ArgoCFProvision is the one-click path: it verifies the token, resolves the
// zone, creates (or reuses) a remotely-managed tunnel, points its ingress at the
// node's Argo inbound loopback port, writes the proxied DNS CNAME, and persists
// the result as a fixed-mode Argo config (domain + run token). The operator then
// just starts the tunnel — no manual cloudflared dashboard steps.
//
// POST /api/v1/argo/cf/provision
func (h *Handler) ArgoCFProvision(c *gin.Context) {
	var body argoCFProvisionReq
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	token := h.resolveCFToken(body.Token, body.ReuseCertToken)
	if token == "" {
		core.Fail(c, http.StatusBadRequest, "NO_CF_TOKEN",
			"提供 Cloudflare API token,或勾选复用证书 token")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(body.Domain))
	if domain == "" {
		core.Fail(c, http.StatusBadRequest, "NO_DOMAIN", "域名必填")
		return
	}

	nodeID := h.parseLocalNodeID()
	// The tunnel must forward to a real local listener — the single Argo inbound.
	port, ok := h.argoInboundPort(nodeID)
	if !ok {
		core.Fail(c, http.StatusBadRequest, "ARGO_NO_INBOUND",
			"没有 Argo 入站可绑定。请先用创建入站向导建一个走 Argo 的 WebSocket 入站,再来一键建隧道。")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 40*time.Second)
	defer cancel()

	cf := cfapi.New(token)
	if err := cf.VerifyToken(ctx); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_CF_TOKEN", err.Error())
		return
	}
	zone, err := cf.ZoneForDomain(ctx, domain)
	if err != nil {
		core.Fail(c, http.StatusBadRequest, cfCode(err, "NO_ZONE"), err.Error())
		return
	}

	// Guard: refuse to tunnel a hostname that already has a direct A/AAAA record
	// (orange-cloud CDN front or grey-cloud direct origin). A named tunnel needs
	// the hostname pointed at <id>.cfargotunnel.com via CNAME; UpsertTunnelCNAME
	// below would silently overwrite the A/AAAA, breaking a CDN/direct setup that
	// shares this domain. (Re-provisioning the SAME tunnel is fine — that's an
	// existing CNAME, which we don't block.) This is exactly the CDN-vs-Argo
	// hostname conflict: the two modes can't share one domain.
	if recs, lerr := cf.LookupRecords(ctx, zone.ID, domain); lerr == nil {
		if msg := argoRecordConflictMsg(recs, domain); msg != "" {
			core.Fail(c, http.StatusConflict, "DOMAIN_HAS_A_RECORD", msg)
			return
		}
	}

	// Create or reuse the tunnel.
	var tunnelID, tunnelName, runToken string
	if strings.TrimSpace(body.TunnelID) != "" {
		tunnelID = strings.TrimSpace(body.TunnelID)
		runToken, err = cf.TunnelToken(ctx, zone.Account.ID, tunnelID)
		if err != nil {
			core.Fail(c, http.StatusBadGateway, cfCode(err, "CF_TOKEN_FAILED"), err.Error())
			return
		}
		tunnelName = body.TunnelName
	} else {
		name := strings.TrimSpace(body.TunnelName)
		if name == "" {
			name = "edgenest-" + strings.ReplaceAll(domain, ".", "-")
		}
		t, tok, err := cf.CreateTunnel(ctx, zone.Account.ID, name)
		if err != nil {
			core.Fail(c, http.StatusBadGateway, cfCode(err, "CF_CREATE_FAILED"), err.Error())
			return
		}
		tunnelID, tunnelName, runToken = t.ID, t.Name, tok
	}

	// Route hostname → local Argo inbound (plaintext loopback origin; the CF edge
	// supplies TLS). Matches the temp-tunnel origin shape the resolver expects.
	service := "http://localhost:" + intStr(port)
	if err := cf.SetIngress(ctx, zone.Account.ID, tunnelID, domain, service); err != nil {
		core.Fail(c, http.StatusBadGateway, cfCode(err, "CF_INGRESS_FAILED"), err.Error())
		return
	}
	if err := cf.UpsertTunnelCNAME(ctx, zone.ID, domain, tunnelID); err != nil {
		core.Fail(c, http.StatusBadGateway, cfCode(err, "CF_DNS_FAILED"), err.Error())
		return
	}

	// Persist as a fixed-mode Argo config so /argo/start can launch it.
	cfg, _ := h.store.GetAdvanced(nodeID)
	if cfg == nil {
		cfg = &model.AdvancedConfig{NodeID: nodeID}
	}
	cfg.ArgoMode = "fixed"
	cfg.ArgoDomain = domain
	cfg.ArgoToken = runToken
	if err := h.store.UpsertAdvanced(cfg); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}

	h.auditLog(c, "argo.cf.provision", "argo", map[string]string{
		"domain": domain, "tunnel_id": tunnelID,
	})
	core.OK(c, gin.H{
		"domain":      domain,
		"tunnel_id":   tunnelID,
		"tunnel_name": tunnelName,
		"zone":        zone.Name,
		"local_port":  port,
	})
}
