package orchestrator

import (
	"fmt"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// Low-profile mode: the direct inbound stops being rendered and loses its
// firewall opening; the edge inbound keeps running on loopback (no opening).
func TestBuildDesired_StealthKeepsOnlyEdge(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID
	seedInbound(t, st, nodeID, "vless-direct", 8443, "alice@example.com")
	edge := &model.Inbound{
		NodeID: nodeID, Tag: "ws-edge", Engine: "singbox", Type: "vless-ws",
		Listen: "0.0.0.0", Port: 20001, Network: "tcp", Enabled: true,
		Settings: `{"ws_path":"/a1b2","edge_bound":"true"}`,
	}
	if err := st.CreateInbound(edge); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateClient(&model.Client{InboundID: edge.ID, Email: "alice@example.com", UUID: "u", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := antiblock.Save(st, antiblock.Config{EdgeDomain: "cdn.example.com", Stealth: true}); err != nil {
		t.Fatal(err)
	}

	cfg, err := New(st, &fakeNode{}).BuildDesired(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 1 || cfg.Inbounds[0].Tag != "ws-edge" {
		t.Fatalf("want only the edge inbound, got %+v", cfg.Inbounds)
	}
	if cfg.Inbounds[0].Listen != "127.0.0.1" {
		t.Errorf("edge inbound must be forced to loopback, got %q", cfg.Inbounds[0].Listen)
	}
	for _, r := range cfg.Firewall.AllowPorts {
		if k := fmt.Sprintf("%d/%s", r.Port, r.Proto); k == "8443/tcp" || k == "20001/tcp" {
			t.Errorf("direct or loopback-origin port opened in low-profile mode: %+v", cfg.Firewall.AllowPorts)
		}
	}

	// Switching it off brings the direct inbound back.
	if err := antiblock.Save(st, antiblock.Config{EdgeDomain: "cdn.example.com"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = New(st, &fakeNode{}).BuildDesired(nodeID)
	if len(cfg.Inbounds) != 2 {
		t.Fatalf("normal mode should run both inbounds, got %d", len(cfg.Inbounds))
	}
}
