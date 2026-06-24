package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
	"github.com/aipo-lenshow/EdgeNest/internal/control/system"
	"github.com/aipo-lenshow/EdgeNest/internal/control/updatecheck"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/gin-gonic/gin"
)

// Version is set by main on startup (linker-injected via -X main.version).
// We accept it as a package-level var rather than another HandlerDeps field
// to keep /api/health a single liner.
var Version = "dev"

// Health is a public health check. Returns version so operators can verify
// the binary they're talking to matches the release they intended to deploy,
// plus the cached latest release tag (when update checking is on and a newer
// version exists) so the About page can surface an upgrade hint.
func (h *Handler) Health(c *gin.Context) {
	latest, updateAvailable := updatecheck.Status(h.store, Version)
	core.OK(c, gin.H{
		"status":           "ok",
		"version":          Version,
		"latest_version":   latest,
		"update_available": updateAvailable,
		"time":             time.Now().Unix(),
	})
}

// ---- Auth ----

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// TOTPCode is the optional second factor. When an admin has 2FA enabled the
	// first call (correct password, no code) returns {totp_required:true} with
	// no token; the client then re-posts with the code filled in.
	TOTPCode string `json:"totp_code"`
}

// Login authenticates an admin and returns a JWT. For 2FA-enabled admins it is
// a two-step exchange: password first (reveals totp_required only AFTER the
// password checks out, so an attacker can't probe which accounts have 2FA),
// then the TOTP / recovery code.
func (h *Handler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	admin, err := h.store.GetAdminByUsername(req.Username)
	if err != nil || !auth.CheckPassword(admin.PasswordHash, req.Password) {
		core.Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "wrong username or password")
		return
	}
	if admin.TOTPEnabled {
		if strings.TrimSpace(req.TOTPCode) == "" {
			// Password is correct but the second factor is still needed. 200 with
			// no token — the client shows the code prompt and re-submits.
			core.OK(c, gin.H{"totp_required": true})
			return
		}
		if !h.verifyTwoFA(admin, req.TOTPCode) {
			core.Fail(c, http.StatusUnauthorized, "INVALID_2FA",
				"two-factor code is wrong or expired")
			return
		}
	}
	token, err := auth.IssueToken(h.jwtSecret, admin.Username, 24*time.Hour)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "TOKEN_ERROR", "failed to issue token")
		return
	}
	core.OK(c, gin.H{
		"token":                token,
		"must_change_password": admin.MustChangePassword,
	})
}

// Logout is a no-op for stateless JWT (client discards the token).
func (h *Handler) Logout(c *gin.Context) {
	core.OK(c, gin.H{"ok": true})
}

// Me returns the current admin plus panel state (wizard done, run mode).
func (h *Handler) Me(c *gin.Context) {
	username, _ := c.Get("username")
	admin, err := h.store.GetAdminByUsername(username.(string))
	if err != nil {
		core.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "admin not found")
		return
	}
	wizardDone, _ := h.store.GetSetting(bootstrap.KeyWizardDone)
	core.OK(c, gin.H{
		"username":             admin.Username,
		"must_change_password": admin.MustChangePassword,
		"wizard_done":          wizardDone == "true",
		"run_mode":             "standalone",
		"totp_enabled":         admin.TOTPEnabled,
	})
}

type passwordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ChangePassword updates the admin password and clears the must-change flag.
func (h *Handler) ChangePassword(c *gin.Context) {
	var req passwordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if len(req.NewPassword) < 8 {
		core.Fail(c, http.StatusBadRequest, "WEAK_PASSWORD", "password must be at least 8 characters")
		return
	}
	username, _ := c.Get("username")
	admin, err := h.store.GetAdminByUsername(username.(string))
	if err != nil {
		core.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "admin not found")
		return
	}
	if !auth.CheckPassword(admin.PasswordHash, req.OldPassword) {
		core.Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "old password incorrect")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "HASH_ERROR", "failed to hash password")
		return
	}
	admin.PasswordHash = hash
	admin.MustChangePassword = false
	if err := h.store.UpdateAdmin(admin); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", "failed to update admin")
		return
	}
	core.OK(c, gin.H{"ok": true})
}

// ---- Dashboard / nodes / engine (scaffold-level) ----

// Dashboard returns a minimal status snapshot via the node client (proves the
// seam works end to end).
func (h *Handler) Dashboard(c *gin.Context) {
	status, err := h.node.EngineStatus(c.Request.Context(), h.localNodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "NODE_ERROR", err.Error())
		return
	}
	health, _ := h.node.Heartbeat(c.Request.Context(), h.localNodeID)
	// node.Health() leaves BBR as a placeholder so the node plane stays free
	// of /proc/sys reads — read the real state from the control side here.
	health.BBR = bbrSummary(system.ReadBBRState())
	nodes, _ := h.store.ListNodes()
	core.OK(c, gin.H{
		"engine": status,
		"health": health,
		"nodes":  len(nodes),
	})
}

// bbrSummary collapses ReadBBRState() into a single token suitable for the
// dashboard Stat card: "bbr+fq" / "cubic+fq_codel" when both sysctls are
// readable, "unsupported" on non-Linux dev hosts, "unknown" if /proc reads
// fail (container without /proc, restricted FS, etc.).
func bbrSummary(st system.BBRState) string {
	if !st.Supported {
		return "unsupported"
	}
	cc := st.CongestionControl
	q := st.DefaultQdisc
	if cc == "" && q == "" {
		return "unknown"
	}
	if cc == "" {
		cc = "?"
	}
	if q == "" {
		return cc
	}
	return cc + "+" + q
}

// ListNodes returns all managed nodes (Lite: just "local").
func (h *Handler) ListNodes(c *gin.Context) {
	nodes, err := h.store.ListNodes()
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, nodes)
}

// EngineStatus reports proxy engine status for the local node.
func (h *Handler) EngineStatus(c *gin.Context) {
	status, err := h.node.EngineStatus(c.Request.Context(), h.localNodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "NODE_ERROR", err.Error())
		return
	}
	core.OK(c, status)
}

// RestartEngine restarts the proxy engine on the local node.
func (h *Handler) RestartEngine(c *gin.Context) {
	if err := h.node.RestartEngine(c.Request.Context(), h.localNodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "NODE_ERROR", err.Error())
		return
	}
	core.OK(c, gin.H{"ok": true})
}
