package share

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// G2: Mihomo (Clash.Meta) subscription carries anti-leak DNS + private-direct,
// and ZERO region routing.
func TestEncodeClash_AntiLeakDNS_RegionFree(t *testing.T) {
	out := EncodeClash(sampleBundles(), "1.2.3.4")
	for _, want := range []string{
		"enhanced-mode: fake-ip",
		"fake-ip-range: 198.18.0.1/16", // Mihomo default, NOT sing-box /15
		"respect-rules: true",
		"proxy-server-nameserver:",
		"https://1.1.1.1/dns-query",
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"MATCH,EdgeNest",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mihomo output missing %q\n---\n%s", want, out)
		}
	}
	// Region-free guard: project rule forbids assuming geography. No geosite /
	// geoip / GEOIP,CN may ever appear.
	for _, banned := range []string{"geosite", "GEOSITE", "GEOIP", "geoip", ",CN", "rule-set"} {
		if strings.Contains(out, banned) {
			t.Errorf("mihomo output leaked region/geo token %q (project rule: no geography)\n%s", banned, out)
		}
	}
}

// G2: Stash stays minimal — Mihomo-only DNS keys (respect-rules /
// proxy-server-nameserver) would trip Stash's strict schema validator, so they
// must NOT appear until Stash's own DNS dialect is researched.
func TestEncodeStash_StaysMinimal(t *testing.T) {
	out := EncodeStash(sampleBundles(), "1.2.3.4")
	for _, absent := range []string{"respect-rules", "proxy-server-nameserver", "enhanced-mode"} {
		if strings.Contains(out, absent) {
			t.Errorf("Stash must stay minimal (strict validator); leaked %q\n%s", absent, out)
		}
	}
	if !strings.Contains(out, "MATCH,EdgeNest") {
		t.Errorf("Stash still needs its MATCH rule\n%s", out)
	}
}

// G2: sing-box subscription routes private/LAN to direct (so tun-captured LAN
// traffic isn't relayed to the VPS), geo-free via ip_is_private.
func TestEncodeSingbox_PrivateDirect(t *testing.T) {
	out := EncodeSingbox(sampleBundles(), "1.2.3.4")
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	rules := cfg["route"].(map[string]any)["rules"].([]any)
	found := false
	for _, r := range rules {
		m := r.(map[string]any)
		if m["ip_is_private"] == true && m["outbound"] == "direct" && m["action"] == "route" {
			found = true
		}
	}
	if !found {
		t.Errorf("sing-box route missing {ip_is_private:true, action:route, outbound:direct}\n%s", out)
	}
}

// G3: Hy2 port hopping reaches sing-box (server_ports) and Mihomo (ports).
func TestEncodeSingboxClash_Hy2PortHopping(t *testing.T) {
	hop := &model.Inbound{
		Tag: "h2", Type: "hysteria2", Port: 41020, Remark: "H2",
		Settings: `{"sni":"edgenest.local","port_hop_start":20000,"port_hop_end":40000}`,
	}
	bundles := []Bundle{{Inbound: hop, Client: model.Client{Email: "u@x", Password: "pw", Enabled: true}}}

	sb := EncodeSingbox(bundles, "1.2.3.4")
	// sing-box server_ports uses COLON ("start:end"); a hyphen makes sing-box
	// reject the outbound with "bad port range" and the whole config fails to load.
	if !strings.Contains(sb, `"server_ports"`) || !strings.Contains(sb, `"20000:40000"`) {
		t.Errorf("sing-box Hy2 missing server_ports range\n%s", sb)
	}
	cl := EncodeClash(bundles, "1.2.3.4")
	if !strings.Contains(cl, "ports: 20000-40000") || !strings.Contains(cl, "hop-interval:") {
		t.Errorf("mihomo Hy2 missing ports/hop-interval\n%s", cl)
	}
}

