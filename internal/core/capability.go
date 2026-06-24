package core

import (
	"encoding/json"
	"os"
)

// NodeCapability mirrors /etc/edgenest/network.json — written by install.sh's
// detect_node_capability. Consumed by sing-box / xray render to pick the
// default outbound strategy, by /api/v1/system/capability so the wizard's
// HostChooser knows what IPs to offer, and by the bootstrap cert SAN code.
//
// IPv4Addrs / IPv6Addrs hold every globally-routable address the install
// script verified can egress to the public internet. A multi-IP VPS shows
// up here with N entries; users pick which IP each inbound binds to.
//
// IPv4Addr / IPv6Addr (singular) remain as IPv4Addrs[0] / IPv6Addrs[0] for
// back-compat with code paths that still read a single literal — share
// resolver's global-host fallback chain, /api/v1/system/info, and the
// bootstrap self-signed cert SAN code prior to the migration.
type NodeCapability struct {
	IPv4       bool     `json:"ipv4"`
	IPv4Addr   string   `json:"ipv4_addr,omitempty"`
	IPv4Addrs  []string `json:"ipv4_addrs,omitempty"`
	IPv6Global bool     `json:"ipv6_global"`
	IPv6Addr   string   `json:"ipv6_addr,omitempty"`
	IPv6Addrs  []string `json:"ipv6_addrs,omitempty"`
}

// DefaultCapabilityPath is the install.sh-managed network.json file.
const DefaultCapabilityPath = "/etc/edgenest/network.json"

// ReadNodeCapability reads + parses the capability JSON at path. On any
// error returns dual-stack — a safe fall-through so v6 is not crippled on a
// brand-new install before detect ran.
func ReadNodeCapability(path string) NodeCapability {
	data, err := os.ReadFile(path)
	if err != nil {
		return NodeCapability{IPv4: true, IPv6Global: true}
	}
	var n NodeCapability
	if err := json.Unmarshal(data, &n); err != nil {
		return NodeCapability{IPv4: true, IPv6Global: true}
	}
	return n
}
