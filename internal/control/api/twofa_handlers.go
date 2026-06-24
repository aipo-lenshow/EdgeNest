package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// twofaIssuer is the label shown in the authenticator app entry.
const twofaIssuer = "EdgeNest"

// TwoFASetup mints a fresh TOTP secret and stashes it as the admin's *pending*
// secret (not yet active). The response carries the secret + otpauth:// URI so
// the front-end can render a QR code; enrollment only completes once the
// operator proves they can produce a code via TwoFAEnable. Re-calling overwrites
// any prior pending secret, so an abandoned setup is harmless.
//
// POST /api/v1/2fa/setup
func (h *Handler) TwoFASetup(c *gin.Context) {
	admin := h.currentAdmin(c)
	if admin == nil {
		return
	}
	if admin.TOTPEnabled {
		core.Fail(c, http.StatusConflict, "ALREADY_ENABLED",
			"2FA is already enabled; disable it first to re-enroll")
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "RAND_ERROR", err.Error())
		return
	}
	admin.TOTPPending = secret
	if err := h.store.UpdateAdmin(admin); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, gin.H{
		"secret": secret,
		"uri":    auth.TOTPURI(twofaIssuer, admin.Username, secret),
	})
}

type twofaCodeReq struct {
	Code string `json:"code"`
}

// TwoFAEnable confirms the pending secret by verifying a live code, flips 2FA
// on, and returns a fresh batch of single-use recovery codes (shown exactly
// once). Without a matching code the secret stays pending and 2FA stays off.
//
// POST /api/v1/2fa/enable
func (h *Handler) TwoFAEnable(c *gin.Context) {
	admin := h.currentAdmin(c)
	if admin == nil {
		return
	}
	var req twofaCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", "invalid request body")
		return
	}
	if admin.TOTPPending == "" {
		core.Fail(c, http.StatusBadRequest, "NO_PENDING",
			"call /2fa/setup first to get a secret")
		return
	}
	if !auth.VerifyTOTP(admin.TOTPPending, req.Code) {
		core.Fail(c, http.StatusBadRequest, "BAD_CODE",
			"code did not match — check your authenticator and try again")
		return
	}
	codes, err := auth.GenerateRecoveryCodes(10)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "RAND_ERROR", err.Error())
		return
	}
	enc, _ := json.Marshal(codes)
	admin.TOTPSecret = admin.TOTPPending
	admin.TOTPPending = ""
	admin.TOTPEnabled = true
	admin.RecoveryCodes = string(enc)
	if err := h.store.UpdateAdmin(admin); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "admin.2fa.enable", "admin", nil)
	core.OK(c, gin.H{"enabled": true, "recovery_codes": codes})
}

type twofaDisableReq struct {
	Password string `json:"password"`
}

// TwoFADisable turns 2FA off. Requires the current password (a sensitive
// operation that weakens account security, so re-auth is mandatory).
//
// POST /api/v1/2fa/disable
func (h *Handler) TwoFADisable(c *gin.Context) {
	admin := h.currentAdmin(c)
	if admin == nil {
		return
	}
	var req twofaDisableReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", "invalid request body")
		return
	}
	if !auth.CheckPassword(admin.PasswordHash, req.Password) {
		core.Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "password incorrect")
		return
	}
	admin.TOTPEnabled = false
	admin.TOTPSecret = ""
	admin.TOTPPending = ""
	admin.RecoveryCodes = ""
	if err := h.store.UpdateAdmin(admin); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "admin.2fa.disable", "admin", nil)
	core.OK(c, gin.H{"enabled": false})
}

// TwoFARegenCodes mints a fresh batch of recovery codes (invalidating the old
// set). Requires the current password. Only valid while 2FA is enabled.
//
// POST /api/v1/2fa/recovery-codes
func (h *Handler) TwoFARegenCodes(c *gin.Context) {
	admin := h.currentAdmin(c)
	if admin == nil {
		return
	}
	var req twofaDisableReq // reuse: {password}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", "invalid request body")
		return
	}
	if !admin.TOTPEnabled {
		core.Fail(c, http.StatusBadRequest, "NOT_ENABLED", "2FA is not enabled")
		return
	}
	if !auth.CheckPassword(admin.PasswordHash, req.Password) {
		core.Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "password incorrect")
		return
	}
	codes, err := auth.GenerateRecoveryCodes(10)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "RAND_ERROR", err.Error())
		return
	}
	enc, _ := json.Marshal(codes)
	admin.RecoveryCodes = string(enc)
	if err := h.store.UpdateAdmin(admin); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "admin.2fa.regen_codes", "admin", nil)
	core.OK(c, gin.H{"recovery_codes": codes})
}

// currentAdmin loads the authenticated admin from the JWT username claim,
// writing a 401 and returning nil on failure so callers can `if a == nil { return }`.
func (h *Handler) currentAdmin(c *gin.Context) *model.Admin {
	username, _ := c.Get("username")
	name, _ := username.(string)
	admin, err := h.store.GetAdminByUsername(name)
	if err != nil {
		core.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "admin not found")
		return nil
	}
	return admin
}

// verifyTwoFA validates a login second factor for an admin with 2FA enabled.
// Accepts a live TOTP code OR a single-use recovery code (which it consumes,
// persisting the shrunken set). Returns true on success.
func (h *Handler) verifyTwoFA(admin *model.Admin, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	if auth.VerifyTOTP(admin.TOTPSecret, code) {
		return true
	}
	// Recovery-code path: match + consume.
	var codes []string
	if admin.RecoveryCodes != "" {
		_ = json.Unmarshal([]byte(admin.RecoveryCodes), &codes)
	}
	for i, rc := range codes {
		if strings.EqualFold(strings.TrimSpace(rc), code) {
			codes = append(codes[:i], codes[i+1:]...)
			enc, _ := json.Marshal(codes)
			admin.RecoveryCodes = string(enc)
			_ = h.store.UpdateAdmin(admin)
			return true
		}
	}
	return false
}
