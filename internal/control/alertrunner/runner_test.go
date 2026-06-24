package alertrunner

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/alerts"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// stubNode satisfies nodeapi.NodeClient; only Heartbeat carries test state.
type stubNode struct{ snap core.HealthSnapshot }

func (s *stubNode) Register(_ context.Context, _ string) (string, string, error) { return "", "", nil }
func (s *stubNode) Heartbeat(_ context.Context, _ string) (core.HealthSnapshot, error) {
	return s.snap, nil
}
func (s *stubNode) ApplyConfig(_ context.Context, _ string, _ core.DesiredConfig) (core.ApplyResult, error) {
	return core.ApplyResult{}, nil
}
func (s *stubNode) QueryStats(_ context.Context, _ string, _ bool) (map[string]core.Traffic, error) {
	return nil, nil
}
func (s *stubNode) RestartEngine(_ context.Context, _ string) error { return nil }
func (s *stubNode) SelfCheck(_ context.Context, _ string) (core.HealthSnapshot, error) {
	return s.snap, nil
}
func (s *stubNode) EngineStatus(_ context.Context, _ string) (core.EngineStatus, error) {
	return core.EngineStatus{}, nil
}

func fp(k alerts.Kind, target string) string {
	return alerts.Fingerprint(alerts.Alert{Kind: k, Target: target})
}

func TestDiffAlerts_OnlyFreshPushed(t *testing.T) {
	cur := []alerts.Alert{
		{Kind: alerts.KindQuota, Target: "a@x", Pct: 92},
		{Kind: alerts.KindExpiry, Target: "b@x", Days: 3},
	}
	prev := map[string]bool{fp(alerts.KindQuota, "a@x"): true} // a@x already alerted

	fresh, recovered := diffAlerts(cur, prev)
	if len(fresh) != 1 || fresh[0].Target != "b@x" {
		t.Fatalf("fresh = %+v, want only b@x", fresh)
	}
	if len(recovered) != 0 {
		t.Errorf("recovered = %v, want none", recovered)
	}
}

func TestDiffAlerts_EngineRecovery(t *testing.T) {
	// sing-box was in alarm; now nothing is in alarm → engine recovery note.
	prev := map[string]bool{
		fp(alerts.KindEngine, "sing-box"): true,
		fp(alerts.KindQuota, "a@x"):       true, // quota cleared → silent, no note
	}
	fresh, recovered := diffAlerts(nil, prev)
	if len(fresh) != 0 {
		t.Errorf("fresh = %+v, want none", fresh)
	}
	if len(recovered) != 1 || recovered[0] != "sing-box" {
		t.Fatalf("recovered = %v, want [sing-box]", recovered)
	}
}

func TestDiffAlerts_StandingConditionNotRepushed(t *testing.T) {
	cur := []alerts.Alert{{Kind: alerts.KindCert, Target: "x.example", Days: 10}}
	prev := map[string]bool{fp(alerts.KindCert, "x.example"): true}
	fresh, recovered := diffAlerts(cur, prev)
	if len(fresh) != 0 || len(recovered) != 0 {
		t.Errorf("standing condition should be silent: fresh=%v recovered=%v", fresh, recovered)
	}
}

func newStore(t *testing.T) (*store.Store, uint, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureLocalNode(); err != nil {
		t.Fatal(err)
	}
	ln, _ := st.GetLocalNode()
	return st, ln.ID, strconv.FormatUint(uint64(ln.ID), 10)
}

func TestEngineAlerts_OnlyWhenServingAndDown(t *testing.T) {
	st, nodeNum, nodeID := newStore(t)
	// One sing-box inbound exists → sing-box "serves inbounds".
	in := &model.Inbound{
		NodeID: nodeNum, Tag: "vless-1", Engine: "singbox", Type: "vless",
		Listen: "::", Port: 8443, Network: "tcp", Enabled: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}

	// sing-box DOWN while serving an inbound → one engine alert.
	rDown := New(st, &stubNode{snap: core.HealthSnapshot{SingboxRunning: false, XrayRunning: false}}, nodeID)
	got := rDown.engineAlerts(context.Background())
	if len(got) != 1 || got[0].Kind != alerts.KindEngine || got[0].Target != "sing-box" {
		t.Fatalf("down: got %+v, want [engine sing-box]", got)
	}

	// sing-box UP → no alert. (xray has no inbound, so xray-down wouldn't alert.)
	rUp := New(st, &stubNode{snap: core.HealthSnapshot{SingboxRunning: true, XrayRunning: false}}, nodeID)
	if got := rUp.engineAlerts(context.Background()); len(got) != 0 {
		t.Fatalf("up: got %+v, want none", got)
	}
}
