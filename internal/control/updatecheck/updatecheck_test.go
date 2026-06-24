package updatecheck

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.23.0620", "0.24.0621", true},  // newer batch
		{"0.23.0620", "0.23.0621", true},  // same batch, later date
		{"0.23.0620", "1.01.0101", true},  // newer major
		{"0.23.0620", "0.23.0620", false}, // identical
		{"0.23.0620", "0.22.0701", false}, // older batch (even if later date)
		{"0.23.0620", "0.23.0619", false}, // earlier date
		{"0.23.0620", "v0.24.0621", true}, // leading v tolerated
		{"0.23.0620", "garbage", false},   // unparseable latest → never nag
		{"dev", "0.24.0621", false},       // unparseable current → never nag
		{"0.23.0620", "", false},          // empty
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q,%q)=%v want %v", c.current, c.latest, got, c.want)
		}
	}
}
