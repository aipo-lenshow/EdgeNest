package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/gateway"
	"github.com/aipo-lenshow/EdgeNest/internal/control/portowner"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// antiblockView is the GET/PUT shape for /api/v1/antiblock: the stored posture
// plus read-only helpers the panel shows next to it (the reverse-proxy config
// to paste, the edge inbounds it covers, and the mirror-list public key).
type antiblockView struct {
	antiblock.Config
	// ProxyConfigs are ready-to-paste configs per tool (caddy, nginx) for an
	// operator whose own web server fronts the edge domain: one rule sending
	// the whole domain to the gateway's loopback backend.
	ProxyConfigs map[string]string `json:"proxy_configs,omitempty"`
	Gateway      gateway.Status    `json:"gateway"`
	// Port443 is whoever listens on 443 right now (nil = free, or the
	// built-in gateway itself), for the one-click setup's choice.
	Port443      *portowner.Owner `json:"port443,omitempty"`
	EdgeInbounds []edgeInboundVM  `json:"edge_inbounds"`
	MirrorPubKey string           `json:"mirror_pubkey"`
	// SubOrigin is the subscription address prefix actually handed out
	// (the explicit one, else the edge domain once a gateway serves it).
	SubOrigin string `json:"sub_origin"`
	// PanelURL is the panel's address through the edge domain, whichever
	// program (built-in gateway or the operator's proxy) serves it.
	PanelURL   string `json:"panel_url,omitempty"`
	HasCFToken bool   `json:"has_cf_token"`
}

type edgeInboundVM struct {
	Tag  string `json:"tag"`
	Path string `json:"path"`
	Port int    `json:"port"`
}

// GetAntiBlock returns the anti-blocking posture.
//
// GET /api/v1/antiblock
func (h *Handler) GetAntiBlock(c *gin.Context) {
	core.OK(c, h.antiblockView(antiblock.Load(h.store)))
}

// PutAntiBlock validates and stores the posture, then re-applies the node so
// a low-profile switch takes effect (listeners + firewall openings) at once.
//
// PUT /api/v1/antiblock
func (h *Handler) PutAntiBlock(c *gin.Context) {
	// Start from what is stored: a field the request leaves out keeps its
	// value instead of being reset (an older panel page or a script that
	// doesn't know a newer field must not silently undo it).
	req := antiblock.Load(h.store)
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	next, err := antiblock.Normalize(req)
	if err != nil {
		core.Fail(c, http.StatusBadRequest, "INVALID_ANTIBLOCK", err.Error())
		return
	}
	if next.Stealth && len(h.edgeInbounds()) == 0 {
		core.Fail(c, http.StatusBadRequest, "NO_EDGE_INBOUND",
			"低调模式会关掉所有直连入站, 但本节点还没有「经反代域名」入站 — 开启后所有用户都会断线。请先用向导创建一个经反代域名的 VLESS-WS 入站。")
		return
	}
	// Closing the panel port is only safe while the gateway actually serves
	// the panel; otherwise the operator would lock themselves out.
	if next.PanelViaGatewayOnly && !antiblock.Load(h.store).PanelViaGatewayOnly {
		if h.gateway == nil || h.gateway.Status().Mode != gateway.ModeBuiltin || !h.gateway.Status().Running || h.gateway.Status().CertExpires == 0 {
			core.Fail(c, http.StatusBadRequest, "GATEWAY_NOT_READY",
				"内置网关还没正常运行（或还没拿到证书），现在关闭面板端口会把你锁在外面。请等网关显示「运行中」并有证书后再开启。")
			return
		}
	}
	prev := antiblock.Load(h.store)
	if err := antiblock.Save(h.store, next); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "antiblock.update", "settings", map[string]string{
		"edge_domain":   next.EdgeDomain,
		"direct_domain": next.DirectDomain,
		"sub_base_url":  next.SubBaseURL,
		"mirrors":       strconv.Itoa(len(next.Mirrors)),
		"stealth":       strconv.FormatBool(next.Stealth),
	})
	h.panelGatewayOnly.Store(next.PanelViaGatewayOnly)
	if h.gateway != nil {
		h.gateway.Reconcile(context.Background())
	}
	// Only the low-profile switch and the edge domain change what the engine
	// serves (and the gateway's firewall openings); names and subscription
	// origins are read at subscription time.
	if prev.Stealth != next.Stealth || prev.EdgeDomain != next.EdgeDomain || prev.GatewayMode != next.GatewayMode {
		if err := h.applyAfterChange(c, h.parseLocalNodeID()); err != nil {
			core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
			return
		}
	}
	core.OK(c, h.antiblockView(next))
}

func (h *Handler) antiblockView(cfg antiblock.Config) antiblockView {
	if cfg.Mirrors == nil {
		cfg.Mirrors = []string{}
	}
	edges := h.edgeInbounds()
	pub, _ := antiblock.PublicKey(h.store)
	view := antiblockView{
		Config:       cfg,
		EdgeInbounds: edges,
		MirrorPubKey: pub,
		SubOrigin:    cfg.SubOrigin(),
	}
	if v, _ := h.store.GetSetting(gateway.CFTokenSetting); v != "" {
		view.HasCFToken = true
	}
	if h.gateway != nil {
		view.Gateway = h.gateway.Status()
		// The gateway may have just resolved auto mode; show the stored value.
		view.GatewayMode = antiblock.Load(h.store).GatewayMode
	}
	// Reverse-proxy configs are only for operators whose own web server
	// fronts the edge domain (or who are about to choose that); the built-in
	// gateway needs none.
	if !h.gatewayServes(443) {
		view.Port443 = portowner.Find(443)
	}
	if p := h.PanelPath(); p != "" && cfg.EdgeDomain != "" &&
		(view.Gateway.Mode == gateway.ModeExternal || (view.Gateway.Mode == gateway.ModeBuiltin && view.Gateway.Running)) {
		view.PanelURL = cfg.EdgeOrigin() + p
	}
	if view.Gateway.Mode == gateway.ModeExternal || view.Gateway.Mode == gateway.ModePending {
		view.ProxyConfigs = antiblock.ProxyConfigs(cfg, view.Gateway.Backend)
	}
	return view
}

// RetryAntiBlockCertificate asks for the edge domain's certificate again now,
// instead of waiting out the backoff after a failure (DNS was just fixed).
//
// POST /api/v1/antiblock/cert/retry
func (h *Handler) RetryAntiBlockCertificate(c *gin.Context) {
	if h.gateway != nil {
		h.gateway.RetryCertificate()
		h.gateway.Reconcile(context.Background())
	}
	h.auditLog(c, "antiblock.cert_retry", "settings", nil)
	core.OK(c, h.antiblockView(antiblock.Load(h.store)))
}

// edgeInbounds lists the enabled edge_bound inbounds on the local node with
// the WebSocket path the reverse proxy has to forward.
func (h *Handler) edgeInbounds() []edgeInboundVM {
	out := []edgeInboundVM{}
	ins, err := h.store.ListInbounds(h.parseLocalNodeID())
	if err != nil {
		return out
	}
	for _, in := range ins {
		if !in.Enabled {
			continue
		}
		var s map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &s)
		}
		if !settingTruthy(s["edge_bound"]) {
			continue
		}
		path, _ := s["ws_path"].(string)
		if path == "" || !strings.HasPrefix(path, "/") {
			continue
		}
		out = append(out, edgeInboundVM{Tag: in.Tag, Path: path, Port: in.Port})
	}
	return out
}
