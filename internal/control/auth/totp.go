package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238) is implemented in-house rather than pulling a dependency:
// the algorithm is a dozen lines (HMAC-SHA1 over a time counter), and every
// authenticator app (Google Authenticator, Authy, 1Password, …) speaks the
// same SHA1 / 6-digit / 30-second defaults. Keeping it here means the panel's
// 2FA has zero new supply-chain surface.

const (
	totpDigits = 6
	totpPeriod = 30 // seconds per step
	// totpSkew allows the code from one step before/after the current one so a
	// user typing right on a boundary (or with a slightly off device clock)
	// isn't rejected. ±1 step = ±30s tolerance, the conventional default.
	totpSkew = 1
)

// GenerateTOTPSecret returns a fresh base32 (no padding) secret — the format
// authenticator apps expect in an otpauth:// URI. 20 bytes = 160 bits, the
// RFC 4226 recommended minimum for SHA1.
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

// TOTPURI builds the otpauth:// provisioning URI the front-end renders as a QR
// code. issuer + account label show in the authenticator app's entry.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// totpCode computes the RFC 6238 code for the given secret and counter.
func totpCode(secret string, counter uint64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	val := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, val%mod), nil
}

// VerifyTOTP reports whether code matches secret within the allowed time skew.
// Compares in constant time and accepts the current step ±totpSkew.
func VerifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	counter := uint64(time.Now().Unix() / totpPeriod)
	for d := -totpSkew; d <= totpSkew; d++ {
		want, err := totpCode(secret, counter+uint64(d))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// GenerateRecoveryCodes returns n single-use backup codes formatted as
// "xxxxx-xxxxx" (10 hex chars). They let an operator who lost their
// authenticator device still get in; each is consumed on use.
func GenerateRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, 5)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		hexs := fmt.Sprintf("%010x", raw)
		codes = append(codes, hexs[:5]+"-"+hexs[5:])
	}
	return codes, nil
}
