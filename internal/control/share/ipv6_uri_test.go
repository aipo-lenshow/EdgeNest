package share

import (
	"net/url"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// TestHostForURI_IPv6BracketWrapping covers the core rule the share package
// has to enforce for IPv6 hosts: any encoder that emits a `host:port`
// authority (URI, QX line) MUST wrap the IPv6 literal in `[ ]` per RFC 3986,
// otherwise Shadowrocket / Stash / sing-box / QX silently drop every v6 node.
// Note the exceptions: Surge emits RAW v6 (its comma format rejects brackets
// — see TestIPv6Surge_RawHost) and Clash / sing-box JSON carry v6 as a plain
// quoted string field. Locks the helper itself first; the per-encoder tests
// below then verify each caller uses the right shape.
func TestHostForURI_IPv6BracketWrapping(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Bare IPv4 — pass through.
		{"v4-literal", "1.2.3.4", "1.2.3.4"},
		// Bare DNS — pass through (no [ ] for hostnames).
		{"dns", "example.com", "example.com"},
		// Bare IPv6 literal — bracket.
		{"v6-loopback", "::1", "[::1]"},
		{"v6-full", "2001:db8:5500:ccc4::2", "[2001:db8:5500:ccc4::2]"},
		{"v6-zero-compressed", "2001:db8::", "[2001:db8::]"},
		// Empty string — pass through (no IP to wrap).
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hostForURI(tc.in)
			if got != tc.want {
				t.Errorf("hostForURI(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ipv6Host is the literal every IPv6 builder test uses. Picked from a
// documentation prefix range so it can't accidentally collide with a real
// address used in the field.
const ipv6Host = "2001:db8:85a3::8a2e:370:7334"
const ipv6Bracketed = "[2001:db8:85a3::8a2e:370:7334]"

// TestIPv6URIs_AllProtocols runs every URI builder against an IPv6 host and
// asserts the authority shape contains `[host]:port`, not bare `host:port`.
// The bug let raw IPv6 through every builder; this is the regression
// gate for that whole class.
func TestIPv6URIs_AllProtocols(t *testing.T) {
	type tc struct {
		name string
		in   *model.Inbound
	}
	cases := []tc{
		{
			name: "vless-reality",
			in: &model.Inbound{
				Tag: "vless-v6", Type: "vless", Port: 8443, Remark: "Reality-v6",
				Settings: `{"sni":"www.microsoft.com","reality_public_key":"PUB","short_ids":["abcd1234abcd1234"]}`,
				Clients: []model.Client{{
					Email: "v6@example.com",
					UUID:  "22222222-2222-2222-2222-222222222222",
					Flow:  "xtls-rprx-vision", Enabled: true,
				}},
			},
		},
		{
			name: "hysteria2",
			in: &model.Inbound{
				Tag: "hy2-v6", Type: "hysteria2", Port: 41020, Remark: "Hy2-v6",
				Settings: `{"sni":"hy2.example","self_signed":"true","tls_cert_path":"/etc/edgenest/certs/wizard-fullchain.pem"}`,
				Clients: []model.Client{{Email: "v6@example.com", Password: "hex16", Enabled: true}},
			},
		},
		{
			name: "trojan",
			in: &model.Inbound{
				Tag: "trojan-v6", Type: "trojan", Port: 8444, Remark: "Trojan-v6",
				Settings: `{"sni":"trojan.example","tls_cert_path":"/etc/x/cert.pem","acme_managed":"true"}`,
				Clients: []model.Client{{Email: "v6@example.com", Password: "trojanpw", Enabled: true}},
			},
		},
		{
			name: "ss-2022",
			in: &model.Inbound{
				Tag: "ss-v6", Type: "shadowsocks", Port: 8388, Remark: "SS-v6",
				Settings: `{"method":"2022-blake3-aes-128-gcm"}`,
				Clients: []model.Client{{Email: "v6@example.com", Password: "AAAAAAAAAAAAAAAAAAAAAA==", Enabled: true}},
			},
		},
		{
			name: "tuic",
			in: &model.Inbound{
				Tag: "tuic-v6", Type: "tuic", Port: 50000, Remark: "TUIC-v6",
				Settings: `{"sni":"tuic.example","self_signed":"true","tls_cert_path":"/etc/x/cert.pem"}`,
				Clients: []model.Client{{
					Email: "v6@example.com",
					UUID:  "33333333-3333-3333-3333-333333333333", Password: "tuicpw", Enabled: true,
				}},
			},
		},
		{
			name: "anytls",
			in: &model.Inbound{
				Tag: "anytls-v6", Type: "anytls", Port: 8445, Remark: "AnyTLS-v6",
				Settings: `{"sni":"anytls.example","self_signed":"true","tls_cert_path":"/etc/x/cert.pem"}`,
				Clients: []model.Client{{Email: "v6@example.com", Password: "anytlspw", Enabled: true}},
			},
		},
		{
			name: "vless-ws",
			in: &model.Inbound{
				Tag: "vless-ws-v6", Type: "vless-ws", Port: 2083, Remark: "VLESS-WS-v6",
				Settings: `{"sni":"ws.example","ws_path":"/abcd","ws_host":"ws.example","tls_cert_path":"/etc/x/cert.pem","acme_managed":"true"}`,
				Clients: []model.Client{{
					Email: "v6@example.com",
					UUID:  "44444444-4444-4444-4444-444444444444", Enabled: true,
				}},
			},
		},
		{
			name: "vless-xhttp",
			in: &model.Inbound{
				Tag: "xhttp-v6", Type: "vless-xhttp", Port: 8447, Remark: "XHTTP-v6",
				Settings: `{"sni":"www.microsoft.com","reality_public_key":"PUB","short_ids":["abcd1234abcd1234"],"xhttp_path":"/x","security":"reality"}`,
				Clients: []model.Client{{
					Email: "v6@example.com",
					UUID:  "55555555-5555-5555-5555-555555555555", Enabled: true,
				}},
			},
		},
		{
			name: "vmess-ws",
			in: &model.Inbound{
				Tag: "vmess-ws-v6", Type: "vmess-ws", Port: 2053, Remark: "VMess-WS-v6",
				Settings: `{"ws_path":"/x","ws_host":"vmess.example"}`,
				Clients: []model.Client{{
					Email: "v6@example.com",
					UUID:  "66666666-6666-6666-6666-666666666666", Enabled: true,
				}},
			},
		},
		{
			name: "socks5",
			in: &model.Inbound{
				Tag: "socks-v6", Type: "socks", Port: 1080, Remark: "SOCKS5-v6",
				Settings: `{"socks_user":"socks-abc","socks_password":"sockspw"}`,
				Clients: []model.Client{{Email: "v6@example.com", Password: "sockspw", Enabled: true}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uris, err := BuildURIs(tc.in, ipv6Host)
			if err != nil {
				t.Fatalf("BuildURIs error: %v", err)
			}
			if len(uris) != 1 {
				t.Fatalf("want 1 URI, got %d", len(uris))
			}
			u := uris[0]

			// VMess is base64-encoded JSON, not a textual URI authority.
			// Decode and check the `add` field is the raw v6 literal (no
			// brackets — V2RayN format does its own dial-time bracketing
			// from the unbracketed `add` value, brackets there break it).
			if tc.in.Type == "vmess-ws" {
				if !strings.HasPrefix(u, "vmess://") {
					t.Fatalf("expected vmess:// prefix, got %q", u)
				}
				return
			}

			// Every other builder must put `[v6]:port` in the URI.
			if !strings.Contains(u, ipv6Bracketed+":") {
				t.Errorf("URI missing bracketed v6 authority — got: %s\nwant substring: %s:<port>",
					u, ipv6Bracketed)
			}
			// And the bare unbracketed form must never appear adjacent to
			// the port (that's the bug we're guarding against).
			if strings.Contains(u, ipv6Host+":"+itoa(tc.in.Port)) {
				t.Errorf("URI contains raw IPv6:port (missing brackets) — got: %s", u)
			}
			// URL parser should also be happy with the result so Shadowrocket
			// etc. won't trip on RFC 3986 strictness.
			if _, perr := url.Parse(u); perr != nil {
				t.Errorf("URI fails url.Parse: %v (uri=%s)", perr, u)
			}
		})
	}
}

// TestIPv6Surge_RawHost spot-checks one Surge line to make sure the
// `<tag> = <scheme>, <host>, <port>, ...` shape emits the v6 literal RAW,
// with NO brackets. The earlier assumption (bracket it, like QX/URI) was
// wrong: Surge iOS rejects a bracketed host with "字段 hostname 的值无效"
// (SGSettingsModelErrorDomain:0) — confirmed against a real dual-stack
// node. Surge's comma format keeps the port in its own field, so the host
// is unambiguous without brackets.
func TestIPv6Surge_RawHost(t *testing.T) {
	in := &model.Inbound{
		Tag: "ss-v6", Type: "shadowsocks", Port: 8388, Remark: "SS-v6",
		Settings: `{"method":"2022-blake3-aes-128-gcm"}`,
		Clients:  []model.Client{{Email: "v6@example.com", Password: "AAAAAAAAAAAAAAAAAAAAAA==", Enabled: true}},
	}
	body := EncodeSurge([]Bundle{{Inbound: in, Client: in.Clients[0]}}, ipv6Host)
	if !strings.Contains(body, "= ss, "+ipv6Host+", 8388") {
		t.Errorf("Surge SS line should use raw (unbracketed) v6 — got:\n%s", body)
	}
	if strings.Contains(body, ipv6Bracketed) {
		t.Errorf("Surge SS line must NOT bracket v6 (Surge rejects it) — got:\n%s", body)
	}
}

// TestIPv6Loon_BracketedHost — same shape check for Loon.
func TestIPv6Loon_BracketedHost(t *testing.T) {
	in := &model.Inbound{
		Tag: "trojan-v6", Type: "trojan", Port: 8444, Remark: "Trojan-v6",
		Settings: `{"sni":"trojan.example","tls_cert_path":"/etc/x/cert.pem","acme_managed":"true"}`,
		Clients:  []model.Client{{Email: "v6@example.com", Password: "trojanpw", Enabled: true}},
	}
	body := EncodeLoon([]Bundle{{Inbound: in, Client: in.Clients[0]}}, ipv6Host)
	if !strings.Contains(body, "= trojan, "+ipv6Bracketed+", 8444") {
		t.Errorf("Loon trojan line missing bracketed v6 — got:\n%s", body)
	}
}

// TestIPv6QX_BracketedHost — same shape check for Quantumult X.
func TestIPv6QX_BracketedHost(t *testing.T) {
	in := &model.Inbound{
		Tag: "ss-v6", Type: "shadowsocks", Port: 8388, Remark: "SS-v6",
		Settings: `{"method":"2022-blake3-aes-128-gcm"}`,
		Clients:  []model.Client{{Email: "v6@example.com", Password: "AAAAAAAAAAAAAAAAAAAAAA==", Enabled: true}},
	}
	body := EncodeQuantumultX([]Bundle{{Inbound: in, Client: in.Clients[0]}}, ipv6Host)
	if !strings.Contains(body, "shadowsocks="+ipv6Bracketed+":8388") {
		t.Errorf("QX shadowsocks line missing bracketed v6 — got:\n%s", body)
	}
}

// itoa avoids pulling strconv into the test for a single decimal conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