// sampleNewProtocolBundles covers the 6 protocols round-7 added across
// URI / Clash / sing-box (tuic / vmess / vless-ws / socks / anytls / vless-xhttp).
func sampleNewProtocolBundles() []Bundle {
	tuic := &model.Inbound{
		Tag: "tu", Type: "tuic", Port: 50000, Remark: "TU",
		Settings: `{"sni":"tu.example","tls_cert_path":"/etc/edgenest/tu.crt"}`,
	}
	vmess := &model.Inbound{
		Tag: "vm", Type: "vmess", Port: 12345, Remark: "VM",
		Settings: `{"ws_path":"/vm","ws_host":"vm.example"}`,
	}
	vlessWS := &model.Inbound{
		Tag: "vws", Type: "vless-ws", Port: 12346, Remark: "VWS",
		Settings: `{"ws_path":"/vw","tls_cert_path":"/etc/edgenest/vws.crt","sni":"vws.example"}`,
	}
	socks := &model.Inbound{
		Tag: "sk", Type: "socks", Port: 1080, Remark: "SK",
	}
	anytls := &model.Inbound{
		Tag: "at", Type: "anytls", Port: 8445, Remark: "AT",
		Settings: `{"sni":"at.example","tls_cert_path":"/etc/edgenest/at.crt"}`,
	}
	xhttp := &model.Inbound{
		Tag: "vx", Type: "vless-xhttp", Port: 8444, Remark: "VX",
		Settings: `{"security":"reality","sni":"vx.example","reality_public_key":"PUB_VX","short_ids":["beef"],"xhttp_path":"/x"}`,
	}
	uuid := "22222222-2222-2222-2222-222222222222"
	return []Bundle{
		{Inbound: tuic, Client: model.Client{Email: "u@x", UUID: uuid, Password: "tuicpw", Enabled: true}},
		{Inbound: vmess, Client: model.Client{Email: "u@x", UUID: uuid, Enabled: true}},
		{Inbound: vlessWS, Client: model.Client{Email: "u@x", UUID: uuid, Enabled: true}},
		{Inbound: socks, Client: model.Client{Email: "u@x", Password: "sokpw", Enabled: true}},
		{Inbound: anytls, Client: model.Client{Email: "u@x", Password: "atpw", Enabled: true}},
		{Inbound: xhttp, Client: model.Client{Email: "u@x", UUID: uuid, Enabled: true}},
	}
}

