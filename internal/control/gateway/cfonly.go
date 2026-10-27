package gateway

import (
	"net"
	"net/netip"
)

// cloudflareRanges are Cloudflare's published edge ranges
// (https://www.cloudflare.com/ips-v4/ and ips-v6/, checked 2026-10). They
// change rarely; a stale list only means a new Cloudflare edge is refused
// until the next release, and the operator can open the origin meanwhile.
var cloudflareRanges = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

// fromCloudflare reports whether addr is a Cloudflare edge (or loopback, for
// on-box health checks).
func fromCloudflare(addr net.Addr) bool {
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return false
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() {
		return true
	}
	for _, p := range cloudflareRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// cfListener drops every connection that doesn't come from Cloudflare before
// a single byte is exchanged: a scanner hitting the VPS address directly sees
// a closed connection, not a certificate or a page.
type cfListener struct{ net.Listener }

func (l *cfListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if fromCloudflare(c.RemoteAddr()) {
			return c, nil
		}
		_ = c.Close()
	}
}
