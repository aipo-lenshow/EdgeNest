package api

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// Setting keys exposed to the panel UI. Kept in one place so additions are
// obvious (don't sprinkle untyped strings around handlers).
const (
	settingNotifyTelegramToken = "notify_telegram_token"
	settingNotifyTelegramChat  = "notify_telegram_chat_id"
	settingNotifyDailyHour     = "notify_daily_hour"    // 0-23 local hour
	settingNotifyDailyMinute   = "notify_daily_minute"  // 0-59
	settingNotifyEnabled       = "notify_enabled"       // "true"/"false"
	settingBotEnabled          = "bot_enabled"          // interactive bot on/off
	settingAlertsEnabled       = "alerts_enabled"       // proactive alerts on/off (default on)
	settingBotAdminChatIDs     = "bot_admin_chat_ids"   // JSON []string allowlist
	settingUpdateCheckEnabled  = "update_check_enabled" // periodic latest-release check (default on)
)

// ListSettings returns the panel-editable settings (host + panel_path).
// Sensitive values (jwt_secret, notify tokens) are NOT returned.
//
// GET /api/v1/settings
func (h *Handler) ListSettings(c *gin.Context) {
	host, _ := h.store.GetSetting(KeyShareHost)
	panelPath, _ := h.store.GetSetting(bootstrap.KeyPanelPath)

	tgChat, _ := h.store.GetSetting(settingNotifyTelegramChat)
	tgTokSet, _ := h.store.GetSetting(settingNotifyTelegramToken)
	dailyHour, _ := h.store.GetSetting(settingNotifyDailyHour)
	dailyMinute, _ := h.store.GetSetting(settingNotifyDailyMinute)
	enabled, _ := h.store.GetSetting(settingNotifyEnabled)
	botEnabled, _ := h.store.GetSetting(settingBotEnabled)
	alertsEnabled, _ := h.store.GetSetting(settingAlertsEnabled)
	updateCheckEnabled, _ := h.store.GetSetting(settingUpdateCheckEnabled)
	adminRaw, _ := h.store.GetSetting(settingBotAdminChatIDs)

	core.OK(c, gin.H{
		"host":       host,
		"panel_path": panelPath,
		"notify": gin.H{
			"enabled":              enabled == "true",
			"telegram_chat_id":     tgChat,
			"telegram_token_set":   tgTokSet != "",
			"daily_hour":           parseHour(dailyHour),
			"daily_minute":         parseMinute(dailyMinute),
			"bot_enabled":          botEnabled == "true",
			"alerts_enabled":       alertsEnabled != "false",      // default on
			"update_check_enabled": updateCheckEnabled != "false", // default on
			"bot_admin_chat_ids":   parseChatIDs(adminRaw),
		},
	})
}

type updateHostRequest struct {
	Host string `json:"host"`
}

// UpdateHost rewrites the share_host setting used by subscription links.
// Accepts a hostname, FQDN, or IPv4 — no scheme, no path.
//
// PUT /api/v1/settings/host
func (h *Handler) UpdateHost(c *gin.Context) {
	var req updateHostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		core.Fail(c, http.StatusBadRequest, "EMPTY_HOST", "host cannot be empty")
		return
	}
	if strings.ContainsAny(host, " \t/?#") || strings.Contains(host, "://") {
		core.Fail(c, http.StatusBadRequest, "INVALID_HOST",
			"host must be a bare hostname or IP — no scheme, no path")
		return
	}
	if err := h.store.SetSetting(KeyShareHost, host); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "settings.host.update", "settings", map[string]string{"host": host})
	core.OK(c, gin.H{"host": host})
}

type updateLanguageRequest struct {
	Lang string `json:"lang"` // one of bootstrap.SupportedLangs
}

// UpdateLanguage persists the panel-wide default language (default_lang). The
// front-end language switcher writes this so the choice also drives server-side
// presentation that can't read the browser's localStorage — notably the
// Telegram bot's reply language and the first-paint language of index.html.
//
// PUT /api/v1/settings/language
func (h *Handler) UpdateLanguage(c *gin.Context) {
	var req updateLanguageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if !bootstrap.IsSupportedLang(req.Lang) {
		core.Fail(c, http.StatusBadRequest, "INVALID_LANG", "lang must be one of: "+strings.Join(bootstrap.SupportedLangs, ", "))
		return
	}
	if err := h.store.SetSetting(bootstrap.KeyDefaultLang, req.Lang); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "settings.language.update", "settings", map[string]string{"lang": req.Lang})
	core.OK(c, gin.H{"lang": req.Lang})
}

type updateTimezoneRequest struct {
	Timezone string `json:"timezone"` // IANA name; empty = follow server TZ
}

