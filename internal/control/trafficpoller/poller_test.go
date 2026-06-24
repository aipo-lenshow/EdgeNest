package trafficpoller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/v2raystats"
)

// fakeStats returns a scripted sequence of QueryUserTraffic results, one per
// call, so a test can drive successive ticks deterministically.
type fakeStats struct {
	calls []map[string]v2raystats.UserTraffic
	i     int
}

func (f *fakeStats) QueryUserTraffic(context.Context) (map[string]v2raystats.UserTraffic, error) {
	if f.i >= len(f.calls) {
		return map[string]v2raystats.UserTraffic{}, nil
	}
	r := f.calls[f.i]
	f.i++
	return r, nil
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

// seedUser creates an inbound + one client credential for email so AddUserTraffic
// has a representative row to land on.
func seedUser(t *testing.T, st *store.Store, email string, port int) {
	t.Helper()
	in := &model.Inbound{
		NodeID: 1, Tag: "vless-" + email, Engine: "singbox", Type: "vless",
		Listen: "::", Port: port, Network: "tcp", Enabled: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	if err := st.CreateClient(&model.Client{InboundID: in.ID, Email: email, Enabled: true}); err != nil {
		t.Fatalf("create client: %v", err)
	}
}

func userUsed(t *testing.T, st *store.Store, email string) int64 {
	t.Helper()
	rows, err := st.ClientsByEmail(email)
	if err != nil {
		t.Fatalf("clients by email %q: %v", email, err)
	}
	var total int64
	for _, c := range rows {
		total += c.TrafficUp + c.TrafficDown
	}
	return total
}

// oneSource wraps a single fake client as the poller's source list.
func oneSource(f statsClient) []Source { return []Source{{Name: "test", Client: f}} }

func TestPoller_DiffsCumulativeCounters(t *testing.T) {
	st := openTestStore(t)
	seedUser(t, st, "a@x", 19601)

	f := &fakeStats{calls: []map[string]v2raystats.UserTraffic{
		{"a@x": {Up: 100, Down: 200}}, // tick 1: first sight => +300
		{"a@x": {Up: 150, Down: 500}}, // tick 2: delta +50/+300 => +350
		{"a@x": {Up: 150, Down: 500}}, // tick 3: no change => +0
	}}
	p := New(oneSource(f), st, time.Second)

	p.tick(context.Background())
	if got := userUsed(t, st, "a@x"); got != 300 {
		t.Fatalf("after tick1 used=%d want 300", got)
	}
	p.tick(context.Background())
	if got := userUsed(t, st, "a@x"); got != 650 {
		t.Fatalf("after tick2 used=%d want 650", got)
	}
	p.tick(context.Background())
	if got := userUsed(t, st, "a@x"); got != 650 {
		t.Fatalf("after tick3 (no change) used=%d want 650", got)
	}
}

func TestPoller_CounterResetCountsFromZero(t *testing.T) {
	st := openTestStore(t)
	seedUser(t, st, "b@x", 19602)

	f := &fakeStats{calls: []map[string]v2raystats.UserTraffic{
		{"b@x": {Up: 1000, Down: 2000}}, // +3000
		{"b@x": {Up: 10, Down: 20}},     // sing-box restarted (counter < prev) => +30, not negative
	}}
	p := New(oneSource(f), st, time.Second)
	p.tick(context.Background())
	p.tick(context.Background())
	if got := userUsed(t, st, "b@x"); got != 3030 {
		t.Fatalf("after restart used=%d want 3030", got)
	}
}

// A user the stats service stops reporting is forgotten from the in-memory
// baseline, so if it reappears it re-baselines instead of double-counting.
func TestPoller_ForgetsVanishedUser(t *testing.T) {
	st := openTestStore(t)
	seedUser(t, st, "c@x", 19603)

	f := &fakeStats{calls: []map[string]v2raystats.UserTraffic{
		{"c@x": {Up: 500, Down: 0}}, // +500
		{},                          // vanished — forgotten
		{"c@x": {Up: 80, Down: 0}},  // reappears, re-baselines from 0 => +80
	}}
	p := New(oneSource(f), st, time.Second)
	p.tick(context.Background())
	p.tick(context.Background())
	p.tick(context.Background())
	if got := userUsed(t, st, "c@x"); got != 580 {
		t.Fatalf("used=%d want 580", got)
	}
}

// A user with credentials on inbounds hosted by BOTH engines must have their
// per-source deltas summed; one engine restarting must not corrupt the other.
// OnCredited must fire exactly on ticks that credited traffic, never on a
// no-change tick — this is what makes quota enforcement event-driven, so the
// enforcer reacts within one poll interval instead of waiting for its own timer.
func TestPoller_OnCreditedFiresOnlyWhenCredited(t *testing.T) {
	st := openTestStore(t)
	seedUser(t, st, "e@x", 19605)

	f := &fakeStats{calls: []map[string]v2raystats.UserTraffic{
		{"e@x": {Up: 100, Down: 200}}, // tick1: +300 => fire
		{"e@x": {Up: 100, Down: 200}}, // tick2: no change => no fire
		{"e@x": {Up: 150, Down: 200}}, // tick3: +50 => fire
	}}
	var fired int
	p := New(oneSource(f), st, time.Second).
		OnCredited(func(context.Context) { fired++ })

	p.tick(context.Background())
	p.tick(context.Background())
	p.tick(context.Background())
	if fired != 2 {
		t.Fatalf("onCredited fired %d times, want 2 (ticks 1 and 3 only)", fired)
	}
}

func TestPoller_TwoSourcesSumPerEmail(t *testing.T) {
	st := openTestStore(t)
	seedUser(t, st, "d@x", 19604)

	singbox := &fakeStats{calls: []map[string]v2raystats.UserTraffic{
		{"d@x": {Up: 100, Down: 100}}, // tick1: +200
		{"d@x": {Up: 5, Down: 5}},     // tick2: restarted => +10
	}}
	xray := &fakeStats{calls: []map[string]v2raystats.UserTraffic{
		{"d@x": {Up: 1000, Down: 1000}}, // tick1: +2000
		{"d@x": {Up: 1500, Down: 1500}}, // tick2: +1000 (no restart)
	}}
	p := New([]Source{{Name: "singbox", Client: singbox}, {Name: "xray", Client: xray}}, st, time.Second)

	p.tick(context.Background()) // 200 + 2000 = 2200
	if got := userUsed(t, st, "d@x"); got != 2200 {
		t.Fatalf("after tick1 used=%d want 2200", got)
	}
	p.tick(context.Background()) // singbox restart +10, xray +1000 => +1010
	if got := userUsed(t, st, "d@x"); got != 3210 {
		t.Fatalf("after tick2 used=%d want 3210 (singbox restart must not corrupt xray)", got)
	}
}
