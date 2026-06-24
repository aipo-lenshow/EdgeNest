// Package digest renders the EdgeNest status+traffic summary as one localized
// plain-text block. It is the SINGLE source of the summary: both the scheduled
// daily push (notifyrunner) and the on-demand bot command (/summary) call
// Build, so the two can never drift — the earlier bug where the daily push was
// hardcoded English while the bot was localized is structurally prevented here.
package digest

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/alerts"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/system"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/aipo-lenshow/EdgeNest/internal/core/nodeapi"
)

// AppVersion is the EdgeNest panel version, injected once at startup (main.go)
// so the digest header can show it without importing the api package.
var AppVersion string

// Build renders the summary in lang ("zh"/"en"), timestamped in loc. Beyond the
// server status it reports today's / month-to-date traffic, the user count, and
// a "needs attention" section (quota / expiry / cert warnings) from the alerts
// package. Returns an error only when the node heartbeat is unreachable.
func Build(ctx context.Context, st *store.Store, node nodeapi.NodeClient, nodeID, lang string, loc *time.Location) (string, error) {
	now := time.Now().In(loc)

	health, err := node.Heartbeat(ctx, nodeID)
	if err != nil {
		return "", err
	}
	eng, _ := node.EngineStatus(ctx, nodeID)

	var b strings.Builder
	ver := ""
	if AppVersion != "" {
		ver = " v" + AppVersion
	}
	// Version sits right after the project name: "📊 EdgeNest v0.23.0620 每日概览".
	fmt.Fprintf(&b, "📊 EdgeNest%s %s · %s\n", ver, tr(lang, "每日概览", "daily summary"),
		now.Format("2006-01-02 15:04 MST"))
	b.WriteString("────────────────────\n")

	// Engine + uptime.
	fmt.Fprintf(&b, "%s: %s (%s) · %s %s\n",
		tr(lang, "引擎", "Engine"),
		boolMark(eng.Running, tr(lang, "运行中", "running"), tr(lang, "已停止", "stopped")),
		nonEmpty(eng.Version, "?"), tr(lang, "已运行", "uptime"), formatUptime(eng.Uptime))

	// Resources. CPU% sampled instantaneously (load avg as fallback); BBR is read
	// on the control side since the node heartbeat hardcodes "unknown".
	bbr := system.ReadBBRState()
	bbrStr := nonEmpty(bbr.CongestionControl, "?")
	if bbr.Enabled {
		bbrStr += " ✓"
	}
	if pct := system.ReadCPUPercent(); pct >= 0 {
		fmt.Fprintf(&b, "CPU %.0f%% · %s %.0f%% · %s %.0f%% · BBR %s\n",
			pct, tr(lang, "内存", "mem"), health.Mem*100, tr(lang, "磁盘", "disk"), health.Disk*100, bbrStr)
	} else {
		fmt.Fprintf(&b, "%s %.2f · %s %.0f%% · %s %.0f%% · BBR %s\n",
			tr(lang, "负载(1m)", "load(1m)"), health.CPU, tr(lang, "内存", "mem"), health.Mem*100,
			tr(lang, "磁盘", "disk"), health.Disk*100, bbrStr)
	}
	// Engine marks are contextual: an engine with no inbounds shows "未用/n-a"
	// (it's idle by design, not a fault), "✓" when it has inbounds and is up,
	// "✗" only when it has inbounds but is down (a real problem). This avoids the
	// alarming "xray ✗" when the operator simply has no xray inbounds.
	sbN, xrN := EngineInboundCounts(st, nodeID)
	fmt.Fprintf(&b, "sing-box %s · xray %s\n",
		EngineMark(health.SingboxRunning, sbN, lang), EngineMark(health.XrayRunning, xrN, lang))

	// Public address(es): list every detected address so a dual-stack node shows
	// both its v4 and v6, not just one. Falls back to the heartbeat IP.
	if addrs := publicIPs(health.PublicIP); len(addrs) > 0 {
		label := tr(lang, "公网", "public")
		if country := nonEmpty(health.Country, ""); country != "" {
			label += " (" + country + ")"
		}
		fmt.Fprintf(&b, "%s: %s\n", label, strings.Join(addrs, " · "))
	}

	// Traffic (today + month-to-date) from the daily bucket table.
	today := now.Format("2006-01-02")
	monthStart := now.Format("2006-01") + "-01"
	tUp, tDown, _ := st.ServerTrafficSince(today)
	mUp, mDown, _ := st.ServerTrafficSince(monthStart)
	fmt.Fprintf(&b, "\n📈 %s ↑%s ↓%s · %s %s\n",
		tr(lang, "今日", "today"), fmtBytes(tUp), fmtBytes(tDown),
		tr(lang, "当月", "month"), fmtBytes(mUp+mDown))

	// Users.
	det := alerts.NewDetector(st, nodeID)
	total, enabled := det.UserCounts()
	fmt.Fprintf(&b, "👥 %s %d · %s %d\n",
		tr(lang, "共", "total"), total, tr(lang, "启用", "enabled"), enabled)

	// Needs-attention (quota ≥90% / user expiring ≤7d / cert expiring ≤14d).
	if items := det.Attention(now, alerts.Default()); len(items) > 0 {
		fmt.Fprintf(&b, "\n⚠️ %s\n", tr(lang, "需关注", "Needs attention"))
		for _, a := range items {
			b.WriteString(alerts.Line(a, lang) + "\n")
		}
	}

	return strings.TrimRight(b.String(), "\n"), nil
}

