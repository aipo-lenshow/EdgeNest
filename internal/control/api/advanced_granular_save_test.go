package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/gin-gonic/gin"
)

// newAdvancedTestHandler opens a real store in a temp dir, ensures the local
// node, and returns a Handler with no orchestrator wired (so applyAfterChange
// is a silent no-op) — enough to exercise the granular advanced save handlers
// end to end over httptest.
func newAdvancedTestHandler(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	node, err := st.EnsureLocalNode()
	if err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	return &Handler{store: st, localNodeID: fmt.Sprint(node.ID)}
}

// callJSON drives a handler with a JSON body and returns the decoded envelope.
func callJSON(t *testing.T, fn gin.HandlerFunc, body string) (int, core.APIResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/x", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	fn(c)
	var env core.APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return w.Code, env
}

func errCode(env core.APIResponse) string {
	if env.Error == nil {
		return ""
	}
	return env.Error.Code
}

// TestGranularSave_FixedArgoThenCDN is the regression guard for the reported
// bug: in fixed mode, after a token is stored, saving the (independent) CDN
// config must NOT trip MISSING_ARGO_TOKEN — the granular CDN save runs no Argo
// validation at all. It then re-saves Argo with a blank token (write-only field
// left empty) and confirms the stored token is preserved, not re-validated away.
func TestGranularSave_FixedArgoThenCDN(t *testing.T) {
	h := newAdvancedTestHandler(t)

	// 1. Save fixed-mode Argo WITH a token — must succeed.
	code, env := callJSON(t, h.PutAdvancedArgo,
		`{"argo_enabled":true,"argo_mode":"fixed","argo_domain":"t.example.com","argo_token":"eyJhbGciOiJ.fake.tunnel.token"}`)
	if code != 200 || !env.Success {
		t.Fatalf("save fixed argo+token: code=%d err=%q", code, errCode(env))
	}

	// 2. Save CDN — must NOT be blocked by Argo's fixed-mode token rule.
	code, env = callJSON(t, h.PutAdvancedCDN,
		`{"cdn_enabled":true,"cdn_preferred_ips":["104.16.0.1","104.17.0.1"]}`)
	if code != 200 || !env.Success {
		t.Fatalf("save CDN after fixed argo: code=%d err=%q (must not be MISSING_ARGO_TOKEN)", code, errCode(env))
	}
	if errCode(env) == "MISSING_ARGO_TOKEN" {
		t.Fatal("CDN save wrongly tripped MISSING_ARGO_TOKEN")
	}

	// 3. Re-save fixed Argo with BLANK token (UI never echoes the secret back).
	//    Effective token = the stored one, so this must pass.
	code, env = callJSON(t, h.PutAdvancedArgo,
		`{"argo_enabled":true,"argo_mode":"fixed","argo_domain":"t.example.com","argo_token":""}`)
	if code != 200 || !env.Success {
		t.Fatalf("re-save fixed argo with blank token: code=%d err=%q (stored token should be preserved)", code, errCode(env))
	}

	// 4. Confirm both slices survived independently.
	got, err := h.store.GetAdvanced(h.parseLocalNodeID())
	if err != nil || got == nil {
		t.Fatalf("get advanced: %v", err)
	}
	if !got.CDNEnabled {
		t.Error("CDN slice lost after Argo re-save")
	}
	if !got.ArgoEnabled || got.ArgoMode != "fixed" || got.ArgoToken == "" {
		t.Errorf("Argo slice corrupted: enabled=%v mode=%q tokenEmpty=%v",
			got.ArgoEnabled, got.ArgoMode, got.ArgoToken == "")
	}
}

// TestGranularSave_FixedArgoNoTokenEverStillRejects keeps the guard honest: if
// NO token was ever stored, fixed mode with a blank token must still be refused.
func TestGranularSave_FixedArgoNoTokenEverStillRejects(t *testing.T) {
	h := newAdvancedTestHandler(t)
	code, env := callJSON(t, h.PutAdvancedArgo,
		`{"argo_enabled":true,"argo_mode":"fixed","argo_domain":"t.example.com","argo_token":""}`)
	if code == 200 || errCode(env) != "MISSING_ARGO_TOKEN" {
		t.Fatalf("fixed mode with no token ever stored must reject: code=%d err=%q", code, errCode(env))
	}
}

// TestGranularSave_QUICIndependent verifies the QUIC-only save toggles its own
// field and preserves the CDN/Argo slices around it.
func TestGranularSave_QUICIndependent(t *testing.T) {
	h := newAdvancedTestHandler(t)

	// Seed CDN + fixed Argo first.
	if code, env := callJSON(t, h.PutAdvancedArgo,
		`{"argo_enabled":true,"argo_mode":"fixed","argo_token":"tok-123"}`); code != 200 {
		t.Fatalf("seed argo: code=%d err=%q", code, errCode(env))
	}
	if code, env := callJSON(t, h.PutAdvancedCDN,
		`{"cdn_enabled":true,"cdn_preferred_ips":["104.16.0.1"]}`); code != 200 {
		t.Fatalf("seed cdn: code=%d err=%q", code, errCode(env))
	}

	// Flip QUIC on — independent of everything else, no Argo validation.
	code, env := callJSON(t, h.PutAdvancedQUIC, `{"block_quic":true}`)
	if code != 200 || !env.Success {
		t.Fatalf("save QUIC: code=%d err=%q", code, errCode(env))
	}

	got, _ := h.store.GetAdvanced(h.parseLocalNodeID())
	if !got.BlockQUIC {
		t.Error("BlockQUIC not persisted")
	}
	if !got.CDNEnabled || !got.ArgoEnabled || got.ArgoToken == "" {
		t.Errorf("QUIC save clobbered other slices: cdn=%v argo=%v tokenEmpty=%v",
			got.CDNEnabled, got.ArgoEnabled, got.ArgoToken == "")
	}
}
