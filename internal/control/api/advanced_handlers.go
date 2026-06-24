package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/aipo-lenshow/EdgeNest/internal/logredact"
)

// advancedDTO is the request/response for /api/v1/advanced. ArgoToken is
// write-only — it's never echoed back on GET (same pattern as warp's
// private_key) since it's a credential.
type advancedDTO struct {
	CDNEnabled      bool     `json:"cdn_enabled"`
	CDNPreferredIPs []string `json:"cdn_preferred_ips"`
	ArgoEnabled     bool     `json:"argo_enabled"`
	ArgoMode        string   `json:"argo_mode"` // "temp" | "fixed"
	ArgoDomain      string   `json:"argo_domain"`
	ArgoToken       string   `json:"argo_token,omitempty"`
	// ArgoHasToken reports whether a token is already stored (the token itself
	// is never echoed). Lets the UI show "token saved" vs "no token yet" instead
	// of a meaningless placeholder.
	ArgoHasToken   bool  `json:"argo_has_token"`
	BlockQUIC      bool  `json:"block_quic"`
	RedactClientIP bool  `json:"redact_client_ip"`
	UpdatedAt      int64 `json:"updated_at"`
}

// GetAdvanced returns the per-node advanced opt-in config. Returns an empty
// DTO when no row exists yet (everything off, no anti-blocking features).
//
// GET /api/v1/advanced
func (h *Handler) GetAdvanced(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	a, err := h.store.GetAdvanced(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if a == nil {
		core.OK(c, advancedDTO{})
		return
	}
	core.OK(c, advancedDTO{
		CDNEnabled:      a.CDNEnabled,
		CDNPreferredIPs: parseJSONStringSliceAPI(a.CDNPreferredIPs),
		ArgoEnabled:     a.ArgoEnabled,
		ArgoMode:        a.ArgoMode,
		ArgoDomain:      a.ArgoDomain,
		// ArgoToken intentionally omitted; expose only its presence.
		ArgoHasToken:   strings.TrimSpace(a.ArgoToken) != "",
		BlockQUIC:      a.BlockQUIC,
		RedactClientIP: a.RedactClientIP,
		UpdatedAt:      a.UpdatedAt,
	})
}

// PutAdvanced upserts the advanced config. Validates that Argo, when enabled,
// has the right shape for the chosen mode (token required for fixed; nothing
// extra required for temp tunnels).
//
// PUT /api/v1/advanced
func (h *Handler) PutAdvanced(c *gin.Context) {
	var body advancedDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	if body.ArgoEnabled {
		mode := strings.TrimSpace(body.ArgoMode)
		if mode != "temp" && mode != "fixed" {
			core.Fail(c, http.StatusBadRequest, "BAD_ARGO_MODE",
				"argo_mode must be 'temp' or 'fixed'")
			return
		}
		if mode == "fixed" && strings.TrimSpace(body.ArgoToken) == "" {
			core.Fail(c, http.StatusBadRequest, "MISSING_ARGO_TOKEN",
				"argo_token is required for fixed Argo tunnels")
			return
		}
	}
	if body.CDNEnabled && countNonBlank(body.CDNPreferredIPs) == 0 {
		core.Fail(c, http.StatusBadRequest, "CDN_NO_IPS",
			"启用 CDN 前置时优选 IP 池不能为空,请先添加至少一个优选 IP/域名")
		return
	}
	nodeID := h.parseLocalNodeID()

	// Preserve previously stored ArgoToken when caller omits it on update
	// (write-only semantics: empty means "no change", not "clear it").
	preservedToken := body.ArgoToken
	if preservedToken == "" {
		if existing, _ := h.store.GetAdvanced(nodeID); existing != nil {
			preservedToken = existing.ArgoToken
		}
	}

	a := &model.AdvancedConfig{
		NodeID:          nodeID,
		CDNEnabled:      body.CDNEnabled,
		CDNPreferredIPs: encodeJSONStringSlice(body.CDNPreferredIPs),
		ArgoEnabled:     body.ArgoEnabled,
		ArgoMode:        body.ArgoMode,
		ArgoDomain:      body.ArgoDomain,
		ArgoToken:       preservedToken,
		BlockQUIC:       body.BlockQUIC,
		RedactClientIP:  body.RedactClientIP,
	}
	if err := h.store.UpsertAdvanced(a); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	logredact.SetEnabled(body.RedactClientIP)
	h.auditLog(c, "advanced.upsert", "advanced", map[string]string{
		"cdn":        boolStr(body.CDNEnabled),
		"argo":       boolStr(body.ArgoEnabled),
		"block_quic": boolStr(body.BlockQUIC),
	})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"cdn_enabled": body.CDNEnabled, "argo_enabled": body.ArgoEnabled})
}

