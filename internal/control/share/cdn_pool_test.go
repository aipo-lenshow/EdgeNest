package share

import "testing"

// RecommendedCDNIPs must be a subset of the full candidate pool — the "fill
// recommended" button seeds from the same set the speed test later probes, so a
// stray IP outside the pool would never get re-measured.
func TestRecommendedCDNIPs_SubsetOfCandidates(t *testing.T) {
	in := make(map[string]bool, len(CFCandidateIPs))
	for _, ip := range CFCandidateIPs {
		in[ip] = true
	}
	for _, ip := range RecommendedCDNIPs() {
		if !in[ip] {
			t.Errorf("recommended IP %q is not in the candidate pool", ip)
		}
	}
	if len(RecommendedCDNIPs()) == 0 {
		t.Fatal("recommended pool is empty")
	}
}

// TopNFastest keeps order, skips unreachable, and caps at N.
func TestTopNFastest(t *testing.T) {
	results := []CDNSpeedResult{
		{IP: "1.1.1.1", Reachable: true, LatencyMs: 10},
		{IP: "2.2.2.2", Reachable: false},
		{IP: "3.3.3.3", Reachable: true, LatencyMs: 20},
		{IP: "4.4.4.4", Reachable: true, LatencyMs: 30},
	}
	got := TopNFastest(results, 2)
	if len(got) != 2 || got[0] != "1.1.1.1" || got[1] != "3.3.3.3" {
		t.Fatalf("want [1.1.1.1 3.3.3.3], got %v", got)
	}
	// Asking for more than reachable returns only the reachable ones.
	all := TopNFastest(results, 10)
	if len(all) != 3 {
		t.Fatalf("want 3 reachable, got %d (%v)", len(all), all)
	}
}
