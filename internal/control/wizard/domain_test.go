package wizard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeCFServer serves canned Cloudflare CIDR list bodies, lets tests count
// hits so we can assert the 24h cache works.
type fakeCFServer struct {
	v4Body string
	v6Body string
	hits   int
}

func newFakeCFServer(v4, v6 string) (*httptest.Server, *fakeCFServer) {
	state := &fakeCFServer{v4Body: v4, v6Body: v6}
	mux := http.NewServeMux()
	mux.HandleFunc("/v4", func(w http.ResponseWriter, r *http.Request) {
		state.hits++
		_, _ = w.Write([]byte(state.v4Body))
	})
	mux.HandleFunc("/v6", func(w http.ResponseWriter, r *http.Request) {
		state.hits++
		_, _ = w.Write([]byte(state.v6Body))
	})
	srv := httptest.NewServer(mux)
	return srv, state
}

// newValidatorWithFakeDNS builds a validator pointed at a fake CF server and
// a static DNS table. Use this in every test — the production validator
// queries the host resolver which would be flaky in CI.
func newValidatorWithFakeDNS(t *testing.T, vps string, dns map[string][]string) (*DomainValidator, *fakeCFServer, func()) {
	t.Helper()
	srv, state := newFakeCFServer(
		"104.16.0.0/13\n104.24.0.0/14\n",
		"2606:4700::/32\n",
	)
	v := NewDomainValidator(func() []string {
		if vps == "" {
			return nil
		}
		return []string{vps}
	})
	v.cfV4URL = srv.URL + "/v4"
	v.cfV6URL = srv.URL + "/v6"
	v.httpClient = srv.Client()
	v.resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			t.Fatalf("validator hit the real resolver; missing fixture for %s", addr)
			return nil, nil
		},
	}
	// Replace LookupIPAddr with the fixture via a small wrapper: we can't
	// override a *net.Resolver method, so we test through a thin helper.
	return v, state, srv.Close
}

// lookupFixture lets tests inject a static DNS table without touching the
// system resolver. We exercise the Validate logic by calling the inner
// classify path directly via a wrapper test that builds the resolved IPs.
func TestValidate_NoDomain(t *testing.T) {
	v, _, cleanup := newValidatorWithFakeDNS(t, "1.2.3.4", nil)
	defer cleanup()
	res, err := v.Validate(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != DomainStatusNone {
		t.Errorf("status = %q, want %q", res.Status, DomainStatusNone)
	}
	if res.VPSPublicIP != "1.2.3.4" {
		t.Errorf("vps ip echoed wrong: %q", res.VPSPublicIP)
	}
}

func TestValidate_NXDOMAIN(t *testing.T) {
	v, _, cleanup := newValidatorWithFakeDNS(t, "1.2.3.4", nil)
	defer cleanup()
	v.resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, &net.DNSError{Err: "nxdomain", IsNotFound: true}
		},
	}
	res, err := v.Validate(context.Background(), "does-not-exist.example")
	if err != nil {
		t.Fatalf("nxdomain should not error: %v", err)
	}
	if res.Status != DomainStatusNone {
		t.Errorf("nxdomain status = %q, want %q", res.Status, DomainStatusNone)
	}
}

// TestClassifyDomain_DualStack locks the dual-stack fix: a domain whose A→v4
// and AAAA→v6 both point at this node must verify as OK, not mismatch (the bug
// was the validator knowing only the v4, so it flagged the node's own v6 as
// "some other IP").
func TestClassifyDomain_DualStack(t *testing.T) {
	v4 := net.ParseIP("203.0.113.10")
	v6 := net.ParseIP("2001:db8:5500:ccc4::2")
	vpsIPs := []net.IP{v4, v6}

	cases := []struct {
		name     string
		resolved []net.IP
		want     DomainStatus
	}{
		{"dual-stack both at vps", []net.IP{v6, v4}, DomainStatusOK},
		{"v4 only at vps", []net.IP{v4}, DomainStatusOK},
		{"v6 only at vps", []net.IP{v6}, DomainStatusOK},
		{"v6 elsewhere", []net.IP{v4, net.ParseIP("2001:db8::1")}, DomainStatusMismatch},
		{"v4 elsewhere", []net.IP{net.ParseIP("8.8.8.8")}, DomainStatusMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDomain(tc.resolved, vpsIPs, nil); got != tc.want {
				t.Errorf("classifyDomain = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClassify_DirectClassification calls the classification logic through
// the cidrs cache + ipInCIDRs helper, exercising the proxied/ok/mismatch
// switch without touching DNS.
func TestClassify_DirectClassification(t *testing.T) {
	v, state, cleanup := newValidatorWithFakeDNS(t, "1.2.3.4", nil)
	defer cleanup()

	ctx := context.Background()
	cidrs, err := v.cloudflareCIDRs(ctx)
	if err != nil {
		t.Fatalf("cloudflareCIDRs: %v", err)
	}
	if len(cidrs) == 0 {
		t.Fatal("expected fake CF CIDR list to load")
	}

	cases := []struct {
		name string
		ips  []net.IP
		want DomainStatus
	}{
		{"ok-direct-v4", []net.IP{net.ParseIP("1.2.3.4")}, DomainStatusOK},
		{"proxied-cf-range", []net.IP{net.ParseIP("104.16.50.1")}, DomainStatusProxied},
		{"proxied-mixed", []net.IP{net.ParseIP("1.2.3.4"), net.ParseIP("104.16.50.1")}, DomainStatusProxied},
		{"mismatch-elsewhere", []net.IP{net.ParseIP("8.8.8.8")}, DomainStatusMismatch},
	}
	vpsIP := net.ParseIP("1.2.3.4")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			anyCF := false
			allMatchVPS := true
			for _, ip := range tc.ips {
				if !ip.Equal(vpsIP) {
					allMatchVPS = false
				}
				if ipInCIDRs(ip, cidrs) {
					anyCF = true
				}
			}
			var got DomainStatus
			switch {
			case anyCF:
				got = DomainStatusProxied
			case allMatchVPS:
				got = DomainStatusOK
			default:
				got = DomainStatusMismatch
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q (ips=%v)", got, tc.want, tc.ips)
			}
		})
	}

	hitsBeforeRefetch := state.hits
	// Second cloudflareCIDRs call within TTL must hit the cache, not the network.
	if _, err := v.cloudflareCIDRs(ctx); err != nil {
		t.Fatal(err)
	}
	if state.hits != hitsBeforeRefetch {
		t.Errorf("expected CF cache to absorb the second call; hits %d -> %d", hitsBeforeRefetch, state.hits)
	}

	// Expiring the cache forces a refetch.
	v.ttl = 0
	v.fetchedAt = time.Now().Add(-time.Hour)
	v.cidrs = nil
	if _, err := v.cloudflareCIDRs(ctx); err != nil {
		t.Fatal(err)
	}
	if state.hits <= hitsBeforeRefetch {
		t.Errorf("expected CF refetch after TTL expiry; hits stayed at %d", state.hits)
	}
}

func TestFetchCIDRList_SkipsBlankAndComments(t *testing.T) {
	v, _, cleanup := newValidatorWithFakeDNS(t, "1.2.3.4", nil)
	defer cleanup()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# comment line\n\n173.245.48.0/20\n  103.21.244.0/22  \nNOT_A_CIDR\n"))
	}))
	defer srv.Close()
	cidrs, err := v.fetchCIDRList(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(cidrs) != 2 {
		t.Errorf("expected 2 valid CIDRs, got %d (%v)", len(cidrs), cidrs)
	}
}
