package singbox

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// renderVLESSReality emits a VLESS inbound with Reality TLS.
//
// Required settings:
//   - sni                 string   (the camouflage server, e.g. www.microsoft.com)
//   - server_port_target  int      (real backend; e.g. 443)        — optional, defaults to 443
//   - reality_private_key string   (x25519 private key)
//   - short_ids           []string (random short IDs)
//   - flow                string   (default: "xtls-rprx-vision")
//
// Per ClientSpec: Email + UUID required, Flow defaults to inbound's flow.
func renderVLESSReality(in core.InboundSpec) (map[string]any, error) {
	sni, err := requireString(in.Settings, "sni")
	if err != nil {
		return nil, err
	}
	privKey, err := requireString(in.Settings, "reality_private_key")
	if err != nil {
		return nil, err
	}
	shortIDs := getStringSlice(in.Settings, "short_ids", []string{""})
	flow := getString(in.Settings, "flow", "xtls-rprx-vision")
	targetPort := getInt(in.Settings, "server_port_target", 443)
	targetHost := getString(in.Settings, "server_name_target", sni)

	users, err := renderClientsAsUsers(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
		if c.UUID == "" {
			return nil, fmt.Errorf("client %q missing UUID", c.Email)
		}
		return map[string]any{
			"name": c.Email, // Invariant I1
			"uuid": c.UUID,
			"flow": orDefault(c.Flow, flow),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"type":        "vless",
		"tag":         in.Tag,
		"listen":      orDefault(in.Listen, "::"),
		"listen_port": in.Port,
		"users":       users,
		"tls": map[string]any{
			"enabled":     true,
			"server_name": sni,
			"reality": map[string]any{
				"enabled": true,
				"handshake": map[string]any{
					"server":      targetHost,
					"server_port": targetPort,
				},
				"private_key": privKey,
				"short_id":    shortIDs,
			},
		},
	}, nil
}

// renderHysteria2 emits a Hysteria2 inbound.
//
// Required:
//   - tls_cert_path / tls_key_path  string  (or use_acme: true with domain)
// Optional:
//   - up_mbps                 int     (default 0 = no limit / negotiated)
//   - down_mbps               int     (default 0 = no limit / negotiated)
//   - obfs                    string  ("salamander" or "")
//   - obfs_password           string  (required if obfs == salamander)
//   - masquerade              string  (URL — shorthand for {type:"proxy", url:X})
//   - masquerade_type         string  ("proxy" | "file" | "string")
//   - masquerade_url          string  (when type=proxy)
//   - masquerade_dir          string  (when type=file)
//   - masquerade_status_code  int     (when type=string; default 200)
//   - masquerade_headers      map     (when type=string)
//   - masquerade_content      string  (when type=string)
//   - ignore_client_bandwidth bool    (force server bandwidth values)
//   - sniff                   bool    (sniff UDP traffic; default true)
//   - sni                     string  (hint only — not consumed by engine)
//
// Per ClientSpec: Email + Password required.
func renderHysteria2(in core.InboundSpec) (map[string]any, error) {
	certPath := getString(in.Settings, "tls_cert_path", "")
	keyPath := getString(in.Settings, "tls_key_path", "")
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("hysteria2 requires tls_cert_path + tls_key_path (acme integration arrives in TASK-10)")
	}

	users, err := renderClientsAsUsers(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
		if c.Password == "" {
			return nil, fmt.Errorf("client %q missing password", c.Email)
		}
		return map[string]any{
			"name":     c.Email, // Invariant I1
			"password": c.Password,
		}, nil
	})
	if err != nil {
		return nil, err
	}

	inb := map[string]any{
		"type":        "hysteria2",
		"tag":         in.Tag,
		"listen":      orDefault(in.Listen, "::"),
		"listen_port": in.Port,
		"users":       users,
		"tls": map[string]any{
			"enabled":          true,
			"certificate_path": certPath,
			"key_path":         keyPath,
			"alpn":             []string{"h3"},
		},
	}
	if up := getInt(in.Settings, "up_mbps", 0); up > 0 {
		inb["up_mbps"] = up
	}
	if dn := getInt(in.Settings, "down_mbps", 0); dn > 0 {
		inb["down_mbps"] = dn
	}
	if getBool(in.Settings, "ignore_client_bandwidth", false) {
		inb["ignore_client_bandwidth"] = true
	}
	if obfs := getString(in.Settings, "obfs", ""); obfs != "" {
		pw, err := requireString(in.Settings, "obfs_password")
		if err != nil {
			return nil, fmt.Errorf("hysteria2 obfs=%s: %w", obfs, err)
		}
		inb["obfs"] = map[string]any{
			"type":     obfs,
			"password": pw,
		}
	}
	if mq := buildHysteria2Masquerade(in.Settings); mq != nil {
		inb["masquerade"] = mq
	}
	return inb, nil
}

