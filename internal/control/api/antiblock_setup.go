package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/gateway"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/portowner"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// antiblockSetupRequest drives the one-click setup on the Anti-blocking page.
type antiblockSetupRequest struct {
	EdgeDomain   string `json:"edge_domain"`
	DirectDomain string `json:"direct_domain"`
	// Use is "builtin" (EdgeNest serves the edge domain itself — on 443, or
	// on a free Cloudflare port when another program owns 443) or "external"
	// (the operator's own reverse proxy forwards the domain to EdgeNest).
	Use string `json:"use"`
}

type antiblockSetupResult struct {
	antiblockView
	CreatedInbound bool `json:"created_inbound"`
	UsersAdded     int  `json:"users_added"`
}

// SetupAntiBlock is the one-click path: it stores the names, picks the
// gateway's port, makes sure a CDN node exists and is in every user's
// subscription, points subscriptions at the edge domain, and applies.
//
// POST /api/v1/antiblock/setup
func (h *Handler) SetupAntiBlock(c *gin.Context) {
	var req antiblockSetupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	cfg := antiblock.Load(h.store)
	cfg.EdgeDomain = req.EdgeDomain
	if strings.TrimSpace(req.DirectDomain) != "" {
		cfg.DirectDomain = req.DirectDomain
	}
	switch req.Use {
	case "external":
		cfg.GatewayMode = antiblock.GatewayExternal
	case "builtin", "":
		cfg.GatewayMode = antiblock.GatewayBuiltin
		cfg.EdgePort = 0
		if owner := portowner.Find(443); owner != nil && !h.gatewayServes(443) {
			alt := 0
			if h.gateway != nil {
				alt = h.gateway.FreeAltPort()
			}
			if alt == 0 {
				core.Fail(c, http.StatusConflict, "NO_FREE_PORT",
					"443 已被其他程序占用，Cloudflare 支持的其他 HTTPS 端口（8443/2053/2083/2087/2096）也都不空闲。请改用「沿用已有反代」。")
				return
			}
			cfg.EdgePort = alt
		}
	default:
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "use must be builtin or external")
		return
	}
	// Subscriptions follow the edge domain unless the operator already chose
	// another public HTTPS origin.
	if cfg.SubBaseURL == "" || strings.HasPrefix(cfg.SubBaseURL, "http://") {
		cfg.SubBaseURL = cfg.EdgeOrigin()
	}
	next, err := antiblock.Normalize(cfg)
	if err != nil {
		core.Fail(c, http.StatusBadRequest, "INVALID_ANTIBLOCK", err.Error())
		return
	}
	if next.EdgeDomain == "" {
		core.Fail(c, http.StatusBadRequest, "INVALID_ANTIBLOCK", "edge_domain required")
		return
	}
	// Save first: the edge inbound and the subscriptions are resolved against
	// the stored posture.
	if err := antiblock.Save(h.store, next); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	created, added, err := h.ensureEdgeInbound()
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "EDGE_INBOUND", err.Error())
		return
	}
	h.auditLog(c, "antiblock.setup", "settings", map[string]string{
		"edge_domain": next.EdgeDomain, "use": req.Use,
		"edge_port": strconv.Itoa(next.GatewayPort()), "users_added": strconv.Itoa(added),
	})
	if err := h.applyAfterChange(c, h.parseLocalNodeID()); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	if h.gateway != nil {
		h.gateway.Reconcile(context.Background())
	}
	core.OK(c, antiblockSetupResult{antiblockView: h.antiblockView(antiblock.Load(h.store)), CreatedInbound: created, UsersAdded: added})
}

// gatewayServes reports whether the built-in gateway itself is the program on
// port (so a re-run of the setup doesn't mistake it for a stranger).
func (h *Handler) gatewayServes(port int) bool {
	if h.gateway == nil {
		return false
	}
	st := h.gateway.Status()
	return st.Mode == gateway.ModeBuiltin && st.Running && st.Port == port
}

