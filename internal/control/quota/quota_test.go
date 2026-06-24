package quota

import (
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

func TestEvaluate_QuotaExceeded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clients := []model.Client{
		{ID: 1, Email: "a", Enabled: true, TrafficUp: 600, TrafficDown: 500, QuotaBytes: 1000},
		{ID: 2, Email: "b", Enabled: true, TrafficUp: 100, TrafficDown: 100, QuotaBytes: 1000},
		{ID: 3, Email: "c", Enabled: true, TrafficUp: 0, TrafficDown: 0, QuotaBytes: 0}, // unlimited
	}
	got := Evaluate(clients, now)
	if len(got) != 1 || got[0].ClientID != 1 || got[0].Reason != ReasonQuotaExceeded {
		t.Errorf("want [client 1 quota_exceeded], got %+v", got)
	}
}

func TestEvaluate_Expired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clients := []model.Client{
		{ID: 1, Email: "old", Enabled: true, ExpiryAt: now.Unix() - 60},
		{ID: 2, Email: "fresh", Enabled: true, ExpiryAt: now.Unix() + 60},
		{ID: 3, Email: "never", Enabled: true, ExpiryAt: 0},
	}
	got := Evaluate(clients, now)
	if len(got) != 1 || got[0].ClientID != 1 || got[0].Reason != ReasonExpired {
		t.Errorf("want [client 1 expired], got %+v", got)
	}
}

func TestEvaluate_SkipsAlreadyDisabled(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clients := []model.Client{
		{ID: 1, Email: "off", Enabled: false, TrafficUp: 9999, QuotaBytes: 1},
	}
	got := Evaluate(clients, now)
	if len(got) != 0 {
		t.Errorf("disabled clients must be skipped, got %+v", got)
	}
}

func TestEvaluate_QuotaWinsOverExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clients := []model.Client{
		{ID: 1, Email: "both", Enabled: true,
			TrafficUp: 2000, TrafficDown: 0, QuotaBytes: 1000,
			ExpiryAt: now.Unix() - 60},
	}
	got := Evaluate(clients, now)
	if len(got) != 1 || got[0].Reason != ReasonQuotaExceeded {
		t.Errorf("when both quota and expiry trigger, quota should win first; got %+v", got)
	}
}

func TestEvaluate_QuotaBoundaryExact(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clients := []model.Client{
		{ID: 1, Email: "exact", Enabled: true,
			TrafficUp: 500, TrafficDown: 500, QuotaBytes: 1000},
	}
	got := Evaluate(clients, now)
	if len(got) != 1 {
		t.Errorf("traffic == quota should disable (>=), got %+v", got)
	}
}

// --- user-centric (EvaluateByUser) ---

// A user's quota is enforced against the SUM across their clients, and when it
// trips, EVERY enabled client of that user is flagged (so the share resolver
// stops serving them on all inbounds at once). Traffic lives on one
// representative row, so the sum is the per-user total.
func TestEvaluateByUser_QuotaSumDisablesAllClients(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clients := []model.Client{
		// user "a": representative row carries the total (700), quota 1000 on
		// each row (uniform). 700 < 1000 → under cap, nothing disabled.
		{ID: 1, Email: "a", Enabled: true, TrafficUp: 700, TrafficDown: 0, QuotaBytes: 1000},
		{ID: 2, Email: "a", Enabled: true, TrafficUp: 0, TrafficDown: 0, QuotaBytes: 1000},
		// user "b": total 1200 >= 1000 → BOTH of b's clients flagged.
		{ID: 3, Email: "b", Enabled: true, TrafficUp: 1200, TrafficDown: 0, QuotaBytes: 1000},
		{ID: 4, Email: "b", Enabled: true, TrafficUp: 0, TrafficDown: 0, QuotaBytes: 1000},
	}
	got := EvaluateByUser(clients, now)
	if len(got) != 2 {
		t.Fatalf("want both of user b's clients flagged, got %+v", got)
	}
	for _, d := range got {
		if d.Email != "b" || d.Reason != ReasonQuotaExceeded {
			t.Errorf("unexpected decision %+v", d)
		}
	}
}

// Quota/expiry are taken as the max non-zero across a user's clients, so a
// stray 0-quota row doesn't hide the real cap.
func TestEvaluateByUser_MaxQuotaWins(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clients := []model.Client{
		{ID: 1, Email: "u", Enabled: true, TrafficUp: 900, QuotaBytes: 0},   // stray unlimited row + total
		{ID: 2, Email: "u", Enabled: true, TrafficUp: 0, QuotaBytes: 800},   // real cap 800
	}
	got := EvaluateByUser(clients, now)
	if len(got) != 2 {
		t.Errorf("900 total >= 800 cap should disable both rows, got %+v", got)
	}
}

// A user with no enabled clients left is skipped (don't re-flag every tick).
func TestEvaluateByUser_SkipsFullyDisabled(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clients := []model.Client{
		{ID: 1, Email: "u", Enabled: false, TrafficUp: 9999, QuotaBytes: 1},
	}
	if got := EvaluateByUser(clients, now); len(got) != 0 {
		t.Errorf("fully-disabled user must be skipped, got %+v", got)
	}
}
