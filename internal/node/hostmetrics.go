package node

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// hostMetrics is the cheaply-collectable host state we report from the node
// side. Read from /proc on Linux; degrades to zeros elsewhere (dev laptops).
type hostMetrics struct {
	CPULoad1 float64 // 1-minute load average (proxy for CPU pressure)
	MemUsed  float64 // 0..1 fraction of RAM in use
	DiskUsed float64 // 0..1 fraction of root filesystem in use
	Notes    string  // explanatory text when metrics aren't available
}

// readHostMetrics returns the snapshot. Errors are folded into Notes so we
// never block Health() reporting.
func readHostMetrics() hostMetrics {
	if runtime.GOOS != "linux" {
		return hostMetrics{Notes: "host metrics are Linux-only; host=" + runtime.GOOS}
	}
	m := hostMetrics{}
	if v, err := readLoadAvg1(); err == nil {
		m.CPULoad1 = v
	} else {
		m.Notes = appendNote(m.Notes, "loadavg: "+err.Error())
	}
	if used, err := readMemUsedFraction(); err == nil {
		m.MemUsed = used
	} else {
		m.Notes = appendNote(m.Notes, "meminfo: "+err.Error())
	}
	if used, err := readDiskUsedFraction("/"); err == nil {
		m.DiskUsed = used
	} else {
		m.Notes = appendNote(m.Notes, "statfs: "+err.Error())
	}
	return m
}

func readLoadAvg1() (float64, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0, errEmptyFile
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readMemUsedFraction() (float64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	var total, available int64
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			total = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			available = parseMeminfoKB(line)
		}
	}
	if total == 0 {
		return 0, errEmptyFile
	}
	used := total - available
	if used < 0 {
		used = 0
	}
	return float64(used) / float64(total), nil
}

func parseMeminfoKB(line string) int64 {
	// "MemTotal:        16384000 kB"
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.ParseInt(fields[1], 10, 64)
	return n
}

func readDiskUsedFraction(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	total := st.Blocks
	free := st.Bavail
	if total == 0 {
		return 0, errEmptyFile
	}
	used := total - free
	return float64(used) / float64(total), nil
}

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// errEmptyFile signals "the procfs entry was unexpectedly empty" — only
// happens when /proc is mounted in a weird container; treat like a normal
// read error so callers fold it into Notes.
type errEmptyFileT struct{}

func (errEmptyFileT) Error() string { return "empty or malformed procfs entry" }

var errEmptyFile = errEmptyFileT{}