func TestBuildURI_NewProtocols(t *testing.T) {
	cases := []struct {
		typ   string
		check func(t *testing.T, uri string)
	}{
		{"tuic", func(t *testing.T, uri string) {
			if !strings.HasPrefix(uri, "tuic://22222222-2222-2222-2222-222222222222:tuicpw@1.2.3.4:50000?") {
				t.Errorf("tuic prefix wrong: %s", uri)
			}
			u, _ := url.Parse(uri)
			if u.Query().Get("congestion_control") != "bbr" {
				t.Errorf("tuic cc = %q", u.Query().Get("congestion_control"))
			}
			// Untrusted cert ⇒ both dialect spellings must be present:
			// allow_insecure (Hiddify / NekoBox), insecure (Shadowrocket).
			if u.Query().Get("allow_insecure") != "1" {
				t.Errorf("tuic allow_insecure = %q", u.Query().Get("allow_insecure"))
			}
			if u.Query().Get("insecure") != "1" {
				t.Errorf("tuic insecure = %q", u.Query().Get("insecure"))
			}
		}},
		{"vmess", func(t *testing.T, uri string) {
			if !strings.HasPrefix(uri, "vmess://") {
				t.Fatalf("vmess prefix wrong: %s", uri)
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
			if err != nil {
				t.Fatalf("vmess base64 decode: %v", err)
			}
			// `port` is a JSON number (sing-box mobile strict) and `aid` is 0 (int),
			// so unmarshal into a heterogeneous map rather than map[string]string.
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("vmess inner json: %v", err)
			}
			if m["id"] != "22222222-2222-2222-2222-222222222222" {
				t.Errorf("vmess id wrong: %v", m["id"])
			}
			if m["net"] != "ws" || m["path"] != "/vm" {
				t.Errorf("vmess ws wiring wrong: %+v", m)
			}
		}},
		{"vless-ws", func(t *testing.T, uri string) {
			if !strings.HasPrefix(uri, "vless://22222222-2222-2222-2222-222222222222@1.2.3.4:12346?") {
				t.Errorf("vless-ws prefix wrong: %s", uri)
			}
			u, _ := url.Parse(uri)
			q := u.Query()
			if q.Get("type") != "ws" {
				t.Errorf("vless-ws type = %q", q.Get("type"))
			}
			if q.Get("path") != "/vw" {
				t.Errorf("vless-ws path = %q", q.Get("path"))
			}
			if q.Get("security") != "tls" {
				t.Errorf("vless-ws security = %q (expected tls because cert path set)", q.Get("security"))
			}
			// XUDP: Shadowrocket shows Full Cone "XUDP" instead of plain UDP.
			if q.Get("packetEncoding") != "xudp" {
				t.Errorf("vless-ws packetEncoding = %q, want xudp", q.Get("packetEncoding"))
			}
		}},
		{"socks", func(t *testing.T, uri string) {
			// Authenticated socks rides the v2rayN / Sub-Store wire form:
			// bare socks:// scheme + base64(user:pass) userinfo. Plain
			// userinfo loses the credentials in Shadowrocket's importer.
			if !strings.HasPrefix(uri, "socks://") || strings.HasPrefix(uri, "socks5://") {
				t.Fatalf("socks prefix wrong (want bare socks:// scheme): %s", uri)
			}
			u, err := url.Parse(uri)
			if err != nil || u.User == nil {
				t.Fatalf("socks userinfo missing: %s", uri)
			}
			dec, err := base64.StdEncoding.DecodeString(u.User.Username())
			if err != nil {
				t.Fatalf("socks userinfo not base64: %v (%s)", err, uri)
			}
			if string(dec) != "u@x:sokpw" {
				t.Errorf("socks credentials = %q, want u@x:sokpw", dec)
			}
		}},
		{"anytls", func(t *testing.T, uri string) {
			// canonical anytls scheme keeps the `/` path delimiter before `?`
			if !strings.HasPrefix(uri, "anytls://atpw@1.2.3.4:8445/?") {
				t.Errorf("anytls prefix wrong: %s", uri)
			}
		}},
		{"vless-xhttp", func(t *testing.T, uri string) {
			u, _ := url.Parse(uri)
			if u.Query().Get("type") != "xhttp" {
				t.Errorf("xhttp type = %q", u.Query().Get("type"))
			}
			if u.Query().Get("pbk") != "PUB_VX" {
				t.Errorf("xhttp pbk = %q", u.Query().Get("pbk"))
			}
		}},
	}
	bundles := sampleNewProtocolBundles()
	byType := map[string]*model.Inbound{}
	clients := map[string]model.Client{}
	for _, b := range bundles {
		byType[b.Inbound.Type] = b.Inbound
		clients[b.Inbound.Type] = b.Client
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			in := byType[tc.typ]
			cl := clients[tc.typ]
			in.Clients = []model.Client{cl}
			uris, err := BuildURIs(in, "1.2.3.4")
			if err != nil {
				t.Fatalf("BuildURIs(%s): %v", tc.typ, err)
			}
			if len(uris) != 1 {
				t.Fatalf("want 1 uri, got %d", len(uris))
			}
			tc.check(t, uris[0])
		})
	}
}

func TestEncodeClash_NewProtocols(t *testing.T) {
	out := EncodeClash(sampleNewProtocolBundles(), "1.2.3.4")
	mustContain := []string{
		"type: tuic", "congestion-controller: bbr",
		"type: vmess", "alterId: 0",
		"type: vless", "network: ws", "network: xhttp",
		"type: socks5",
		"type: anytls",
		"reality-opts:", "public-key: PUB_VX",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("clash missing %q\n---\n%s", want, out)
		}
	}
}

func TestEncodeSingbox_NewProtocols(t *testing.T) {
	out := EncodeSingbox(sampleNewProtocolBundles(), "1.2.3.4")
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("singbox json: %v", err)
	}
	outs := cfg["outbounds"].([]any)
	types := map[string]int{}
	for _, o := range outs {
		m := o.(map[string]any)
		types[m["type"].(string)]++
	}
	for _, want := range []string{"tuic", "vmess", "vless", "socks"} {
		if types[want] == 0 {
			t.Errorf("missing outbound type %q (got %v)", want, types)
		}
	}
	// anytls is intentionally skipped: it only exists as a sing-box outbound
	// type since v1.12.0, and one unknown type makes older cores reject the
	// whole config — see singboxAnyTLS. It still ships via the URI/clash/etc.
	// encoders.
	if types["anytls"] != 0 {
		t.Errorf("expected anytls to be omitted from sing-box JSON, got %d", types["anytls"])
	}
	// vless-ws emits one vless outbound; vless-xhttp is intentionally skipped
	// (sing-box core has no xhttp transport — see singboxVLESSXHTTP).
	if types["vless"] != 1 {
		t.Errorf("expected vless count == 1 (ws only, xhttp skipped), got %d", types["vless"])
	}
}

