package bootstrap

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"gorm.io/gorm"
)

// AddrChange is one public address the host lost, and the one that replaced it.
type AddrChange struct {
	Old string
	New string
}

// HealAddressChange follows a provider-side IP change. Inbounds that bind a
// literal address keep it in the database (listenForHost only writes a literal
// when the address is on a local interface; NAT hosts get a wildcard), so a
// literal listen address that is no longer local can only mean the host's
// address changed. Left alone, sing-box fails to bind on every start and the
// engine stays down until someone edits the database by hand.
//
// For each family with exactly one lost address and one unambiguous
// replacement, every place the old address was recorded is moved to the new
// one: inbound listen / subscription host, the local node's public IP,
// share_host, network.json, and a Reality neighbour scan made around the old
// address (dropped so the next start rescans). Anything ambiguous is logged
// and left for the operator.
func HealAddressChange(s *store.Store, capPath string) []AddrChange {
	local, err := localAddrs()
	if err != nil {
		log.Printf("bootstrap: address check skipped: %v", err)
		return nil
	}
	return healAddressChange(s, capPath, local)
}

func healAddressChange(s *store.Store, capPath string, local []net.IP) []AddrChange {
	node, err := s.GetLocalNode()
	if err != nil {
		return nil
	}
	inbounds, err := s.ListInbounds(node.ID)
	if err != nil {
		return nil
	}
	cp := core.ReadNodeCapability(capPath)
	changes := planAddressChange(inbounds, local, append(append([]string{}, cp.IPv4Addrs...), cp.IPv6Addrs...))
	var applied []AddrChange
	for _, ch := range changes {
		n, err := applyAddressChange(s, node.ID, ch)
		if err != nil {
			log.Printf("bootstrap: move %s -> %s: %v", ch.Old, ch.New, err)
			continue
		}
		if err := rewriteCapability(capPath, ch); err != nil {
			log.Printf("bootstrap: update %s for %s -> %s: %v", capPath, ch.Old, ch.New, err)
		}
		log.Printf("bootstrap: host address changed %s -> %s, moved %d inbound(s)", ch.Old, ch.New, n)
		meta, _ := json.Marshal(map[string]any{"old": ch.Old, "new": ch.New, "inbounds": n})
		_ = s.DB().Create(&model.AuditLog{
			Actor:     "system",
			Action:    "system.address_change",
			Resource:  "node",
			Meta:      string(meta),
			CreatedAt: time.Now().Unix(),
		}).Error
		applied = append(applied, ch)
	}
	return applied
}

// planAddressChange is the pure decision: which recorded literal addresses
// are gone, and what replaces each. known is the install-time address list
// (network.json); a local public address not on it is the likely newcomer.
func planAddressChange(inbounds []model.Inbound, local []net.IP, known []string) []AddrChange {
	isLocal := func(ip net.IP) bool {
		for _, l := range local {
			if l.Equal(ip) {
				return true
			}
		}
		return false
	}
	isKnown := func(ip net.IP) bool {
		for _, k := range known {
			if kip := net.ParseIP(k); kip != nil && kip.Equal(ip) {
				return true
			}
		}
		return false
	}

	var out []AddrChange
	for _, v4 := range []bool{true, false} {
		sameFamily := func(ip net.IP) bool { return (ip.To4() != nil) == v4 }

		var lost []string
		seen := map[string]bool{}
		for _, in := range inbounds {
			ip := net.ParseIP(in.Listen)
			if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || !sameFamily(ip) || isLocal(ip) {
				continue
			}
			if s := ip.String(); !seen[s] {
				seen[s] = true
				lost = append(lost, s)
			}
		}
		if len(lost) == 0 {
			continue
		}

		var public, fresh []net.IP
		for _, l := range local {
			if !sameFamily(l) || !l.IsGlobalUnicast() || l.IsPrivate() {
				continue
			}
			public = append(public, l)
			if !isKnown(l) {
				fresh = append(fresh, l)
			}
		}
		var pick net.IP
		switch {
		case len(lost) != 1:
		case len(fresh) == 1:
			pick = fresh[0]
		case len(public) == 1:
			pick = public[0]
		}
		if pick == nil {
			log.Printf("bootstrap: inbounds listen on %v, which this host no longer has, and the replacement is ambiguous (local public: %v); fix the inbound addresses in the panel", lost, public)
			continue
		}
		out = append(out, AddrChange{Old: lost[0], New: pick.String()})
	}
	return out
}

func applyAddressChange(s *store.Store, nodeID uint, ch AddrChange) (int64, error) {
	var moved int64
	err := s.DB().Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Inbound{}).
			Where("node_id = ? AND listen = ?", nodeID, ch.Old).
			Update("listen", ch.New)
		if res.Error != nil {
			return res.Error
		}
		moved = res.RowsAffected
		if err := tx.Model(&model.Inbound{}).
			Where("node_id = ? AND subscription_host = ?", nodeID, ch.Old).
			Update("subscription_host", ch.New).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Node{}).
			Where("id = ? AND public_ip = ?", nodeID, ch.Old).
			Updates(map[string]any{"public_ip": ch.New, "updated_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if v, _ := s.GetSetting("share_host"); v == ch.Old {
		if err := s.SetSetting("share_host", ch.New); err != nil {
			log.Printf("bootstrap: update share_host: %v", err)
		}
	}
	// The Reality neighbour scan looks for sites next to the server's own
	// address; one made around the old address no longer fits.
	if raw, _ := s.GetSetting(realityNeighborsKey); raw != "" {
		var r struct {
			ServerIP string `json:"server_ip"`
		}
		if json.Unmarshal([]byte(raw), &r) == nil && r.ServerIP == ch.Old {
			_ = s.SetSetting(realityNeighborsKey, "")
		}
	}
	return moved, nil
}

// realityNeighborsKey mirrors reality.SettingKey (not imported: reality pulls
// in the network scanner, which bootstrap has no other use for).
const realityNeighborsKey = "reality_neighbors"

func rewriteCapability(path string, ch AddrChange) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var cp core.NodeCapability
	if err := json.Unmarshal(data, &cp); err != nil {
		return err
	}
	swap := func(list []string) []string {
		out := make([]string, 0, len(list))
		has := false
		for _, a := range list {
			if a == ch.New {
				has = true
			}
		}
		for _, a := range list {
			switch {
			case a == ch.Old && !has:
				out = append(out, ch.New)
				has = true
			case a == ch.Old:
			default:
				out = append(out, a)
			}
		}
		return out
	}
	cp.IPv4Addrs = swap(cp.IPv4Addrs)
	cp.IPv6Addrs = swap(cp.IPv6Addrs)
	if cp.IPv4Addr == ch.Old {
		cp.IPv4Addr = ch.New
	}
	if cp.IPv6Addr == ch.Old {
		cp.IPv6Addr = ch.New
	}
	out, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func localAddrs() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			out = append(out, ipnet.IP)
		}
	}
	return out, nil
}
