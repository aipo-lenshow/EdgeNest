package system

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ReadCPUPercent returns instantaneous CPU utilisation (0-100) by sampling
// /proc/stat's aggregate "cpu" line twice over a short window and diffing the
// busy vs total jiffies. Returns -1 when unavailable (non-Linux / parse fail)
// so callers can fall back to load average.
//
// The window is ~900ms (not 250ms): on a near-idle multi-core VPS, 250ms can
// catch literally 0 busy jiffies and report a flat 0% even when the box is
// genuinely at ~1% — too short a window reads as "dead". ~900ms gives a
// representative reading and is still fine for an on-demand bot/digest reply.
func ReadCPUPercent() float64 {
	t1, b1, ok1 := readCPUJiffies()
	if !ok1 {
		return -1
	}
	time.Sleep(900 * time.Millisecond)
	t2, b2, ok2 := readCPUJiffies()
	if !ok2 || t2 <= t1 {
		return -1
	}
	pct := float64(b2-b1) / float64(t2-t1) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// readCPUJiffies parses the aggregate "cpu" line of /proc/stat, returning total
// and busy (total - idle - iowait) jiffies.
func readCPUJiffies() (total, busy uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := ""
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "cpu ") {
			line = l
			break
		}
	}
	if line == "" {
		return 0, 0, false
	}
	fields := strings.Fields(line)[1:] // drop "cpu"
	var vals []uint64
	for _, f := range fields {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			break
		}
		vals = append(vals, v)
	}
	if len(vals) < 5 {
		return 0, 0, false
	}
	for _, v := range vals {
		total += v
	}
	// idle = vals[3], iowait = vals[4].
	idle := vals[3] + vals[4]
	return total, total - idle, true
}

// CPUInfo describes the host CPU layout in a way that distinguishes vCPU
// (= logical processors visible to the kernel) from physical cores and
// threads-per-core, which matters in virtualised environments where naïve
// "core count" (nproc / runtime.NumCPU) reports the hyper-threaded vCPU total
// and misleads users into thinking they have more compute than they do.
type CPUInfo struct {
	VCPU           int    `json:"vcpu"`
	PhysicalCores  int    `json:"physical_cores"`
	ThreadsPerCore int    `json:"threads_per_core"`
	Model          string `json:"model"`
}

// ReadCPUInfo derives the layout from /proc/cpuinfo by counting unique
// (physical id, core id) tuples. Falls back to {vCPU, 1 thread/core} when
// /proc/cpuinfo is unavailable (non-Linux) or doesn't expose those fields
// (some VMs).
func ReadCPUInfo() CPUInfo {
	out := CPUInfo{VCPU: runtime.NumCPU()}
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		out.PhysicalCores = out.VCPU
		out.ThreadsPerCore = 1
		return out
	}
	type key struct{ phys, core string }
	seen := map[key]struct{}{}
	var curPhys, curCore string
	flush := func() {
		if curPhys == "" && curCore == "" {
			return
		}
		seen[key{curPhys, curCore}] = struct{}{}
		curPhys, curCore = "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "physical id":
			curPhys = v
		case "core id":
			curCore = v
		case "model name":
			if out.Model == "" {
				out.Model = v
			}
		}
	}
	flush()
	out.PhysicalCores = len(seen)
	if out.PhysicalCores > 0 && out.VCPU > 0 {
		out.ThreadsPerCore = out.VCPU / out.PhysicalCores
		if out.ThreadsPerCore < 1 {
			out.ThreadsPerCore = 1
		}
	} else {
		out.PhysicalCores = out.VCPU
		out.ThreadsPerCore = 1
	}
	return out
}
