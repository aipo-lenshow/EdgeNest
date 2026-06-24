package health

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// stubNode satisfies just enough of nodeapi.NodeClient for Heartbeat.
type stubNode struct {
	snap  core.HealthSnapshot
	calls int
}

func (s *stubNode) Register(_ context.Context, _ string) (string, string, error) {
	return "", "", nil
}
func (s *stubNode) Heartbeat(_ context.Context, _ string) (core.HealthSnapshot, error) {
	s.calls++
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

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureLocalNode(); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSampleOnce_PersistsRow(t *testing.T) {
	st := openStore(t)
	n, _ := st.GetLocalNode()
	stub := &stubNode{snap: core.HealthSnapshot{
		CPU: 0.5, Mem: 0.4, Disk: 0.3, SingboxRunning: true, BBR: "enabled",
	}}
	r := &Recorder{Store: st, Node: stub, NodeID: "1", NumericNodeID: n.ID}

	if _, err := r.SampleOnce(context.Background()); err != nil {
		t.Fatalf("sample: %v", err)
	}
	var rows []model.HealthSnapshot
	st.DB().Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].CPU != 0.5 || !rows[0].SingboxRunning {
		t.Errorf("row missing fields: %+v", rows[0])
	}
	if stub.calls != 1 {
		t.Errorf("Heartbeat calls = %d, want 1", stub.calls)
	}
}

func TestSampleOnce_PrunesPastRetain(t *testing.T) {
	st := openStore(t)
	n, _ := st.GetLocalNode()
	stub := &stubNode{snap: core.HealthSnapshot{}}
	r := &Recorder{Store: st, Node: stub, NodeID: "1", NumericNodeID: n.ID, Retain: 3}

	for i := 0; i < 7; i++ {
		if _, err := r.SampleOnce(context.Background()); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	var count int64
	st.DB().Model(&model.HealthSnapshot{}).Count(&count)
	if count != 3 {
		t.Errorf("Retain=3 should keep 3 rows, got %d", count)
	}
}
