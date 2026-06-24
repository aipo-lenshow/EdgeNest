package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/orchestrator"
	"github.com/aipo-lenshow/EdgeNest/internal/control/system"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/aipo-lenshow/EdgeNest/internal/node/engine"
)

// cdnFrontableTypes are the protocols whose traffic can ride a Cloudflare
// anycast IP (HTTP/WS/XHTTP over TLS). Mirrors share.isCDNCompatibleType —
// only these honour settings.cdn_mode, and only on a CF HTTPS port.
var cdnFrontableTypes = map[string]bool{
	"vmess": true, "vmess-ws": true, "vless-ws": true, "vless-xhttp": true,
}

// settingTruthy accepts cdn_mode/argo_bound in either the bool form a JSON
// body carries or the "true"/"1" string autofill normalises into.
func settingTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "yes"
	}
	return false
}

// cdnPortGate refuses a cdn_mode inbound on a port Cloudflare won't proxy.
// The wizard's funnel already gates this (funnel.go), but the direct
// create/update handlers bypass the funnel — without this an operator could
// flip cdn_mode on an existing inbound sitting on a non-CF port and the
// subscription would silently hand out a CF anycast IP that can't reach it.
// Returns "" when the inbound is fine to save.
func cdnPortGate(typ string, port int, settings map[string]any) string {
	if !settingTruthy(settings["cdn_mode"]) || !cdnFrontableTypes[typ] {
		return ""
	}
	if !system.IsCFWhitelisted(port) {
		return fmt.Sprintf(
			"CDN 已开启, 但端口 %d 不是 Cloudflare 可代理的 HTTPS 端口 %v — CDN 不会生效。请改用白名单端口或关闭 CDN。",
			port, system.CFHTTPSWhitelist)
	}
	return ""
}

// argoBindGate refuses an argo_bound inbound that isn't a plaintext WebSocket
// origin on loopback. cloudflared connects to the origin with plain HTTP and
// Cloudflare terminates TLS at its edge, so a TLS origin (certificate present)
// or a public-IP listener cannot work — the tunnel either fails the handshake
// or the port is needlessly exposed and bypassed. The wizard's "Access
// optimisation → Argo" path builds such inbounds correctly (plaintext ws on
// 127.0.0.1); this stops a manual toggle on an existing public/TLS inbound
// from silently producing a dead tunnel. Returns "" when the inbound is fine.
func argoBindGate(typ, listen string, settings map[string]any) string {
	if !settingTruthy(settings["argo_bound"]) {
		return ""
	}
	if !cdnFrontableTypes[typ] {
		return "Argo 绑定仅支持 WebSocket 协议(VMess-WS / VLESS-WS)。请用向导的「接入优化 → Argo」创建专用入站。"
	}
	if cert, _ := settings["tls_cert_path"].(string); cert != "" {
		return "Argo 绑定要求入站为明文 WebSocket(cloudflared 以纯 HTTP 接入, Cloudflare 边缘提供 TLS), 当前入站带证书。请用向导的「接入优化 → Argo」创建专用明文入站, 不要在已有 TLS 入站上手动勾选。"
	}
	if !isLoopbackListen(listen) {
		return "Argo 绑定要求入站只监听 127.0.0.1(仅 cloudflared 可达), 当前监听公网地址。请用向导的「接入优化 → Argo」创建专用入站。"
	}
	return ""
}

// argoSingletonGate enforces the one-Argo-inbound-per-node constraint (1a): a
// node runs a single cloudflared tunnel pointing at a single loopback port, so
// only one argo_bound inbound can ever carry traffic. settings is the candidate
// inbound's filled settings; excludeID is the inbound being updated (0 on
// create). Returns a user-facing message if a DIFFERENT argo_bound inbound
// already exists on the node, else "".
func (h *Handler) argoSingletonGate(nodeID uint, settings map[string]any, excludeID uint) string {
	if !settingTruthy(settings["argo_bound"]) {
		return ""
	}
	ins, err := h.store.ListInbounds(nodeID)
	if err != nil {
		return ""
	}
	for _, in := range ins {
		if in.ID == excludeID {
			continue
		}
		var s map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &s)
		}
		if settingTruthy(s["argo_bound"]) {
			return fmt.Sprintf("本节点已存在 Argo 入站(%s), 一条隧道只能服务一个协议。请先删除它或把它改为不走 Argo, 再绑定", in.Tag)
		}
	}
	return ""
}