// loadOrNewAdvanced fetches the node's AdvancedConfig row, or a fresh zero-value
// one bound to the node when none exists yet. Granular PUTs read-modify-write
// through this so each touches only its own fields and preserves the rest —
// CDN / Argo / QUIC are independent concerns that used to share one PUT (and
// one validation pass), which is what blocked saving CDN once Argo went fixed.
func (h *Handler) loadOrNewAdvanced(nodeID uint) *model.AdvancedConfig {
	a, _ := h.store.GetAdvanced(nodeID)
	if a == nil {
		a = &model.AdvancedConfig{NodeID: nodeID}
	}
	return a
}

// countNonBlank counts entries that aren't empty/whitespace — used to reject
// "CDN enabled with an empty preferred-IP pool".
func countNonBlank(in []string) int {
	n := 0
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			n++
		}
	}
	return n
}

// cdnConfigDTO is the request for the CDN-only granular save.
type cdnConfigDTO struct {
	CDNEnabled      bool     `json:"cdn_enabled"`
	CDNPreferredIPs []string `json:"cdn_preferred_ips"`
}

// PutAdvancedCDN saves only the CDN preferred-IP config, preserving Argo/QUIC.
// No Argo validation runs here — that's the whole point of the split: saving
// CDN can never be blocked by Argo's fixed-mode token requirement.
//
// PUT /api/v1/advanced/cdn
func (h *Handler) PutAdvancedCDN(c *gin.Context) {
	var body cdnConfigDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	// Enabling CDN front with an empty preferred-IP pool is a no-op trap: the
	// resolver has nothing to swap the server address to, so clients dial the
	// inbound's real host and "CDN on" silently does nothing. Reject it.
	if body.CDNEnabled && countNonBlank(body.CDNPreferredIPs) == 0 {
		core.Fail(c, http.StatusBadRequest, "CDN_NO_IPS",
			"启用 CDN 前置时优选 IP 池不能为空,请先添加至少一个优选 IP/域名")
		return
	}
	nodeID := h.parseLocalNodeID()
	a := h.loadOrNewAdvanced(nodeID)
	a.CDNEnabled = body.CDNEnabled
	a.CDNPreferredIPs = encodeJSONStringSlice(body.CDNPreferredIPs)
	if err := h.store.UpsertAdvanced(a); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "advanced.cdn.save", "advanced", map[string]string{"cdn": boolStr(body.CDNEnabled)})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"cdn_enabled": body.CDNEnabled})
}

// argoConfigDTO is the request for the Argo-only granular save. ArgoToken is
// write-only: empty means "keep the stored one", not "clear it".
type argoConfigDTO struct {
	ArgoEnabled bool   `json:"argo_enabled"`
	ArgoMode    string `json:"argo_mode"`
	ArgoDomain  string `json:"argo_domain"`
	ArgoToken   string `json:"argo_token,omitempty"`
}

// PutAdvancedArgo saves only the Argo tunnel config, preserving CDN/QUIC. The
// fixed-mode token check validates the EFFECTIVE token — the one in the request
// OR the one already stored — so a re-save with the field left blank (the token
// is write-only and never echoed) no longer trips MISSING_ARGO_TOKEN.
//
// PUT /api/v1/advanced/argo
func (h *Handler) PutAdvancedArgo(c *gin.Context) {
	var body argoConfigDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	a := h.loadOrNewAdvanced(nodeID)

	// Effective token: caller-provided wins, else preserve the stored one.
	effToken := strings.TrimSpace(body.ArgoToken)
	if effToken == "" {
		effToken = a.ArgoToken
	}
	if body.ArgoEnabled {
		mode := strings.TrimSpace(body.ArgoMode)
		if mode != "temp" && mode != "fixed" {
			core.Fail(c, http.StatusBadRequest, "BAD_ARGO_MODE", "argo_mode must be 'temp' or 'fixed'")
			return
		}
		if mode == "fixed" && effToken == "" {
			core.Fail(c, http.StatusBadRequest, "MISSING_ARGO_TOKEN", "argo_token is required for fixed Argo tunnels")
			return
		}
		// Best-effort orange-cloud guard: a fixed tunnel's domain must be a CNAME
		// to the tunnel, NOT an A/AAAA record (CDN front / direct origin). Reject
		// the save when we can confirm a conflicting record (needs a stored CF API
		// token; silently skipped when unavailable — provision is the hard guard).
		if mode == "fixed" {
			if msg := h.argoDomainConflict(c.Request.Context(), body.ArgoDomain); msg != "" {
				core.Fail(c, http.StatusConflict, "DOMAIN_HAS_A_RECORD", msg)
				return
			}
		}
	}
	a.ArgoEnabled = body.ArgoEnabled
	a.ArgoMode = body.ArgoMode
	a.ArgoDomain = body.ArgoDomain
	a.ArgoToken = effToken
	if err := h.store.UpsertAdvanced(a); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "advanced.argo.save", "advanced", map[string]string{"argo": boolStr(body.ArgoEnabled)})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"argo_enabled": body.ArgoEnabled})
}

