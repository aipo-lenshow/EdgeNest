package share

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

func sampleBundles() []Bundle {
	vless := &model.Inbound{
		Tag: "EdgeNest-VLESS-Reality", Type: "vless", Port: 8443,
		Remark: "EdgeNest-VLESS-Reality",
		Settings: `{
			"sni":"www.microsoft.com",
			"reality_public_key":"PUB_X",
			"short_ids":["abcdef0123456789"]
		}`,
	}
	hy2 := &model.Inbound{
		Tag: "EdgeNest-Hysteria2", Type: "hysteria2", Port: 41020,
		Remark:   "EdgeNest-Hysteria2",
		Settings: `{"sni":"edgenest.local","tls_cert_path":"/etc/edgenest/wizard.pem"}`,
	}
	trojan := &model.Inbound{
		Tag: "tj", Type: "trojan", Port: 443,
		Settings: `{"sni":"trojan.example"}`,
	}
	alice := model.Client{
		Email: "alice@example.com",
		UUID:  "11111111-1111-1111-1111-111111111111",
		Flow:  "xtls-rprx-vision", Enabled: true,
	}
	aliceH2 := model.Client{Email: "alice@example.com", Password: "h2pw", Enabled: true}
	aliceTj := model.Client{Email: "alice@example.com", Password: "tjpw", Enabled: true}
	return []Bundle{
		{Inbound: vless, Client: alice},
		{Inbound: hy2, Client: aliceH2},
		{Inbound: trojan, Client: aliceTj},
	}
}

func TestEncodeClash_RealityAndHy2(t *testing.T) {
	out := EncodeClash(sampleBundles(), "1.2.3.4")
	mustContain := []string{
		"mixed-port: 7890",
		"type: vless",
		"uuid: 11111111-1111-1111-1111-111111111111",
		"reality-opts:",
		"public-key: PUB_X",
		"short-id: abcdef0123456789",
		"servername: www.microsoft.com",
		"type: hysteria2",
		"password: h2pw",
		"sni: edgenest.local",
		"skip-cert-verify: true", // self-signed-ish cert path
		"type: trojan",
		"name: EdgeNest",
		"MATCH,EdgeNest",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("clash output missing %q\n---\n%s", want, out)
		}
	}
}

func TestEncodeClash_EmptyBundles(t *testing.T) {
	out := EncodeClash(nil, "1.2.3.4")
	if !strings.Contains(out, "proxies:\n  []") {
		t.Errorf("empty bundle should render empty proxies list, got:\n%s", out)
	}
	if !strings.Contains(out, "- DIRECT") {
		t.Errorf("empty auto group should fall back to DIRECT, got:\n%s", out)
	}
}

func TestEncodeSingbox_ValidJSONStructure(t *testing.T) {
	out := EncodeSingbox(sampleBundles(), "1.2.3.4")
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("sing-box output is not valid JSON: %v\n%s", err, out)
	}
	outs, ok := cfg["outbounds"].([]any)
	if !ok {
		t.Fatalf("outbounds missing or wrong type: %T", cfg["outbounds"])
	}
	// 3 proxies + selector + urltest + direct (mobile profile). `dns` AND
	// `block` are both legacy special outbounds (sing-box 1.11+ migration
	// doc) and must be absent — SFI / SFA fire the "legacy special
	// outbounds will be removed" deprecation warning whenever either is
	// declared, even with no rule referencing it. `direct` is not a legacy
	// special outbound and remains.
	if len(outs) != 6 {
		t.Errorf("want 6 outbounds, got %d", len(outs))
	}
	types := map[string]int{}
	for _, o := range outs {
		m := o.(map[string]any)
		types[m["type"].(string)]++
	}
	for _, want := range []string{"vless", "hysteria2", "trojan", "selector", "urltest", "direct"} {
		if types[want] == 0 {
			t.Errorf("missing outbound type %q (got %v)", want, types)
		}
	}
	for _, banned := range []string{"dns", "block"} {
		if types[banned] != 0 {
			t.Errorf("legacy special outbound %q must not appear in modern (rule_action) profile", banned)
		}
	}
	// Mobile profile requires tun inbound + DNS hijack route rules — without
	// them SFI App Store builds import cleanly but the VPN session relays
	// no traffic.
	inbounds, _ := cfg["inbounds"].([]any)
	if len(inbounds) == 0 {
		t.Errorf("mobile profile must declare a tun inbound, got 0 inbounds")
	} else {
		first := inbounds[0].(map[string]any)
		if first["type"] != "tun" {
			t.Errorf("first inbound must be tun, got %v", first["type"])
		}
	}
	routeRules, _ := cfg["route"].(map[string]any)["rules"].([]any)
	if len(routeRules) == 0 {
		t.Errorf("mobile profile must declare DNS hijack route rules")
	}
	// rule_action shape: every rule carries `action`, no rule still
	// references the legacy `outbound: dns-out` shape.
	for i, r := range routeRules {
		m := r.(map[string]any)
		if _, ok := m["action"]; !ok {
			t.Errorf("route.rules[%d] missing action (legacy outbound-style rule no longer accepted)", i)
		}
		if m["outbound"] == "dns-out" {
			t.Errorf("route.rules[%d] still references legacy dns-out outbound", i)
		}
	}
	route, _ := cfg["route"].(map[string]any)
	if route["final"] != "EdgeNest" {
		t.Errorf("route.final = %v, want EdgeNest", route["final"])
	}
	// Android sing-box clients (SFA, hiddify-sing-box, etc.) need this flag
	// set or TUN outbound packets are routed back into TUN and
	// ECONNABORTED'd by the kernel — all proxy protocols silently fail. iOS
	// clients work without it but the field is harmless there (Apple
	// NEPacketTunnelProvider). Field has been in sing-box since v1.0 and
	// has never been deprecated.
	if route["auto_detect_interface"] != true {
		t.Errorf("route.auto_detect_interface must be true for Android sing-box VpnService clients")
	}
}

