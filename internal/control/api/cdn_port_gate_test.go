package api

import "testing"

// TestCdnPortGate guards the create/update edit-path CDN gate (0-48): a
// cdn_mode inbound on a non-Cloudflare port must be refused so the
// subscription never hands out a CF anycast IP that can't reach the origin.
func TestCdnPortGate(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		port     int
		settings map[string]any
		reject   bool
	}{
		{"ws cdn on CF port ok", "vless-ws", 2083, map[string]any{"cdn_mode": "true"}, false},
		{"ws cdn on CF port 443 ok", "vmess-ws", 443, map[string]any{"cdn_mode": true}, false},
		{"ws cdn on non-CF port rejected", "vless-ws", 2085, map[string]any{"cdn_mode": "true"}, true},
		{"ws cdn bool on non-CF port rejected", "vmess-ws", 8080, map[string]any{"cdn_mode": true}, true},
		{"xhttp cdn on non-CF port rejected", "vless-xhttp", 2098, map[string]any{"cdn_mode": "true"}, true},
		{"cdn off on non-CF port ok", "vless-ws", 2085, map[string]any{"cdn_mode": "false"}, false},
		{"cdn absent on non-CF port ok", "vless-ws", 2085, map[string]any{}, false},
		{"non-frontable type ignored", "trojan", 8444, map[string]any{"cdn_mode": "true"}, false},
		{"reality not frontable", "vless", 8443, map[string]any{"cdn_mode": "true"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := cdnPortGate(tc.typ, tc.port, tc.settings)
			if tc.reject && msg == "" {
				t.Fatalf("expected rejection for %s:%d, got none", tc.typ, tc.port)
			}
			if !tc.reject && msg != "" {
				t.Fatalf("expected pass for %s:%d, got %q", tc.typ, tc.port, msg)
			}
		})
	}
}
