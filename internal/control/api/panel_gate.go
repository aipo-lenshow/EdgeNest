package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
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

// PanelRequestAllowed is checkPanelGate for the built-in edge gateway, which
// sees plain *http.Request: the secret panel path itself, or any request that
// carries the gate cookie that path issued. Everything else stays a 404 there.
func (h *Handler) PanelRequestAllowed(r *http.Request) bool {
	panelPath, _ := h.store.GetSetting(bootstrap.KeyPanelPath)
	if panelPath == "" {
		return false // never fail open on the public edge
	}
	if p := r.URL.Path; p == panelPath || strings.HasPrefix(p, panelPath+"/") {
		return true
	}
	ck, err := r.Cookie(panelGateCookieName)
	if err != nil || ck.Value == "" {
		return false
	}
	return hmac.Equal([]byte(ck.Value), []byte(h.panelGateToken(panelPath)))
}

// PanelPath is the secret panel path, for the URL the gateway advertises.
func (h *Handler) PanelPath() string {
	p, _ := h.store.GetSetting(bootstrap.KeyPanelPath)
	return p
}

// GuardPanelListener wraps the panel's own listener: while "panel only via
// the gateway" is on, connections from anywhere but loopback are closed
// before a byte is read, so the panel port looks closed from outside. The
// switch is read on every accept, so toggling it needs no restart.
func (h *Handler) GuardPanelListener(ln net.Listener) net.Listener {
	h.panelGatewayOnly.Store(antiblock.Load(h.store).PanelViaGatewayOnly)
	return &panelListener{Listener: ln, only: &h.panelGatewayOnly}
}

type panelListener struct {
	net.Listener
	only *atomic.Bool
}

func (l *panelListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return c, err
		}
		if ap, perr := netip.ParseAddrPort(c.RemoteAddr().String()); perr == nil && ap.Addr().Unmap().IsLoopback() {
			return c, nil
		}
		if l.only.Load() {
			_ = c.Close()
			continue
		}
		return &panelConn{Conn: c, only: l.only}, nil
	}
}

// panelConn is an outside connection accepted while the panel port was open.
// Browsers keep such a connection alive indefinitely (the panel polls), so
// turning the switch on must also end connections that already exist: the
// next read after the switch flips closes it, after the response in flight
// (the one that saved the switch) has gone out.
type panelConn struct {
	net.Conn
	only *atomic.Bool
}

func (c *panelConn) Read(p []byte) (int, error) {
	if c.only.Load() {
		_ = c.Conn.Close()
		return 0, net.ErrClosed
	}
	return c.Conn.Read(p)
}
