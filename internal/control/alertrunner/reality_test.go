package alertrunner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/alerts"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func TestRealityAlerts_NeedTwoMissesInARow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []model.Inbound{
		{NodeID: 1, Tag: "reality", Type: "vless", Port: 8443, Enabled: true,
			Settings: `{"reality_private_key":"k","sni":"down.example.com"}`},
		{NodeID: 1, Tag: "reality2", Type: "vless", Port: 8444, Enabled: true,
			Settings: `{"reality_private_key":"k","sni":"up.example.com"}`},
		{NodeID: 1, Tag: "hy2", Type: "hysteria2", Port: 41020, Enabled: true,
			Settings: `{"sni":"down.example.com"}`},
	} {
		in := in
		if err := st.CreateInbound(&in); err != nil {
			t.Fatal(err)
		}
	}
	checked := map[string]int{}
	r := &Runner{store: st, nodeID: "1", checkTarget: func(_ context.Context, host string, _ int, _ string) error {
		checked[host]++
		if host == "down.example.com" {
			return errors.New("unreachable")
		}
		return nil
	}}
	now := time.Now()
	if got := r.realityAlerts(context.Background(), now); len(got) != 0 {
		t.Fatalf("alarm after one miss: %v", got)
	}
	// Within the check interval nothing is re-probed.
	r.realityAlerts(context.Background(), now.Add(time.Minute))
	if checked["down.example.com"] != 1 {
		t.Fatalf("re-probed inside the interval: %v", checked)
	}
	got := r.realityAlerts(context.Background(), now.Add(realityCheckEvery))
	if len(got) != 1 || got[0].Kind != alerts.KindRealityTarget || got[0].Target != "down.example.com" {
		t.Fatalf("want one alarm for down.example.com, got %v", got)
	}
	// The verdict holds between checks so the alarm doesn't clear and re-fire.
	if got := r.realityAlerts(context.Background(), now.Add(realityCheckEvery+time.Minute)); len(got) != 1 {
		t.Fatalf("alarm dropped between checks: %v", got)
	}
}