// isLoopbackListen reports whether a listen address is loopback-only.
func isLoopbackListen(listen string) bool {
	if listen == "localhost" {
		return true
	}
	if ip := net.ParseIP(listen); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// ---- Inbound CRUD ----

type inboundCreateRequest struct {
	Tag     string `json:"tag"`
	Type    string `json:"type"`
	Listen  string `json:"listen"`
	Port    int    `json:"port"`
	Network string `json:"network"`
	Remark  string `json:"remark"`
	Enabled *bool  `json:"enabled"`
	// Advanced is the structured payload the panel form submits — only fields
	// in [advancedFieldsByType] are honoured. When present it takes precedence
	// over Settings (the legacy `?raw=1` path).
	Advanced map[string]any `json:"advanced"`
	Settings map[string]any `json:"settings"`
}

// CreateInbound creates a new inbound on the local node and triggers an Apply.
func (h *Handler) CreateInbound(c *gin.Context) {
	var req inboundCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.Tag == "" || req.Type == "" || req.Port <= 0 {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "tag, type and port are required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	var (
		filled map[string]any
		err    error
	)
	if req.Advanced != nil {
		filled, err = BuildInboundSettings(req.Type, req.Advanced)
	} else {
		filled, err = autofillInboundSettings(req.Type, req.Settings, nil)
	}
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "AUTOFILL", err.Error())
		return
	}
	if msg := cdnPortGate(req.Type, req.Port, filled); msg != "" {
		core.Fail(c, http.StatusBadRequest, "CDN_PORT", msg)
		return
	}
	if msg := argoBindGate(req.Type, req.Listen, filled); msg != "" {
		core.Fail(c, http.StatusBadRequest, "ARGO_BIND", msg)
		return
	}
	if msg := h.argoSingletonGate(h.parseLocalNodeID(), filled, 0); msg != "" {
		core.Fail(c, http.StatusBadRequest, "ARGO_SINGLETON", msg)
		return
	}
	settingsJSON := "{}"
	if len(filled) > 0 {
		b, err := json.Marshal(filled)
		if err != nil {
			core.Fail(c, http.StatusBadRequest, "BAD_SETTINGS", "settings is not valid JSON")
			return
		}
		settingsJSON = string(b)
	}
	nodeID := h.parseLocalNodeID()
	ib := &model.Inbound{
		NodeID:   nodeID,
		Tag:      req.Tag,
		Type:     req.Type,
		Engine:   engine.EngineForType(req.Type),
		Listen:   req.Listen,
		Port:     req.Port,
		Network:  req.Network,
		Remark:   req.Remark,
		Enabled:  enabled,
		Settings: settingsJSON,
	}
	if err := h.store.CreateInbound(ib); err != nil {
		core.Fail(c, http.StatusConflict, "DB_ERROR", err.Error())
		return
	}
	if err := h.applyAfterChange(c, nodeID); err != nil {
		// Apply failed: surface the message but keep the inbound (user can edit
		// settings and retry). Note in audit.
		h.auditLog(c, "inbound.create", "inbound:"+strconv.Itoa(int(ib.ID)),
			map[string]string{"warning": "apply failed", "detail": err.Error()})
		core.Fail(c, http.StatusConflict, "APPLY_FAILED", err.Error())
		return
	}
	h.auditLog(c, "inbound.create", "inbound:"+strconv.Itoa(int(ib.ID)), nil)
	core.Created(c, ib)
}

// ListInbounds returns all inbounds on the local node. The Settings JSON is
// scrubbed so secrets (Reality private key, etc.) never reach the panel API.
func (h *Handler) ListInbounds(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	ins, err := h.store.ListInbounds(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	for i := range ins {
		ins[i].Settings = scrubInboundSettings(ins[i].Settings)
	}
	core.OK(c, ins)
}

// GetInbound returns one inbound by id (with secrets scrubbed from Settings),
// plus an `advanced` map the structured edit form can prefill from directly.
// `advanced` is nil when reverse-parsing fails — the UI then falls back to a
// raw JSON textarea with a warning banner.
type inboundDetailResponse struct {
	*model.Inbound
	Advanced      map[string]any `json:"advanced"`
	AdvancedError string         `json:"advanced_error,omitempty"`
}

func (h *Handler) GetInbound(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	in, err := h.store.GetInbound(id)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "inbound not found")
		return
	}
	advanced, parseErr := ParseInboundAdvanced(in.Type, in.Settings)
	in.Settings = scrubInboundSettings(in.Settings)
	resp := inboundDetailResponse{Inbound: in, Advanced: advanced}
	if parseErr != nil {
		resp.AdvancedError = "unparseable"
	}
	core.OK(c, resp)
}

// inboundSecretKeys are Settings keys that must never leave the server. They
// are still kept on disk so the engine renderer can read them; the API just
// hides them on the way out.
var inboundSecretKeys = []string{
	"reality_private_key",
}

