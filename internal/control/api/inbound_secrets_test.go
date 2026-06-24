package api

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestAutofill_VLESSReality_GeneratesKeypair(t *testing.T) {
	s, err := autofillInboundSettings("vless", map[string]any{
		"reality_private_key": "<base64 X25519 private key>",
		"short_ids":           []any{""},
	}, nil)
	if err != nil {
		t.Fatalf("autofill: %v", err)
	}
	priv, _ := s["reality_private_key"].(string)
	pub, _ := s["reality_public_key"].(string)
	if priv == "" || strings.HasPrefix(priv, "<") {
		t.Errorf("reality_private_key not regenerated: %q", priv)
	}
	if pub == "" {
		t.Errorf("reality_public_key not emitted")
	}
	if _, err := base64.RawURLEncoding.DecodeString(priv); err != nil {
		t.Errorf("priv not valid base64url: %v", err)
	}
	sids, _ := s["short_ids"].([]any)
	if len(sids) == 0 {
		t.Errorf("short_ids should be filled")
	}
	if s["sni"] != "www.microsoft.com" {
		t.Errorf("sni default missing: %v", s["sni"])
	}
	if s["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow default missing: %v", s["flow"])
	}
}

func TestAutofill_VLESSReality_PreservesExistingOnUpdate(t *testing.T) {
	// Simulates panel UI round-trip: GET scrubs reality_private_key, user
	// edits an unrelated field, PUTs back without the secret. We must keep
	// the original private key from the existing row.
	existing := map[string]any{
		"reality_private_key": "EXISTING_PRIV",
		"reality_public_key":  "EXISTING_PUB",
		"sni":                 "www.cloudflare.com",
	}
	incoming := map[string]any{
		"sni": "www.cloudflare.com", // unchanged, no private key
	}
	out, err := autofillInboundSettings("vless", incoming, existing)
	if err != nil {
		t.Fatalf("autofill: %v", err)
	}
	if out["reality_private_key"] != "EXISTING_PRIV" {
		t.Errorf("private key not carried over: %v", out["reality_private_key"])
	}
	if out["reality_public_key"] != "EXISTING_PUB" {
		t.Errorf("public key not carried over: %v", out["reality_public_key"])
	}
}

func TestAutofill_Shadowsocks_GeneratesPSK(t *testing.T) {
	s, err := autofillInboundSettings("shadowsocks", map[string]any{
		"password": "<base64 16-byte PSK>",
	}, nil)
	if err != nil {
		t.Fatalf("autofill: %v", err)
	}
	pw, _ := s["password"].(string)
	if pw == "" || strings.HasPrefix(pw, "<") {
		t.Errorf("password not regenerated: %q", pw)
	}
	if s["method"] != "2022-blake3-aes-128-gcm" {
		t.Errorf("method default missing: %v", s["method"])
	}
	dec, err := base64.StdEncoding.DecodeString(pw)
	if err != nil {
		t.Fatalf("psk not base64: %v", err)
	}
	if len(dec) != 16 {
		t.Errorf("aes-128 PSK should be 16 bytes, got %d", len(dec))
	}
}

func TestAutofill_Shadowsocks_PSKLengthFollowsMethod(t *testing.T) {
	s, err := autofillInboundSettings("shadowsocks", map[string]any{
		"method": "2022-blake3-aes-256-gcm",
	}, nil)
	if err != nil {
		t.Fatalf("autofill: %v", err)
	}
	pw, _ := s["password"].(string)
	dec, err := base64.StdEncoding.DecodeString(pw)
	if err != nil {
		t.Fatalf("psk not base64: %v", err)
	}
	if len(dec) != 32 {
		t.Errorf("aes-256 PSK should be 32 bytes, got %d", len(dec))
	}
}

func TestAutofill_Hysteria2_FillsCertAndObfs(t *testing.T) {
	s, err := autofillInboundSettings("hysteria2", map[string]any{
		"obfs_password": "<random>",
	}, nil)
	if err != nil {
		t.Fatalf("autofill: %v", err)
	}
	if s["tls_cert_path"] != defaultWizardCertPath {
		t.Errorf("cert default missing: %v", s["tls_cert_path"])
	}
	if s["tls_key_path"] != defaultWizardKeyPath {
		t.Errorf("key default missing: %v", s["tls_key_path"])
	}
	obfsPW, _ := s["obfs_password"].(string)
	if obfsPW == "" || strings.HasPrefix(obfsPW, "<") {
		t.Errorf("obfs_password not regenerated: %q", obfsPW)
	}
	if s["obfs"] != "salamander" {
		t.Errorf("obfs name not seeded: %v", s["obfs"])
	}
}

func TestAutofill_Trojan_FillsCertDefaults(t *testing.T) {
	s, err := autofillInboundSettings("trojan", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("autofill: %v", err)
	}
	if s["tls_cert_path"] != defaultWizardCertPath {
		t.Errorf("trojan cert default missing")
	}
}

func TestAutofill_VLESSXHTTP_RealityKeys(t *testing.T) {
	s, err := autofillInboundSettings("vless-xhttp", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("autofill: %v", err)
	}
	if s["security"] != "reality" {
		t.Errorf("security default missing")
	}
	priv, _ := s["reality_private_key"].(string)
	if priv == "" {
		t.Errorf("xhttp reality priv not generated")
	}
	if s["xhttp_path"] != "/xhttp" {
		t.Errorf("xhttp_path default missing")
	}
}

func TestAutofill_UnknownType_PassesThrough(t *testing.T) {
	// Operator-typed exotic protocol — autofill shouldn't error, just
	// hand back what they typed.
	in := map[string]any{"any": "thing"}
	out, err := autofillInboundSettings("custom", in, nil)
	if err != nil {
		t.Fatalf("autofill: %v", err)
	}
	if out["any"] != "thing" {
		t.Errorf("custom settings lost")
	}
}

func TestMeaningful_Cases(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"nil", nil, false},
		{"empty string", "", false},
		{"placeholder", "<base64 X25519 private key>", false},
		{"real string", "abc", true},
		{"zero number", float64(0), true},
		{"empty slice", []any{}, false},
		{"slice of empty", []any{"", ""}, false},
		{"slice with real", []any{"", "x"}, true},
	}
	for _, c := range cases {
		if got := meaningful(c.in); got != c.want {
			t.Errorf("%s: meaningful(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
