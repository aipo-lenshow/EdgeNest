package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/wizard"
)

// WizardStatus reports whether first-run setup has been completed.
//
// GET /api/v1/wizard/status
func (h *Handler) WizardStatus(c *gin.Context) {
	if h.wiz == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"error":   gin.H{"code": "WIZARD_DISABLED", "message": "wizard not available"},
		})
		return
	}
	nodeID, err := strconv.ParseUint(h.localNodeID, 10, 64)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   gin.H{"code": "BAD_NODE_ID", "message": err.Error()},
		})
		return
	}
	st, err := h.wiz.Status(uint(nodeID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   gin.H{"code": "STATUS_ERROR", "message": err.Error()},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": st})
}

// WizardComplete provisions default VLESS-Reality + Hysteria2 inbounds and
// marks the wizard done. Refuses if wizard_done is already true.
//
// POST /api/v1/wizard/complete
func (h *Handler) WizardComplete(c *gin.Context) {
	if h.wiz == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"error":   gin.H{"code": "WIZARD_DISABLED", "message": "wizard not available"},
		})
		return
	}
	var req wizard.CompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   gin.H{"code": "BAD_REQUEST", "message": err.Error()},
		})
		return
	}
	nodeID, err := strconv.ParseUint(h.localNodeID, 10, 64)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   gin.H{"code": "BAD_NODE_ID", "message": err.Error()},
		})
		return
	}
	res, err := h.wiz.Complete(c.Request.Context(), uint(nodeID), req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   gin.H{"code": "WIZARD_FAILED", "message": err.Error()},
		})
		return
	}
	h.auditLog(c, "wizard.complete", "wizard", map[string]string{
		"client_email": res.ClientEmail,
	})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": res})
}

// WizardCreateFunnel runs the 4-step inbound wizard: provisions one inbound
// per selected protocol with the right per-protocol defaults applied, mints a
// subscription that bundles all of them, and pushes config to the node.
//
// POST /api/v1/wizard/create-funnel
func (h *Handler) WizardCreateFunnel(c *gin.Context) {
	if h.wiz == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"error":   gin.H{"code": "WIZARD_DISABLED", "message": "wizard not available"},
		})
		return
	}
	var req wizard.FunnelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   gin.H{"code": "BAD_REQUEST", "message": err.Error()},
		})
		return
	}
	req.PanelPort = h.panelPort
	nodeID, err := strconv.ParseUint(h.localNodeID, 10, 64)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   gin.H{"code": "BAD_NODE_ID", "message": err.Error()},
		})
		return
	}
	res, err := h.wiz.CreateFromFunnel(c.Request.Context(), uint(nodeID), req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   gin.H{"code": "FUNNEL_FAILED", "message": err.Error()},
		})
		return
	}
	h.auditLog(c, "wizard.create_funnel", "wizard", map[string]string{
		"client_email": res.ClientEmail,
		"protocols":    fmt.Sprintf("%d", len(res.Inbounds)),
	})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": res})
}

// WizardValidateDomain runs Step-2's DNS check and returns one of
// ok / proxied / mismatch / none. The verdict drives Step-4 visibility
// (CDN/Argo toggles) and lets the wizard recommend the right protocol mix.
//
// POST /api/v1/wizard/validate-domain
//
//	body: {"domain": "panel.example.com"}
//	resp: {"status":"ok|proxied|mismatch|none","domain":..,
//	       "resolved_ips":[..],"vps_public_ip":".."}
func (h *Handler) WizardValidateDomain(c *gin.Context) {
	if h.wiz == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"error":   gin.H{"code": "WIZARD_DISABLED", "message": "wizard not available"},
		})
		return
	}
	var body struct {
		Domain string `json:"domain"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   gin.H{"code": "BAD_REQUEST", "message": err.Error()},
		})
		return
	}
	body.Domain = strings.TrimSpace(body.Domain)
	res, err := h.wiz.ValidateDomain(c.Request.Context(), body.Domain)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"success": false,
			"error":   gin.H{"code": "VALIDATE_FAILED", "message": err.Error()},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": res})
}