// Regression: sing-box has no xhttp transport. The encoder must skip
// vless-xhttp bundles silently so that Hiddify / sing-box GUI / SFI / Karing /
// NekoBox can still consume the rest of the subscription.
// Client-dialect regressions (sourced fixes).
// Wizard-shape self-signed inbounds — assert each corrected wire form so the
// dialect bugs (third-falsified-comment class) can't silently regress.
func selfSignedDialectBundles() []Bundle {
	uuid := "22222222-2222-2222-2222-222222222222"
	tuic := &model.Inbound{Tag: "tu", Type: "tuic", Port: 50000, Remark: "TU",
		Settings: `{"self_signed":"true","sni":"1.2.3.4","tls_cert_path":"/etc/edgenest/tu.crt"}`}
	vmess := &model.Inbound{Tag: "vm", Type: "vmess-ws", Port: 2053, Remark: "VM",
		Settings: `{"self_signed":"true","sni":"1.2.3.4","tls_cert_path":"/etc/edgenest/vm.crt","ws_path":"/vm"}`}
	vlessWS := &model.Inbound{Tag: "vws", Type: "vless-ws", Port: 2083, Remark: "VWS",
		Settings: `{"self_signed":"true","sni":"1.2.3.4","tls_cert_path":"/etc/edgenest/vws.crt","ws_path":"/vw"}`}
	reality := &model.Inbound{Tag: "vr", Type: "vless", Port: 8443, Remark: "VR",
		Settings: `{"sni":"www.microsoft.com","reality_public_key":"PUBKEY","short_ids":["beef"]}`}
	anytls := &model.Inbound{Tag: "at", Type: "anytls", Port: 8445, Remark: "AT",
		Settings: `{"self_signed":"true","sni":"1.2.3.4","tls_cert_path":"/etc/edgenest/at.crt"}`}
	return []Bundle{
		{Inbound: tuic, Client: model.Client{Email: "u@x", UUID: uuid, Password: "pw", Enabled: true}},
		{Inbound: vmess, Client: model.Client{Email: "u@x", UUID: uuid, Enabled: true}},
		{Inbound: vlessWS, Client: model.Client{Email: "u@x", UUID: uuid, Enabled: true}},
		{Inbound: reality, Client: model.Client{Email: "u@x", UUID: uuid, Flow: "xtls-rprx-vision", Enabled: true}},
		{Inbound: anytls, Client: model.Client{Email: "u@x", Password: "atpw", Enabled: true}},
	}
}

// Surge TUIC v5: `tuic-v5` type + uuid=/password=. The bare `tuic` type is
// TUIC v4 and demands token= (official manual: "token: Required."); `version=`
// is not a Surge param at all. Confirmed on-device 2026-06-12: `tuic, ...,
// version=5` → "字段 `token` 必须被提供".
func TestSurge_TUICv5Fields(t *testing.T) {
	out := EncodeSurge(selfSignedDialectBundles(), "1.2.3.4")
	if !strings.Contains(out, "= tuic-v5, 1.2.3.4, 50000,") {
		t.Errorf("surge TUIC must use `tuic-v5` type, got:\n%s", out)
	}
	for _, want := range []string{"uuid=22222222-2222-2222-2222-222222222222", "password=pw"} {
		if !strings.Contains(out, want) {
			t.Errorf("surge TUIC missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "version=5") || strings.Contains(out, "token=") {
		t.Errorf("surge TUIC leaked v4 token= / nonexistent version= param:\n%s", out)
	}
}

// Surge VMess-WS: must carry vmess-aead=true. Our sing-box VMess inbound is
// AEAD-only (alterId removed upstream), so without this flag Surge falls back
// to legacy MD5 auth and the handshake mismatches — the node imports fine but
// won't proxy. Confirmed on-device 2026-06-12 (Surge VMess-WS "无法代理").
// Sub-Store's surge producer emits vmess-aead for every alterId=0 node
// (vmess-security.js / surge.js producer).
func TestSurge_VMessAEAD(t *testing.T) {
	out := EncodeSurge(selfSignedDialectBundles(), "1.2.3.4")
	var vmLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "= vmess,") {
			vmLine = line
			break
		}
	}
	if vmLine == "" {
		t.Fatalf("no surge vmess line emitted:\n%s", out)
	}
	if !strings.Contains(vmLine, "vmess-aead=true") {
		t.Errorf("surge vmess-ws missing vmess-aead=true (AEAD-only server → won't proxy):\n%s", vmLine)
	}
	if !strings.Contains(vmLine, "ws=true") || !strings.Contains(vmLine, "username=22222222") {
		t.Errorf("surge vmess-ws malformed:\n%s", vmLine)
	}
}

