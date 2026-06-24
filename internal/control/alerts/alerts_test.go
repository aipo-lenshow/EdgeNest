package alerts

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// now is fixed so day-count assertions are deterministic.
var testNow = time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return st
}

func mkInbound(t *testing.T, st *store.Store) uint {
	t.Helper()
	ib := &model.Inbound{NodeID: 1, Tag: "in-1", Type: "vless", Listen: "0.0.0.0", Port: 443, Enabled: true}
	if err := st.CreateInbound(ib); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	return ib.ID
}

func mkClient(t *testing.T, st *store.Store, ibID uint, c model.Client) {
	t.Helper()
	c.InboundID = ibID
	c.UUID = c.Email + "-uuid"
	if err := st.CreateClient(&c); err != nil {
		t.Fatalf("create client %s: %v", c.Email, err)
	}
}

func TestDetectorQuotaAndExpiry(t *testing.T) {
	st := openStore(t)
	ib := mkInbound(t, st)
	d3 := testNow.Add(3 * 24 * time.Hour).Unix()
	d30 := testNow.Add(30 * 24 * time.Hour).Unix()

	// alice — 96% of quota, enabled → quota warning.
	mkClient(t, st, ib, model.Client{Email: "alice", Enabled: true, QuotaBytes: 100, TrafficUp: 50, TrafficDown: 46})
	// bob — expires in 3 days, enabled, no quota → expiry warning.
	mkClient(t, st, ib, model.Client{Email: "bob", Enabled: true, ExpiryAt: d3})
	// carol — 50% quota, enabled → no warning.
	mkClient(t, st, ib, model.Client{Email: "carol", Enabled: true, QuotaBytes: 100, TrafficUp: 30, TrafficDown: 20})
	// dave — over quota but DISABLED (already enforced) → not a warning, counts
	// toward total but not enabled.
	mkClient(t, st, ib, model.Client{Email: "dave", Enabled: false, QuotaBytes: 100, TrafficUp: 150, TrafficDown: 50})
	// erin — expires in 30 days → outside the 7-day window.
	mkClient(t, st, ib, model.Client{Email: "erin", Enabled: true, ExpiryAt: d30})

	det := NewDetector(st, "1")
	th := Default()

	// Quota warnings: alice only.
	qw := det.quotaWarnings(th.QuotaPct)
	if len(qw) != 1 || qw[0].Target != "alice" || qw[0].Pct != 96 {
		t.Fatalf("quotaWarnings = %+v, want [alice 96%%]", qw)
	}

	// Expiry warnings: bob only, 3 days out.
	ew := det.expiringUsers(testNow, th.ExpiryDays)
	if len(ew) != 1 || ew[0].Target != "bob" || ew[0].Days != 3 {
		t.Fatalf("expiringUsers = %+v, want [bob 3d]", ew)
	}

	// User counts: 5 total, 4 enabled (dave disabled).
	total, enabled := det.UserCounts()
	if total != 5 || enabled != 4 {
		t.Fatalf("UserCounts = %d/%d, want 5/4", total, enabled)
	}
}

// TestDetectorAggregatesByEmail verifies a multi-client user is collapsed to one
// row: used summed, quota = max non-zero, enabled = any.
func TestDetectorAggregatesByEmail(t *testing.T) {
	st := openStore(t)
	ib := mkInbound(t, st)
	ib2 := &model.Inbound{NodeID: 1, Tag: "in-2", Type: "hysteria2", Listen: "0.0.0.0", Port: 8443, Enabled: true}
	if err := st.CreateInbound(ib2); err != nil {
		t.Fatalf("create inbound2: %v", err)
	}
	// frank has two credential rows: uncapped (used 40) + capped 100 (used 55).
	// Aggregated: used 95, quota 100 → 95% warning.
	mkClient(t, st, ib, model.Client{Email: "frank", Enabled: true, QuotaBytes: 0, TrafficUp: 20, TrafficDown: 20})
	mkClient(t, st, ib2.ID, model.Client{Email: "frank", Enabled: true, QuotaBytes: 100, TrafficUp: 30, TrafficDown: 25})

	det := NewDetector(st, "1")
	qw := det.quotaWarnings(Default().QuotaPct)
	if len(qw) != 1 || qw[0].Target != "frank" || qw[0].Pct != 95 {
		t.Fatalf("quotaWarnings = %+v, want [frank 95%%]", qw)
	}
	total, enabled := det.UserCounts()
	if total != 1 || enabled != 1 {
		t.Fatalf("UserCounts = %d/%d, want 1/1", total, enabled)
	}
}

func TestLineRendering(t *testing.T) {
	cases := []struct {
		a      Alert
		zh, en string
	}{
		{Alert{Kind: KindQuota, Target: "alice", Pct: 96}, "• alice — 配额 96%", "• alice — quota 96%"},
		{Alert{Kind: KindExpiry, Target: "bob", Days: 3}, "• bob — 3 天后到期", "• bob — expires in 3d"},
		{Alert{Kind: KindExpiry, Target: "bob", Days: 0}, "• bob — 今天到期", "• bob — expires today"},
		{Alert{Kind: KindCert, Target: "example.com", Days: 12}, "• 证书 example.com — 12 天后到期", "• cert example.com — expires in 12d"},
		{Alert{Kind: KindCert, Target: "old.com", Days: -2}, "• 证书 old.com — 已过期", "• cert old.com — expired"},
	}
	for _, c := range cases {
		if got := Line(c.a, "zh"); got != c.zh {
			t.Errorf("Line(zh) = %q, want %q", got, c.zh)
		}
		if got := Line(c.a, "en"); got != c.en {
			t.Errorf("Line(en) = %q, want %q", got, c.en)
		}
	}
}

func TestDaysUntil(t *testing.T) {
	n := testNow.Unix()
	if d := daysUntil(testNow.Add(3*24*time.Hour).Unix(), n); d != 3 {
		t.Errorf("daysUntil(+3d) = %d, want 3", d)
	}
	if d := daysUntil(testNow.Add(-2*24*time.Hour).Unix(), n); d != -2 {
		t.Errorf("daysUntil(-2d) = %d, want -2", d)
	}
}

func TestFingerprint(t *testing.T) {
	a := Alert{Kind: KindQuota, Target: "u@x", Pct: 95}
	b := Alert{Kind: KindQuota, Target: "u@x", Pct: 99} // same kind+target, diff pct
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("fingerprint must ignore severity so 95→99% doesn't re-fire")
	}
	if Fingerprint(a) != "quota:u@x" {
		t.Errorf("fingerprint = %q, want quota:u@x", Fingerprint(a))
	}
	if Fingerprint(Alert{Kind: KindEngine, Target: "sing-box"}) != "engine:sing-box" {
		t.Error("engine fingerprint mismatch")
	}
}

func TestLine_Engine(t *testing.T) {
	a := Alert{Kind: KindEngine, Target: "sing-box"}
	if got := Line(a, "en"); got != "• sing-box engine offline" {
		t.Errorf("en engine line = %q", got)
	}
	if got := Line(a, "zh"); got != "• sing-box 引擎已掉线" {
		t.Errorf("zh engine line = %q", got)
	}
}
