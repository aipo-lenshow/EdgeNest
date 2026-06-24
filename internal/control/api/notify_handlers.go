package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/notify"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

type updateNotifyRequest struct {
	Enabled        *bool     `json:"enabled,omitempty"`
	TelegramToken  *string   `json:"telegram_token,omitempty"` // empty string clears
	TelegramChatID *string   `json:"telegram_chat_id,omitempty"`
	DailyHour      *int      `json:"daily_hour,omitempty"`   // 0-23
	DailyMinute    *int      `json:"daily_minute,omitempty"` // 0-59
	BotEnabled     *bool     `json:"bot_enabled,omitempty"`
	AlertsEnabled  *bool     `json:"alerts_enabled,omitempty"`
	UpdateCheck    *bool     `json:"update_check_enabled,omitempty"`
	AdminChatIDs   *[]string `json:"bot_admin_chat_ids,omitempty"`
}

// UpdateNotify updates notify bot settings. Only the keys present in the JSON
// payload are touched (nil pointer = leave alone).
//
// PUT /api/v1/settings/notify
func (h *Handler) UpdateNotify(c *gin.Context) {
	var req updateNotifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	set := func(k string, v *string) {
		if v == nil {
			return
		}
		_ = h.store.SetSetting(k, *v)
	}
	boolStr := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}
	if req.Enabled != nil {
		_ = h.store.SetSetting(settingNotifyEnabled, boolStr(*req.Enabled))
	}
	if req.BotEnabled != nil {
		_ = h.store.SetSetting(settingBotEnabled, boolStr(*req.BotEnabled))
	}
	if req.AlertsEnabled != nil {
		_ = h.store.SetSetting(settingAlertsEnabled, boolStr(*req.AlertsEnabled))
	}
	if req.UpdateCheck != nil {
		_ = h.store.SetSetting(settingUpdateCheckEnabled, boolStr(*req.UpdateCheck))
	}
	if req.AdminChatIDs != nil {
		// Normalize: trim + drop blanks; store as JSON []string. Empty list →
		// "[]" so the bot falls back to the daily-summary chat as the allowlist.
		ids := []string{}
		for _, id := range *req.AdminChatIDs {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
		if b, err := json.Marshal(ids); err == nil {
			_ = h.store.SetSetting(settingBotAdminChatIDs, string(b))
		}
	}
	set(settingNotifyTelegramToken, req.TelegramToken)
	set(settingNotifyTelegramChat, req.TelegramChatID)
	if req.DailyHour != nil {
		hr := *req.DailyHour
		if hr < 0 || hr > 23 {
			core.Fail(c, http.StatusBadRequest, "INVALID_HOUR", "daily_hour must be 0-23")
			return
		}
		// Re-arm today's send when the operator changes the hour: clear the
		// once-per-day marker so the new schedule actually fires today (for
		// confirmation) instead of being suppressed because a summary already
		// went out earlier today under the old time.
		if prev, _ := h.store.GetSetting(settingNotifyDailyHour); prev != strconv.Itoa(hr) {
			_ = h.store.SetSetting("notify_last_sent_date", "")
		}
		_ = h.store.SetSetting(settingNotifyDailyHour, strconv.Itoa(hr))
	}
	if req.DailyMinute != nil {
		mn := *req.DailyMinute
		if mn < 0 || mn > 59 {
			core.Fail(c, http.StatusBadRequest, "INVALID_MINUTE", "daily_minute must be 0-59")
			return
		}
		// Same re-arm intent as the hour: changing the minute re-arms today.
		if prev, _ := h.store.GetSetting(settingNotifyDailyMinute); prev != strconv.Itoa(mn) {
			_ = h.store.SetSetting("notify_last_sent_date", "")
		}
		_ = h.store.SetSetting(settingNotifyDailyMinute, strconv.Itoa(mn))
	}
	h.auditLog(c, "settings.notify.update", "settings", nil)
	core.OK(c, gin.H{"ok": true})
}

type testNotifyRequest struct {
	Channel string `json:"channel"` // "telegram"
}

// TestNotify sends a small test message to the requested channel using the
// currently saved credentials. Returns the upstream response so the user can
// see immediately whether the integration works.
//
// POST /api/v1/settings/notify/test
func (h *Handler) TestNotify(c *gin.Context) {
	var req testNotifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	stamp := time.Now().In(h.displayLocation()).Format("2006-01-02 15:04 MST")
	lang, _ := h.store.GetSetting("default_lang")
	msg := "EdgeNest test notification @ " + stamp + " — if you see this, the bot is configured correctly."
	if lang == "zh" {
		msg = "EdgeNest 测试通知 @ " + stamp + " — 收到这条说明机器人配置正常。"
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	switch req.Channel {
	case "telegram", "": // default to telegram — the only channel now
		token, _ := h.store.GetSetting(settingNotifyTelegramToken)
		chat, _ := h.store.GetSetting(settingNotifyTelegramChat)
		if token == "" || chat == "" {
			core.Fail(c, http.StatusBadRequest, "TELEGRAM_NOT_CONFIGURED",
				"set telegram_token and telegram_chat_id first")
			return
		}
		if err := notify.SendTelegram(ctx, token, chat, msg); err != nil {
			h.auditLog(c, "notify.test.fail", "telegram", map[string]string{"err": err.Error()})
			// The most common first-run failure: the operator filled in the token
			// + chat ID but never opened a chat with the bot, so Telegram refuses
			// ("chat not found" / "bot can't initiate conversation"). Surface a
			// distinct code the front-end localizes into a clear next step.
			if isNeedStartErr(err) {
				core.Fail(c, http.StatusBadGateway, "TELEGRAM_NEED_START", err.Error())
				return
			}
			core.Fail(c, http.StatusBadGateway, "TELEGRAM_FAILED", err.Error())
			return
		}
	default:
		core.Fail(c, http.StatusBadRequest, "INVALID_CHANNEL", `channel must be "telegram"`)
		return
	}
	h.auditLog(c, "notify.test", "telegram", nil)
	core.OK(c, gin.H{"ok": true})
}

// isNeedStartErr matches the Telegram errors that mean "the user hasn't opened
// a chat with this bot yet" — a bot cannot message someone who never pressed
// Start. The fix is always: open the bot in Telegram and send /start.
func isNeedStartErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "chat not found") ||
		strings.Contains(s, "can't initiate conversation") ||
		strings.Contains(s, "bot was blocked") ||
		strings.Contains(s, "forbidden")
}

// displayLocation resolves the panel display timezone (display_tz), falling
// back to the server's local zone.
func (h *Handler) displayLocation() *time.Location {
	if tz, _ := h.store.GetSetting("display_tz"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local
}
