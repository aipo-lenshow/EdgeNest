package botrunner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aipo-lenshow/EdgeNest/internal/control/config"
	"github.com/aipo-lenshow/EdgeNest/internal/control/digest"
	"github.com/aipo-lenshow/EdgeNest/internal/control/notify"
	"github.com/aipo-lenshow/EdgeNest/internal/control/updatecheck"
)

// cmdUpgrade kicks off a self-upgrade to the latest cached stable release. Like
// the panel, the bot is part of the edgenest service, so the upgrade must run in
// its own transient systemd unit (outside the cgroup) to survive the restart it
// triggers. Admin-only by virtue of the loop's allowlist gate. Reports the final
// result after restart via announceUpgradeIfPending.
// cmdUpgradeCheck is the inline-button entry: it checks live for a newer stable
// release and, if one exists, asks the admin to confirm before starting (a tap
// shouldn't trigger a panel-restarting upgrade by accident). Confirming sends
// cmd:doupgrade → cmdUpgrade; the typed /upgrade command remains a direct start.
func (r *Runner) cmdUpgradeCheck(ctx context.Context, token string, chatID int64, lang string) {
	latest, available := updatecheck.StatusLive(r.store, digest.AppVersion)
	if !available {
		r.reply(ctx, token, chatID, tr(lang,
			"✅ 已是最新稳定版。", "✅ Already on the latest stable version."))
		return
	}
	html := tr(lang,
		"🆙 有新版 <b>v"+esc(latest)+"</b> 可升级。升级期间面板会短暂重启，失败会自动回滚。",
		"🆙 <b>v"+esc(latest)+"</b> is available. The panel restarts briefly during the upgrade; it auto-rolls back on failure.")
	rows := [][][2]string{{
		{tr(lang, "✅ 确认升级", "✅ Confirm upgrade"), "cmd:doupgrade"},
		{tr(lang, "✖ 取消", "✖ Cancel"), "act:cancel"},
	}}
	_ = notify.SendTelegramKeyboard(ctx, token, strconv.FormatInt(chatID, 10), html, rows)
}

func (r *Runner) cmdUpgrade(ctx context.Context, token string, chatID int64, lang string) {
	latest, available := updatecheck.StatusLive(r.store, digest.AppVersion)
	if !available {
		r.reply(ctx, token, chatID, tr(lang,
			"✅ 已是最新稳定版。", "✅ Already on the latest stable version."))
		return
	}
	script := filepath.Join(config.Default().DataDir, "upgrade.sh")
	if _, err := os.Stat(script); err != nil {
		r.reply(ctx, token, chatID, tr(lang,
			"⚠️ 未找到升级脚本，请用最新版重装以启用自升级。",
			"⚠️ Upgrade script missing; reinstall from the latest release to enable self-upgrade."))
		return
	}
	cmd := exec.Command("systemd-run", "--quiet", "--unit=edgenest-upgrade", "--collect",
		"bash", script, latest)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "already exists") {
			r.reply(ctx, token, chatID, tr(lang, "⏳ 升级已在进行中。", "⏳ An upgrade is already in progress."))
			return
		}
		r.reply(ctx, token, chatID, tr(lang, "❌ 升级启动失败：", "❌ Failed to start upgrade: ")+esc(msg))
		return
	}
	r.reply(ctx, token, chatID, tr(lang,
		"⏳ 已开始升级到 v"+esc(latest)+"。面板会重启，完成后我会再通知你。",
		"⏳ Upgrade to v"+esc(latest)+" started. The panel will restart; I'll message you when it's done."))
}

// announceUpgradeIfPending posts the final upgrade result to every admin chat
// once, after the panel (and thus the bot) restarts following an upgrade. The
// upgrade script sets upgrade_notify_pending=1 and writes upgrade-status.json.
func (r *Runner) announceUpgradeIfPending(ctx context.Context, token string) {
	if v, _ := r.store.GetSetting("upgrade_notify_pending"); v != "1" {
		return
	}
	// Clear first so a send failure can't loop on every poll.
	_ = r.store.SetSetting("upgrade_notify_pending", "")
	if token == "" {
		return
	}
	lang := r.lang()
	msg := tr(lang, "✅ 升级流程已结束。", "✅ Upgrade finished.")
	if b, err := os.ReadFile(filepath.Join(config.Default().DataDir, "upgrade-status.json")); err == nil {
		var s struct {
			State     string `json:"state"`
			MessageZH string `json:"message_zh"`
			MessageEN string `json:"message_en"`
		}
		if json.Unmarshal(b, &s) == nil {
			m := s.MessageEN
			if lang == "zh" && s.MessageZH != "" {
				m = s.MessageZH
			}
			if m != "" {
				icon := "✅ "
				if s.State != "success" {
					icon = "⚠️ "
				}
				msg = icon + esc(m)
			}
		}
	}
	for chatID := range r.allowlist() {
		_ = notify.SendTelegramHTML(ctx, token, chatID, msg)
	}
}
