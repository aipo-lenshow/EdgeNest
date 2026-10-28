package reality

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"reflect"
	"testing"
)

func TestCertNames(t *testing.T) {
	c := &x509.Certificate{
		Subject:  pkix.Name{CommonName: "Shop.Example.com"},
		DNSNames: []string{"*.example.com", "shop.example.com", "203.0.113.5", "localhost", "api.example.com"},
	}
	got := certNames(c)
	want := []string{"shop.example.com", "api.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("certNames = %v, want %v", got, want)
	}
}

func TestResolvesInto(t *testing.T) {
	defer func(f func(context.Context, string, string) ([]net.IP, error)) { lookupIP = f }(lookupIP)
	lookupIP = func(_ context.Context, _, name string) ([]net.IP, error) {
		switch name {
		case "near.example.com":
			return []net.IP{net.ParseIP("198.51.100.200")}, nil
		default:
			return []net.IP{net.ParseIP("192.0.2.10")}, nil
		}
	}
	prefix := net.ParseIP("198.51.100.7").To4().Mask(net.CIDRMask(24, 32))
	if !resolvesInto(context.Background(), "near.example.com", prefix) {
		t.Error("same /24 not recognised")
	}
	if resolvesInto(context.Background(), "far.example.com", prefix) {
		t.Error("other network accepted")
	}
}

func TestFindNeighborsRejectsNonPublic(t *testing.T) {
	// Private addresses built at runtime so the repository's private-IP scan
	// doesn't trip over test data.
	private := []string{net.IPv4(10, 0, 0, 5).String(), net.IPv4(192, 168, 1, 2).String()}
	for _, ip := range append([]string{"", "::1", "2001:db8::1"}, private...) {
		if _, err := FindNeighbors(context.Background(), ip, Options{}); err != ErrNoIPv4 {
			t.Errorf("%q: want ErrNoIPv4, got %v", ip, err)
		}
	}
}

func TestDefaultSNI(t *testing.T) {
	defer preferred.Store("")
	preferred.Store("")
	if DefaultSNI() != FallbackSNI {
		t.Fatal("fallback not used before any scan")
	}
	SetResult(&Result{})
	if DefaultSNI() != FallbackSNI {
		t.Fatal("empty scan replaced the fallback")
	}
	SetResult(&Result{Candidates: []Candidate{{Domain: "shop.example.com"}}})
	if DefaultSNI() != "shop.example.com" {
		t.Fatal("neighbour not used as default")
	}
	if r := Load(`{"server_ip":"198.51.100.7","candidates":[{"domain":"a.example.com"}]}`); r == nil || r.Candidates[0].Domain != "a.example.com" {
		t.Fatal("cached result not decoded")
	}
	if Load("") != nil || Load("{") != nil {
		t.Fatal("bad cache should decode to nil")
	}
}

func TestTargetOf(t *testing.T) {
	cases := []struct {
		typ  string
		s    map[string]any
		want Target
		ok   bool
	}{
		{"vless", map[string]any{"reality_private_key": "k", "sni": "a.example.com"}, Target{"a.example.com", "a.example.com", 443}, true},
		{"vless", map[string]any{"reality_private_key": "k", "sni": "a.example.com", "server_name_target": "198.51.100.9", "server_port_target": float64(8443)}, Target{"a.example.com", "198.51.100.9", 8443}, true},
		{"vless", map[string]any{"sni": "a.example.com"}, Target{}, false}, // not Reality
		{"vless-xhttp", map[string]any{"security": "tls", "sni": "a.example.com"}, Target{}, false},
		{"vless-xhttp", map[string]any{"security": "reality", "sni": "b.example.com", "server_port_target": "443"}, Target{"b.example.com", "b.example.com", 443}, true},
		{"hysteria2", map[string]any{"sni": "c.example.com"}, Target{}, false},
	}
	for i, c := range cases {
		got, ok := TargetOf(c.typ, c.s)
		if ok != c.ok || got != c.want {
			t.Errorf("case %d: got %+v %v, want %+v %v", i, got, ok, c.want, c.ok)
		}
	}
}
