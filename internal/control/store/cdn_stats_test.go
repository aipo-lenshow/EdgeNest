package store

import "testing"

func TestPruneCDNStatsBefore_DeletesOnlyExpired(t *testing.T) {
	s := openTestStore(t)
	for _, day := range []string{"2026-09-01", "2026-09-30", "2026-10-29"} {
		if err := s.AddCDNSamples(day, "CN|联通|北方", "联通·北方", []CDNSample{{IP: "1.1.1.1", OK: true, Ms: 100}}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneCDNStatsBefore("2026-09-30")
	if err != nil || n != 1 {
		t.Fatalf("pruned %d err %v, want 1", n, err)
	}
	rows, _ := s.CDNStatsSince("2000-01-01", "")
	if len(rows) != 1 || rows[0].Samples != 2 || rows[0].MsSum != 200 {
		t.Fatalf("left %+v", rows)
	}
}