func TestEncodeSingbox_RealityWiring(t *testing.T) {
	out := EncodeSingbox(sampleBundles(), "1.2.3.4")
	var cfg map[string]any
	_ = json.Unmarshal([]byte(out), &cfg)
	outs := cfg["outbounds"].([]any)
	var vless map[string]any
	for _, o := range outs {
		m := o.(map[string]any)
		if m["type"] == "vless" {
			vless = m
			break
		}
	}
	if vless == nil {
		t.Fatal("vless outbound not emitted")
	}
	if vless["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %v, want xtls-rprx-vision", vless["flow"])
	}
	tls := vless["tls"].(map[string]any)
	if tls["server_name"] != "www.microsoft.com" {
		t.Errorf("server_name = %v, want www.microsoft.com", tls["server_name"])
	}
	reality := tls["reality"].(map[string]any)
	if reality["public_key"] != "PUB_X" {
		t.Errorf("public_key = %v, want PUB_X", reality["public_key"])
	}
	if reality["short_id"] != "abcdef0123456789" {
		t.Errorf("short_id = %v, want abcdef0123456789", reality["short_id"])
	}
}

func TestEncodeQuantumultX_PlainBody(t *testing.T) {
	out := EncodeQuantumultX(sampleBundles(), "1.2.3.4")
	// QX subscription import expects pure entry lines — section headers and
	// comment lines break the parser, so they MUST NOT appear.
	mustNotContain := []string{"[server_local]", "# unsupported"}
	for _, bad := range mustNotContain {
		if strings.Contains(out, bad) {
			t.Errorf("qx output should not contain %q\n---\n%s", bad, out)
		}
	}
	mustContain := []string{
		"trojan=1.2.3.4:443",
		"password=tjpw",
		"tls-host=trojan.example",
		"tag=tj-alice@example.com",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("qx output missing %q\n---\n%s", want, out)
		}
	}
}

// hy2WithObfsBundle mirrors what the wizard creates: hysteria2 inbound with
// salamander obfs autofilled. Every non-URI encoder must propagate `obfs` +
// `obfs_password` or Mihomo / Stash / sing-box / Surge / Loon import the node
// but never complete the handshake (silent "connection failed").
func hy2WithObfsBundle() []Bundle {
	hy2 := &model.Inbound{
		Tag: "hy2-obfs", Type: "hysteria2", Port: 41020, Remark: "Hy2Obfs",
		Settings: `{"sni":"www.bing.com","obfs":"salamander","obfs_password":"OBFSPW","self_signed":"true","tls_cert_path":"/etc/edgenest/certs/wizard-fullchain.pem"}`,
	}
	return []Bundle{
		{Inbound: hy2, Client: model.Client{Email: "u@x", Password: "hy2pw", Enabled: true}},
	}
}

