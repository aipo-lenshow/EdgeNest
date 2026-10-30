package bootstrap

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// privateV4 stands in for a NAT host's interface address.
var privateV4 = net.IPv4(10, 0, 0, 5)

func ips(ss ...string) []net.IP {
	var out []net.IP
	for _, s := range ss {
		out = append(out, net.ParseIP(s))
	}
	return out
}

func TestPlanAddressChange(t *testing.T) {
	in := func(listen string) model.Inbound { return model.Inbound{Listen: listen} }
	cases := []struct {
		name     string
		inbounds []model.Inbound
		local    []net.IP
		known    []string
		want     []AddrChange
	}{
		{
			name:     "single public address replaced",
			inbounds: []model.Inbound{in("203.0.113.7"), in("203.0.113.7"), in("127.0.0.1")},
			local:    ips("127.0.0.1", "198.51.100.9", "2001:db8::1"),
			known:    []string{"203.0.113.7", "2001:db8::1"},
			want:     []AddrChange{{Old: "203.0.113.7", New: "198.51.100.9"}},
		},
		{
			name:     "address still present",
			inbounds: []model.Inbound{in("203.0.113.7")},
			local:    ips("203.0.113.7"),
			known:    []string{"203.0.113.7"},
		},
		{
			name:     "NAT host listens on wildcards",
			inbounds: []model.Inbound{in("0.0.0.0"), in("::"), in("127.0.0.1")},
			local:    []net.IP{privateV4},
			known:    []string{"203.0.113.7"},
		},
		{
			name:     "private address is not a replacement",
			inbounds: []model.Inbound{in("203.0.113.7")},
			local:    []net.IP{privateV4},
			known:    []string{"203.0.113.7"},
		},
		{
			name:     "two new public addresses is ambiguous",
			inbounds: []model.Inbound{in("203.0.113.7")},
			local:    ips("198.51.100.9", "198.51.100.10"),
			known:    []string{"203.0.113.7"},
		},
		{
			name:     "multi-IP host: the address not on record wins",
			inbounds: []model.Inbound{in("203.0.113.7"), in("203.0.113.8")},
			local:    ips("203.0.113.8", "198.51.100.9"),
			known:    []string{"203.0.113.7", "203.0.113.8"},
			want:     []AddrChange{{Old: "203.0.113.7", New: "198.51.100.9"}},
		},
		{
			name:     "two lost addresses is ambiguous",
			inbounds: []model.Inbound{in("203.0.113.7"), in("203.0.113.8")},
			local:    ips("198.51.100.9"),
			known:    []string{"203.0.113.7", "203.0.113.8"},
		},
		{
			name:     "families are handled separately",
			inbounds: []model.Inbound{in("203.0.113.7"), in("2001:db8::1")},
			local:    ips("198.51.100.9", "2001:db8::2"),
			known:    []string{"203.0.113.7", "2001:db8::1"},
			want: []AddrChange{
				{Old: "203.0.113.7", New: "198.51.100.9"},
				{Old: "2001:db8::1", New: "2001:db8::2"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planAddressChange(tc.inbounds, tc.local, tc.known)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestHealAddressChange_MovesEveryRecord(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "edgenest.db"))
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.EnsureLocalNode()
	if err != nil {
		t.Fatal(err)
	}
	node.PublicIP = "203.0.113.7"
	if err := s.UpdateNode(node); err != nil {
		t.Fatal(err)
	}
	for i, in := range []model.Inbound{
		{Tag: "reality", Listen: "203.0.113.7", Port: 8443, SubscriptionHost: "203.0.113.7"},
		{Tag: "hy2", Listen: "203.0.113.7", Port: 41020, SubscriptionHost: "203.0.113.7"},
		{Tag: "argo", Listen: "127.0.0.1", Port: 2083},
	} {
		in.NodeID = node.ID
		in.Type = "vless"
		if err := s.CreateInbound(&in); err != nil {
			t.Fatalf("inbound %d: %v", i, err)
		}
	}
	_ = s.SetSetting("share_host", "203.0.113.7")
	_ = s.SetSetting(realityNeighborsKey, `{"server_ip":"203.0.113.7","candidates":[]}`)
	capPath := filepath.Join(dir, "network.json")
	if err := os.WriteFile(capPath, []byte(`{"ipv4":true,"ipv4_addr":"203.0.113.7","ipv4_addrs":["203.0.113.7"],"ipv6_global":true,"ipv6_addr":"2001:db8::1","ipv6_addrs":["2001:db8::1"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := healAddressChange(s, capPath, ips("127.0.0.1", "198.51.100.9", "2001:db8::1"))
	if len(got) != 1 || got[0] != (AddrChange{Old: "203.0.113.7", New: "198.51.100.9"}) {
		t.Fatalf("changes = %v", got)
	}

	list, _ := s.ListInbounds(node.ID)
	for _, in := range list {
		if in.Tag == "argo" {
			if in.Listen != "127.0.0.1" || in.SubscriptionHost != "" {
				t.Errorf("argo inbound touched: %+v", in)
			}
			continue
		}
		if in.Listen != "198.51.100.9" || in.SubscriptionHost != "198.51.100.9" {
			t.Errorf("%s: listen=%s host=%s", in.Tag, in.Listen, in.SubscriptionHost)
		}
	}
	if n, _ := s.GetLocalNode(); n.PublicIP != "198.51.100.9" {
		t.Errorf("node public ip = %s", n.PublicIP)
	}
	if v, _ := s.GetSetting("share_host"); v != "198.51.100.9" {
		t.Errorf("share_host = %s", v)
	}
	if v, _ := s.GetSetting(realityNeighborsKey); v != "" {
		t.Errorf("stale reality scan kept: %s", v)
	}
	cp := core.ReadNodeCapability(capPath)
	if cp.IPv4Addr != "198.51.100.9" || len(cp.IPv4Addrs) != 1 || cp.IPv4Addrs[0] != "198.51.100.9" {
		t.Errorf("capability v4 = %s %v", cp.IPv4Addr, cp.IPv4Addrs)
	}
	if cp.IPv6Addr != "2001:db8::1" || !cp.IPv4 || !cp.IPv6Global {
		t.Errorf("capability otherwise changed: %+v", cp)
	}
	var audits int64
	s.DB().Model(&model.AuditLog{}).Where("action = ?", "system.address_change").Count(&audits)
	if audits != 1 {
		t.Errorf("audit rows = %d", audits)
	}

	// Second start: nothing left to move.
	if again := healAddressChange(s, capPath, ips("127.0.0.1", "198.51.100.9", "2001:db8::1")); len(again) != 0 {
		t.Errorf("second pass changed %v", again)
	}
}
