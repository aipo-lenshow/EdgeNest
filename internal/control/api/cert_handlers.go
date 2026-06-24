package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// ---- Cert CRUD / actions ----

type certIssueRequest struct {
	Domain      string            `json:"domain"`
	Email       string            `json:"email"`        // optional; falls back to acme_email setting
	Mode        string            `json:"mode"`         // http-01 (default) | dns-01
	DNSProvider string            `json:"dns_provider"` // cloudflare | aliyun | ...
	DNSConfig   map[string]string `json:"dns_config"`
	HTTPPort    int               `json:"http_port"` // HTTP-01 listener; 0 = 80
}

// IssueCert issues a new certificate (or re-issues for an existing domain).
// Long-running: blocks until the ACME flow completes (or fails).
//
// POST /api/v1/certs
func (h *Handler) IssueCert(c *gin.Context) {
	if h.certMgr == nil {
		core.Fail(c, http.StatusServiceUnavailable, "ACME_DISABLED", "cert manager not configured")
		return
	}
	var req certIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if req.Domain == "" {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "domain is required")
		return
	}
	if req.Email == "" {
		req.Email, _ = h.store.GetSetting("acme_email")
	}
	if req.Email == "" {
		core.Fail(c, http.StatusBadRequest, "ACME_EMAIL_UNSET",
			"set acme_email in settings (or provide 'email' in request)")
		return
	}
	nodeID := h.parseLocalNodeID()
	out, err := h.certMgr.Issue(c.Request.Context(), nodeID, cert.IssueRequest{
		Domain:      req.Domain,
		Email:       req.Email,
		Mode:        req.Mode,
		DNSProvider: req.DNSProvider,
		DNSConfig:   req.DNSConfig,
		HTTPPort:    req.HTTPPort,
	})
	if err != nil {
		h.auditLog(c, "cert.issue", "cert:"+req.Domain, map[string]string{
			"error": err.Error(), "mode": req.Mode,
		})
		core.Fail(c, http.StatusBadGateway, "ACME_FAILED", err.Error())
		return
	}
	h.auditLog(c, "cert.issue", "cert:"+strconv.Itoa(int(out.ID)), map[string]string{
		"domain": out.Domain, "mode": out.Mode,
	})
	core.Created(c, out)
}

// ListDNSProviders returns the curated DNS-01 provider catalog (provider name +
// the credential fields it needs) so the UI can render a picker with the right
// inputs instead of a raw JSON box.
//
// GET /api/v1/certs/dns-providers
func (h *Handler) ListDNSProviders(c *gin.Context) {
	core.OK(c, cert.DNSProviders())
}

// ListCerts returns all certificates on the local node.
//
// GET /api/v1/certs
func (h *Handler) ListCerts(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	cs, err := h.store.ListCertificates(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, cs)
}

// RenewCert force-renews a single cert immediately.
//
// POST /api/v1/certs/:id/renew
func (h *Handler) RenewCert(c *gin.Context) {
	if h.certMgr == nil {
		core.Fail(c, http.StatusServiceUnavailable, "ACME_DISABLED", "cert manager not configured")
		return
	}
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	out, err := h.certMgr.Renew(c.Request.Context(), id)
	if err != nil {
		h.auditLog(c, "cert.renew", "cert:"+strconv.Itoa(int(id)),
			map[string]string{"error": err.Error()})
		core.Fail(c, http.StatusBadGateway, "ACME_FAILED", err.Error())
		return
	}
	h.auditLog(c, "cert.renew", "cert:"+strconv.Itoa(int(id)), nil)
	core.OK(c, out)
}

type certAutoRenewRequest struct {
	AutoRenew bool `json:"auto_renew"`
}

// SetCertAutoRenew toggles a cert's background auto-renewal.
//
// PATCH /api/v1/certs/:id/auto-renew
func (h *Handler) SetCertAutoRenew(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req certAutoRenewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	out, err := h.store.SetCertAutoRenew(id, req.AutoRenew)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	h.auditLog(c, "cert.auto_renew", "cert:"+strconv.Itoa(int(id)),
		map[string]string{"auto_renew": strconv.FormatBool(req.AutoRenew)})
	core.OK(c, out)
}

// DeleteCert removes a cert row. Files on disk are left in place so an inbound
// using them keeps working until its Settings are updated (manual hand-off).
//
// DELETE /api/v1/certs/:id
func (h *Handler) DeleteCert(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.store.DB().Delete(&[]any{}, id).Error; err != nil {
		// Fall through — using store.DB() Delete on a slice is awkward.
	}
	// Use direct delete on Certificate model.
	if err := h.deleteCertRow(id); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "cert.delete", "cert:"+strconv.Itoa(int(id)), nil)
	core.OK(c, gin.H{"id": id})
}

func (h *Handler) deleteCertRow(id uint) error {
	// Inline (no Store helper yet) — keeps cert CRUD self-contained.
	return h.store.DB().Exec("DELETE FROM certificates WHERE id = ?", id).Error
}
