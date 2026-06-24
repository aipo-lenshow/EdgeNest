package orchestrator

import (
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// TestSafeMode_AddsSSHAndPanel verifies Invariant I7: the SSH port and panel
// port are always reserved in the allow list, even when no inbound or user
// rule covers them.
func TestSafeMode_AddsSSHAndPanel(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	orch := NewWithPanelPort(st, &fakeNode{}, 2087)
	cfg, err := orch.BuildDesired(nodeID)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := map[string]string{}
	for _, r := range cfg.Firewall.AllowPorts {
		got[r.Note] = r.Proto
	}
	if got["edgenest:safe-mode-ssh"] != "tcp" {
		t.Errorf("missing SSH safe-mode rule: %+v", cfg.Firewall.AllowPorts)
	}
	if got["edgenest:safe-mode-panel"] != "tcp" {
		t.Errorf("missing panel safe-mode rule: %+v", cfg.Firewall.AllowPorts)
	}
}

// TestSafeMode_RespectsSSHPortSetting verifies that operators running SSH on
// a non-default port get their port reserved instead of 22.
func TestSafeMode_RespectsSSHPortSetting(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	if err := st.SetSetting("ssh_port", "2222"); err != nil {
		t.Fatal(err)
	}
	orch := New(st, &fakeNode{})
	cfg, err := orch.BuildDesired(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range cfg.Firewall.AllowPorts {
		if r.Port == 2222 && r.Proto == "tcp" && r.Note == "edgenest:safe-mode-ssh" {
			found = true
		}
		if r.Port == 22 {
			t.Errorf("default SSH 22 leaked when ssh_port=2222: %+v", cfg.Firewall.AllowPorts)
		}
	}
	if !found {
		t.Errorf("custom SSH port 2222 not reserved: %+v", cfg.Firewall.AllowPorts)
	}
}

// TestSafeMode_DisabledViaSSHPortZero lets containerised deploys without SSH
// opt out of the reservation by setting ssh_port=0.
func TestSafeMode_DisabledViaSSHPortZero(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	if err := st.SetSetting("ssh_port", "0"); err != nil {
		t.Fatal(err)
	}
	orch := New(st, &fakeNode{})
	cfg, err := orch.BuildDesired(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.Firewall.AllowPorts {
		if r.Note == "edgenest:safe-mode-ssh" {
			t.Errorf("SSH safe-mode emitted despite ssh_port=0: %+v", r)
		}
	}
}

// TestSafeMode_UserRuleSuppressesManagedRow verifies the bug fix that powers
// TestApply_SyncsManagedFirewallTable: if the operator already has a user
// rule on port 22, safe-mode does NOT emit a parallel managed row (which
// would later overwrite the user's note in syncManagedFirewall).
func TestSafeMode_UserRuleSuppressesManagedRow(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	if err := st.DB().Create(&model.FirewallRule{
		NodeID: nodeID, Port: 22, Proto: "tcp", Note: "ssh (user)", Managed: false,
	}).Error; err != nil {
		t.Fatal(err)
	}

	orch := New(st, &fakeNode{})
	managed, full := orch.computeAllowPorts(nodeID, nil)

	for _, r := range managed {
		if r.Port == 22 {
			t.Errorf("safe-mode emitted managed rule for port 22 already covered by user: %+v", r)
		}
	}
	foundUser := false
	for _, r := range full {
		if r.Port == 22 && r.Note == "user:ssh (user)" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Errorf("user rule not preserved in full allow list: %+v", full)
	}
}

// TestSafeMode_UserRuleMergedIntoFullAllow ensures user rules at a port not
// otherwise covered still reach the engine payload.
func TestSafeMode_UserRuleMergedIntoFullAllow(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	if err := st.DB().Create(&model.FirewallRule{
		NodeID: nodeID, Port: 9100, Proto: "tcp", Note: "node-exporter", Managed: false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	orch := New(st, &fakeNode{})
	_, full := orch.computeAllowPorts(nodeID, nil)
	found := false
	for _, r := range full {
		if r.Port == 9100 && r.Note == "user:node-exporter" {
			found = true
		}
	}
	if !found {
		t.Errorf("user rule 9100 not propagated to full allow list: %+v", full)
	}
}

// TestFirewall_ExcludesLoopbackInbound verifies that a loopback-bound inbound
// (e.g. an Argo origin on 127.0.0.1) is left out of the firewall allow list,
// while a public inbound on the *same port number* is still included. The
// decision keys on the listen address, not the port.
func TestFirewall_ExcludesLoopbackInbound(t *testing.T) {
	st := newTestStore(t)
	localNode, _ := st.GetLocalNode()
	nodeID := localNode.ID

	// Argo-bound VMess-WS on loopback — must NOT appear.
	if err := st.CreateInbound(&model.Inbound{
		NodeID: nodeID, Tag: "argo-vmess", Engine: "singbox", Type: "vmess",
		Listen: "127.0.0.1", Port: 2053, Network: "tcp", Enabled: true,
		Settings: `{"argo_bound":"true"}`,
	}); err != nil {
		t.Fatalf("create loopback inbound: %v", err)
	}
	// Public VMess-WS on a different port — must appear.
	if err := st.CreateInbound(&model.Inbound{
		NodeID: nodeID, Tag: "public-vmess", Engine: "singbox", Type: "vmess",
		Listen: "::", Port: 2083, Network: "tcp", Enabled: true,
		Settings: `{}`,
	}); err != nil {
		t.Fatalf("create public inbound: %v", err)
	}

	orch := New(st, &fakeNode{})
	cfg, err := orch.BuildDesired(nodeID)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var sawLoopback, sawPublic bool
	for _, r := range cfg.Firewall.AllowPorts {
		if r.Port == 2053 {
			sawLoopback = true
		}
		if r.Port == 2083 {
			sawPublic = true
		}
	}
	if sawLoopback {
		t.Errorf("loopback inbound (127.0.0.1:2053) leaked into allow list: %+v", cfg.Firewall.AllowPorts)
	}
	if !sawPublic {
		t.Errorf("public inbound (::2083) missing from allow list: %+v", cfg.Firewall.AllowPorts)
	}
}
