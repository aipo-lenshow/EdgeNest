package firewall

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// hopCommentTag marks the nat PREROUTING REDIRECT rules we manage for
// Hysteria2 port hopping. Distinct from commentTag (filter INPUT ACCEPT) so
// the two reconcilers never touch each other's rules — and so the operator's
// own nat rules stay sacrosanct.
const hopCommentTag = "edgenest-managed-hop"

// ApplyPortHops reconciles nat-table PREROUTING REDIRECT rules to match the
// given Hysteria2 hop ranges, for BOTH iptables (v4) and ip6tables (v6) —
// EdgeNest serves dual-stack inbound, so a client may dial the VPS over either
// family and must hit the redirect on both.
//
// Each rule redirects UDP packets whose dport lands in [Start,End] to ToPort
// (the protocol's real single listen port). REDIRECT happens in nat
// PREROUTING, before the filter INPUT chain sees the packet — by then the
// dport is already rewritten to ToPort, so the existing INPUT ACCEPT rule for
// ToPort/udp already admits the redirected traffic (no range-wide ACCEPT
// needed). This is purely inbound (client→VPS); the server's own outbound
// dials go OUTPUT→POSTROUTING and never traverse PREROUTING, so dual-stack
// egress is unaffected.
//
// Like Apply, missing binaries are a silent no-op (dev laptops / containers).
func ApplyPortHops(hops []core.PortHopRule) error {
	var errs []string
	for _, fam := range []struct {
		bin     string
		saveBin string
	}{
		{"iptables", "iptables-save"},
		{"ip6tables", "ip6tables-save"},
	} {
		if _, err := exec.LookPath(fam.bin); err != nil {
			continue // family unavailable on this host — skip quietly
		}
		if err := reconcileHopFamily(fam.bin, fam.saveBin, hops); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", fam.bin, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("port-hop sync: %s", strings.Join(errs, "; "))
	}
	return nil
}

func reconcileHopFamily(bin, saveBin string, hops []core.PortHopRule) error {
	want := map[string]core.PortHopRule{}
	for _, h := range hops {
		if h.Start <= 0 || h.End < h.Start || h.ToPort <= 0 {
			continue // skip malformed ranges rather than feed iptables garbage
		}
		want[hopKey(h)] = h
	}

	have, err := listManagedHops(saveBin)
	if err != nil {
		// iptables-save can fail if the nat table isn't loaded; treat as "no
		// managed rules yet" so we still insert the wanted ones below.
		have = map[string]core.PortHopRule{}
	}

	var errs []string
	for k, h := range have {
		if _, ok := want[k]; ok {
			continue
		}
		if err := hopRuleCmd(bin, "-D", h); err != nil {
			errs = append(errs, fmt.Sprintf("delete %d:%d→%d: %v", h.Start, h.End, h.ToPort, err))
		}
	}
	for k, w := range want {
		if _, ok := have[k]; ok {
			continue
		}
		if err := hopRuleCmd(bin, "-A", w); err != nil {
			errs = append(errs, fmt.Sprintf("add %d:%d→%d: %v", w.Start, w.End, w.ToPort, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func hopKey(h core.PortHopRule) string {
	return strconv.Itoa(h.Start) + ":" + strconv.Itoa(h.End) + ">" + strconv.Itoa(h.ToPort)
}

// listManagedHops parses `<save> -t nat` and returns every PREROUTING REDIRECT
// rule carrying our hop comment, keyed by start:end>toport.
func listManagedHops(saveBin string) (map[string]core.PortHopRule, error) {
	out, err := exec.Command(saveBin, "-t", "nat").Output()
	if err != nil {
		return nil, err
	}
	res := map[string]core.PortHopRule{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "-A PREROUTING") {
			continue
		}
		if !strings.Contains(line, hopCommentTag) {
			continue
		}
		if h, ok := parseHop(line); ok {
			res[hopKey(h)] = h
		}
	}
	return res, nil
}

// parseHop extracts --dport START:END and --to-ports TOPORT from a saved rule.
func parseHop(line string) (core.PortHopRule, bool) {
	fields := strings.Fields(line)
	var h core.PortHopRule
	for i, f := range fields {
		switch f {
		case "--dport":
			if i+1 < len(fields) {
				start, end, ok := parseRange(fields[i+1])
				if !ok {
					return h, false
				}
				h.Start, h.End = start, end
			}
		case "--to-ports":
			if i+1 < len(fields) {
				if p, err := strconv.Atoi(fields[i+1]); err == nil {
					h.ToPort = p
				}
			}
		}
	}
	if h.Start <= 0 || h.End < h.Start || h.ToPort <= 0 {
		return h, false
	}
	return h, true
}

// parseRange accepts iptables' "START:END" dport syntax (a single port has no
// colon, which we treat as Start==End).
func parseRange(s string) (int, int, bool) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		start, err1 := strconv.Atoi(s[:i])
		end, err2 := strconv.Atoi(s[i+1:])
		if err1 != nil || err2 != nil {
			return 0, 0, false
		}
		return start, end, true
	}
	p, err := strconv.Atoi(s)
	if err != nil {
		return 0, 0, false
	}
	return p, p, true
}

// hopRuleCmd runs `<bin> -t nat <op> PREROUTING -p udp --dport START:END -j
// REDIRECT --to-ports TOPORT -m comment --comment edgenest-managed-hop`.
// op is "-A" (add) or "-D" (delete) — identical match spec either way so
// deletion finds the exact rule we inserted.
func hopRuleCmd(bin, op string, h core.PortHopRule) error {
	dport := strconv.Itoa(h.Start) + ":" + strconv.Itoa(h.End)
	return exec.Command(bin,
		"-t", "nat",
		op, "PREROUTING",
		"-p", "udp",
		"--dport", dport,
		"-m", "comment", "--comment", hopCommentTag,
		"-j", "REDIRECT",
		"--to-ports", strconv.Itoa(h.ToPort),
	).Run()
}
