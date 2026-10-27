package antiblock

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestNormalize(t *testing.T) {
	c, err := Normalize(Config{
		EdgeDomain: " CDN.Example.com. ", SubBaseURL: "https://cdn.example.com/",
		Mirrors: []string{"https://b.example.net/", "https://b.example.net", " "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.EdgeDomain != "cdn.example.com" || c.SubBaseURL != "https://cdn.example.com" {
		t.Fatalf("not canonicalised: %+v", c)
	}
	if len(c.Mirrors) != 1 || c.Mirrors[0] != "https://b.example.net" {
		t.Fatalf("mirrors not deduped: %v", c.Mirrors)
	}
	for name, bad := range map[string]Config{
		"ip as domain":         {EdgeDomain: "1.2.3.4"},
		"http mirror":          {Mirrors: []string{"http://b.example.net"}},
		"mirror with query":    {Mirrors: []string{"https://b.example.net/?x=1"}},
		"stealth without edge": {Stealth: true},
	} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("%s: accepted %+v", name, bad)
		}
	}
}

func TestMirrorsHeaderVerifies(t *testing.T) {
	st := newStore(t)
	if h, err := MirrorsHeader(st); err != nil || h != "" {
		t.Fatalf("no mirrors should mean no header, got %q %v", h, err)
	}
	if err := Save(st, Config{SubBaseURL: "https://cdn.example.com", Mirrors: []string{"https://b.example.net"}}); err != nil {
		t.Fatal(err)
	}
	h, err := MirrorsHeader(st)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(h, ".")
	if len(parts) != 2 {
		t.Fatalf("header shape: %q", h)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		V     int      `json:"v"`
		Seq   int64    `json:"seq"`
		Pub   string   `json:"pub"`
		Bases []string `json:"bases"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	pub, _ := base64.StdEncoding.DecodeString(p.Pub)
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("signature does not verify")
	}
	if p.V != 1 || p.Seq <= 0 || len(p.Bases) != 2 || p.Bases[0] != "https://cdn.example.com" {
		t.Fatalf("payload: %+v", p)
	}
	// Key is stable across calls; seq only moves when the list changes.
	h2, _ := MirrorsHeader(st)
	if h2 != h {
		t.Fatal("header changed without a config change")
	}
	k1, _ := PublicKey(st)
	if k1 != p.Pub {
		t.Fatal("PublicKey disagrees with the signed payload")
	}
}

func TestProxyConfigs(t *testing.T) {
	if ProxyConfigs(Config{}, "127.0.0.1:20000") != nil {
		t.Fatal("configs without an edge domain")
	}
	m := ProxyConfigs(Config{EdgeDomain: "cdn.example.com"}, "127.0.0.1:20000")
	if m["caddy"] != "cdn.example.com {\n\treverse_proxy 127.0.0.1:20000\n}\n" {
		t.Fatalf("caddy:\n%s", m["caddy"])
	}
	for _, want := range []string{"server_name cdn.example.com;", "proxy_pass http://127.0.0.1:20000;", "Upgrade $http_upgrade", "listen 443 ssl;"} {
		if !strings.Contains(m["nginx"], want) {
			t.Errorf("nginx missing %q", want)
		}
	}
}

func TestEdgePortAndOrigin(t *testing.T) {
	c, err := Normalize(Config{EdgeDomain: "cdn.example.com", EdgePort: 8443})
	if err != nil || c.GatewayPort() != 8443 || c.EdgeOrigin() != "https://cdn.example.com:8443" {
		t.Fatalf("%+v %v %s", c, err, c.EdgeOrigin())
	}
	c, _ = Normalize(Config{EdgeDomain: "cdn.example.com", EdgePort: 443})
	if c.EdgePort != 0 || c.EdgeOrigin() != "https://cdn.example.com" {
		t.Fatalf("443 should canonicalise to 0: %+v", c)
	}
	if _, err := Normalize(Config{EdgeDomain: "cdn.example.com", EdgePort: 9999}); err == nil {
		t.Fatal("non-Cloudflare port accepted")
	}
	// An external proxy always serves 443, whatever port was stored.
	if (Config{EdgeDomain: "cdn.example.com", EdgePort: 8443, GatewayMode: GatewayExternal}).EdgeOrigin() != "https://cdn.example.com" {
		t.Fatal("external origin must be :443")
	}
}

func TestRuns(t *testing.T) {
	c := Config{Stealth: true}
	if c.Runs(map[string]any{}) || !c.Runs(map[string]any{"edge_bound": "true"}) || !c.Runs(map[string]any{"argo_bound": true}) {
		t.Fatal("low-profile filter wrong")
	}
	if !(Config{}).Runs(map[string]any{}) {
		t.Fatal("normal mode must run everything")
	}
}
