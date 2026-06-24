package auth

import (
	"strings"
	"testing"
	"time"
)

// A freshly generated secret must produce a code that verifies immediately —
// the round-trip an operator does at enrollment.
func TestTOTP_RoundTrip(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	counter := uint64(time.Now().Unix() / totpPeriod)
	code, err := totpCode(secret, counter)
	if err != nil {
		t.Fatalf("compute code: %v", err)
	}
	if !VerifyTOTP(secret, code) {
		t.Fatalf("freshly computed code %q failed to verify", code)
	}
}

// VerifyTOTP rejects malformed / wrong codes.
func TestVerifyTOTP_Rejects(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "000000"} {
		if VerifyTOTP(secret, bad) && bad == "000000" {
			// 000000 *could* legitimately match in a rare window; skip that case.
			continue
		}
		if VerifyTOTP(secret, bad) {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestGenerateRecoveryCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("want 10 codes, got %d", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if !strings.Contains(c, "-") || len(c) != 11 {
			t.Errorf("bad code format %q", c)
		}
		if seen[c] {
			t.Errorf("duplicate code %q", c)
		}
		seen[c] = true
	}
}

func TestTOTPURI(t *testing.T) {
	uri := TOTPURI("EdgeNest", "admin", "ABC234")
	for _, want := range []string{"otpauth://totp/", "secret=ABC234", "issuer=EdgeNest", "algorithm=SHA1", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q missing %q", uri, want)
		}
	}
}
