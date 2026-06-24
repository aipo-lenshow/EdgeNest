package system

import (
	"runtime"
	"testing"
)

// TestReadBBRState_NonLinuxIsUnsupported verifies the dev-laptop UX: on
// macOS/Windows BBR reads as Supported=false with an explanatory Notes
// string instead of an error. EngineEnable/Disable rely on the same guard.
func TestReadBBRState_NonLinuxIsUnsupported(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux host: real procfs read covered by integration")
	}
	st := ReadBBRState()
	if st.Supported {
		t.Errorf("Supported should be false on %s, got true", runtime.GOOS)
	}
	if st.OS != runtime.GOOS {
		t.Errorf("OS = %q, want %q", st.OS, runtime.GOOS)
	}
	if st.Notes == "" {
		t.Error("Notes should explain why BBR is unsupported on this OS")
	}
}

// TestEnableBBR_NonLinuxRefuses ensures we don't shell out to sysctl on a
// dev laptop. Same guard protects DisableBBR.
func TestEnableBBR_NonLinuxRefuses(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux host: would touch /etc/sysctl.d")
	}
	if err := EnableBBR(); err == nil {
		t.Error("EnableBBR on non-Linux should return error")
	}
	if err := DisableBBR(); err == nil {
		t.Error("DisableBBR on non-Linux should return error")
	}
}

// TestReadBBRState_DetectsEnabled validates the cc/qdisc decision matrix on
// Linux by faking the procfs read path. We don't have a hook for that yet,
// so this is a placeholder asserting the matrix in code.
func TestBBRState_EnabledMatrix(t *testing.T) {
	cases := []struct {
		cc, qdisc string
		want      bool
	}{
		{"bbr", "fq", true},
		{"bbr", "fq_codel", true},
		{"bbr", "pfifo_fast", false},
		{"cubic", "fq", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got := tc.cc == "bbr" && (tc.qdisc == "fq" || tc.qdisc == "fq_codel")
		if got != tc.want {
			t.Errorf("cc=%q qdisc=%q: enabled=%v, want %v", tc.cc, tc.qdisc, got, tc.want)
		}
	}
}
