package system

import "testing"

func TestReservedPorts_IncludesPanelAndSystem(t *testing.T) {
	got := ReservedPorts(2087)
	want := map[int]bool{22: true, 53: true, 2087: true}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected reserved port: %d", p)
		}
		delete(want, p)
	}
	if len(want) > 0 {
		t.Errorf("missing reserved ports: %v", want)
	}
}

func TestReservedPorts_OmitsZeroPanel(t *testing.T) {
	got := ReservedPorts(0)
	for _, p := range got {
		if p == 0 {
			t.Errorf("zero panel port leaked into reserved list")
		}
	}
}

func TestIsReserved(t *testing.T) {
	cases := []struct {
		port, panel int
		want        bool
	}{
		{22, 2087, true},   // SSH
		{53, 2087, true},   // DNS
		{2087, 2087, true}, // panel
		{8443, 2087, false},
		{443, 2087, false}, // panel != 443, so it stays available
		{443, 443, true},   // operator put panel on 443, now 443 is reserved
	}
	for _, c := range cases {
		if got := IsReserved(c.port, c.panel); got != c.want {
			t.Errorf("IsReserved(%d, panel=%d) = %v, want %v",
				c.port, c.panel, got, c.want)
		}
	}
}

func TestIsCFWhitelisted(t *testing.T) {
	for _, p := range CFHTTPSWhitelist {
		if !IsCFWhitelisted(p) {
			t.Errorf("CF whitelist member %d failed lookup", p)
		}
	}
	for _, p := range []int{80, 8080, 1080, 8388, 41020} {
		if IsCFWhitelisted(p) {
			t.Errorf("non-whitelist port %d returned true", p)
		}
	}
}
