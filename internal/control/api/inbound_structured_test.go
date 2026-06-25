package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestBuildInboundSettings_FullProtocolMatrix is the load-bearing test for U1
// — it asserts that the structured form path produces engine-ready settings
// for every protocol in the dropdown, with secrets auto-minted and defaults
// applied. A regression here means a user creating an inbound through the new
// UI gets a config the engine rejects.
func TestBuildInboundSettings_FullProtocolMatrix(t *testing.T) {
	type expect struct {
		mustHave map[string]any // exact key→value match (defaults / autofill)
		mustGen  []string       // keys that must be present + non-empty (secrets)
	}
	cases := []struct {
		typ      string
		advanced map[string]any
		expect   expect
	}{
		{
			typ:      "vless",
			advanced: map[string]any{},
			expect: expect{
				mustHave: map[string]any{
					"sni":                "www.apple.com",
					"flow":               "xtls-rprx-vision",
					"server_port_target": 443,
				},
				mustGen: []string{"reality_private_key", "reality_public_key", "short_ids"},
			},
		},
		{
			typ: "vless",
			advanced: map[string]any{
				"sni":                "www.cloudflare.com",
				"server_port_target": 8443,
			},
			expect: expect{
				mustHave: map[string]any{
					"sni":                "www.cloudflare.com",
					"server_port_target": 8443,
				},
				mustGen: []string{"reality_private_key", "reality_public_key"},
			},
		},
		{
			typ:      "hysteria2",
			advanced: map[string]any{"obfs": true, "up_mbps": 200, "down_mbps": 800},
			expect: expect{
				mustHave: map[string]any{
					"obfs":          "salamander",
					"up_mbps":       200,
					"down_mbps":     800,
					"tls_cert_path": defaultWizardCertPath,
				},
				mustGen: []string{"obfs_password"},
			},
		},
		{
			typ:      "hysteria2",
			advanced: map[string]any{"obfs": false},
			expect: expect{
				mustHave: map[string]any{
					"tls_cert_path": defaultWizardCertPath,
					"up_mbps":       100,
					"down_mbps":     500,
				},
				// when obfs off, obfs_password must be absent
			},
		},
		{
			typ:      "trojan",
			advanced: map[string]any{"sni": "example.com"},
			expect: expect{
				mustHave: map[string]any{
					"sni":           "example.com",
					"tls_cert_path": defaultWizardCertPath,
					"tls_key_path":  defaultWizardKeyPath,
				},
			},
		},
		{
			typ:      "shadowsocks",
			advanced: map[string]any{"method": "2022-blake3-aes-256-gcm"},
			expect: expect{
				mustHave: map[string]any{"method": "2022-blake3-aes-256-gcm"},
				mustGen:  []string{"password"},
			},
		},
		{
			typ:      "tuic",
			advanced: map[string]any{},
			expect: expect{
				mustHave: map[string]any{
					"congestion_control": "bbr",
					"tls_cert_path":      defaultWizardCertPath,
				},
			},
		},
		{
			typ:      "anytls",
			advanced: map[string]any{"sni": "any.example"},
			expect: expect{
				mustHave: map[string]any{
					"sni":           "any.example",
					"tls_cert_path": defaultWizardCertPath,
				},
			},
		},
		{
			typ:      "vless-ws",
			advanced: map[string]any{"ws_path": "/custom"},
			expect: expect{
				mustHave: map[string]any{"ws_path": "/custom"},
			},
		},
		{
			typ:      "vmess",
			advanced: map[string]any{},
			expect: expect{
				mustHave: map[string]any{"ws_path": "/vmess"},
			},
		},
		{
			typ:      "vmess-ws",
			advanced: map[string]any{"ws_host": "edge.example"},
			expect: expect{
				mustHave: map[string]any{"ws_path": "/vmess", "ws_host": "edge.example"},
			},
		},
		{
			typ:      "vless-xhttp",
			advanced: map[string]any{"security": "reality"},
			expect: expect{
				mustHave: map[string]any{
					"security":   "reality",
					"xhttp_path": "/xhttp",
				},
				mustGen: []string{"reality_private_key", "reality_public_key", "short_ids"},
			},
		},
		{
			typ:      "socks",
			advanced: map[string]any{},
			expect: expect{
				mustHave: map[string]any{"require_auth": true},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			out, err := BuildInboundSettings(tc.typ, tc.advanced)
			if err != nil {
				t.Fatalf("BuildInboundSettings(%s): %v", tc.typ, err)
			}
			for k, want := range tc.expect.mustHave {
				got := out[k]
				if !equalAny(got, want) {
					t.Errorf("%s: settings[%q] = %v (%T), want %v (%T)",
						tc.typ, k, got, got, want, want)
				}
			}
			for _, k := range tc.expect.mustGen {
				v, ok := out[k]
				if !ok {
					t.Errorf("%s: missing generated key %q", tc.typ, k)
					continue
				}
				if !meaningful(v) {
					t.Errorf("%s: generated key %q is empty/placeholder: %v", tc.typ, k, v)
				}
			}
			// Hy2 with obfs:false must NOT have obfs_password.
			if tc.typ == "hysteria2" {
				if v, ok := tc.advanced["obfs"]; ok {
					if b, isBool := v.(bool); isBool && !b {
						if _, present := out["obfs_password"]; present {
							t.Errorf("hy2 obfs=false: obfs_password should not be present")
						}
						if _, present := out["obfs"]; present {
							t.Errorf("hy2 obfs=false: obfs key should be stripped")
						}
					}
				}
			}
		})
	}
}