// v2rayNG (Android) VMess-WS self-signed: the base64-JSON must carry the
// string key `insecure:"1"`. v2rayNG ignores verify_cert/skip-cert-verify/
// allowInsecure/allowinsecure on the JSON path (VmessQRCode.kt has a plain
// `var insecure: String`, VmessFmt.kt maps only "1" -> true); without it
// self-signed VMess-WS defaults verification ON and fails the TLS handshake.
// Confirmed on-device 2026-06-13: vmess-ws was the only protocol that couldn't
// proxy on v2rayNG while vless/trojan worked via their &allowInsecure=1 query.
// Source: 2dust/v2rayNG commit c0141225 (2025-11-09), shipped v1.10.28.
func TestURI_VMessSelfSignedInsecure(t *testing.T) {
	bundles := selfSignedDialectBundles()
	var vm *model.Inbound
	var cl model.Client
	for _, b := range bundles {
		if b.Inbound.Type == "vmess-ws" {
			vm, cl = b.Inbound, b.Client
		}
	}
	if vm == nil {
		t.Fatal("no self-signed vmess-ws bundle in fixture")
	}
	vm.Clients = []model.Client{cl}
	uris, err := BuildURIs(vm, "1.2.3.4")
	if err != nil || len(uris) != 1 {
		t.Fatalf("BuildURIs vmess-ws: err=%v n=%d", err, len(uris))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uris[0], "vmess://"))
	if err != nil {
		t.Fatalf("vmess base64 decode: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("vmess inner json: %v", err)
	}
	if m["insecure"] != "1" {
		t.Errorf("self-signed vmess must carry insecure=\"1\" for v2rayNG; got %#v", m["insecure"])
	}
}

// Loon VMess/VLESS-WS: credential must be POSITIONAL quoted, not key=value.
func TestLoon_WSPositionalCredential(t *testing.T) {
	out := EncodeLoon(selfSignedDialectBundles(), "1.2.3.4")
	if !strings.Contains(out, `= vmess, 1.2.3.4, 2053, auto, "22222222-2222-2222-2222-222222222222"`) {
		t.Errorf("loon vmess-ws must use positional `auto, \"uuid\"`:\n%s", out)
	}
	if !strings.Contains(out, `= VLESS, 1.2.3.4, 2083, "22222222-2222-2222-2222-222222222222"`) {
		t.Errorf("loon vless-ws must use positional quoted uuid:\n%s", out)
	}
	// TUIC legitimately uses uuid= key=value in Loon; the leak we guard against
	// is key=value credential on the vmess/vless WS lines specifically.
	for _, line := range strings.Split(out, "\n") {
		isWS := strings.Contains(line, "= vmess,") || strings.Contains(line, "= VLESS,")
		if isWS && strings.Contains(line, "transport=ws") &&
			(strings.Contains(line, "username=22222222") || strings.Contains(line, "uuid=22222222")) {
			t.Errorf("loon WS leaked key=value credential (auth fails on-device):\n%s", line)
		}
	}
	// vless-ws plain TLS uses tls-name=, not sni= (sni is Reality-only in Loon).
	if !strings.Contains(out, "tls-name=1.2.3.4") {
		t.Errorf("loon vless-ws must use tls-name= for server name:\n%s", out)
	}
}