// scrubInboundSettings parses the Settings JSON, drops secret keys, and
// re-serialises. On parse failure the original string is returned unchanged
// so we never replace good config with `{}`.
func scrubInboundSettings(raw string) string {
	if raw == "" {
		return raw
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	dropped := false
	for _, k := range inboundSecretKeys {
		if _, ok := m[k]; ok {
			delete(m, k)
			dropped = true
		}
	}
	if !dropped {
		return raw
	}
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return string(b)
}

type inboundUpdateRequest struct {
	Listen   *string         `json:"listen"`
	Port     *int            `json:"port"`
	Network  *string         `json:"network"`
	Remark   *string         `json:"remark"`
	Enabled  *bool           `json:"enabled"`
	Advanced *map[string]any `json:"advanced"`
	Settings *map[string]any `json:"settings"`
}

// UpdateInbound patches an inbound. Type and Engine are immutable post-create.
func (h *Handler) UpdateInbound(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	in, err := h.store.GetInbound(id)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "inbound not found")
		return
	}
	var req inboundUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.Listen != nil {
		in.Listen = *req.Listen
	}
	if req.Port != nil {
		in.Port = *req.Port
	}
	if req.Network != nil {
		in.Network = *req.Network
	}
	if req.Remark != nil {
		in.Remark = *req.Remark
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	// Port/listen-only change (no settings in the request) still has to honour
	// the CDN/Argo port constraints: a CDN inbound must stay on a CF-proxyable
	// HTTPS port and an Argo inbound on its loopback listen. Without this guard
	// the gates below only run when settings change, so a bare port edit could
	// silently move a CDN inbound off the CF whitelist or break the Argo bind.
	if (req.Port != nil || req.Listen != nil) && req.Advanced == nil && req.Settings == nil {
		var existing map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &existing)
		}
		if msg := cdnPortGate(in.Type, in.Port, existing); msg != "" {
			core.Fail(c, http.StatusBadRequest, "CDN_PORT", msg)
			return
		}
		if msg := argoBindGate(in.Type, in.Listen, existing); msg != "" {
			core.Fail(c, http.StatusBadRequest, "ARGO_BIND", msg)
			return
		}
	}
	if req.Advanced != nil || req.Settings != nil {
		var existing map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &existing)
		}
		var (
			filled map[string]any
			err    error
		)
		if req.Advanced != nil {
			filled, err = ApplyAdvancedUpdate(in.Type, *req.Advanced, existing)
		} else {
			filled, err = autofillInboundSettings(in.Type, *req.Settings, existing)
		}
		if err != nil {
			core.Fail(c, http.StatusInternalServerError, "AUTOFILL", err.Error())
			return
		}
		if msg := cdnPortGate(in.Type, in.Port, filled); msg != "" {
			core.Fail(c, http.StatusBadRequest, "CDN_PORT", msg)
			return
		}
		if msg := argoBindGate(in.Type, in.Listen, filled); msg != "" {
			core.Fail(c, http.StatusBadRequest, "ARGO_BIND", msg)
			return
		}
		if msg := h.argoSingletonGate(in.NodeID, filled, in.ID); msg != "" {
			core.Fail(c, http.StatusBadRequest, "ARGO_SINGLETON", msg)
			return
		}
		b, err := json.Marshal(filled)
		if err != nil {
			core.Fail(c, http.StatusBadRequest, "BAD_SETTINGS", "settings is not valid JSON")
			return
		}
		in.Settings = string(b)
	}
	if err := h.store.UpdateInbound(in); err != nil {
		core.Fail(c, http.StatusConflict, "DB_ERROR", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusConflict, "APPLY_FAILED", err.Error())
		return
	}
	h.auditLog(c, "inbound.update", "inbound:"+strconv.Itoa(int(id)), nil)
	core.OK(c, in)
}

// DeleteInbound removes an inbound and its clients, then applies.
func (h *Handler) DeleteInbound(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	// Capture whether this is the Argo-bound inbound BEFORE deleting it. The
	// tunnel points at this inbound's loopback port, so once it's gone the
	// tunnel would serve a dead origin — a zombie. We stop it and clear the
	// Argo config (enabled/mode/domain/token) so the node returns to a clean
	// "no Argo" state; recreating an Argo inbound later reconfigures and
	// restarts the tunnel from scratch. CDN config is deliberately untouched —
	// the speed-tested IP pool is reusable global state, not tied to one inbound.
	wasArgo := false
	if in, err := h.store.GetInbound(id); err == nil && in != nil {
		var s map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &s)
		}
		wasArgo = settingTruthy(s["argo_bound"])
	}
	if err := h.store.DeleteInbound(id); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	if wasArgo {
		globalArgo.stop()
		if cfg, _ := h.store.GetAdvanced(nodeID); cfg != nil {
			cfg.ArgoEnabled = false
			cfg.ArgoMode = ""
			cfg.ArgoDomain = ""
			cfg.ArgoToken = ""
			_ = h.store.UpsertAdvanced(cfg)
		}
	}
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusConflict, "APPLY_FAILED", err.Error())
		return
	}
	h.auditLog(c, "inbound.delete", "inbound:"+strconv.Itoa(int(id)), nil)
	core.OK(c, gin.H{"ok": true})
}

