package capture

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"netflix.com", "https://netflix.com", false},
		{"https://netflix.com/browse", "https://netflix.com/browse", false},
		{"http://example.org", "http://example.org", false},
		{"  spotify.com  ", "https://spotify.com", false},
		{"", "", true},
		{"ftp://example.org", "", true},
		{"https://", "", true},
		{"s送积分", "", true},        // free text, not a domain (no dot / suffix)
		{"hello world", "", true}, // space → invalid host
		{"abc", "", true},         // no public suffix
		{"1.2.3.4", "https://1.2.3.4", false},
	}
	for _, tc := range cases {
		got, err := normalizeURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeURL(%q): want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeURL(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGroupHosts(t *testing.T) {
	hosts := map[string]struct{}{
		"www.netflix.com":    {},
		"assets.netflix.com": {},
		"nflxso.net":         {},
		"occ-0.nflxso.net":   {},
		"1.2.3.4":            {},
		"":                   {},
	}
	groups := groupHosts(hosts)

	byReg := map[string]DomainGroup{}
	for _, g := range groups {
		byReg[g.Registrable] = g
	}

	nf, ok := byReg["netflix.com"]
	if !ok {
		t.Fatalf("expected netflix.com group, got %+v", groups)
	}
	if nf.Count != 2 || len(nf.Hosts) != 2 {
		t.Errorf("netflix.com: want 2 hosts, got %d (%v)", nf.Count, nf.Hosts)
	}
	// Hosts must be sorted for stable UI rendering.
	if nf.Hosts[0] != "assets.netflix.com" || nf.Hosts[1] != "www.netflix.com" {
		t.Errorf("netflix.com hosts not sorted: %v", nf.Hosts)
	}
	if g, ok := byReg["nflxso.net"]; !ok || g.Count != 2 {
		t.Errorf("nflxso.net: want 2 hosts, got %+v", g)
	}
	// An IP has no registrable domain — it should survive under itself.
	if _, ok := byReg["1.2.3.4"]; !ok {
		t.Errorf("IP host dropped: %+v", groups)
	}
	// The empty host must not produce a group.
	if _, ok := byReg[""]; ok {
		t.Errorf("empty host produced a group")
	}
	// Groups must be sorted by registrable domain.
	for i := 1; i < len(groups); i++ {
		if groups[i-1].Registrable > groups[i].Registrable {
			t.Errorf("groups not sorted: %v", groups)
			break
		}
	}
}

func TestStaticCapture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head>
			<link href="https://fonts.example-cdn.com/style.css" rel="stylesheet">
			<script src="https://cdn.analytics.io/a.js"></script>
		</head><body>
			<img src="/local.png">
			<img srcset="https://img.example-cdn.com/1x.png 1x, https://img.example-cdn.com/2x.png 2x">
			<form action="https://api.example-backend.com/submit"></form>
			<a href="mailto:x@y.z">mail</a>
			<a href="https://social.example-anchor.net/profile">follow us</a>
		</body></html>`))
	}))
	defer srv.Close()

	hosts, err := staticCapture(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("staticCapture: %v", err)
	}
	// Loaded resources (link/script/img/srcset) + form action backend are kept.
	want := []string{
		"fonts.example-cdn.com",
		"cdn.analytics.io",
		"img.example-cdn.com",
		"api.example-backend.com",
	}
	for _, h := range want {
		if _, ok := hosts[h]; !ok {
			t.Errorf("staticCapture missed %q; got %v", h, hosts)
		}
	}
	// <a href> is navigation, not a loaded resource — must be excluded so footer
	// / social links don't pollute the captured domain list.
	if _, ok := hosts["social.example-anchor.net"]; ok {
		t.Errorf("staticCapture must exclude <a href> hosts; got %v", hosts)
	}
	// mailto: has no http host and must be skipped.
	if _, ok := hosts["x@y.z"]; ok {
		t.Errorf("staticCapture should skip mailto host")
	}
}