// QX dialect guards (official sample.conf, fetched 2026-06-12):
//   - vless= lines: official examples never carry tls-verification; keep it off.
//   - every TLS trojan/anytls line MUST carry over-tls=true (all official TLS
//     examples do; tls-host without it is undocumented and skips TLS entirely).
//   - anytls= takes tls-verification like trojan (generic TLS key), needed for
//     self-signed nodes.
func TestQX_DialectKeys(t *testing.T) {
	out := EncodeQuantumultX(selfSignedDialectBundles(), "1.2.3.4")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "vless=") && strings.Contains(line, "tls-verification") {
			t.Errorf("QX vless line carries tls-verification (not in official examples):\n%s", line)
		}
		if (strings.HasPrefix(line, "trojan=") || strings.HasPrefix(line, "anytls=")) &&
			!strings.Contains(line, "over-tls=true") {
			t.Errorf("QX %s line missing mandatory over-tls=true:\n%s", strings.SplitN(line, "=", 2)[0], line)
		}
	}
	if !strings.Contains(out, "anytls=1.2.3.4:8445") || !strings.Contains(out, "tls-verification=false, udp-relay=true, tag=") {
		t.Errorf("QX anytls line missing or lacks tls-verification=false for self-signed node:\n%s", out)
	}
	// Reality vless line still present (must not be dropped, just cleaned).
	if !strings.Contains(out, "reality-base64-pubkey=PUBKEY") {
		t.Errorf("QX dropped the reality vless node entirely:\n%s", out)
	}
}

// Hiddify (ray2sing) reads ONLY lowercase insecure/allowinsecure from vmess JSON.
func TestBuildVMess_HiddifyLowercaseInsecure(t *testing.T) {
	in := &model.Inbound{Tag: "vm", Type: "vmess-ws", Port: 2053,
		Settings: `{"self_signed":"true","sni":"1.2.3.4","tls_cert_path":"/etc/edgenest/vm.crt","ws_path":"/vm"}`,
		Clients:  []model.Client{{Email: "u@x", UUID: "22222222-2222-2222-2222-222222222222", Enabled: true}}}
	uris, err := BuildURIs(in, "1.2.3.4")
	if err != nil || len(uris) != 1 {
		t.Fatalf("BuildURIs: %v (%d uris)", err, len(uris))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uris[0], "vmess://"))
	if err != nil {
		t.Fatalf("vmess base64 decode: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("vmess json: %v", err)
	}
	if cfg["allowinsecure"] != "true" {
		t.Errorf("vmess JSON missing lowercase allowinsecure=\"true\" (Hiddify/ray2sing): %v", cfg)
	}
}