// ---- Client CRUD ----

type clientCreateRequest struct {
	Email      string `json:"email"`
	UUID       string `json:"uuid"`
	Password   string `json:"password"`
	Flow       string `json:"flow"`
	QuotaBytes int64  `json:"quota_bytes"`
	ExpiryAt   int64  `json:"expiry_at"`
	Enabled    *bool  `json:"enabled"`
}

// CreateClient adds a client to an inbound and applies.
//
// Shadowsocks is special: SS-2022 single-user mode means one inbound = one
// client. Adding a second client to an SS inbound returns 422
// SS_INBOUND_SINGLE_CLIENT — the panel UI handles this by auto-creating a
// fresh SS inbound on the next available port and posting the client there.
func (h *Handler) CreateClient(c *gin.Context) {
	inboundID, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req clientCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.Email == "" {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "email is required (invariant I1)")
		return
	}

	// SS single-client guard: refuse before touching the DB so the panel can
	// auto-fan-out cleanly.
	ib, err := h.store.GetInbound(inboundID)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "inbound not found")
		return
	}
	if ib.Type == "shadowsocks" {
		existing, err := h.store.ListClients(inboundID)
		if err != nil {
			core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
			return
		}
		if len(existing) >= 1 {
			core.Fail(c, http.StatusUnprocessableEntity, "SS_INBOUND_SINGLE_CLIENT",
				"shadowsocks (SS-2022 single-user) supports exactly 1 client per inbound; create a separate inbound for additional users")
			return
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	password := req.Password
	if ib.Type == "shadowsocks" {
		normalized, err := normalizeSSClientPassword(ib, password)
		if err != nil {
			core.Fail(c, http.StatusInternalServerError, "SS_PSK_MINT", err.Error())
			return
		}
		password = normalized
	}
	cl := &model.Client{
		InboundID:  inboundID,
		Email:      req.Email,
		UUID:       req.UUID,
		Password:   password,
		Flow:       req.Flow,
		QuotaBytes: req.QuotaBytes,
		ExpiryAt:   req.ExpiryAt,
		Enabled:    enabled,
	}
	if err := h.store.CreateClient(cl); err != nil {
		core.Fail(c, http.StatusConflict, "DB_ERROR", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusConflict, "APPLY_FAILED", err.Error())
		return
	}
	h.auditLog(c, "client.create", "client:"+strconv.Itoa(int(cl.ID)), nil)
	core.Created(c, cl)
}

// ListClients returns the clients of an inbound.
func (h *Handler) ListClients(c *gin.Context) {
	inboundID, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	cs, err := h.store.ListClients(inboundID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, cs)
}

type clientUpdateRequest struct {
	Email      *string `json:"email"`
	UUID       *string `json:"uuid"`
	Password   *string `json:"password"`
	Flow       *string `json:"flow"`
	QuotaBytes *int64  `json:"quota_bytes"`
	ExpiryAt   *int64  `json:"expiry_at"`
	Enabled    *bool   `json:"enabled"`
}

// UpdateClient patches a client.
func (h *Handler) UpdateClient(c *gin.Context) {
	id, ok := parseIDParam(c, "cid")
	if !ok {
		return
	}
	cl, err := h.store.GetClient(id)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "client not found")
		return
	}
	var req clientUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.Email != nil {
		cl.Email = *req.Email
	}
	if req.UUID != nil {
		cl.UUID = *req.UUID
	}
	if req.Password != nil {
		cl.Password = *req.Password
	}
	if req.Flow != nil {
		cl.Flow = *req.Flow
	}
	// SS-2022 PSK: normalize after taking the user's input — same rule as
	// CreateClient (Mihomo / Stash refuse mismatched key lengths).
	ib, err := h.store.GetInbound(cl.InboundID)
	if err == nil && ib != nil && ib.Type == "shadowsocks" {
		normalized, nerr := normalizeSSClientPassword(ib, cl.Password)
		if nerr != nil {
			core.Fail(c, http.StatusInternalServerError, "SS_PSK_MINT", nerr.Error())
			return
		}
		cl.Password = normalized
	}
	if req.QuotaBytes != nil {
		cl.QuotaBytes = *req.QuotaBytes
	}
	if req.ExpiryAt != nil {
		cl.ExpiryAt = *req.ExpiryAt
	}
	if req.Enabled != nil {
		cl.Enabled = *req.Enabled
	}
	if err := h.store.UpdateClient(cl); err != nil {
		core.Fail(c, http.StatusConflict, "DB_ERROR", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusConflict, "APPLY_FAILED", err.Error())
		return
	}
	h.auditLog(c, "client.update", "client:"+strconv.Itoa(int(id)), nil)
	core.OK(c, cl)
}

