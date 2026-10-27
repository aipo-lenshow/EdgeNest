package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// A save that leaves a field out (an older panel page, a script) keeps the
// stored value instead of resetting it — here the gateway mode that keeps
// EdgeNest off another web server's port 443.
func TestPutAntiBlockKeepsOmittedFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = antiblock.Save(st, antiblock.Config{EdgeDomain: "cdn.example.com", GatewayMode: antiblock.GatewayExternal, DirectDomain: "n1.example.com"})
	h := &Handler{store: st}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/api/v1/antiblock", strings.NewReader(`{"edge_domain":"cdn.example.com","sub_base_url":"https://cdn.example.com"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PutAntiBlock(c)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := antiblock.Load(st)
	if got.GatewayMode != antiblock.GatewayExternal || got.DirectDomain != "n1.example.com" {
		t.Fatalf("omitted fields were reset: %+v", got)
	}
	if got.SubBaseURL != "https://cdn.example.com" {
		t.Fatalf("sent field not applied: %+v", got)
	}
}

func TestVerifyCloudflareToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good" {
			_, _ = w.Write([]byte(`{"success":true,"result":{"status":"active"}}`))
			return
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"success":false,"result":null}`))
	}))
	defer srv.Close()
	old := cfVerifyURL
	cfVerifyURL = srv.URL
	defer func() { cfVerifyURL = old }()
	if err := verifyCloudflareToken(context.Background(), "good"); err != nil {
		t.Fatalf("good token rejected: %v", err)
	}
	if err := verifyCloudflareToken(context.Background(), "bad"); err == nil {
		t.Fatal("bad token accepted")
	}
}