func TestEncodeSingbox_VLESSXHTTPSkipped(t *testing.T) {
	xhttp := &model.Inbound{
		Tag: "vx", Type: "vless-xhttp", Port: 8444, Remark: "VX",
		Settings: `{"security":"reality","sni":"vx.example","reality_public_key":"PUB_VX","short_ids":["beef"],"xhttp_path":"/x"}`,
	}
	bundle := []Bundle{{
		Inbound: xhttp,
		Client:  model.Client{Email: "u@x", UUID: "22222222-2222-2222-2222-222222222222", Enabled: true},
	}}
	out := EncodeSingbox(bundle, "1.2.3.4")
	if strings.Contains(out, `"xhttp"`) {
		t.Errorf("sing-box config leaked xhttp transport (unsupported by sing-box core):\n%s", out)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	for _, o := range cfg["outbounds"].([]any) {
		m := o.(map[string]any)
		if m["type"] == "vless" {
			t.Errorf("vless outbound emitted for vless-xhttp bundle; should have been skipped\n%v", m)
		}
	}
}

// Regression (real-machine IPv6, 2026-06): Stash does NOT support the
// xhttp transport (stash.wiki proxy-types documents only ws/grpc/h2 for VLESS).
// EncodeStash must skip vless-xhttp bundles rather than ship a dead node Stash
// can't dial — while EncodeClash (Mihomo / ClashMi DO support xhttp) keeps it.
func TestEncodeStash_VLESSXHTTPSkipped(t *testing.T) {
	xhttp := &model.Inbound{
		Tag: "vx", Type: "vless-xhttp", Port: 8444, Remark: "VX",
		Settings: `{"security":"tls","sni":"vx.example","xhttp_path":"/x"}`,
	}
	bundle := []Bundle{{
		Inbound: xhttp,
		Client:  model.Client{Email: "u@x", UUID: "22222222-2222-2222-2222-222222222222", Enabled: true},
	}}
	stash := EncodeStash(bundle, "1.2.3.4")
	if strings.Contains(stash, "xhttp") {
		t.Errorf("Stash format leaked xhttp transport (unsupported by Stash core):\n%s", stash)
	}
	if strings.Contains(stash, "type: vless") {
		t.Errorf("Stash emitted a vless proxy for a vless-xhttp bundle; should be skipped:\n%s", stash)
	}
	// Mihomo / Clash must STILL emit it — the skip is Stash-only.
	clash := EncodeClash(bundle, "1.2.3.4")
	if !strings.Contains(clash, "network: xhttp") {
		t.Errorf("EncodeClash must keep xhttp for Mihomo/ClashMi; got:\n%s", clash)
	}
}

// Regression: VLESS-XHTTP URI must not carry flow=xtls-rprx-vision (xray-core
// rejects vision flow on xhttp transport at startup). Even if the operator /
// wizard sets Flow on the client row (carry-over from a Reality-TCP default),
// the URI encoder must strip it.
func TestBuildVLESSXHTTP_FlowStripped(t *testing.T) {
	in := &model.Inbound{
		Tag: "vx", Type: "vless-xhttp", Port: 8444,
		Settings: `{"security":"reality","sni":"vx.example","reality_public_key":"PUB_VX","short_ids":["beef"]}`,
		Clients: []model.Client{
			{Email: "u@x", UUID: "22222222-2222-2222-2222-222222222222", Flow: "xtls-rprx-vision", Enabled: true},
		},
	}
	uris, err := BuildURIs(in, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 {
		t.Fatalf("want 1 uri, got %d", len(uris))
	}
	u, _ := url.Parse(uris[0])
	if flow := u.Query().Get("flow"); flow != "" {
		t.Errorf("vless-xhttp URI must not carry flow; got %q", flow)
	}
	if u.Query().Get("type") != "xhttp" {
		t.Errorf("type should still be xhttp (regression baseline), got %q", u.Query().Get("type"))
	}
}

// Regression: Quantumult X 1.5.5+ supports VLESS over Reality and WS. The
// encoder must emit both shapes, so QX users do not miss VLESS nodes.
func TestEncodeQuantumultX_VLESSRealityAndWS(t *testing.T) {
	vlessReality := &model.Inbound{
		Tag: "vr", Type: "vless", Port: 8443, Remark: "VR",
		Settings: `{"sni":"www.microsoft.com","reality_public_key":"PUB_QX","short_ids":["dead"]}`,
	}
	vlessWS := &model.Inbound{
		Tag: "vw", Type: "vless-ws", Port: 12346, Remark: "VW",
		Settings: `{"ws_path":"/vw","ws_host":"vw.example","tls_cert_path":"/etc/edgenest/vws.crt"}`,
	}
	uuid := "33333333-3333-3333-3333-333333333333"
	bundles := []Bundle{
		{Inbound: vlessReality, Client: model.Client{Email: "u@x", UUID: uuid, Enabled: true}},
		{Inbound: vlessWS, Client: model.Client{Email: "u@x", UUID: uuid, Enabled: true}},
	}
	out := EncodeQuantumultX(bundles, "1.2.3.4")
	mustContain := []string{
		// Reality line markers — QX 1.5.5+ uses `reality-base64-pubkey` /
		// `reality-hex-shortid` (not the pre-1.7 `public-key` / `short-id`),
		// and SNI for Reality goes into `obfs-host=` not `tls-host=`.
		"vless=1.2.3.4:8443",
		"obfs=over-tls",
		"reality-base64-pubkey=PUB_QX",
		"reality-hex-shortid=dead",
		"obfs-host=www.microsoft.com",
		"vless-flow=xtls-rprx-vision",
		// WS line markers
		"vless=1.2.3.4:12346",
		"obfs=wss",
		"obfs-uri=/vw",
		"obfs-host=vw.example",
		"password=" + uuid,
		"method=none",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("qx output missing %q\n---\n%s", want, out)
		}
	}
}

// TestBuildURI_VMessInsecureAliases locks the self-signed VMess JSON shape:
// the vmess base64-JSON spec has no standard insecure field, so the builder
// must emit every alias with a real-world parser behind it (verify_cert for
// V2RayN / NekoBox, skip-cert-verify for Clash-dialect importers,
// allowInsecure for the 3x-ui v2 wire form). acme_managed flips them all off.
func TestBuildURI_VMessInsecureAliases(t *testing.T) {
	in := &model.Inbound{
		Tag: "vmt", Type: "vmess-ws", Port: 2053, Remark: "VMT",
		Settings: `{"ws_path":"/vm","sni":"1.2.3.4","tls_cert_path":"/etc/edgenest/wizard-fullchain.pem","self_signed":"true"}`,
		Clients: []model.Client{{
			Email: "u@x", UUID: "22222222-2222-2222-2222-222222222222", Enabled: true,
		}},
	}
	decode := func(t *testing.T, uri string) map[string]any {
		t.Helper()
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
		if err != nil {
			t.Fatalf("vmess base64 decode: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("vmess inner json: %v", err)
		}
		return m
	}

	uris, err := BuildURIs(in, "1.2.3.4")
	if err != nil || len(uris) != 1 {
		t.Fatalf("BuildURIs: %v (%d uris)", err, len(uris))
	}
	m := decode(t, uris[0])
	if m["tls"] != "tls" {
		t.Fatalf("vmess tls = %v, want \"tls\"", m["tls"])
	}
	if m["verify_cert"] != false {
		t.Errorf("vmess verify_cert = %v, want false", m["verify_cert"])
	}
	if m["skip-cert-verify"] != true {
		t.Errorf("vmess skip-cert-verify = %v, want true", m["skip-cert-verify"])
	}
	if m["allowInsecure"] != true {
		t.Errorf("vmess allowInsecure = %v, want true", m["allowInsecure"])
	}

	// ACME-managed cert ⇒ strict verification, none of the aliases present.
	in.Settings = `{"ws_path":"/vm","sni":"vm.example","tls_cert_path":"/etc/edgenest/acme.pem","acme_managed":"true"}`
	uris, err = BuildURIs(in, "1.2.3.4")
	if err != nil || len(uris) != 1 {
		t.Fatalf("BuildURIs (acme): %v (%d uris)", err, len(uris))
	}
	m = decode(t, uris[0])
	for _, k := range []string{"verify_cert", "skip-cert-verify", "allowInsecure"} {
		if _, present := m[k]; present {
			t.Errorf("vmess %s present on acme_managed inbound (want absent)", k)
		}
	}
}

// TestBuildURI_Hysteria2ACMEManaged locks the uri.go:206 gate unification:
// a self-signed Hy2 inbound carries insecure=1, an acme_managed one verifies
// normally (no insecure / no pinSHA256).
func TestBuildURI_Hysteria2ACMEManaged(t *testing.T) {
	mk := func(settings string) url.Values {
		t.Helper()
		in := &model.Inbound{
			Tag: "hy", Type: "hysteria2", Port: 8443, Remark: "HY",
			Settings: settings,
			Clients:  []model.Client{{Email: "u@x", Password: "pw", Enabled: true}},
		}
		uris, err := BuildURIs(in, "1.2.3.4")
		if err != nil || len(uris) != 1 {
			t.Fatalf("BuildURIs: %v (%d uris)", err, len(uris))
		}
		u, err := url.Parse(uris[0])
		if err != nil {
			t.Fatalf("parse %q: %v", uris[0], err)
		}
		return u.Query()
	}

	self := mk(`{"sni":"1.2.3.4","tls_cert_path":"/etc/edgenest/wizard-fullchain.pem","self_signed":"true"}`)
	if self.Get("insecure") != "1" {
		t.Errorf("self-signed hy2 insecure = %q, want 1", self.Get("insecure"))
	}

	acme := mk(`{"sni":"hy.example","tls_cert_path":"/etc/edgenest/acme.pem","acme_managed":"true"}`)
	if v, ok := acme["insecure"]; ok {
		t.Errorf("acme_managed hy2 has insecure=%v, want absent", v)
	}
	if v, ok := acme["pinSHA256"]; ok {
		t.Errorf("acme_managed hy2 has pinSHA256=%v, want absent", v)
	}
}
