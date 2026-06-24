package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// fakeNode records ApplyConfig calls and returns a configurable result.
type fakeNode struct {
	got      core.DesiredConfig
	calls    int
	applyRes core.ApplyResult
}

func (f *fakeNode) Register(ctx context.Context, t string) (string, string, error) {
	return "", "", nil
}
func (f *fakeNode) Heartbeat(ctx context.Context, n string) (core.HealthSnapshot, error) {
	return core.HealthSnapshot{}, nil
}
func (f *fakeNode) ApplyConfig(ctx context.Context, n string, cfg core.DesiredConfig) (core.ApplyResult, error) {
	f.got = cfg
	f.calls++
	return f.applyRes, nil
}
func (f *fakeNode) QueryStats(ctx context.Context, n string, r bool) (map[string]core.Traffic, error) {
	return nil, nil
}
func (f *fakeNode) RestartEngine(ctx context.Context, n string) error { return nil }
func (f *fakeNode) SelfCheck(ctx context.Context, n string) (core.HealthSnapshot, error) {
	return core.HealthSnapshot{}, nil
}
func (f *fakeNode) EngineStatus(ctx context.Context, n string) (core.EngineStatus, error) {
	return core.EngineStatus{}, nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := st.EnsureLocalNode(); err != nil {
		t.Fatalf("ensure local node: %v", err)
	}
	return st
}