// buildHysteria2Masquerade returns the value to set for the inbound's
// "masquerade" key. v1.13 accepts both the string-URL shorthand and the typed
// object form ({type: proxy|file|string, ...}); we emit whichever matches the
// settings the operator configured.
//
//   - masquerade            string → string-URL shorthand (back-compat).
//   - masquerade_type=proxy + masquerade_url=X            → {type:"proxy", url:X, rewrite_host:true}
//   - masquerade_type=file  + masquerade_dir=X            → {type:"file",  directory:X}
//   - masquerade_type=string + masquerade_content=X       → {type:"string", status_code:N, headers:..., content:X}
func buildHysteria2Masquerade(s map[string]any) any {
	if mq := getString(s, "masquerade", ""); mq != "" {
		return mq
	}
	switch getString(s, "masquerade_type", "") {
	case "proxy":
		if u := getString(s, "masquerade_url", ""); u != "" {
			return map[string]any{
				"type":         "proxy",
				"url":          u,
				"rewrite_host": true,
			}
		}
	case "file":
		if d := getString(s, "masquerade_dir", ""); d != "" {
			return map[string]any{
				"type":      "file",
				"directory": d,
			}
		}
	case "string":
		out := map[string]any{
			"type":        "string",
			"status_code": getInt(s, "masquerade_status_code", 200),
			"content":     getString(s, "masquerade_content", ""),
		}
		if h, ok := s["masquerade_headers"]; ok {
			out["headers"] = h
		}
		return out
	}
	return nil
}

