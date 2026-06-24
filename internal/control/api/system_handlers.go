package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/system"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// BBRStatus reports current TCP congestion control + qdisc.
//
// GET /api/v1/system/bbr/status
func (h *Handler) BBRStatus(c *gin.Context) {
	core.OK(c, system.ReadBBRState())
}

// BBREnable turns on BBR + fq.
//
// POST /api/v1/system/bbr/enable
func (h *Handler) BBREnable(c *gin.Context) {
	if runtime.GOOS != "linux" {
		core.Fail(c, http.StatusNotImplemented, "UNSUPPORTED_OS",
			"BBR toggling is Linux-only; current host is "+runtime.GOOS)
		return
	}
	if err := system.EnableBBR(); err != nil {
		h.auditLog(c, "system.bbr.enable", "bbr", map[string]string{"error": err.Error()})
		core.Fail(c, http.StatusInternalServerError, "BBR_ENABLE_FAILED", err.Error())
		return
	}
	h.auditLog(c, "system.bbr.enable", "bbr", nil)
	core.OK(c, system.ReadBBRState())
}

// BBRDisable reverts to kernel defaults.
//
// POST /api/v1/system/bbr/disable
func (h *Handler) BBRDisable(c *gin.Context) {
	if runtime.GOOS != "linux" {
		core.Fail(c, http.StatusNotImplemented, "UNSUPPORTED_OS",
			"BBR toggling is Linux-only; current host is "+runtime.GOOS)
		return
	}
	if err := system.DisableBBR(); err != nil {
		h.auditLog(c, "system.bbr.disable", "bbr", map[string]string{"error": err.Error()})
		core.Fail(c, http.StatusInternalServerError, "BBR_DISABLE_FAILED", err.Error())
		return
	}
	h.auditLog(c, "system.bbr.disable", "bbr", nil)
	core.OK(c, system.ReadBBRState())
}

// SystemInfo reports host facts the System Info / Cloud Firewall pages render:
// OS, kernel, arch, CPU layout (vCPU vs physical cores vs threads-per-core),
// memory total, BBR state, and the protocol ports currently exposed by enabled
// inbounds (so the cloud-firewall guide can name the exact rules to open).
//
// GET /api/v1/system/info
func (h *Handler) SystemInfo(c *gin.Context) {
	osID, osName := readOSRelease()
	cpu := system.ReadCPUInfo()
	info := gin.H{
		"os":              runtime.GOOS,
		"os_id":           osID,
		"os_name":         osName,
		"arch":            runtime.GOARCH,
		"kernel":          readKernel(),
		"cpu":             cpu,
		"cpu_cores":       cpu.VCPU, // legacy alias for older clients
		"memory_total_kb": readMemTotalKB(),
		"hostname":        readHostname(),
		"bbr":             system.ReadBBRState(),
		"panel_port":      h.panelPort,
		// server_tz is the host's IANA timezone (e.g. "Asia/Shanghai"), used as
		// the default for the display-timezone picker. display_tz (the operator's
		// chosen presentation TZ; empty = follow server) drives front-end
		// timestamp rendering + the bot/notify clock.
		"server_tz":  serverTimezone(),
		"display_tz": func() string { v, _ := h.store.GetSetting("display_tz"); return v }(),
		// network_capability lets the Advanced page conditionally render the
		// IP-version toggle: on a v4-only node, "prefer IPv6" is not a real
		// option (no v6 egress exists), so the front-end disables the Select
		// and shows a hint. Defaults to dual-stack when network.json is missing
		// (brand-new install before detect ran).
		"network_capability": core.ReadNodeCapability(core.DefaultCapabilityPath),
	}

	// Aggregate enabled inbound ports for the cloud firewall guide.
	type portRow struct {
		Proto string `json:"proto"`
		Port  int    `json:"port"`
		Tag   string `json:"tag"`
	}
	rows := []portRow{}
	if ins, err := h.store.ListInbounds(h.parseLocalNodeID()); err == nil {
		for _, ib := range ins {
			if !ib.Enabled {
				continue
			}
			proto := inboundTransportProto(ib.Type)
			rows = append(rows, portRow{Proto: proto, Port: ib.Port, Tag: ib.Tag})
		}
	}
	info["inbound_ports"] = rows
	core.OK(c, info)
}

// inboundTransportProto returns the L4 transport ("tcp" / "udp") for an
// inbound type — used by the cloud-firewall guide so users know whether to
// add a TCP or UDP allow rule. UDP-only protocols: hysteria2, tuic.
func inboundTransportProto(t string) string {
	switch strings.ToLower(t) {
	case "hysteria2", "tuic":
		return "udp"
	}
	return "tcp"
}

func readOSRelease() (id, name string) {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			id = v
		case "PRETTY_NAME":
			name = v
		}
	}
	if id == "" {
		id = "unknown"
	}
	return id, name
}