// DeleteClient removes a client.
func (h *Handler) DeleteClient(c *gin.Context) {
	id, ok := parseIDParam(c, "cid")
	if !ok {
		return
	}
	if err := h.store.DeleteClient(id); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusConflict, "APPLY_FAILED", err.Error())
		return
	}
	h.auditLog(c, "client.delete", "client:"+strconv.Itoa(int(id)), nil)
	core.OK(c, gin.H{"ok": true})
}

// ---- Firewall ----

// ListFirewall returns managed + user firewall rules on the local node.
func (h *Handler) ListFirewall(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	rs, err := h.store.ListFirewallRules(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, rs)
}

// ResyncFirewall re-applies the desired config, refreshing managed firewall
// rules to match enabled inbounds. Useful after manual DB edits or fixing a
// stuck state.
func (h *Handler) ResyncFirewall(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusConflict, "APPLY_FAILED", err.Error())
		return
	}
	rs, _ := h.store.ListFirewallRules(nodeID)
	core.OK(c, rs)
}

// ---- helpers ----

func (h *Handler) parseLocalNodeID() uint {
	n, _ := strconv.ParseUint(h.localNodeID, 10, 64)
	return uint(n)
}

func parseIDParam(c *gin.Context, name string) (uint, bool) {
	s := c.Param(name)
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 {
		core.Fail(c, http.StatusBadRequest, "BAD_ID", "invalid id parameter")
		return 0, false
	}
	return uint(n), true
}

// applyAfterChange rebuilds DesiredConfig from DB and pushes through NodeClient.
// Errors are returned to the caller; the caller decides whether to roll back
// the DB change or surface a warning (currently we surface).
func (h *Handler) applyAfterChange(c *gin.Context, nodeID uint) error {
	if h.orch == nil {
		// No orchestrator wired (older constructor); skip apply silently.
		return nil
	}
	res, err := h.orch.Apply(c.Request.Context(), nodeID)
	if err != nil {
		return err
	}
	if !res.OK {
		return errFromApply(res)
	}
	return nil
}

func errFromApply(res core.ApplyResult) error {
	msg := res.Message
	if res.RolledBack {
		msg = "rolled back: " + msg
	}
	return &applyError{msg: msg}
}

type applyError struct{ msg string }

func (e *applyError) Error() string { return e.msg }

// Ensure orchestrator import is used even when Handler.orch is nil-checked
// elsewhere; this keeps the linter quiet without adding dead code.
var _ = (*orchestrator.Orchestrator)(nil)

// normalizeSSClientPassword ensures a Shadowsocks client password is a valid
// PSK for the inbound's cipher. SS-2022 ciphers (`2022-blake3-*`) require the
// password to be base64 of exactly the key length (16 bytes for AES-128,
// 32 for AES-256 / ChaCha). If the caller passed something that doesn't
// decode to that length we mint a fresh PSK ourselves — Mihomo / Stash /
// sing-box all reject mismatched-length keys with "bad key length",
// which manifests as "subscription import failed" on the user's phone.
//
// Non-2022 (legacy AEAD) ciphers accept any UTF-8 string, so we leave the
// password as-is when the cipher isn't a 2022 variant.
func normalizeSSClientPassword(in *model.Inbound, password string) (string, error) {
	var settings map[string]any
	if in.Settings != "" {
		_ = json.Unmarshal([]byte(in.Settings), &settings)
	}
	method, _ := settings["method"].(string)
	if !strings.HasPrefix(method, "2022-blake3-") {
		return password, nil
	}
	wantLen := 16
	if method == "2022-blake3-aes-256-gcm" || method == "2022-blake3-chacha20-poly1305" {
		wantLen = 32
	}
	if password != "" {
		if dec, err := base64.StdEncoding.DecodeString(password); err == nil && len(dec) == wantLen {
			return password, nil
		}
	}
	return auth.RandomBase64(wantLen)
}
