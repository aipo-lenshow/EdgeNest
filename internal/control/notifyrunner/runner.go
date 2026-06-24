// Package notifyrunner runs the once-a-day "VPS status + traffic summary"
// notification. It's deliberately a simple ticker rather than a full cron —
// the only schedule we expose to the user is "hour of day" (0-23 local), so
// we wake every minute and fire when the local hour matches and we haven't
// fired yet today.
package notifyrunner

import (
	"context"
	"strconv"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/digest"
	"github.com/aipo-lenshow/EdgeNest/internal/control/notify"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core/nodeapi"
)

// Setting keys mirror the ones in internal/control/api/settings_handlers.go.
// We don't import that package here to avoid cycles (api imports us indirectly
// via the router → handler graph).
const (
	keyEnabled        = "notify_enabled"
	keyTelegramToken  = "notify_telegram_token"
	keyTelegramChatID = "notify_telegram_chat_id"
	keyDailyHour      = "notify_daily_hour"   // 0-23
	keyDailyMinute    = "notify_daily_minute" // 0-59
	keyLastSentDate   = "notify_last_sent_date"
	keyDisplayTZ      = "display_tz" // IANA; empty = server local
)

// Runner ticks every minute and dispatches the daily summary when the local
// hour matches the configured hour. Idempotent: tracks last-sent-date in the
// settings table so a service restart mid-day doesn't double-send.
type Runner struct {
	store  *store.Store
	node   nodeapi.NodeClient
	nodeID string
	tick   time.Duration
}

func New(s *store.Store, n nodeapi.NodeClient, nodeID string) *Runner {
	return &Runner{store: s, node: n, nodeID: nodeID, tick: time.Minute}
}

func (r *Runner) Start(ctx context.Context) {
	go r.loop(ctx)
}

func (r *Runner) loop(ctx context.Context) {
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.maybeSend(ctx)
		}
	}
}

func (r *Runner) maybeSend(ctx context.Context) {
	enabled, _ := r.store.GetSetting(keyEnabled)
	if enabled != "true" {
		return
	}
	hourStr, _ := r.store.GetSetting(keyDailyHour)
	hour, err := strconv.Atoi(hourStr)
	if err != nil || hour < 0 || hour > 23 {
		hour = 9
	}
	minStr, _ := r.store.GetSetting(keyDailyMinute)
	minute, merr := strconv.Atoi(minStr)
	if merr != nil || minute < 0 || minute > 59 {
		minute = 0
	}
	// Fire at/after the configured H:M in the operator's display timezone (not
	// raw server local — a VPS in a far-off region shouldn't push at 3am). Using
	// ">=" rather than "==" makes it a catch-up: if the panel was down or being
	// restarted during the exact target minute (so the once-a-minute tick never
	// landed on it), it still sends later the same day. The once-per-day guard
	// below keeps it to a single send.
	now := time.Now().In(r.location())
	if now.Hour() < hour || (now.Hour() == hour && now.Minute() < minute) {
		return
	}
	today := now.Format("2006-01-02")
	last, _ := r.store.GetSetting(keyLastSentDate)
	if last == today {
		return
	}
	body, err := r.buildSummary(ctx)
	if err != nil {
		return
	}
	sent := false
	if tok, _ := r.store.GetSetting(keyTelegramToken); tok != "" {
		if chat, _ := r.store.GetSetting(keyTelegramChatID); chat != "" {
			if err := notify.SendTelegram(ctx, tok, chat, body); err == nil {
				sent = true
			}
		}
	}
	if sent {
		_ = r.store.SetSetting(keyLastSentDate, today)
	}
}

// location resolves the operator's display timezone (display_tz), falling back
// to the server's local zone when unset or invalid.
func (r *Runner) location() *time.Location {
	if tz, _ := r.store.GetSetting(keyDisplayTZ); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local
}

// buildSummary renders the daily digest via the shared digest package, so the
// scheduled push and the bot's on-demand /summary command are byte-identical.
func (r *Runner) buildSummary(ctx context.Context) (string, error) {
	return digest.Build(ctx, r.store, r.node, r.nodeID, r.lang(), r.location())
}

// lang returns the panel display language ("zh"/"en") from default_lang;
// defaults to "en" when unset.
func (r *Runner) lang() string {
	if v, _ := r.store.GetSetting("default_lang"); v == "zh" {
		return "zh"
	}
	return "en"
}
