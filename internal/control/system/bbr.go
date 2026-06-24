// Package system holds host-OS helpers the control plane uses (BBR toggling,
// firewall introspection, free-port checks). These wrap shell commands and
// only do anything meaningful on Linux — on macOS/Windows they degrade to
// "unsupported" without errors so dev work on a laptop doesn't blow up.
package system

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// BBRState is the current TCP congestion control / qdisc combo.
type BBRState struct {
	Supported          bool   `json:"supported"`
	CongestionControl  string `json:"congestion_control"`
	DefaultQdisc       string `json:"default_qdisc"`
	Enabled            bool   `json:"enabled"` // cc==bbr && qdisc==fq (or fq_codel)
	OS                 string `json:"os"`
	Notes              string `json:"notes,omitempty"`
}

// ReadBBRState introspects current sysctl values.
func ReadBBRState() BBRState {
	st := BBRState{OS: runtime.GOOS, Supported: runtime.GOOS == "linux"}
	if !st.Supported {
		st.Notes = "BBR toggling is Linux-only; this host is " + runtime.GOOS
		return st
	}
	st.CongestionControl = strings.TrimSpace(readFileOr("/proc/sys/net/ipv4/tcp_congestion_control", ""))
	st.DefaultQdisc = strings.TrimSpace(readFileOr("/proc/sys/net/core/default_qdisc", ""))
	st.Enabled = st.CongestionControl == "bbr" && (st.DefaultQdisc == "fq" || st.DefaultQdisc == "fq_codel")
	return st
}

// EnableBBR runs `sysctl -w` for the two values and persists them to
// /etc/sysctl.d/99-edgenest-bbr.conf so they survive reboot. Requires root.
func EnableBBR() error {
	if runtime.GOOS != "linux" {
		return errors.New("BBR toggling is Linux-only")
	}
	if err := runSysctl("net.core.default_qdisc=fq"); err != nil {
		return fmt.Errorf("set default_qdisc=fq: %w", err)
	}
	if err := runSysctl("net.ipv4.tcp_congestion_control=bbr"); err != nil {
		return fmt.Errorf("set tcp_congestion_control=bbr: %w", err)
	}
	persist := `net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
`
	if err := os.WriteFile("/etc/sysctl.d/99-edgenest-bbr.conf", []byte(persist), 0o644); err != nil {
		// runtime sysctl succeeded; persistence is best-effort.
		return fmt.Errorf("persist sysctl.d: %w (BBR enabled for this boot only)", err)
	}
	return nil
}

// DisableBBR reverts to the kernel defaults (cubic + pfifo_fast/fq_codel).
// We remove our sysctl.d drop-in; existing /etc/sysctl.conf is untouched.
func DisableBBR() error {
	if runtime.GOOS != "linux" {
		return errors.New("BBR toggling is Linux-only")
	}
	if err := runSysctl("net.ipv4.tcp_congestion_control=cubic"); err != nil {
		return fmt.Errorf("set tcp_congestion_control=cubic: %w", err)
	}
	if err := runSysctl("net.core.default_qdisc=fq_codel"); err != nil {
		return fmt.Errorf("set default_qdisc=fq_codel: %w", err)
	}
	_ = os.Remove("/etc/sysctl.d/99-edgenest-bbr.conf") // best-effort
	return nil
}

func runSysctl(arg string) error {
	cmd := exec.Command("sysctl", "-w", arg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func readFileOr(path, def string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	return string(b)
}
