package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
)

// Panel-path obscurity is the only thing standing between an unauthenticated
// HTTP probe and the login form. Without it, anyone who finds the panel port
// can hit /login or / and start guessing passwords; the random ENPanel-XXXX
// path raises that bar from "any scanner" to "someone who knows the secret".
//
// Implementation: on first hit to the configured panel path we set a signed
// cookie tied to that path; subsequent SPA requests check the cookie.
// Rotating the panel path changes the HMAC so old cookies stop validating
// without us touching a cookie store.
const panelGateCookieName = "edgenest_gate"

// panelGateToken signs panelPath with the JWT secret so cookies invalidate
// automatically when the operator rotates the path via Settings.
func (h *Handler) panelGateToken(panelPath string) string {
	mac := hmac.New(sha256.New, []byte(h.jwtSecret))
	mac.Write([]byte(panelPath))
	return hex.EncodeToString(mac.Sum(nil))
}

// checkPanelGate decides whether an SPA-style request may proceed.
// Returns true if the request URL matches the configured panel path (in
// which case the gate cookie is issued as a side effect) or carries a
// previously-issued cookie. Returns false when the caller should respond
// 404 — the goal is to look indistinguishable from an unused port.
func (h *Handler) checkPanelGate(c *gin.Context) bool {
	panelPath, _ := h.store.GetSetting(bootstrap.KeyPanelPath)
	if panelPath == "" {
		// Bootstrap hasn't seeded a panel path yet; fail-open so the operator
		// can still recover.
		return true
	}
	expected := h.panelGateToken(panelPath)
	p := c.Request.URL.Path
	if p == panelPath || strings.HasPrefix(p, panelPath+"/") {
		h.setGateCookie(c, expected)
		return true
	}
	cookie, _ := c.Cookie(panelGateCookieName)
	if cookie == "" {
		return false
	}
	return hmac.Equal([]byte(cookie), []byte(expected))
}

func (h *Handler) setGateCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     panelGateCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 30,
	})
}