// TestBuildInboundSettings_DropsUnknownFields proves the structured form is a
// firewall: advanced fields not in the per-protocol whitelist are silently
// dropped so the API can't be tricked into stuffing arbitrary settings keys
// into the engine config.
func TestBuildInboundSettings_DropsUnknownFields(t *testing.T) {
	out, err := BuildInboundSettings("vless", map[string]any{
		"sni":             "www.microsoft.com",
		"reality_private_key": "INJECTED_BY_ATTACKER",
		"random_unknown":  "should be dropped",
	})
	if err != nil {
		t.Fatalf("BuildInboundSettings: %v", err)
	}
	if v := out["random_unknown"]; v != nil {
		t.Errorf("unknown key leaked through: %v", v)
	}
	// reality_private_key still gets generated (autofill), but the attacker's
	// value must not survive.
	if strings.Contains(toStr(out["reality_private_key"]), "INJECTED") {
		t.Errorf("attacker private key was honoured: %v", out["reality_private_key"])
	}
}

// TestApplyAdvancedUpdate_PreservesSecrets confirms an edit that doesn't
// re-send secrets keeps them on the row (otherwise the engine breaks the next
// time the user toggles enabled).
func TestApplyAdvancedUpdate_PreservesSecrets(t *testing.T) {
	existing := map[string]any{
		"reality_private_key": "PRESERVED_PRIV",
		"reality_public_key":  "PRESERVED_PUB",
		"short_ids":           []any{"abcd"},
		"sni":                 "www.microsoft.com",
		"server_port_target":  443,
		"flow":                "xtls-rprx-vision",
	}
	out, err := ApplyAdvancedUpdate("vless", map[string]any{
		"sni": "www.cloudflare.com",
	}, existing)
	if err != nil {
		t.Fatalf("ApplyAdvancedUpdate: %v", err)
	}
	if out["reality_private_key"] != "PRESERVED_PRIV" {
		t.Errorf("private key lost on edit: %v", out["reality_private_key"])
	}
	if out["reality_public_key"] != "PRESERVED_PUB" {
		t.Errorf("public key lost on edit: %v", out["reality_public_key"])
	}
	if out["sni"] != "www.cloudflare.com" {
		t.Errorf("sni not updated: %v", out["sni"])
	}
}

// TestParseInboundAdvanced_RoundTrip proves the form can round-trip: build
// → store → parse should give back the original advanced fields (minus
// secrets, which the form never displays anyway).
func TestParseInboundAdvanced_RoundTrip(t *testing.T) {
	cases := []struct {
		typ      string
		advanced map[string]any
	}{
		{"vless", map[string]any{"sni": "www.bing.com", "server_port_target": 443}},
		{"hysteria2", map[string]any{"obfs": true, "up_mbps": 50, "down_mbps": 300, "sni": "www.bing.com"}},
		{"trojan", map[string]any{"sni": "trojan.example"}},
		{"shadowsocks", map[string]any{"method": "2022-blake3-aes-128-gcm"}},
		{"tuic", map[string]any{"congestion_control": "cubic", "sni": "tu.example"}},
		{"vless-ws", map[string]any{"ws_path": "/foo"}},
		{"vmess-ws", map[string]any{"ws_path": "/bar"}},
		{"anytls", map[string]any{"sni": "any.example"}},
		{"vless-xhttp", map[string]any{"security": "tls", "sni": "x.example", "xhttp_path": "/x"}},
		{"socks", map[string]any{"require_auth": true}},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			built, err := BuildInboundSettings(tc.typ, tc.advanced)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			raw, _ := json.Marshal(built)
			parsed, err := ParseInboundAdvanced(tc.typ, string(raw))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			for k, want := range tc.advanced {
				if got := parsed[k]; !equalAny(got, want) {
					t.Errorf("%s: advanced[%q] roundtrip: got %v, want %v", tc.typ, k, got, want)
				}
			}
			// Secrets must NEVER appear in the parsed advanced.
			for _, k := range []string{"reality_private_key", "obfs_password", "password"} {
				if _, ok := parsed[k]; ok {
					t.Errorf("%s: parsed advanced still has secret %q", tc.typ, k)
				}
			}
		})
	}
}

// TestParseInboundAdvanced_UnparseableReturnsError mirrors the UI fallback:
// when settings JSON is corrupt, return ErrUnparseableSettings so the form
// shows the raw textarea + warning.
func TestParseInboundAdvanced_UnparseableReturnsError(t *testing.T) {
	_, err := ParseInboundAdvanced("vless", "this is not json")
	if err != ErrUnparseableSettings {
		t.Errorf("want ErrUnparseableSettings, got %v", err)
	}
}

// equalAny compares values that came back through json.Unmarshal (numbers as
// float64) against the int / string literals the test uses.
func equalAny(got, want any) bool {
	if reflect.DeepEqual(got, want) {
		return true
	}
	switch w := want.(type) {
	case int:
		if g, ok := got.(int); ok {
			return g == w
		}
		if g, ok := got.(float64); ok {
			return g == float64(w)
		}
	case float64:
		if g, ok := got.(int); ok {
			return float64(g) == w
		}
	}
	return false
}