func readKernel() string {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func readMemTotalKB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		n, _ := strconv.ParseInt(fields[1], 10, 64)
		return n
	}
	return 0
}

func readHostname() string {
	h, _ := os.Hostname()
	return h
}

// serverTimezone returns the host's IANA timezone name (e.g. "Asia/Shanghai").
// Tries TZ env → /etc/timezone (Debian/Ubuntu) → the /etc/localtime symlink
// target → "UTC". Used only as the default for the display-TZ picker; canonical
// storage is always unix epoch, so this never affects stored data.
func serverTimezone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return tz
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(b)); tz != "" {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "/zoneinfo/"); i >= 0 {
			if tz := target[i+len("/zoneinfo/"):]; tz != "" {
				return tz
			}
		}
	}
	return "UTC"
}

// SystemPortsReserved gives the wizard front-end everything it needs to
// render port pickers without ever offering a port the back-end would refuse.
// Reserved = SSH + DNS + panel; cf_https_whitelist = the only ports CDN-eligible
// inbounds can ride; occupied = ports already claimed by other inbounds on
// this node (so the same physical port is not offered twice).
//
// socks_taken: kept for back-compat with older front-ends lifted
// the SOCKS5 single-instance restriction (each protocol can now exist on
// multiple ports per IP, and on multiple IPs per family if you split
// protocols across IPs, see [[edgenest-multi-ip-constraint]]). The current
// front-end ignores this field; we return false unconditionally so any
// stale cached UI also stops disabling the SOCKS5 checkbox.
//
// GET /api/v1/system/ports/reserved
func (h *Handler) SystemPortsReserved(c *gin.Context) {
	occupied := []int{}
	occupiedByFamily := map[string][]int{"v4": {}, "v6": {}}
	if ins, err := h.store.ListInbounds(h.parseLocalNodeID()); err == nil {
		for _, ib := range ins {
			occupied = append(occupied, ib.Port)
			// Classify by Listen IP family — multi-IP design lets
			// v4 SOCKS5:1080 and v6 SOCKS5:1080 coexist on different sockets,
			// so the wizard's "next free port" picker must filter by family.
			fam := "v4"
			if ip := net.ParseIP(ib.Listen); ip != nil && ip.To4() == nil {
				fam = "v6"
			}
			occupiedByFamily[fam] = append(occupiedByFamily[fam], ib.Port)
		}
	}
	core.OK(c, gin.H{
		"reserved":           system.ReservedPorts(h.panelPort),
		"panel_port":         h.panelPort,
		"cf_https_whitelist": system.CFHTTPSWhitelist,
		"occupied":           occupied,
		"occupied_by_family": occupiedByFamily,
		"socks_taken":        false,
		"min_allowed":        system.MinAllowedPort,
		"max_allowed":        system.MaxAllowedPort,
	})
}

// SystemXrayStatus reports whether xray-core is installed on the host plus
// the pinned version the panel would install if the operator clicks the
// button. Cheap enough to call on a 30s polling interval from the dashboard.
//
// GET /api/v1/system/xray/status
func (h *Handler) SystemXrayStatus(c *gin.Context) {
	core.OK(c, system.ReadXrayStatus())
}

// SystemXrayInstall fetches the pinned xray-core release, lays the binary +
// geo files into the standard locations, and returns the resulting status.
// Refuses if xray is already installed at the pinned version so a stuck
// button press cannot re-download repeatedly.
//
// POST /api/v1/system/xray/install
func (h *Handler) SystemXrayInstall(c *gin.Context) {
	cur := system.ReadXrayStatus()
	if cur.Installed && cur.Version == system.PinnedXrayVersion {
		core.OK(c, cur) // idempotent — already at the pinned version
		return
	}
	// Allow up to 5 minutes; the binary is ~25 MB and the operator's link
	// to GitHub may be slow.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*60*time.Second)
	defer cancel()
	res, err := system.InstallXray(ctx)
	if err != nil {
		h.auditLog(c, "system.xray.install", "xray",
			map[string]string{"error": err.Error()})
		core.Fail(c, http.StatusInternalServerError, "XRAY_INSTALL_FAILED", err.Error())
		return
	}
	h.auditLog(c, "system.xray.install", "xray",
		map[string]string{"version": res.Version})
	core.OK(c, res)
}

// FirewallPreview returns the DesiredConfig.Firewall the orchestrator WOULD
// push right now. Useful before clicking "Apply" — operators can verify SSH +
// panel are in the allow-list before any change touches the system.
//
// GET /api/v1/firewall/preview
func (h *Handler) FirewallPreview(c *gin.Context) {
	if h.orch == nil {
		core.Fail(c, http.StatusServiceUnavailable, "ORCH_DISABLED", "orchestrator not configured")
		return
	}
	cfg, err := h.orch.BuildDesired(h.parseLocalNodeID())
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "BUILD_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{
		"allow_ports": cfg.Firewall.AllowPorts,
	})
}
