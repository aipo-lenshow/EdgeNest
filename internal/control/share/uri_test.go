package share

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/selfsigned"
)

func TestBuildVLESS_Reality(t *testing.T) {
	in := &model.Inbound{
		Tag: "vless-a", Type: "vless", Port: 8443, Remark: "Reality",
		Settings: `{
			"sni":"www.microsoft.com",
			"reality_public_key":"PUB_KEY_X",
			"short_ids":["0123456789abcdef"]
		}`,
		Clients: []model.Client{
			{Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111", Flow: "xtls-rprx-vision", Enabled: true},
		},
	}
	uris, err := BuildURIs(in, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 {
		t.Fatalf("want 1 uri, got %d", len(uris))
	}
	u := uris[0]
	if !strings.HasPrefix(u, "vless://11111111-1111-1111-1111-111111111111@1.2.3.4:8443?") {
		t.Errorf("unexpected uri prefix: %s", u)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("security") != "reality" {
		t.Errorf("security = %q, want reality", q.Get("security"))
	}
	if q.Get("pbk") != "PUB_KEY_X" {
		t.Errorf("pbk = %q, want PUB_KEY_X", q.Get("pbk"))
	}
	if q.Get("sid") != "0123456789abcdef" {
		t.Errorf("sid = %q", q.Get("sid"))
	}
	if q.Get("sni") != "www.microsoft.com" {
		t.Errorf("sni = %q", q.Get("sni"))
	}
	if q.Get("flow") != "xtls-rprx-vision" {
		t.Errorf("flow = %q", q.Get("flow"))
	}
	// XUDP: Shadowrocket displays VLESS/XTLS-RPRX-VISION/XUDP (Full Cone UDP
	// mux) rather than plain UDP. Sourced from Sub-Store uri.js producer +
	// mihomo convert/v.go; ignored by clients that don't read the key.
	if q.Get("packetEncoding") != "xudp" {
		t.Errorf("packetEncoding = %q, want xudp", q.Get("packetEncoding"))
	}
	// Reality field completeness: raw-TCP VLESS declares headerType=none.
	if q.Get("type") != "tcp" || q.Get("headerType") != "none" {
		t.Errorf("want type=tcp&headerType=none, got type=%q headerType=%q", q.Get("type"), q.Get("headerType"))
	}
}

func TestBuildHysteria2_SelfSignedInsecure(t *testing.T) {
	in := &model.Inbound{
		Tag: "h2", Type: "hysteria2", Port: 41020, Remark: "H2",
		Settings: `{"sni":"edgenest.local","tls_cert_path":"/etc/edgenest/certs/wizard-fullchain.pem"}`,
		Clients: []model.Client{
			{Email: "alice@example.com", Password: "p@ss w/spaces", Enabled: true},
		},
	}
	uris, err := BuildURIs(in, "example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 {
		t.Fatalf("want 1, got %d", len(uris))
	}
	u := uris[0]
	if !strings.Contains(u, "@example.org:41020") {
		t.Errorf("host:port wrong in %s", u)
	}
	parsed, _ := url.Parse(u)
	q := parsed.Query()
	if q.Get("sni") != "edgenest.local" {
		t.Errorf("sni = %q", q.Get("sni"))
	}
	if q.Get("insecure") != "1" {
		t.Errorf("insecure should be 1 for self-signed; got %q", q.Get("insecure"))
	}
	// Password should be URL-encoded (spaces, special chars).
	if strings.Contains(u, "p@ss w/spaces") {
		t.Errorf("password not url-encoded: %s", u)
	}
	// No real cert at the fake path → pinSHA256 degrades to absent (bare
	// insecure=1), the pre-existing behaviour.
	if q.Get("pinSHA256") != "" {
		t.Errorf("pinSHA256 should be absent when cert unreadable; got %q", q.Get("pinSHA256"))
	}
}

// G1: when a real self-signed cert exists on disk, the Hy2 URI carries
// pinSHA256 = lowercase-hex SHA-256 of the leaf DER, alongside insecure=1.
func TestBuildHysteria2_PinSHA256(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "hy2.crt")
	keyPath := filepath.Join(dir, "hy2.key")
	if err := selfsigned.Write("edgenest.local", certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	// Independent expected pin: hex(sha256(leaf DER)).
	pemBytes, _ := os.ReadFile(certPath)
	block, _ := pem.Decode(pemBytes)
	sum := sha256.Sum256(block.Bytes)
	wantPin := hex.EncodeToString(sum[:])

	in := &model.Inbound{
		Tag: "h2", Type: "hysteria2", Port: 41020, Remark: "H2",
		Settings: `{"sni":"edgenest.local","self_signed":"true","tls_cert_path":"` + certPath + `"}`,
		Clients: []model.Client{
			{Email: "alice@example.com", Password: "pw", Enabled: true},
		},
	}
	uris, err := BuildURIs(in, "example.org")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.Parse(uris[0])
	qq := q.Query()
	if qq.Get("insecure") != "1" {
		t.Errorf("insecure should stay 1 with pinSHA256; got %q", qq.Get("insecure"))
	}
	if got := qq.Get("pinSHA256"); got != wantPin {
		t.Errorf("pinSHA256 = %q, want %q", got, wantPin)
	}
	if len(wantPin) != 64 {
		t.Errorf("pin should be 64 hex chars (whole DER), got %d", len(wantPin))
	}
}

// G3: Hy2 port hopping — the URI authority carries the range and mport mirrors
// it; sing-box reads server_ports; Mihomo reads ports. No range → single port.
func TestBuildHysteria2_PortHopping(t *testing.T) {
	in := &model.Inbound{
		Tag: "h2", Type: "hysteria2", Port: 41020, Remark: "H2",
		Settings: `{"sni":"edgenest.local","port_hop_start":20000,"port_hop_end":40000}`,
		Clients: []model.Client{
			{Email: "alice@example.com", Password: "pw", Enabled: true},
		},
	}
	uris, err := BuildURIs(in, "example.org")
	if err != nil {
		t.Fatal(err)
	}
	u := uris[0]
	// Authority port slot is the range, not the single listen port. (Note:
	// Go's net/url.Parse rejects a port range as an invalid port — clients use
	// lenient parsers per the Hysteria URI scheme, so assert on the raw string
	// rather than round-tripping through url.Parse.)
	if !strings.Contains(u, "@example.org:20000-40000?") {
		t.Errorf("Hy2 hop authority should be host:START-END, got %s", u)
	}
	if !strings.Contains(u, "mport=20000-40000") {
		t.Errorf("mport mirror missing, got %s", u)
	}
}

func TestBuildHysteria2_NoHopWhenUnset(t *testing.T) {
	in := &model.Inbound{
		Tag: "h2", Type: "hysteria2", Port: 41020, Remark: "H2",
		Settings: `{"sni":"edgenest.local"}`,
		Clients:  []model.Client{{Email: "a@x", Password: "pw", Enabled: true}},
	}
	uris, _ := BuildURIs(in, "example.org")
	if !strings.Contains(uris[0], "@example.org:41020?") {
		t.Errorf("no hop range → single listen port authority, got %s", uris[0])
	}
	if strings.Contains(uris[0], "mport") {
		t.Errorf("no hop range → no mport, got %s", uris[0])
	}
}

func TestBuildURIs_SkipsDisabledClients(t *testing.T) {
	in := &model.Inbound{
		Tag: "vless-a", Type: "vless", Port: 8443,
		Settings: `{"sni":"x","reality_public_key":"P","short_ids":["s"]}`,
		Clients: []model.Client{
			{Email: "alice", UUID: "u-1", Enabled: true},
			{Email: "bob", UUID: "u-2", Enabled: false},
		},
	}
	uris, err := BuildURIs(in, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 1 {
		t.Fatalf("disabled client should be skipped; got %d uris", len(uris))
	}
}

func TestBuildURIs_UnknownProtocolSilent(t *testing.T) {
	in := &model.Inbound{
		Tag: "x", Type: "novel-protocol", Port: 1,
		Clients: []model.Client{{Email: "a", Enabled: true}},
	}
	uris, err := BuildURIs(in, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(uris) != 0 {
		t.Fatalf("unknown protocol should silently produce no uris; got %v", uris)
	}
}

func TestEncodeSubscriptionBody(t *testing.T) {
	uris := []string{"vless://x@h:1?#a", "hysteria2://y@h:2?#b"}
	body := EncodeSubscriptionBody(uris)
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(decoded)
	want := "vless://x@h:1?#a\nhysteria2://y@h:2?#b"
	if got != want {
		t.Errorf("decoded = %q, want %q", got, want)
	}
}

func TestBuildURIs_HostEmpty(t *testing.T) {
	in := &model.Inbound{Type: "vless", Tag: "x", Port: 1}
	if _, err := BuildURIs(in, ""); err == nil {
		t.Fatal("expected error for empty host")
	}
}