// UpdateTimezone sets the panel display timezone (display_tz). This is a
// presentation setting only — all timestamps are stored as unix epoch; this
// drives front-end rendering (via fmtTime) plus the bot/notify clock. Empty
// clears it, falling back to the server's own timezone.
//
// PUT /api/v1/settings/timezone
func (h *Handler) UpdateTimezone(c *gin.Context) {
	var req updateTimezoneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	tz := strings.TrimSpace(req.Timezone)
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			core.Fail(c, http.StatusBadRequest, "INVALID_TZ",
				"unknown timezone (must be an IANA name like Asia/Shanghai)")
			return
		}
	}
	if err := h.store.SetSetting("display_tz", tz); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "settings.timezone.update", "settings", map[string]string{"timezone": tz})
	core.OK(c, gin.H{"timezone": tz})
}

type updatePanelPathRequest struct {
	PanelPath string `json:"panel_path"` // optional; if empty, regenerate
}

var panelPathRe = regexp.MustCompile(`^/[A-Za-z0-9_-]{4,64}$`)

// UpdatePanelPath rotates the panel obscurity path. Pass an empty/missing
// panel_path to regenerate a random ENPanel-XXXXXXXX value.
//
// After this call the client must redirect to the new URL — the JWT remains
// valid (panel_path is not part of auth).
//
// PUT /api/v1/settings/panel-path
func (h *Handler) UpdatePanelPath(c *gin.Context) {
	var req updatePanelPathRequest
	_ = c.ShouldBindJSON(&req)

	newPath := strings.TrimSpace(req.PanelPath)
	if newPath == "" {
		generated, err := auth.RandomPanelPath()
		if err != nil {
			core.Fail(c, http.StatusInternalServerError, "RAND_ERROR", err.Error())
			return
		}
		newPath = generated
	} else {
		if !strings.HasPrefix(newPath, "/") {
			newPath = "/" + newPath
		}
		if !panelPathRe.MatchString(newPath) {
			core.Fail(c, http.StatusBadRequest, "INVALID_PATH",
				"panel_path must be /[A-Za-z0-9_-]{4,64}")
			return
		}
	}
	if err := h.store.SetSetting(bootstrap.KeyPanelPath, newPath); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "settings.panel_path.update", "settings", map[string]string{"panel_path": newPath})
	core.OK(c, gin.H{"panel_path": newPath})
}

type updateUsernameRequest struct {
	NewUsername string `json:"new_username"`
	Password    string `json:"password"` // current admin password, for confirmation
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

// UpdateUsername changes the current admin's username. Requires re-entering
// the current password (this is a sensitive operation).
//
// PUT /api/v1/admin/username
func (h *Handler) UpdateUsername(c *gin.Context) {
	var req updateUsernameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	newName := strings.TrimSpace(req.NewUsername)
	if !usernameRe.MatchString(newName) {
		core.Fail(c, http.StatusBadRequest, "INVALID_USERNAME",
			"username must be 3-32 chars of [A-Za-z0-9_-]")
		return
	}
	username, _ := c.Get("username")
	admin, err := h.store.GetAdminByUsername(username.(string))
	if err != nil {
		core.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "admin not found")
		return
	}
	if !auth.CheckPassword(admin.PasswordHash, req.Password) {
		core.Fail(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "password incorrect")
		return
	}
	if newName == admin.Username {
		core.OK(c, gin.H{"username": admin.Username, "changed": false})
		return
	}
	// Ensure target name isn't taken by another admin.
	if existing, err := h.store.GetAdminByUsername(newName); err == nil && existing != nil && existing.ID != admin.ID {
		core.Fail(c, http.StatusConflict, "USERNAME_TAKEN", "username already in use")
		return
	}
	admin.Username = newName
	if err := h.store.UpdateAdmin(admin); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "admin.username.update", "admin", map[string]string{"new": newName})
	// JWT still carries the old username claim; client should re-login.
	core.OK(c, gin.H{"username": newName, "changed": true, "must_reauth": true})
}

func parseHour(s string) int {
	if s == "" {
		return 9
	}
	h := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 9
		}
		h = h*10 + int(s[i]-'0')
	}
	if h < 0 || h > 23 {
		return 9
	}
	return h
}

// parseMinute parses the notify minute (0-59); defaults to 0 on empty/invalid.
func parseMinute(s string) int {
	if s == "" {
		return 0
	}
	m, err := strconv.Atoi(s)
	if err != nil || m < 0 || m > 59 {
		return 0
	}
	return m
}

// parseChatIDs decodes the JSON []string allowlist; always returns a non-nil
// slice so the panel gets [] (not null) when unset.
func parseChatIDs(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil {
		return out
	}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}
