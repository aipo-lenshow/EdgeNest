package api

import (
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/system"
)

func TestBBRSummary(t *testing.T) {
	cases := []struct {
		name string
		in   system.BBRState
		want string
	}{
		{"linux bbr+fq enabled", system.BBRState{Supported: true, CongestionControl: "bbr", DefaultQdisc: "fq", Enabled: true}, "bbr+fq"},
		{"linux cubic+fq_codel disabled", system.BBRState{Supported: true, CongestionControl: "cubic", DefaultQdisc: "fq_codel"}, "cubic+fq_codel"},
		{"linux but /proc empty", system.BBRState{Supported: true}, "unknown"},
		{"non-linux", system.BBRState{Supported: false}, "unsupported"},
		{"linux cc only", system.BBRState{Supported: true, CongestionControl: "bbr"}, "bbr"},
		{"linux qdisc only", system.BBRState{Supported: true, DefaultQdisc: "fq"}, "?+fq"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bbrSummary(tc.in)
			if got != tc.want {
				t.Errorf("bbrSummary(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
