package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPanelGateToken_DeterministicAndKeyed(t *testing.T) {
	h := &Handler{jwtSecret: "secret-A"}
	t1a := h.panelGateToken("/ENPanel-aaaa")
	t1b := h.panelGateToken("/ENPanel-aaaa")
	if t1a != t1b {
		t.Errorf("token not deterministic: %q vs %q", t1a, t1b)
	}

	t2 := h.panelGateToken("/ENPanel-bbbb")
	if t1a == t2 {
		t.Errorf("rotating panel path should produce a new token, both = %q", t1a)
	}

	h2 := &Handler{jwtSecret: "secret-B"}
	if h2.panelGateToken("/ENPanel-aaaa") == t1a {
		t.Errorf("changing jwt secret should invalidate the cookie")
	}
}

func TestPanelGate_SetGateCookie_Attributes(t *testing.T) {
	h := &Handler{jwtSecret: "k"}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ENPanel-abcdef", nil)

	c, _ := gin.CreateTestContext(w)
	c.Request = req
	h.setGateCookie(c, "deadbeef")

	resp := w.Result()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want 1 cookie, got %d", len(cookies))
	}
	got := cookies[0]
	if got.Name != panelGateCookieName {
		t.Errorf("cookie name = %q", got.Name)
	}
	if got.Value != "deadbeef" {
		t.Errorf("cookie value = %q", got.Value)
	}
	if got.Path != "/" {
		t.Errorf("cookie path = %q", got.Path)
	}
	if !got.HttpOnly {
		t.Errorf("cookie should be HttpOnly")
	}
	// req has no TLS, so Secure should be false.
	if got.Secure {
		t.Errorf("cookie Secure should be false over plain HTTP")
	}
}
