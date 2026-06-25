package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/updatecheck"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/gin-gonic/gin"
)

// upgradeScriptName / upgradeStatusName live in the data dir, installed there by
// install.sh. The script is the single self-upgrade implementation shared by the
// panel, the CLI menu, and the Telegram bot.
const (
	upgradeScriptName = "upgrade.sh"
	upgradeStatusName = "upgrade-status.json"
)

// Upgrade kicks off a self-upgrade to the latest cached stable release. It must
// outlive the panel restart the upgrade triggers, so it launches the script as a
// transient systemd unit (its own cgroup) rather than a child of this process —
// a plain child would be killed when `systemctl restart edgenest` runs.
//
// Admin-only by virtue of the authed route group (single-admin model). Returns
// immediately; callers poll GET /upgrade/status for progress.
func (h *Handler) Upgrade(c *gin.Context) {
	var req struct {
		Tag string `json:"tag"`
	}
	_ = c.ShouldBindJSON(&req)

	target := strings.TrimSpace(strings.TrimPrefix(req.Tag, "v"))
	if target == "" {
		latest, available := updatecheck.StatusLive(h.store, Version)
		if !available {
			core.Fail(c, http.StatusBadRequest, "UP_TO_DATE", "no newer stable version available")
			return
		}
		target = latest
	}
	if !updatecheck.Newer(Version, target) {
		core.Fail(c, http.StatusBadRequest, "UP_TO_DATE", "target version is not newer than the running version")
		return
	}

	script := filepath.Join(h.dataDir, upgradeScriptName)
	if _, err := os.Stat(script); err != nil {
		core.Fail(c, http.StatusServiceUnavailable, "NO_UPGRADE_SCRIPT",
			"upgrade script missing; reinstall from the latest release to enable self-upgrade")
		return
	}

	// Transient unit, garbage-collected after exit. --unit gives a stable name so
	// a second concurrent request fails fast ("unit already exists") instead of
	// racing the in-flight upgrade (the script also holds a flock).
	cmd := exec.Command("systemd-run", "--quiet", "--unit=edgenest-upgrade", "--collect",
		"bash", script, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "already exists") {
			core.Fail(c, http.StatusConflict, "UPGRADE_RUNNING", "an upgrade is already in progress")
			return
		}
		core.Fail(c, http.StatusInternalServerError, "UPGRADE_SPAWN_FAILED", msg)
		return
	}

	h.auditLog(c, "system.upgrade.start", "system", map[string]string{"from": Version, "to": target})
	core.OK(c, gin.H{"started": true, "from": Version, "to": target})
}

// VersionCheck forces a fresh GitHub latest-release lookup and returns the
// result, for the panel's "check now" button. The passive /api/health hint only
// refreshes every hour, so right after a release it stays stale (a panel that
// started before the release was published would otherwise never surface the
// update until the next periodic check). StatusLive fetches live and falls back
// to the cache on a network/rate-limit failure; it also refreshes the cache, so
// the sidebar badge driven by /api/health updates too.
func (h *Handler) VersionCheck(c *gin.Context) {
	// auto=1 marks the panel's automatic on-load check; it respects the operator's
	// "update check" opt-out, so a disabled install isn't probed behind their back.
	// The manual "check now" button omits auto, so an explicit click still checks
	// live even when the periodic check is turned off.
	if c.Query("auto") == "1" {
		if v, _ := h.store.GetSetting(settingUpdateCheckEnabled); v == "false" {
			core.OK(c, gin.H{"version": Version, "latest_version": "", "update_available": false})
			return
		}
	}
	latest, available := updatecheck.StatusLive(h.store, Version)
	core.OK(c, gin.H{
		"version":          Version,
		"latest_version":   latest,
		"update_available": available,
	})
}

// UpgradeStatus returns the latest upgrade-status.json the script writes, so the
// panel can show progress and the final result (including after the restart).
func (h *Handler) UpgradeStatus(c *gin.Context) {
	b, err := os.ReadFile(filepath.Join(h.dataDir, upgradeStatusName))
	if err != nil {
		core.OK(c, gin.H{"state": "idle"})
		return
	}
	var status map[string]any
	if err := json.Unmarshal(b, &status); err != nil {
		core.OK(c, gin.H{"state": "idle"})
		return
	}
	core.OK(c, status)
}