func TestEncodeClash_Hy2EmitsObfs(t *testing.T) {
	out := EncodeClash(hy2WithObfsBundle(), "1.2.3.4")
	for _, want := range []string{"obfs: salamander", "obfs-password: OBFSPW"} {
		if !strings.Contains(out, want) {
			t.Errorf("clash hy2 output missing %q\n---\n%s", want, out)
		}
	}
}

func TestEncodeSingbox_Hy2EmitsObfs(t *testing.T) {
	out := EncodeSingbox(hy2WithObfsBundle(), "1.2.3.4")
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("singbox json: %v\n%s", err, out)
	}
	var hy2 map[string]any
	for _, o := range cfg["outbounds"].([]any) {
		m := o.(map[string]any)
		if m["type"] == "hysteria2" {
			hy2 = m
			break
		}
	}
	if hy2 == nil {
		t.Fatalf("hy2 outbound not emitted\n%s", out)
	}
	obfs, ok := hy2["obfs"].(map[string]any)
	if !ok {
		t.Fatalf("hy2.obfs missing or wrong type: %T\n%s", hy2["obfs"], out)
	}
	if obfs["type"] != "salamander" {
		t.Errorf("hy2.obfs.type = %v, want salamander", obfs["type"])
	}
	if obfs["password"] != "OBFSPW" {
		t.Errorf("hy2.obfs.password = %v, want OBFSPW", obfs["password"])
	}
}

func TestEncodeSurge_Hy2EmitsObfs(t *testing.T) {
	out := EncodeSurge(hy2WithObfsBundle(), "1.2.3.4")
	// Surge takes the obfs password on a dedicated `salamander-password=`
	// key; the generic `obfs=salamander` / `obfs-password=` pair is silently
	// ignored on the Hy2 line (Surge only parses `obfs` for SS / VMess).
	for _, want := range []string{"salamander-password=OBFSPW"} {
		if !strings.Contains(out, want) {
			t.Errorf("surge hy2 output missing %q\n---\n%s", want, out)
		}
	}
}

// Loon's .conf grammar (nsloon.app/docs/Node) requires UUID as the 4th
// positional parameter wrapped in double quotes — NOT a `uuid=<UUID>` key.
// Emitting the key form leaves Loon's vless authentication handshake using
// the literal string `uuid=<UUID>` (45 chars) as the UUID, which fails at
// the server side and surfaces as "test failed" on every Loon 3.x VLESS
// proxy. Verified against Loon 3.4.0(962) on 2026-06-06.
func TestEncodeLoon_VLESSUUIDIsPositionalNotKey(t *testing.T) {
	out := EncodeLoon(sampleBundles(), "1.2.3.4")
	if strings.Contains(out, "uuid=") {
		t.Errorf("loon VLESS line contains literal `uuid=` key — UUID must be 4th positional parameter:\n%s", out)
	}
}

// Same Loon grammar rule for Shadowsocks: cipher is the 4th positional
// (bare), password is the 5th positional (double-quoted). Loon refuses to
// look up `method=<cipher>` in its supported-ciphers table.
func TestEncodeLoon_ShadowsocksCipherIsPositionalNotKey(t *testing.T) {
	out := EncodeLoon(sampleBundles(), "1.2.3.4")
	if strings.Contains(out, "method=") {
		t.Errorf("loon Shadowsocks line contains literal `method=` key — cipher must be 4th positional parameter:\n%s", out)
	}
}

func TestEncodeLoon_Hy2EmitsObfs(t *testing.T) {
	out := EncodeLoon(hy2WithObfsBundle(), "1.2.3.4")
	for _, want := range []string{"obfs=salamander", "obfs-password=OBFSPW"} {
		if !strings.Contains(out, want) {
			t.Errorf("loon hy2 output missing %q\n---\n%s", want, out)
		}
	}
}

func TestEncodeQuantumultX_ShadowsocksDefaultMethod(t *testing.T) {
	ss := &model.Inbound{Tag: "ss", Type: "shadowsocks", Port: 2087}
	bundles := []Bundle{
		{
			Inbound: ss,
			Client:  model.Client{Email: "bob@example.com", Password: "sspw", Enabled: true},
		},
	}
	out := EncodeQuantumultX(bundles, "1.2.3.4")
	if !strings.Contains(out, "shadowsocks=1.2.3.4:2087") {
		t.Errorf("shadowsocks host missing:\n%s", out)
	}
	if !strings.Contains(out, "method=2022-blake3-aes-128-gcm") {
		t.Errorf("default cipher missing:\n%s", out)
	}
	if !strings.Contains(out, "password=sspw") {
		t.Errorf("password missing:\n%s", out)
	}
}
