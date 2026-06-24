package firewall

import "testing"

func TestParseHop(t *testing.T) {
	line := `-A PREROUTING -p udp -m udp --dport 20000:40000 -m comment --comment edgenest-managed-hop -j REDIRECT --to-ports 41020`
	h, ok := parseHop(line)
	if !ok {
		t.Fatalf("parseHop failed on a well-formed rule")
	}
	if h.Start != 20000 || h.End != 40000 || h.ToPort != 41020 {
		t.Errorf("parsed %+v, want {20000 40000 41020}", h)
	}
}

func TestParseHop_RejectsMalformed(t *testing.T) {
	for _, line := range []string{
		`-A PREROUTING -p udp --dport 20000:40000 -j REDIRECT`,        // no --to-ports
		`-A PREROUTING -p udp --to-ports 41020 -j REDIRECT`,          // no --dport
		`-A PREROUTING -p udp --dport notaport -j REDIRECT --to-ports 1`, // junk range
	} {
		if _, ok := parseHop(line); ok {
			t.Errorf("parseHop should reject %q", line)
		}
	}
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		in              string
		start, end      int
		ok              bool
	}{
		{"20000:40000", 20000, 40000, true},
		{"443", 443, 443, true}, // single port → start==end
		{"x:y", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		s, e, ok := parseRange(c.in)
		if ok != c.ok || (ok && (s != c.start || e != c.end)) {
			t.Errorf("parseRange(%q) = (%d,%d,%v), want (%d,%d,%v)", c.in, s, e, ok, c.start, c.end, c.ok)
		}
	}
}
