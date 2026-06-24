// Package system — ports.go centralises the "what ports can the Wizard
// possibly bind, and which ones must we refuse?" rules. Two product invariants
// drive the API surface here:
//
//   - CDN-eligible protocols (VMess-WS+TLS, VLESS-WS+TLS, VLESS-XHTTP-TLS) can
//     only ride Cloudflare's free-tier HTTPS port whitelist. Picking anything
//     outside the whitelist means CF silently refuses to proxy — the operator
//     ends up with an inbound the panel says is live but every client connects
//     to a black hole.
//
//   - Some ports are off-limits regardless of protocol: panel's own listen
//     port (the operator would lock themselves out), SSH, DNS, and any port
//     that's already bound by another inbound on the same node.
//
// Wizard front-end pulls a Reserved snapshot once at mount and uses it to
// (a) render the CDN port <select> as the whitelist minus the reserved /
// occupied union and (b) blur-validate free-text port inputs for the
// non-CDN protocols. Backend re-validates on POST so a hand-rolled request
// can't bypass the UI.

package system

// CFHTTPSWhitelist is the Cloudflare free-tier HTTPS port set. CDN-eligible
// inbounds can only ride one of these or the CF edge refuses to proxy
// (silently — there is no useful client-side error).
//
// Source: Cloudflare network ports documentation, current as of 2026-06.
// HTTP whitelist (80, 8080, 8880, 2052, 2082, 2086, 2095) is not exposed here
// because every CDN-eligible inbound EdgeNest provisions uses TLS upstream.
var CFHTTPSWhitelist = []int{443, 2053, 2083, 2087, 2096, 8443}

// SystemReservedPorts are unconditionally off-limits on every Linux box the
// panel might run on. Adding to this list quietly retires a port from the
// wizard's pickFreePort search.
var SystemReservedPorts = []int{
	22, // SSH — refuse to fight with the operator's own shell
	53, // DNS (systemd-resolved is bound to 127.0.0.53:53; refuse loopback collisions)
}

// ReservedPorts returns every port the wizard must refuse to bind: the static
// SystemReservedPorts + the panel's own listen port (so the operator can't
// accidentally lock themselves out by minting an inbound on the same port).
func ReservedPorts(panelPort int) []int {
	out := make([]int, 0, len(SystemReservedPorts)+1)
	out = append(out, SystemReservedPorts...)
	if panelPort > 0 {
		out = append(out, panelPort)
	}
	return out
}

// IsReserved reports whether port is in ReservedPorts(panelPort).
func IsReserved(port, panelPort int) bool {
	for _, p := range ReservedPorts(panelPort) {
		if p == port {
			return true
		}
	}
	return false
}

// IsCFWhitelisted reports whether port is in CFHTTPSWhitelist.
func IsCFWhitelisted(port int) bool {
	for _, p := range CFHTTPSWhitelist {
		if p == port {
			return true
		}
	}
	return false
}

// MinAllowedPort is the floor the wizard accepts for any inbound — privileged
// ports below 1024 are off-limits to avoid clashing with SSH/HTTP/HTTPS/SMTP
// surprises and to make sure the binary can bind without raising CAP_NET_BIND.
const MinAllowedPort = 1024

// MaxAllowedPort is the standard TCP/UDP ceiling.
const MaxAllowedPort = 65535
