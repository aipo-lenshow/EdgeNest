package api

import (
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// TestArgoSingletonGate enforces the one-Argo-inbound-per-node rule (1a): a
// node runs a single cloudflared tunnel pointing at a single loopback port, so
// only one argo_bound inbound can carry traffic. A second argo binding must be
// refused; updating the SAME inbound (excludeID) must be allowed.
func TestArgoSingletonGate(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	node, err := st.EnsureLocalNode()
	if err != nil {
		t.Fatalf("ensure node: %v", err)
	}
	h := &Handler{store: st}

	// First argo inbound — nothing else exists, so binding is allowed.
	if msg := h.argoSingletonGate(node.ID, map[string]any{"argo_bound": "true"}, 0); msg != "" {
		t.Fatalf("first argo inbound should pass, got %q", msg)
	}

	// Persist an existing argo_bound inbound on the node.
	existing := &model.Inbound{
		NodeID: node.ID, Tag: "vless-argo", Engine: "singbox", Type: "vless-ws",
		Listen: "127.0.0.1", Port: 2084, Network: "tcp", Enabled: true,
		Settings: `{"argo_bound":"true","ws_path":"/a"}`,
	}
	if err := st.CreateInbound(existing); err != nil {
		t.Fatalf("create existing: %v", err)
	}

	// A NEW argo binding (excludeID 0) must now be refused.
	if msg := h.argoSingletonGate(node.ID, map[string]any{"argo_bound": true}, 0); msg == "" {
		t.Fatal("second argo inbound must be refused, got pass")
	}

	// Updating the SAME inbound (excludeID == existing.ID) must be allowed.
	if msg := h.argoSingletonGate(node.ID, map[string]any{"argo_bound": true}, existing.ID); msg != "" {
		t.Fatalf("updating the existing argo inbound should pass, got %q", msg)
	}

	// A non-argo candidate is never gated, even with an existing argo inbound.
	if msg := h.argoSingletonGate(node.ID, map[string]any{"cdn_mode": "true"}, 0); msg != "" {
		t.Fatalf("non-argo inbound should pass, got %q", msg)
	}
}