// publicIPs returns every detected public address (all v4 then all v6) so a
// dual-stack node reports both, falling back to the heartbeat IP when capability
// detection found nothing. Mirrors the bot /status address list.
func publicIPs(fallback string) []string {
	cap := core.ReadNodeCapability(core.DefaultCapabilityPath)
	var out []string
	out = append(out, cap.IPv4Addrs...)
	if len(cap.IPv4Addrs) == 0 && cap.IPv4Addr != "" {
		out = append(out, cap.IPv4Addr)
	}
	out = append(out, cap.IPv6Addrs...)
	if len(cap.IPv6Addrs) == 0 && cap.IPv6Addr != "" {
		out = append(out, cap.IPv6Addr)
	}
	if len(out) == 0 && fallback != "" {
		out = append(out, fallback)
	}
	return out
}

func tr(lang, zh, en string) string {
	if lang == "zh" {
		return zh
	}
	return en
}

// EngineInboundCounts returns how many inbounds each engine serves (xray vs the
// rest = sing-box), so callers can tell "idle by design" from "down". Shared by
// the digest and the bot /status so the two never disagree.
func EngineInboundCounts(st *store.Store, nodeID string) (singbox, xray int) {
	nid, err := strconv.ParseUint(nodeID, 10, 64)
	if err != nil {
		return 0, 0
	}
	ibs, err := st.ListInbounds(uint(nid))
	if err != nil {
		return 0, 0
	}
	for _, ib := range ibs {
		if ib.Engine == "xray" {
			xray++
		} else {
			singbox++
		}
	}
	return singbox, xray
}

// EngineMark renders an engine's status: "未用/n-a" when it has no inbounds (idle
// by design), "✓" running, "✗" down despite having inbounds.
func EngineMark(running bool, inboundCount int, lang string) string {
	if inboundCount == 0 {
		return tr(lang, "未用", "n/a")
	}
	if running {
		return "✓"
	}
	return "✗"
}

func boolMark(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func formatUptime(sec int64) string {
	if sec <= 0 {
		return "0s"
	}
	d := time.Duration(sec) * time.Second
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd%dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh%dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", sec)
}

func fmtBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	i := -1
	for f >= u && i < len(units)-1 {
		f /= u
		i++
	}
	return fmt.Sprintf("%.2f %s", f, units[i])
}
