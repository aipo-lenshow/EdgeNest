package cdnstats

import (
	"context"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

var cands = []string{"1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4"}

func TestValidNetKey(t *testing.T) {
	for k, want := range map[string]bool{
		"CN|联通|北方": true, "HK|AS4760": true, "US|Comcast": true,
		"": false, "cn|联通": false, "CN": false, "CN|": false, "CN|a|b|c": false,
		"CHN|联通": false, "CN|a\x00b": false,
	} {
		if got := ValidNetKey(k); got != want {
			t.Errorf("ValidNetKey(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestCleanDropsRoundsTakenOnAPoorNetwork(t *testing.T) {
	ok := []Sample{{IP: "1.1.1.1", OK: true, Ms: 100}, {IP: "1.1.1.2", OK: true, Ms: 200}}
	for name, r := range map[string]Report{
		"reference failed": {NetKey: "CN|联通|北方", ControlMs: 0, Results: ok},
		"reference crawls": {NetKey: "CN|联通|北方", ControlMs: 3000, Results: ok},
		"mostly failed": {NetKey: "CN|联通|北方", ControlMs: 80, Results: []Sample{
			{IP: "1.1.1.1", OK: true, Ms: 100}, {IP: "1.1.1.2"}, {IP: "1.1.1.3"}}},
		"bad network key": {NetKey: "nope", ControlMs: 80, Results: ok},
	} {
		if s, reason := Clean(r, cands); reason == "" || s != nil {
			t.Errorf("%s: got %v %q, want the round dropped", name, s, reason)
		}
	}
}

func TestCleanKeepsOnlyHandedOutAddresses(t *testing.T) {
	s, reason := Clean(Report{NetKey: "CN|联通|北方", ControlMs: 80, Results: []Sample{
		{IP: "1.1.1.1", OK: true, Ms: 100},
		{IP: "9.9.9.9", OK: true, Ms: 10},   // never handed out
		{IP: "1.1.1.1", OK: true, Ms: 5},    // duplicate
		{IP: "1.1.1.2", OK: true, Ms: 9999}, // implausible: counts as failed
	}}, cands)
	if reason != "" || len(s) != 2 || s[0].IP != "1.1.1.1" || s[1].OK {
		t.Fatalf("got %+v %q", s, reason)
	}
}

func row(net, ip string, samples, ok, msSum int64) store.CDNStatTotal {
	return store.CDNStatTotal{NetKey: net, NetLabel: "L", IP: ip, Samples: samples, OK: ok, MsSum: msSum}
}

func TestSummariseRanksReliableFastFirst(t *testing.T) {
	nets := Summarise([]store.CDNStatTotal{
		row("CN|联通|北方", "1.1.1.1", 10, 10, 3000), // 300ms
		row("CN|联通|北方", "1.1.1.2", 10, 10, 1000), // 100ms
		row("CN|联通|北方", "1.1.1.3", 10, 5, 250),   // fast but unreliable
		row("CN|联通|北方", "1.1.1.4", 1, 1, 10),     // too few samples
	})
	if len(nets) != 1 {
		t.Fatal(nets)
	}
	if got := nets[0].Recommended(3); len(got) != 2 || got[0] != "1.1.1.2" || got[1] != "1.1.1.1" {
		t.Fatalf("recommended %v", got)
	}
	if nets[0].CDNDown {
		t.Fatal("answering network marked down")
	}
	down := Summarise([]store.CDNStatTotal{row("CN|移动|华南", "1.1.1.1", 9, 0, 0)})
	if !down[0].CDNDown {
		t.Fatal("nothing answers after many tries: want CDN marked down there")
	}
}

func TestOrderPutsRecommendedFirst(t *testing.T) {
	got := Order(cands, []string{"1.1.1.3", "8.8.8.8", "1.1.1.1"})
	want := []string{"1.1.1.3", "1.1.1.1", "1.1.1.2", "1.1.1.4"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func samples(winner string) []store.CDNSample {
	out := []store.CDNSample{}
	for _, ip := range cands {
		ms := int64(300)
		if ip == winner {
			ms = 100
		}
		out = append(out, store.CDNSample{IP: ip, OK: true, Ms: ms})
	}
	return out
}

func TestLimiterManualPolicy(t *testing.T) {
	l := NewLimiter()
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	p := Policy{IntervalSec: 120, SameRounds: 3, CooldownSec: 3600}
	r := Report{NetKey: "CN|联通|北方", Manual: true, CurrentIP: "1.1.1.1"}

	if v := l.Check("s", r, samples("1.1.1.1"), p); !v.Accept {
		t.Fatal("first manual round refused")
	}
	now = now.Add(time.Minute)
	if v := l.Check("s", r, samples("1.1.1.1"), p); v.Accept || v.Reason != "too_soon" {
		t.Fatalf("inside the interval: %+v", v)
	}
	now = now.Add(2 * time.Minute)
	l.Check("s", r, samples("1.1.1.1"), p)
	now = now.Add(3 * time.Minute)
	v := l.Check("s", r, samples("1.1.1.1"), p)
	if !v.Accept || v.CoolingUntil.IsZero() {
		t.Fatalf("third same winner should be taken and start the cooldown: %+v", v)
	}
	now = now.Add(10 * time.Minute)
	if v := l.Check("s", r, samples("1.1.1.1"), p); v.Accept || v.Reason != "cooling" {
		t.Fatalf("cooling: %+v", v)
	}
	// The address in use is now clearly beaten: the round is taken anyway.
	if v := l.Check("s", r, samples("1.1.1.2"), p); !v.Accept {
		t.Fatalf("current no longer best should lift the cooldown: %+v", v)
	}
	// Another subscription is not affected.
	if v := l.Check("other", r, samples("1.1.1.1"), p); !v.Accept {
		t.Fatal("limits leak across subscriptions")
	}
}

func TestLimiterAutoGap(t *testing.T) {
	l := NewLimiter()
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	r := Report{NetKey: "CN|联通|北方"}
	if !l.Check("s", r, nil, DefaultPolicy).Accept {
		t.Fatal("first automatic round refused")
	}
	now = now.Add(time.Minute)
	if l.Check("s", r, nil, DefaultPolicy).Accept {
		t.Fatal("automatic rounds a minute apart from one subscription should not both pool")
	}
	now = now.Add(autoGap)
	if !l.Check("s", r, nil, DefaultPolicy).Accept {
		t.Fatal("after the gap the round should pool again")
	}
}

type fakePrune struct {
	before chan string
}

func (f fakePrune) PruneCDNStatsBefore(day string) (int64, error) {
	f.before <- day
	return 0, nil
}

func TestPrunerRunsAtStartWithTheRetentionWindow(t *testing.T) {
	f := fakePrune{before: make(chan string, 1)}
	p := NewPruner(f, 30)
	p.now = func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	select {
	case day := <-f.before:
		if day != "2026-09-30" {
			t.Fatalf("pruned before %s, want 2026-09-30", day)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no prune at start")
	}
	if n, _ := NewPruner(f, 0).PruneOnce(); n != 0 || len(f.before) != 0 {
		t.Fatal("retention <= 0 must disable pruning")
	}
}

func TestPolicyLoadFallsBackOnBadValues(t *testing.T) {
	m := mapSettings{"cdn_manual_interval_sec": "5"} // below the floor
	if got := LoadPolicy(m); got != DefaultPolicy {
		t.Fatalf("got %+v", got)
	}
	want := Policy{IntervalSec: 300, SameRounds: 2, CooldownSec: 0}
	if err := SavePolicy(m, want); err != nil {
		t.Fatal(err)
	}
	if got := LoadPolicy(m); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if got := want.Header(); got != "interval=300;same=2;cooldown=0" {
		t.Fatal(got)
	}
}

type mapSettings map[string]string

func (m mapSettings) GetSetting(k string) (string, error) { return m[k], nil }
func (m mapSettings) SetSetting(k, v string) error        { m[k] = v; return nil }
