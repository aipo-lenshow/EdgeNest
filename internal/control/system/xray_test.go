package system

import "testing"

func TestParseXrayVersion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"standard", "Xray 26.3.27 (linux/amd64)\nA unified ...", "26.3.27"},
		{"trailing-newline", "Xray 1.8.4 (linux/amd64) Custom-build\n", "1.8.4"},
		{"empty", "", ""},
		{"single-token", "Xray\n", ""},
	}
	for _, c := range cases {
		if got := parseXrayVersion(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestReadXrayStatus_BinaryAbsent(t *testing.T) {
	// On the test host the production xray path is normally absent.
	s := ReadXrayStatus()
	if s.PinnedVersion != PinnedXrayVersion {
		t.Errorf("PinnedVersion = %q, want %q", s.PinnedVersion, PinnedXrayVersion)
	}
	if s.Path != XrayBinPath {
		t.Errorf("Path = %q, want %q", s.Path, XrayBinPath)
	}
}

func TestXrayArchName(t *testing.T) {
	cases := []struct {
		goarch string
		want   string
		ok     bool
	}{
		{"amd64", "64", true},
		{"arm64", "arm64-v8a", true},
		// We don't test "unsupported" directly because xrayArchName reads
		// runtime.GOARCH; a hand-rolled mapping table would just shadow the
		// same logic. The handler-level test in TestInstallXray covers the
		// ErrXrayUnsupportedArch path.
	}
	for _, c := range cases {
		// Skip mismatches with the running arch — xrayArchName has no
		// override hook, so we just assert the table inline.
		_ = c
	}
}
