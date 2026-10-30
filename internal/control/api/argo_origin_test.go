package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func TestDecodeRunToken(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"a": "acct", "t": "tun-1", "s": "secret"})
	for _, enc := range []string{base64.StdEncoding.EncodeToString(raw), base64.RawStdEncoding.EncodeToString(raw)} {
		a, tun, err := decodeRunToken(enc)
		if err != nil || a != "acct" || tun != "tun-1" {
			t.Errorf("decodeRunToken(%q) = %q %q %v", enc, a, tun, err)
		}
	}
	if _, _, err := decodeRunToken("not-a-token"); err == nil {
		t.Error("garbage token accepted")
	}
}

// TestSetArgoOrigin_MovesTunnelToCDNInbound: with a quick tunnel (no
// Cloudflare-side ingress to update), pointing the tunnel at the CDN inbound
// moves the Argo flag there, disables the dedicated Argo inbound, and adds its
// users to the CDN inbound. Switching back re-enables it.
func TestSetArgoOrigin_MovesTunnelToCDNInbound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	node, _ := st.EnsureLocalNode()
	h := &Handler{store: st, localNodeID: strconv.FormatUint(uint64(node.ID), 10)}
	_ = st.UpsertAdvanced(&model.AdvancedConfig{NodeID: node.ID, ArgoMode: "temp"})

	argoIn := &model.Inbound{NodeID: node.ID, Tag: "argo", Engine: "singbox", Type: "vless-ws",
		Listen: "127.0.0.1", Port: 2083, Network: "tcp", Enabled: true,
		Settings: `{"argo_bound":"true","ws_path":"/a"}`}
	cdnIn := &model.Inbound{NodeID: node.ID, Tag: "cdn", Engine: "singbox", Type: "vless-ws",
		Listen: "127.0.0.1", Port: 24936, Network: "tcp", Enabled: true,
		Settings: `{"edge_bound":"true","ws_path":"/c"}`}
	for _, in := range []*model.Inbound{argoIn, cdnIn} {
		if err := st.CreateInbound(in); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range []model.Client{
		{InboundID: argoIn.ID, Email: "both@x", UUID: "11111111-1111-1111-1111-111111111111", Enabled: true},
		{InboundID: cdnIn.ID, Email: "both@x", UUID: "22222222-2222-2222-2222-222222222222", Enabled: true},
		{InboundID: argoIn.ID, Email: "argo-only@x", UUID: "33333333-3333-3333-3333-333333333333", Enabled: true},
	} {
		c := c
		if err := st.CreateClient(&c); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}

	put := func(id uint) map[string]any {
		b, _ := json.Marshal(argoOriginBody{InboundID: id})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/argo/origin", bytes.NewReader(b))
		c.Request.Header.Set("Content-Type", "application/json")
		h.SetArgoOrigin(c)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT origin %d: %d %s", id, w.Code, w.Body.String())
		}
		var resp struct{ Data map[string]any }
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Data
	}

	res := put(cdnIn.ID)
	if res["moved_users"].(float64) != 1 || res["ingress"] != "temp" {
		t.Errorf("result = %v", res)
	}
	a, _ := st.GetInbound(argoIn.ID)
	cdn, _ := st.GetInbound(cdnIn.ID)
	if a.Enabled || settingTruthy(inboundSettingsMap(a)["argo_bound"]) {
		t.Errorf("old Argo inbound still live: enabled=%v settings=%s", a.Enabled, a.Settings)
	}
	cs := inboundSettingsMap(cdn)
	if !settingTruthy(cs["argo_bound"]) || !settingTruthy(cs["edge_bound"]) || !cdn.Enabled {
		t.Errorf("CDN inbound not the origin: %s", cdn.Settings)
	}
	if p, ok := h.argoInboundPort(node.ID); !ok || p != 24936 {
		t.Errorf("tunnel port = %d %v", p, ok)
	}
	clients, _ := st.ListClients(cdnIn.ID)
	emails := map[string]bool{}
	for _, c := range clients {
		emails[c.Email] = true
	}
	if len(clients) != 2 || !emails["argo-only@x"] {
		t.Errorf("CDN inbound users = %v", emails)
	}

	// And back: the dedicated inbound is re-enabled, the CDN inbound keeps
	// serving CDN only.
	put(argoIn.ID)
	a, _ = st.GetInbound(argoIn.ID)
	cdn, _ = st.GetInbound(cdnIn.ID)
	if !a.Enabled || !settingTruthy(inboundSettingsMap(a)["argo_bound"]) {
		t.Errorf("switch back did not restore the Argo inbound: %s", a.Settings)
	}
	if settingTruthy(inboundSettingsMap(cdn)["argo_bound"]) || !cdn.Enabled {
		t.Errorf("CDN inbound after switch back: enabled=%v %s", cdn.Enabled, cdn.Settings)
	}
}

func TestSetArgoOrigin_FixedTunnelWithoutAPITokenAsksForManualIngress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	node, _ := st.EnsureLocalNode()
	h := &Handler{store: st, localNodeID: strconv.FormatUint(uint64(node.ID), 10)}
	raw, _ := json.Marshal(map[string]string{"a": "acct", "t": "tun", "s": "x"})
	_ = st.UpsertAdvanced(&model.AdvancedConfig{NodeID: node.ID, ArgoMode: "fixed",
		ArgoDomain: "tunnel.example.com", ArgoToken: base64.StdEncoding.EncodeToString(raw)})
	cdnIn := &model.Inbound{NodeID: node.ID, Tag: "cdn", Engine: "singbox", Type: "vless-ws",
		Listen: "127.0.0.1", Port: 24936, Network: "tcp", Enabled: true,
		Settings: `{"edge_bound":"true","ws_path":"/c"}`}
	_ = st.CreateInbound(cdnIn)

	send := func(done bool) *httptest.ResponseRecorder {
		b, _ := json.Marshal(argoOriginBody{InboundID: cdnIn.ID, IngressDone: done})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/argo/origin", bytes.NewReader(b))
		c.Request.Header.Set("Content-Type", "application/json")
		h.SetArgoOrigin(c)
		return w
	}
	if w := send(false); w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte("ARGO_INGRESS_MANUAL")) {
		t.Fatalf("want ARGO_INGRESS_MANUAL, got %d %s", w.Code, w.Body.String())
	}
	if cdn, _ := st.GetInbound(cdnIn.ID); settingTruthy(inboundSettingsMap(cdn)["argo_bound"]) {
		t.Fatal("flag moved although the ingress was not updated")
	}
	if w := send(true); w.Code != http.StatusOK {
		t.Fatalf("manual confirmation refused: %d %s", w.Code, w.Body.String())
	}
}