// quicConfigDTO is the request for the QUIC-hardening-only granular save.
type quicConfigDTO struct {
	BlockQUIC bool `json:"block_quic"`
}

// PutAdvancedQUIC saves only the QUIC/STUN-block toggle, preserving CDN/Argo.
//
// PUT /api/v1/advanced/quic
func (h *Handler) PutAdvancedQUIC(c *gin.Context) {
	var body quicConfigDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	a := h.loadOrNewAdvanced(nodeID)
	a.BlockQUIC = body.BlockQUIC
	if err := h.store.UpsertAdvanced(a); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "advanced.quic.save", "advanced", map[string]string{"block_quic": boolStr(body.BlockQUIC)})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"block_quic": body.BlockQUIC})
}

// logPrivacyDTO is the request for the log-privacy-only granular save.
type logPrivacyDTO struct {
	RedactClientIP bool `json:"redact_client_ip"`
}

// PutAdvancedLogPrivacy saves only the "don't log client IP" toggle, preserving
// CDN/Argo/QUIC. It deliberately does NOT re-apply the engine config: redaction
// happens in the log write path (internal/logredact), gated by a process-global
// atomic the running engines read on every write. So flipping it takes effect on
// the live log stream instantly — no re-render, no engine restart, sing-box.json
// untouched.
//
// PUT /api/v1/advanced/logprivacy
func (h *Handler) PutAdvancedLogPrivacy(c *gin.Context) {
	var body logPrivacyDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	a := h.loadOrNewAdvanced(nodeID)
	a.RedactClientIP = body.RedactClientIP
	if err := h.store.UpsertAdvanced(a); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	logredact.SetEnabled(body.RedactClientIP)
	h.auditLog(c, "advanced.logprivacy.save", "advanced", map[string]string{"redact_client_ip": boolStr(body.RedactClientIP)})
	core.OK(c, gin.H{"redact_client_ip": body.RedactClientIP})
}

// DeleteAdvanced clears every advanced toggle (turns everything off).
//
// DELETE /api/v1/advanced
func (h *Handler) DeleteAdvanced(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	if err := h.store.DeleteAdvanced(nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	logredact.SetEnabled(false)
	h.auditLog(c, "advanced.delete", "advanced", nil)
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"deleted": true})
}

// cdnPoolForLocalNode returns the Cloudflare anycast IPs the operator saved
// under Advanced → CDN preferred IPs for this node. Best-effort: any DB
// failure returns nil and CDN-mode inbounds fall back to the share host.
// Called by the subscription handler before constructing a Resolver.
//
// The global CDN switch (a.CDNEnabled) gates the whole pool: when the operator
// turns CDN off, we return nil so every cdn_mode inbound falls back to the VPS
// host immediately — otherwise a stale preferred-IP list would keep rewriting
// subscription hosts even though CDN is "off", which is the exact desync we hit
// in the field (the switch was decorative). One gate here covers all callers.
func (h *Handler) cdnPoolForLocalNode() []string {
	a, err := h.store.GetAdvanced(h.parseLocalNodeID())
	if err != nil || a == nil || !a.CDNEnabled {
		return nil
	}
	return parseJSONStringSliceAPI(a.CDNPreferredIPs)
}

func parseJSONStringSliceAPI(raw string) []string {
	if raw == "" {
		return nil
	}
	var v []string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

func encodeJSONStringSlice(v []string) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