// ensureEdgeInbound creates the CDN node when the node has none: VLESS-WS
// served behind the edge domain (plaintext on loopback, a random path), with
// a credential for every existing user (reusing their UUID where they have
// one) and added to every subscription that lists its inbounds explicitly.
// It reports whether it created the inbound and how many users it added.
func (h *Handler) ensureEdgeInbound() (bool, int, error) {
	nodeID := h.parseLocalNodeID()
	ins, err := h.store.ListInbounds(nodeID)
	if err != nil {
		return false, 0, err
	}
	used := map[int]bool{}
	for _, in := range ins {
		used[in.Port] = true
		var s map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &s)
		}
		if in.Enabled && settingTruthy(s["edge_bound"]) {
			return false, 0, nil
		}
	}

	// One credential per user (email), seeded from their existing ones.
	type seed struct{ c model.Client }
	users := map[string]*seed{}
	var order []string
	for _, in := range ins {
		clients, err := h.store.ListClients(in.ID)
		if err != nil {
			return false, 0, err
		}
		for _, cl := range clients {
			if cl.Email == "" {
				continue
			}
			if u, ok := users[cl.Email]; ok {
				if u.c.UUID == "" && cl.UUID != "" {
					u.c.UUID = cl.UUID
				}
				continue
			}
			users[cl.Email] = &seed{c: cl}
			order = append(order, cl.Email)
		}
	}

	port := 0
	for i := 0; i < 50 && port == 0; i++ {
		p := 20000 + rand.Intn(12000)
		if used[p] {
			continue
		}
		if ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p))); err == nil {
			_ = ln.Close()
			port = p
		}
	}
	if port == 0 {
		return false, 0, fmt.Errorf("no free loopback port for the CDN node")
	}
	suffix, err := auth.RandomHex(4)
	if err != nil {
		return false, 0, err
	}
	settings, _ := json.Marshal(map[string]any{"edge_bound": "true", "ws_path": "/" + suffix})
	in := &model.Inbound{
		NodeID: nodeID, Tag: fmt.Sprintf("EdgeNest-VLESS-WS-CDN-%d", port), Engine: "singbox",
		Type: "vless-ws", Remark: "EdgeNest-VLESS-WS-CDN", Listen: "127.0.0.1", Port: port,
		Network: "tcp", Enabled: true, Settings: string(settings),
	}
	if err := h.store.CreateInbound(in); err != nil {
		return false, 0, err
	}
	for _, email := range order {
		src := users[email].c
		id := src.UUID
		if id == "" {
			id = uuid.NewString()
		}
		if err := h.store.CreateClient(&model.Client{
			InboundID: in.ID, Email: email, UUID: id,
			QuotaBytes: src.QuotaBytes, ExpiryAt: src.ExpiryAt, Enabled: src.Enabled,
		}); err != nil {
			return true, 0, err
		}
	}
	subs, err := h.store.ListSubscriptions()
	if err != nil {
		return true, len(order), err
	}
	for _, sub := range subs {
		var ids []uint
		if json.Unmarshal([]byte(sub.AllowedInbounds), &ids) != nil || len(ids) == 0 {
			continue // empty list = all inbounds already
		}
		ids = append(ids, in.ID)
		b, _ := json.Marshal(ids)
		if err := h.store.UpdateSubscriptionScope(sub.ID, sub.ClientID, string(b)); err != nil {
			return true, len(order), err
		}
	}
	return true, len(order), nil
}

// PutCloudflareToken stores the Cloudflare API token the edge gateway uses
// for DNS validation when port 80 belongs to another program. The token is
// checked against Cloudflare first, so a typo surfaces here and not as a
// failed certificate later.
//
// PUT /api/v1/antiblock/cf-token
func (h *Handler) PutCloudflareToken(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "token required")
		return
	}
	tok := strings.TrimSpace(req.Token)
	if err := verifyCloudflareToken(c.Request.Context(), tok); err != nil {
		core.Fail(c, http.StatusBadRequest, "CF_TOKEN_INVALID", "Cloudflare 令牌校验失败: "+err.Error())
		return
	}
	if err := h.store.SetSetting(gateway.CFTokenSetting, tok); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "antiblock.cf_token", "settings", nil)
	if h.gateway != nil {
		h.gateway.RetryCertificate()
		h.gateway.Reconcile(context.Background())
	}
	core.OK(c, h.antiblockView(antiblock.Load(h.store)))
}

// cfVerifyURL is Cloudflare's token self-check endpoint (a seam for tests).
var cfVerifyURL = "https://api.cloudflare.com/client/v4/user/tokens/verify"

func verifyCloudflareToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfVerifyURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		Success bool `json:"success"`
		Result  struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("unexpected response (%d)", resp.StatusCode)
	}
	if !body.Success || body.Result.Status != "active" {
		return fmt.Errorf("token not active (%d)", resp.StatusCode)
	}
	return nil
}