// renderTrojan emits a Trojan inbound (TLS-only).
func renderTrojan(in core.InboundSpec) (map[string]any, error) {
	certPath, err := requireString(in.Settings, "tls_cert_path")
	if err != nil {
		return nil, err
	}
	keyPath, err := requireString(in.Settings, "tls_key_path")
	if err != nil {
		return nil, err
	}
	users, err := renderClientsAsUsers(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
		if c.Password == "" {
			return nil, fmt.Errorf("client %q missing password", c.Email)
		}
		return map[string]any{
			"name":     c.Email,
			"password": c.Password,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":        "trojan",
		"tag":         in.Tag,
		"listen":      orDefault(in.Listen, "::"),
		"listen_port": in.Port,
		"users":       users,
		"tls": map[string]any{
			"enabled":          true,
			"certificate_path": certPath,
			"key_path":         keyPath,
		},
	}, nil
}

// renderShadowsocks emits an SS-2022 inbound in single-user mode. The inbound
// password IS the user's PSK (no users[] array, no separate master key) — this
// is the form every mainstream client (Shadowrocket / Stash / Mihomo / Surge /
// QX / Clash / Loon / v2rayN) decodes reliably. EIH multi-user mode (master +
// users[]) breaks Shadowrocket and is dropped in U1.7.
//
// One SS inbound = one client. The API layer enforces this; adding a second
// client to an SS inbound returns 422 SS_INBOUND_SINGLE_CLIENT.
//
// Method default: 2022-blake3-aes-128-gcm.
func renderShadowsocks(in core.InboundSpec) (map[string]any, error) {
	method := getString(in.Settings, "method", "2022-blake3-aes-128-gcm")
	if len(in.Clients) == 0 {
		return nil, fmt.Errorf("shadowsocks inbound %q has no client; create one before applying", in.Tag)
	}
	if len(in.Clients) > 1 {
		return nil, fmt.Errorf("shadowsocks inbound %q has %d clients; single-user mode supports exactly 1 (create a separate SS inbound per user)", in.Tag, len(in.Clients))
	}
	c := in.Clients[0]
	if c.Email == "" {
		return nil, fmt.Errorf("shadowsocks client missing email (Invariant I1)")
	}
	if c.Password == "" {
		return nil, fmt.Errorf("shadowsocks client %q missing password (PSK)", c.Email)
	}
	return map[string]any{
		"type":        "shadowsocks",
		"tag":         in.Tag,
		"listen":      orDefault(in.Listen, "::"),
		"listen_port": in.Port,
		"network":     orDefault(in.Network, "tcp"),
		"method":      method,
		"password":    c.Password,
	}, nil
}

// renderTUIC emits a TUIC v5 inbound.
func renderTUIC(in core.InboundSpec) (map[string]any, error) {
	certPath, err := requireString(in.Settings, "tls_cert_path")
	if err != nil {
		return nil, err
	}
	keyPath, err := requireString(in.Settings, "tls_key_path")
	if err != nil {
		return nil, err
	}
	users, err := renderClientsAsUsers(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
		if c.UUID == "" || c.Password == "" {
			return nil, fmt.Errorf("client %q needs both uuid and password", c.Email)
		}
		return map[string]any{
			"name":     c.Email,
			"uuid":     c.UUID,
			"password": c.Password,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":              "tuic",
		"tag":               in.Tag,
		"listen":            orDefault(in.Listen, "::"),
		"listen_port":       in.Port,
		"users":             users,
		"congestion_control": getString(in.Settings, "congestion_control", "bbr"),
		// 0-RTT off on both ends: the saved 0.5-RTT key invites replay attacks
		// against the inbound, and the matched client setting (singbox.go) is
		// already false — so leaving the server at true gave the worst of both
		// worlds (replay risk on server, no resumption gain on client).
		"zero_rtt_handshake": false,
		"tls": map[string]any{
			"enabled":          true,
			"certificate_path": certPath,
			"key_path":         keyPath,
			"alpn":             []string{"h3"},
		},
	}, nil
}

// renderVMessWS emits a VMess-over-WebSocket inbound (TLS optional, usually behind CDN).
func renderVMessWS(in core.InboundSpec) (map[string]any, error) {
	path := getString(in.Settings, "ws_path", "/vmess")
	wsHost := getString(in.Settings, "ws_host", "")
	users, err := renderClientsAsUsers(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
		if c.UUID == "" {
			return nil, fmt.Errorf("client %q missing UUID", c.Email)
		}
		return map[string]any{
			"name":  c.Email,
			"uuid":  c.UUID,
			"alterId": 0,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	transport := map[string]any{
		"type": "ws",
		"path": path,
	}
	// ws_host becomes the upstream Host header — required when the inbound sits
	// behind a CDN / reverse proxy that routes by Host.
	if wsHost != "" {
		transport["headers"] = map[string]any{"Host": wsHost}
	}
	inb := map[string]any{
		"type":        "vmess",
		"tag":         in.Tag,
		"listen":      orDefault(in.Listen, "::"),
		"listen_port": in.Port,
		"users":       users,
		"transport":   transport,
	}
	if certPath := getString(in.Settings, "tls_cert_path", ""); certPath != "" {
		keyPath, err := requireString(in.Settings, "tls_key_path")
		if err != nil {
			return nil, err
		}
		inb["tls"] = map[string]any{
			"enabled":          true,
			"certificate_path": certPath,
			"key_path":         keyPath,
		}
	}
	return inb, nil
}

// renderVLESSWS emits VLESS-over-WebSocket. Same transport shape as VMess-WS.
func renderVLESSWS(in core.InboundSpec) (map[string]any, error) {
	path := getString(in.Settings, "ws_path", "/vless")
	wsHost := getString(in.Settings, "ws_host", "")
	users, err := renderClientsAsUsers(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
		if c.UUID == "" {
			return nil, fmt.Errorf("client %q missing UUID", c.Email)
		}
		return map[string]any{
			"name": c.Email,
			"uuid": c.UUID,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	transport := map[string]any{
		"type": "ws",
		"path": path,
	}
	if wsHost != "" {
		transport["headers"] = map[string]any{"Host": wsHost}
	}
	inb := map[string]any{
		"type":        "vless",
		"tag":         in.Tag,
		"listen":      orDefault(in.Listen, "::"),
		"listen_port": in.Port,
		"users":       users,
		"transport":   transport,
	}
	if certPath := getString(in.Settings, "tls_cert_path", ""); certPath != "" {
		keyPath, err := requireString(in.Settings, "tls_key_path")
		if err != nil {
			return nil, err
		}
		inb["tls"] = map[string]any{
			"enabled":          true,
			"certificate_path": certPath,
			"key_path":         keyPath,
		}
	}
	return inb, nil
}

// renderSocks emits a plain SOCKS5 inbound. Auth credentials come from inbound
// settings first (socks_user / socks_password — wizard-shape, lets the SOCKS5
// auth handle stay URI-safe ASCII while client.Email stays aligned with the
// rest of the bundle so subscription aggregation works). Falls back to
// client.Email / client.Password for manually-created inbounds.
//
// Listen defaults to "::" — same as every other protocol — so a user who
// creates a SOCKS5 inbound via the API without specifying Listen ends up
// bound to all interfaces (the old "127.0.0.1" default was wrong, an
// unauthenticated SOCKS5 on loopback was useless to external clients).
func renderSocks(in core.InboundSpec) (map[string]any, error) {
	inb := map[string]any{
		"type":        "socks",
		"tag":         in.Tag,
		"listen":      orDefault(in.Listen, "::"),
		"listen_port": in.Port,
	}
	settingsUser := getString(in.Settings, "socks_user", "")
	settingsPass := getString(in.Settings, "socks_password", "")
	if settingsUser != "" && settingsPass != "" {
		inb["users"] = []map[string]any{{
			"username": settingsUser,
			"password": settingsPass,
		}}
		return inb, nil
	}
	if len(in.Clients) > 0 {
		users, err := renderClientsAsUsers(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
			if c.Password == "" {
				return nil, fmt.Errorf("client %q missing password", c.Email)
			}
			return map[string]any{
				"username": c.Email,
				"password": c.Password,
			}, nil
		})
		if err != nil {
			return nil, err
		}
		inb["users"] = users
	}
	return inb, nil
}

// renderAnyTLS emits an anytls inbound (sing-box v1.12+ native protocol).
// Lives in sing-box because xray-core mainline does not implement anytls as
// of v26.x; the previous project tree had this in xray/, which was wrong.
//
// Required:
//   - tls_cert_path  string
//   - tls_key_path   string
//
// Per ClientSpec: Email + Password required.
func renderAnyTLS(in core.InboundSpec) (map[string]any, error) {
	certPath, err := requireString(in.Settings, "tls_cert_path")
	if err != nil {
		return nil, err
	}
	keyPath, err := requireString(in.Settings, "tls_key_path")
	if err != nil {
		return nil, err
	}
	users, err := renderClientsAsUsers(in.Clients, func(c core.ClientSpec) (map[string]any, error) {
		if c.Password == "" {
			return nil, fmt.Errorf("client %q missing password", c.Email)
		}
		return map[string]any{
			"name":     c.Email, // Invariant I1
			"password": c.Password,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":        "anytls",
		"tag":         in.Tag,
		"listen":      orDefault(in.Listen, "::"),
		"listen_port": in.Port,
		"users":       users,
		"tls": map[string]any{
			"enabled":          true,
			"certificate_path": certPath,
			"key_path":         keyPath,
		},
	}, nil
}

// renderClientsAsUsers maps each ClientSpec through f and returns the slice.
// Enforces Invariant I1: every client must have a non-empty Email.
func renderClientsAsUsers(clients []core.ClientSpec, f func(core.ClientSpec) (map[string]any, error)) ([]map[string]any, error) {
	if len(clients) == 0 {
		return nil, fmt.Errorf("at least one client required (users[].name == Client.Email)")
	}
	seen := map[string]bool{}
	out := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		if c.Email == "" {
			return nil, fmt.Errorf("client missing email (Invariant I1)")
		}
		if seen[c.Email] {
			return nil, fmt.Errorf("duplicate client email %q", c.Email)
		}
		seen[c.Email] = true
		u, err := f(c)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// ---- settings helpers ----

func getString(m map[string]any, key, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func requireString(m map[string]any, key string) (string, error) {
	v := getString(m, key, "")
	if v == "" {
		return "", fmt.Errorf("missing required setting %q", key)
	}
	return v, nil
}

func getBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	if v, ok := m[key]; ok {
		switch t := v.(type) {
		case bool:
			return t
		case string:
			switch t {
			case "true", "1", "yes":
				return true
			case "false", "0", "no":
				return false
			}
		}
	}
	return def
}

func getInt(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		case string:
			if i, err := strconv.Atoi(n); err == nil {
				return i
			}
		}
	}
	return def
}

func getStringSlice(m map[string]any, key string, def []string) []string {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return def
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// splitHostPort returns the host portion of "host:port" or def when absent.
func splitHostPort(hp, def string) string {
	if i := strings.LastIndex(hp, ":"); i > 0 {
		return hp[:i]
	}
	if hp == "" {
		return def
	}
	return hp
}

// splitHostPortPort returns the port portion of "host:port" or def when absent/invalid.
func splitHostPortPort(hp string, def int) int {
	if i := strings.LastIndex(hp, ":"); i > 0 {
		if p, err := strconv.Atoi(hp[i+1:]); err == nil {
			return p
		}
	}
	return def
}
