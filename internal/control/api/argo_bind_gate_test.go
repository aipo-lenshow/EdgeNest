package api

import "testing"

// TestArgoBindGate guards the create/update edit-path Argo gate: argo_bound is
// only valid on a plaintext WebSocket inbound listening on loopback, because
// cloudflared reaches the origin over plain HTTP and Cloudflare supplies TLS at
// its edge. A TLS origin (cert present) or a public listener would yield a dead
// tunnel, so a manual toggle on such an inbound must be refused (the wizard's
// Argo path builds correct inbounds).
func TestArgoBindGate(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		listen   string
		settings map[string]any
		reject   bool
	}{
		{"plaintext ws on loopback ok", "vmess-ws", "127.0.0.1", map[string]any{"argo_bound": "true", "ws_path": "/x"}, false},
		{"vless-ws plaintext loopback ok", "vless-ws", "127.0.0.1", map[string]any{"argo_bound": true}, false},
		{"ws with cert rejected", "vmess-ws", "127.0.0.1", map[string]any{"argo_bound": "true", "tls_cert_path": "/etc/edgenest/certs/x.pem"}, true},
		{"ws on public IP rejected", "vless-ws", "203.0.113.9", map[string]any{"argo_bound": "true"}, true},
		{"ws on wildcard rejected", "vless-ws", "::", map[string]any{"argo_bound": "true"}, true},
		{"non-ws type rejected", "trojan", "127.0.0.1", map[string]any{"argo_bound": "true"}, true},
		{"argo off ignores everything", "trojan", "203.0.113.9", map[string]any{"argo_bound": "false", "tls_cert_path": "/x"}, false},
		{"argo absent ok", "vmess-ws", "203.0.113.9", map[string]any{}, false},
		{"localhost listen ok", "vmess-ws", "localhost", map[string]any{"argo_bound": "true"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := argoBindGate(tc.typ, tc.listen, tc.settings)
			if tc.reject && msg == "" {
				t.Fatalf("expected rejection for %s on %s, got none", tc.typ, tc.listen)
			}
			if !tc.reject && msg != "" {
				t.Fatalf("expected pass for %s on %s, got %q", tc.typ, tc.listen, msg)
			}
		})
	}
}
