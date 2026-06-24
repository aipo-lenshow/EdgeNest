// Package firewall reconciles iptables INPUT ACCEPT rules to match the
// node's effective AllowPorts list. Every rule we add is tagged with the
// comment "edgenest-managed" so reconciliation can find and remove stale
// rules without ever touching the operator's own rules.
package firewall

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

const commentTag = "edgenest-managed"

// Apply makes the live iptables INPUT chain reflect the given allow list.
// Rules tagged with our comment that are no longer wanted are deleted; new
// rules are inserted. Anything without the tag is left alone — the operator's
// own iptables config is sacrosanct.
//
// Errors from individual rule edits are accumulated but don't abort the run;
// we want to try every rule before reporting a partial-success message.
func Apply(rules []core.PortRule) error {
	if _, err := exec.LookPath("iptables"); err != nil {
		// No iptables on this host — bail silently so dev laptops (macOS) and
		// containers without NET_ADMIN don't error out. Production installs
		// run install.sh which guarantees iptables exists.
		return nil
	}

	want := map[string]core.PortRule{}
	for _, r := range expand(rules) {
		want[key(r.Port, r.Proto)] = r
	}

	have, err := listManaged()
	if err != nil {
		return fmt.Errorf("list managed: %w", err)
	}

	var errs []string
	// Drop rules we no longer want.
	for k, h := range have {
		if _, ok := want[k]; ok {
			continue
		}
		if err := deleteRule(h.Port, h.Proto); err != nil {
			errs = append(errs, fmt.Sprintf("delete %d/%s: %v", h.Port, h.Proto, err))
		}
	}
	// Insert rules we want but don't yet have.
	for k, w := range want {
		if _, ok := have[k]; ok {
			continue
		}
		if err := insertRule(w.Port, w.Proto); err != nil {
			errs = append(errs, fmt.Sprintf("insert %d/%s: %v", w.Port, w.Proto, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("firewall sync: %s", strings.Join(errs, "; "))
	}
	return nil
}

// expand splits "both" into ("tcp", "udp") so iptables (which only knows one
// proto per rule) gets one row per real port/proto pair.
func expand(rules []core.PortRule) []core.PortRule {
	out := make([]core.PortRule, 0, len(rules))
	for _, r := range rules {
		switch r.Proto {
		case "both", "":
			out = append(out, core.PortRule{Port: r.Port, Proto: "tcp", Note: r.Note})
			out = append(out, core.PortRule{Port: r.Port, Proto: "udp", Note: r.Note})
		default:
			out = append(out, r)
		}
	}
	return out
}

func key(port int, proto string) string {
	return strconv.Itoa(port) + "/" + proto
}

// listManaged parses `iptables-save -t filter` and returns every INPUT rule
// carrying our comment tag, keyed by port/proto.
func listManaged() (map[string]core.PortRule, error) {
	out, err := exec.Command("iptables-save", "-t", "filter").Output()
	if err != nil {
		return nil, err
	}
	res := map[string]core.PortRule{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "-A INPUT") {
			continue
		}
		if !strings.Contains(line, commentTag) {
			continue
		}
		port, proto, ok := parsePortProto(line)
		if !ok {
			continue
		}
		res[key(port, proto)] = core.PortRule{Port: port, Proto: proto}
	}
	return res, nil
}

// parsePortProto pulls the --dport and -p values out of an iptables-save line.
func parsePortProto(line string) (int, string, bool) {
	fields := strings.Fields(line)
	var port int
	var proto string
	for i, f := range fields {
		switch f {
		case "-p":
			if i+1 < len(fields) {
				proto = fields[i+1]
			}
		case "--dport":
			if i+1 < len(fields) {
				if p, err := strconv.Atoi(fields[i+1]); err == nil {
					port = p
				}
			}
		}
	}
	if port == 0 || (proto != "tcp" && proto != "udp") {
		return 0, "", false
	}
	return port, proto, true
}

func insertRule(port int, proto string) error {
	return exec.Command("iptables",
		"-I", "INPUT",
		"-p", proto,
		"--dport", strconv.Itoa(port),
		"-m", "comment", "--comment", commentTag,
		"-j", "ACCEPT",
	).Run()
}

func deleteRule(port int, proto string) error {
	return exec.Command("iptables",
		"-D", "INPUT",
		"-p", proto,
		"--dport", strconv.Itoa(port),
		"-m", "comment", "--comment", commentTag,
		"-j", "ACCEPT",
	).Run()
}