// seedInbound inserts an enabled VLESS-Reality inbound with two enabled clients.
func seedInbound(t *testing.T, st *store.Store, nodeID uint, tag string, port int, clientEmails ...string) *model.Inbound {
	t.Helper()
	in := &model.Inbound{
		NodeID:  nodeID,
		Tag:     tag,
		Engine:  "singbox",
		Type:    "vless",
		Listen:  "::",
		Port:    port,
		Network: "tcp",
		Enabled: true,
		Settings: `{
			"sni":"www.microsoft.com",
			"reality_private_key":"mDuMKKpJ_DMK5Qj1k9D3qV5T0bM4y3-N0kZbW2X9tJ4",
			"short_ids":["0123456789abcdef"]
		}`,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	for i, e := range clientEmails {
		uuid := "00000000-0000-0000-0000-00000000000" + string(rune('0'+i))
		if err := st.CreateClient(&model.Client{
			InboundID: in.ID, Email: e, UUID: uuid, Enabled: true,
		}); err != nil {
			t.Fatalf("create client %s: %v", e, err)
		}
	}
	return in
}

func TestBuildDesired_EnabledOnly(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	// One enabled VLESS inbound with two clients.
	seedInbound(t, st, nodeID, "vless-good", 8443, "alice@example.com", "bob@example.com")
	// One disabled VLESS inbound (should be skipped).
	disabled := seedInbound(t, st, nodeID, "vless-disabled", 8444, "skip@example.com")
	disabled.Enabled = false
	if err := st.UpdateInbound(disabled); err != nil {
		t.Fatal(err)
	}

	orch := New(st, &fakeNode{})
	cfg, err := orch.BuildDesired(nodeID)
	if err != nil {
		t.Fatalf("build desired: %v", err)
	}
	if len(cfg.Inbounds) != 1 {
		t.Fatalf("want 1 enabled inbound, got %d", len(cfg.Inbounds))
	}
	in := cfg.Inbounds[0]
	if in.Tag != "vless-good" {
		t.Errorf("wrong inbound: %s", in.Tag)
	}
	if len(in.Clients) != 2 {
		t.Errorf("want 2 clients, got %d", len(in.Clients))
	}
	if in.Clients[0].Email == "" {
		t.Error("client email empty (invariant I1)")
	}
	if got, want := in.Settings["sni"], "www.microsoft.com"; got != want {
		t.Errorf("settings.sni = %v, want %v", got, want)
	}
	// Firewall: enabled inbound at 8443/tcp + SSH safe-mode at 22/tcp.
	// Safe-mode is on by default even via the plain New() constructor
	// because the SSH lock-out risk is universal (Invariant I7); only
	// setting "ssh_port" = "0" disables it.
	gotPorts := map[string]string{}
	for _, r := range cfg.Firewall.AllowPorts {
		key := fmt.Sprintf("%d/%s", r.Port, r.Proto)
		gotPorts[key] = r.Note
	}
	if gotPorts["8443/tcp"] != "edgenest:vless-good" {
		t.Errorf("missing inbound rule 8443/tcp: %+v", cfg.Firewall.AllowPorts)
	}
	if gotPorts["22/tcp"] != "edgenest:safe-mode-ssh" {
		t.Errorf("missing safe-mode SSH rule 22/tcp: %+v", cfg.Firewall.AllowPorts)
	}
}

func TestBuildDesired_Hysteria2UDP(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	in := &model.Inbound{
		NodeID:  nodeID,
		Tag:     "h2",
		Engine:  "singbox",
		Type:    "hysteria2",
		Listen:  "::",
		Port:    41020,
		Network: "udp",
		Enabled: true,
		Settings: `{"tls_cert_path":"/tmp/c","tls_key_path":"/tmp/k"}`,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateClient(&model.Client{
		InboundID: in.ID, Email: "carol@example.com", Password: "hunter2", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	orch := New(st, &fakeNode{})
	cfg, err := orch.BuildDesired(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Firewall.AllowPorts[0].Proto != "udp" {
		t.Errorf("hysteria2 should map to UDP firewall rule, got %s", cfg.Firewall.AllowPorts[0].Proto)
	}
}

func TestApply_SyncsManagedFirewallTable(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	seedInbound(t, st, nodeID, "vless-a", 8443, "a@example.com")

	// Pre-seed a stale managed firewall rule on a port no longer used.
	if err := st.UpsertManagedFirewallRule(nodeID, 9999, "tcp", "stale"); err != nil {
		t.Fatal(err)
	}
	// Pre-seed a user-managed rule that should be left alone.
	if err := st.DB().Create(&model.FirewallRule{
		NodeID: nodeID, Port: 22, Proto: "tcp", Note: "ssh (user)", Managed: false,
	}).Error; err != nil {
		t.Fatal(err)
	}

	fn := &fakeNode{applyRes: core.ApplyResult{OK: true, Message: "applied"}}
	orch := New(st, fn)

	if _, err := orch.Apply(context.Background(), nodeID); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Expect: managed 8443 exists, managed 9999 gone, user 22 still there.
	rules, _ := st.ListFirewallRules(nodeID)
	got := map[string]bool{}
	for _, r := range rules {
		key := r.Note + ":" + r.Proto
		got[key] = true
		if r.Port == 9999 {
			t.Error("stale managed rule should have been deleted")
		}
		if r.Port == 22 && r.Managed {
			t.Error("user rule should not have been flagged managed")
		}
		if r.Port == 22 && r.Note != "ssh (user)" {
			t.Errorf("user rule note clobbered: %q", r.Note)
		}
	}
	if !got["edgenest:vless-a:tcp"] {
		t.Errorf("missing managed rule for 8443/tcp; got %+v", rules)
	}
	if fn.calls != 1 {
		t.Errorf("ApplyConfig called %d times, want 1", fn.calls)
	}
}

func TestApply_DoesNotSyncFirewallOnApplyFailure(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	seedInbound(t, st, nodeID, "vless-a", 8443, "a@example.com")

	fn := &fakeNode{applyRes: core.ApplyResult{OK: false, Message: "engine refused"}}
	orch := New(st, fn)

	if _, err := orch.Apply(context.Background(), nodeID); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Firewall table should not have grown.
	rules, _ := st.ListFirewallRules(nodeID)
	for _, r := range rules {
		if r.Managed {
			t.Errorf("managed rule created despite engine failure: %+v", r)
		}
	}
}
