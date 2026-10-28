package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// fetchSubHeader serves GET /sub/:token and returns one response header.
func fetchSubHeader(t *testing.T, h *Handler, token, header string) string {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/sub/"+token, nil)
	c.Params = gin.Params{{Key: "token", Value: token}}
	h.PublicSubscription(c)
	if w.Code != 200 {
		t.Fatalf("subscription: code=%d body=%s", w.Code, w.Body.String())
	}
	return w.Header().Get(header)
}

func seedSubscription(t *testing.T, h *Handler) string {
	t.Helper()
	if err := h.store.SetSetting(KeyShareHost, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	in := &model.Inbound{
		NodeID: h.parseLocalNodeID(), Tag: "vless-ws", Engine: "singbox", Type: "vless-ws",
		Listen: "0.0.0.0", Port: 2083, Network: "tcp", Enabled: true,
		Settings: `{"ws_path":"/p"}`,
	}
	if err := h.store.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	cl := &model.Client{InboundID: in.ID, Email: "u@example.com",
		UUID: "33333333-3333-3333-3333-333333333333", Enabled: true}
	if err := h.store.CreateClient(cl); err != nil {
		t.Fatal(err)
	}
	token := "tok-cand"
	if err := h.store.CreateSubscription(&model.Subscription{
		Name: "u", TokenHash: store.HashToken(token), ClientID: cl.ID,
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestSubscription_CDNCandidatesHeaderFollowsCDNSwitch(t *testing.T) {
	h := newAdvancedTestHandler(t)
	token := seedSubscription(t, h)

	if got := fetchSubHeader(t, h, token, "X-EdgeNest-CDN-Candidates"); got != "" {
		t.Fatalf("CDN off should send no candidates, got %q", got)
	}

	if code, env := callJSON(t, h.PutAdvancedCDN,
		`{"cdn_enabled":true,"cdn_preferred_ips":["198.51.100.9"]}`); code != 200 || !env.Success {
		t.Fatalf("enable CDN: code=%d err=%q", code, errCode(env))
	}
	got := fetchSubHeader(t, h, token, "X-EdgeNest-CDN-Candidates")
	ips := strings.Split(got, ",")
	if len(ips) < 2 || ips[0] != "198.51.100.9" {
		t.Fatalf("want the operator pool first, then the curated list; got %q", got)
	}

	if code, env := callJSON(t, h.PutAdvancedCDN,
		`{"cdn_enabled":false,"cdn_preferred_ips":["198.51.100.9"]}`); code != 200 || !env.Success {
		t.Fatalf("disable CDN: code=%d err=%q", code, errCode(env))
	}
	if got := fetchSubHeader(t, h, token, "X-EdgeNest-CDN-Candidates"); got != "" {
		t.Fatalf("CDN switched off should drop the header, got %q", got)
	}
}
