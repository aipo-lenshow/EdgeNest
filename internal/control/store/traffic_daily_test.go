package store

import (
	"path/filepath"
	"testing"
)

// TestDailyTrafficUpsertAndSum verifies the ON CONFLICT increment path (two
// credits to the same date+email accumulate into one row) and the Since sums
// (per-user and server-wide, date-bounded).
func TestDailyTrafficUpsertAndSum(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Two credits same (date, email) must add up into a single row.
	mustNil(t, st.AddDailyTraffic("2026-06-20", "alice", 100, 200))
	mustNil(t, st.AddDailyTraffic("2026-06-20", "alice", 50, 25))
	// A different day for alice (still in June).
	mustNil(t, st.AddDailyTraffic("2026-06-21", "alice", 10, 10))
	// A prior month — must be excluded by a June-1 cutoff.
	mustNil(t, st.AddDailyTraffic("2026-05-31", "alice", 9999, 9999))
	// Another user, same month.
	mustNil(t, st.AddDailyTraffic("2026-06-20", "bob", 1, 2))
	// Zero delta is a no-op (must not create a row / error).
	mustNil(t, st.AddDailyTraffic("2026-06-20", "carol", 0, 0))

	// alice month-to-date (>= 2026-06-01): 150+25 on the 20th + 10+10 on the
	// 21st = up 160, down 235. The May row is excluded.
	up, down, err := st.UserTrafficSince("alice", "2026-06-01")
	mustNil(t, err)
	if up != 160 || down != 235 {
		t.Fatalf("alice MTD = up %d down %d, want 160/235", up, down)
	}

	// Server month-to-date: alice 160/235 + bob 1/2 = 161/237.
	sUp, sDown, err := st.ServerTrafficSince("2026-06-01")
	mustNil(t, err)
	if sUp != 161 || sDown != 237 {
		t.Fatalf("server MTD = up %d down %d, want 161/237", sUp, sDown)
	}

	// Prune drops the May bucket; alice all-time then equals her June total.
	mustNil(t, st.PruneDailyBefore("2026-06-01"))
	up, down, err = st.UserTrafficSince("alice", "2000-01-01")
	mustNil(t, err)
	if up != 160 || down != 235 {
		t.Fatalf("alice after prune = up %d down %d, want 160/235", up, down)
	}
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
