package share

import (
	"encoding/binary"
	"math/rand"
	"net"
	"strings"
)

// CFCandidateIPs is a curated pool of Cloudflare anycast IPs drawn from
// Cloudflare's published address ranges (104.16.0.0/13, 172.64.0.0/13,
// 162.159.0.0/16, 188.114.96.0/20). Every one of them fronts the same global
// CDN, so a CDN-mode inbound can dial whichever responds fastest from the
// operator's network without changing what the client sees (SNI/Host stay the
// real domain).
//
// The list serves two purposes:
//   - "Fill recommended" (P0): seed an operator's empty preferred-IP pool with
//     a sane default so they never have to hunt for IPs.
//   - Speed test (P1): the candidate set the server probes and ranks by
//     measured latency, writing the fastest N back into the pool.
//
// Anycast routes the whole prefix to the nearest edge, but not every individual
// /32 inside a range has a customer-facing HTTPS frontend — some addresses
// (notably the first-in-range .0.1 ones) don't answer on :443 at all. The reps
// below were verified to complete a TLS handshake (with SNI) from a live VPS;
// the dead ones (104.22.0.1, 172.64.0.1, 172.67.0.1) are replaced with reachable
// neighbours. Operators can always add/remove their own; the speed test sorts
// out whichever are slow or unreachable from their network anyway.
// The pool is intentionally broad — the speed test sorts out whichever are slow
// or unreachable from a given network, so more candidates means a better chance
// of finding a fast edge. Spread across all of Cloudflare's main customer-facing
// HTTPS ranges (104.16.0.0/13, 172.64.0.0/13, 162.159.0.0/16, 188.114.96.0/20).
var CFCandidateIPs = []string{
	// 104.16.0.0/13
	"104.16.0.1",
	"104.16.96.1",
	"104.17.0.1",
	"104.17.96.1",
	"104.18.0.1",
	"104.18.96.1",
	"104.19.0.1",
	"104.19.96.1",
	"104.20.0.1",
	"104.21.0.1",
	"104.22.32.1",
	"104.23.96.1",
	"104.24.0.1",
	"104.25.0.1",
	"104.26.0.1",
	"104.27.0.1",
	"104.28.0.1",
	"104.31.0.1",
	// 172.64.0.0/13
	"172.64.80.1",
	"172.64.96.1",
	"172.65.0.1",
	"172.66.0.1",
	"172.67.73.1",
	"172.67.180.1",
	// 162.159.0.0/16
	"162.159.0.1",
	"162.159.36.1",
	"162.159.135.1",
	"162.159.152.1",
	"162.159.192.1",
	// 188.114.96.0/20
	"188.114.96.1",
	"188.114.97.1",
	"188.114.98.1",
	"188.114.99.1",
}

// RecommendedCDNIPs returns the default subset to fill an operator's pool with
// when they click "fill recommended". A spread across the main ranges gives
// the per-user deterministic hash (PickCDNHost) enough entropy to distribute
// clients while staying short enough to stay readable in the UI.
func RecommendedCDNIPs() []string {
	return []string{
		"104.16.0.1",
		"104.17.0.1",
		"104.18.0.1",
		"172.64.80.1",
		"172.67.73.1",
		"162.159.0.1",
	}
}

// cfCDNRanges are Cloudflare's main customer-facing HTTPS prefixes — the same
// ranges the curated reps above are drawn from, and a subset of what Cloudflare
// publishes at cloudflare.com/ips-v4 (the website frontends live here; the
// infra-only ranges answer :443 far less often). Anycast routes every address
// in them to the nearest edge, so sampling random hosts and probing them — then
// dropping the unreachable at speed-test time — discovers fast edges the fixed
// /32 list misses, without anyone hand-maintaining IPs.
var cfCDNRanges = []string{
	"104.16.0.0/12",   // 104.16–104.31
	"172.64.0.0/13",   // 172.64–172.71
	"162.159.0.0/16",  //
	"188.114.96.0/20", //
}

// SampleCandidateIPs returns the curated static reps plus n freshly sampled
// random hosts from cfCDNRanges (deduped). It backs the "refresh candidate
// pool" action: when too few of the fixed reps are reachable from an operator's
// network, resampling surfaces more live edges to speed-test. Nothing is
// persisted — the caller feeds the returned list straight into the next sweep.
func SampleCandidateIPs(n int) []string {
	seen := make(map[string]struct{}, len(CFCandidateIPs)+n)
	out := make([]string, 0, len(CFCandidateIPs)+n)
	for _, ip := range CFCandidateIPs {
		if _, ok := seen[ip]; !ok {
			seen[ip] = struct{}{}
			out = append(out, ip)
		}
	}
	if n <= 0 || len(cfCDNRanges) == 0 {
		return out
	}
	// Cap attempts so a pathological range can't spin forever chasing dupes.
	for added, attempts := 0, 0; added < n && attempts < n*20; attempts++ {
		ip := randomHostInRange(cfCDNRanges[rand.Intn(len(cfCDNRanges))])
		if ip == "" {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
		added++
	}
	return out
}

// randomHostInRange picks a random usable host inside an IPv4 CIDR, skipping the
// network and broadcast addresses. Returns "" for a malformed or too-small range.
func randomHostInRange(cidr string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet.IP.To4() == nil {
		return ""
	}
	ones, bits := ipnet.Mask.Size()
	hostBits := bits - ones
	if hostBits <= 1 {
		return ""
	}
	size := uint32(1) << uint(hostBits)
	base := binary.BigEndian.Uint32(ipnet.IP.To4())
	off := uint32(rand.Int63n(int64(size-2))) + 1 // [1, size-2]
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, base+off)
	return net.IPv4(b[0], b[1], b[2], b[3]).String()
}

// clientProbeCandidates are Cloudflare addresses that held up when probed from
// end-user access networks (home broadband and mobile data), not from a VPS.
// What is fast from a datacenter says little about what a user's ISP routes
// well, so this list is kept apart from CFCandidateIPs. It is spread over
// several /24s on purpose: an ISP that routes one block badly usually routes
// another one fine.
var clientProbeCandidates = []string{
	"103.31.4.230",
	"103.31.4.68",
	"103.31.4.231",
	"103.31.4.170",
	"103.31.4.2",
	"103.31.4.71",
	"162.159.129.27",
	"162.159.133.199",
	"162.159.143.206",
	"162.159.139.2",
	"162.159.152.20",
	"162.159.151.216",
	"162.159.82.27",
	"104.17.152.60",
	"188.114.97.75",
	"188.114.99.9",
	"188.114.96.31",
	"188.114.96.37",
	"188.114.96.156",
}

// maxClientCandidates caps the list handed to clients. Each client probes only
// a handful at a time, so a longer list adds no coverage, just header bytes.
const maxClientCandidates = 30

// ClientCandidates returns the addresses an EdgeNest client may probe from its
// own network to re-point CDN nodes: the operator's preferred pool first (it is
// what the subscription already dials), then the curated client list, deduped
// and capped. Blank entries and anything that isn't an IPv4 address are
// dropped so a typo in the pool can't reach clients.
func ClientCandidates(pool []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{pool, clientProbeCandidates} {
		for _, ip := range list {
			ip = strings.TrimSpace(ip)
			if len(out) == maxClientCandidates {
				return out
			}
			if seen[ip] || net.ParseIP(ip).To4() == nil {
				continue
			}
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out
}
