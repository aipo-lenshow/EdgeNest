// Package portowner finds which program is listening on a TCP port, so the
// panel can say "port 443 belongs to Caddy" and offer that tool's config.
// Linux only (it reads /proc); elsewhere it reports nothing.
package portowner

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Known reverse proxies / web servers, by the family the panel has a config
// template for. Anything else is reported by its process name as "other".
const (
	ToolCaddy  = "caddy"
	ToolNginx  = "nginx"
	ToolLucky  = "lucky"
	ToolApache = "apache"
	ToolOther  = "other"
)

// Owner describes the listener on a port.
type Owner struct {
	Port    int    `json:"port"`
	Process string `json:"process"` // process name as the kernel reports it
	Tool    string `json:"tool"`    // one of the Tool* families
}

// Find returns who listens on TCP port (IPv4 or IPv6), or nil when the port
// is free or the owner can't be determined.
func Find(port int) *Owner {
	return find("/proc", port)
}

func find(proc string, port int) *Owner {
	inodes := map[string]bool{}
	for _, f := range []string{"net/tcp", "net/tcp6"} {
		for _, ino := range listeningInodes(filepath.Join(proc, f), port) {
			inodes[ino] = true
		}
	}
	if len(inodes) == 0 {
		return nil
	}
	pids, _ := os.ReadDir(proc)
	for _, p := range pids {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		fdDir := filepath.Join(proc, p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			if inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] {
				name := processName(filepath.Join(proc, p.Name()))
				return &Owner{Port: port, Process: name, Tool: Classify(name)}
			}
		}
	}
	return &Owner{Port: port, Tool: ToolOther}
}

// listeningInodes parses a /proc/net/tcp{,6} table for sockets in LISTEN
// state (0A) on port and returns their inode numbers.
func listeningInodes(path string, port int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	want := fmt.Sprintf(":%04X", port)
	var out []string
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[3] != "0A" || !strings.HasSuffix(fields[1], want) {
			continue
		}
		out = append(out, fields[9])
	}
	return out
}

func processName(pidDir string) string {
	if b, err := os.ReadFile(filepath.Join(pidDir, "comm")); err == nil {
		if n := strings.TrimSpace(string(b)); n != "" {
			return n
		}
	}
	if exe, err := os.Readlink(filepath.Join(pidDir, "exe")); err == nil {
		return filepath.Base(exe)
	}
	return ""
}

// Classify maps a process name onto a config-template family.
func Classify(process string) string {
	n := strings.ToLower(process)
	switch {
	case strings.Contains(n, "caddy"):
		return ToolCaddy
	case strings.Contains(n, "nginx"), strings.Contains(n, "openresty"), strings.Contains(n, "tengine"):
		return ToolNginx
	case strings.Contains(n, "lucky"):
		return ToolLucky
	case strings.Contains(n, "apache"), strings.Contains(n, "httpd"):
		return ToolApache
	}
	return ToolOther
}
