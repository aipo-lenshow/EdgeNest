// Package alertrunner runs the proactive alerter: it wakes on a short tick,
// detects "needs attention" conditions (a user near quota, a user/cert expiring
// soon, a proxy engine that serves inbounds gone offline) and pushes only the
// NEWLY-appeared ones to the operator's Telegram. State-deduped: the set of
// fingerprints currently in alarm is persisted, so a standing condition is
// pushed once, not every tick.
//
// Detection of quota/expiry/cert reuses alerts.Detector — the same code the
// daily digest runs — so the live alerts and the digest never disagree. Engine
// offline is detected here (it needs the node heartbeat, which the store-only
// Detector doesn't have).
package alertrunner

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/alerts"
	"github.com/aipo-lenshow/EdgeNest/internal/control/digest"
	"github.com/aipo-lenshow/EdgeNest/internal/control/notify"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/updatecheck"
	"github.com/aipo-lenshow/EdgeNest/internal/core/nodeapi"
)

const (
	keyAlertsEnabled = "alerts_enabled"          // "false" = opt out; default on
	keyNotifyToken   = "notify_telegram_token"   // shared with the daily digest
	keyNotifyChatID  = "notify_telegram_chat_id" // push target
	keyAlertState    = "alerts_state"            // JSON []fingerprint in alarm
	keyDisplayTZ     = "display_tz"              // IANA; empty = server local
	keyDefaultLang   = "default_lang"            // "zh"/"en"
)

// tick is how often we scan. Short enough to catch an engine outage quickly,
// cheap enough to run every minute (a few store reads + one heartbeat).
const tick = time.Minute

type Runner struct {
	store  *store.Store
	node   nodeapi.NodeClient
	nodeID string
}

func New(s *store.Store, n nodeapi.NodeClient, nodeID string) *Runner {
	return &Runner{store: s, node: n, nodeID: nodeID}
}

func (r *Runner) Start(ctx context.Context) { go r.loop(ctx) }

func (r *Runner) loop(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.scan(ctx)
		}
	}
}

func (r *Runner) scan(ctx context.Context) {
	// Opt-out switch (default on) + a configured push target.
	if v, _ := r.store.GetSetting(keyAlertsEnabled); v == "false" {
		return
	}
	token, _ := r.store.GetSetting(keyNotifyToken)
	chat, _ := r.store.GetSetting(keyNotifyChatID)
	if token == "" || chat == "" {
		return
	}

	lang := r.lang()
	now := time.Now().In(r.location())

	// Current alarm set: quota/expiry/cert (shared detector) + engine offline.
	det := alerts.NewDetector(r.store, r.nodeID)
	cur := det.Attention(now, alerts.Default())
	cur = append(cur, r.engineAlerts(ctx)...)
	if a, ok := r.updateAlert(); ok {
		cur = append(cur, a)
	}

	curFP := map[string]alerts.Alert{}
	for _, a := range cur {
		curFP[alerts.Fingerprint(a)] = a
	}
	prev := r.loadState()
	fresh, recovered := diffAlerts(cur, prev)

	if len(fresh) == 0 && len(recovered) == 0 {
		// State may still have shrunk (a condition cleared); persist so the next
		// re-occurrence fires again. No message — that's the whole point of dedup.
		r.saveState(curFP)
		return
	}

	msg := r.compose(now, lang, fresh, recovered)
	if err := notify.SendTelegram(ctx, token, chat, msg); err != nil {
		return // leave prev state intact → retry next tick
	}
	r.saveState(curFP)
}

// diffAlerts computes what to push given the current alarm set and the
// previously-pushed fingerprint set: fresh = alerts newly in alarm (preserving
// cur's category order); recovered = engine targets that were in alarm and have
// now cleared (only engines get a recovery note — a binary up/down state where
// "back online" is worth saying; quota/expiry/cert clear silently).
func diffAlerts(cur []alerts.Alert, prev map[string]bool) (fresh []alerts.Alert, recovered []string) {
	curFP := map[string]bool{}
	for _, a := range cur {
		curFP[alerts.Fingerprint(a)] = true
	}
	for _, a := range cur {
		if !prev[alerts.Fingerprint(a)] {
			fresh = append(fresh, a)
		}
	}
	enginePrefix := string(alerts.KindEngine) + ":"
	for fp := range prev {
		if !curFP[fp] && strings.HasPrefix(fp, enginePrefix) {
			recovered = append(recovered, strings.TrimPrefix(fp, enginePrefix))
		}
	}
	sort.Strings(recovered)
	return fresh, recovered
}

// engineAlerts flags a proxy engine that serves at least one inbound but is not
// running. A heartbeat error is treated as "unknown" (no false alarm). Only
// engines that actually host inbounds are checked — no point alerting xray-down
// when no inbound uses xray.
func (r *Runner) engineAlerts(ctx context.Context) []alerts.Alert {
	health, err := r.node.Heartbeat(ctx, r.nodeID)
	if err != nil {
		return nil
	}
	sbN, xrN := digest.EngineInboundCounts(r.store, r.nodeID)
	var out []alerts.Alert
	if sbN > 0 && !health.SingboxRunning {
		out = append(out, alerts.Alert{Kind: alerts.KindEngine, Target: "sing-box"})
	}
	if xrN > 0 && !health.XrayRunning {
		out = append(out, alerts.Alert{Kind: alerts.KindEngine, Target: "xray"})
	}
	return out
}

// updateAlert flags that a newer EdgeNest release is available (cached by the
// updatecheck runner). Fingerprint includes the version, so each new release
// pushes once. Reads cache only — no network call on the tick.
func (r *Runner) updateAlert() (alerts.Alert, bool) {
	latest, available := updatecheck.Status(r.store, digest.AppVersion)
	if !available {
		return alerts.Alert{}, false
	}
	return alerts.Alert{Kind: alerts.KindUpdate, Target: latest}, true
}

// compose builds the plain-text push (SendTelegram is sent without HTML mode).
func (r *Runner) compose(now time.Time, lang string, fresh []alerts.Alert, recovered []string) string {
	var b strings.Builder
	stamp := now.Format("2006-01-02 15:04 MST")
	if len(fresh) > 0 {
		b.WriteString(tr(lang, "🚨 EdgeNest 告警 · ", "🚨 EdgeNest alert · ") + stamp + "\n")
		for _, a := range fresh {
			b.WriteString(alerts.Line(a, lang) + "\n")
		}
	}
	for _, eng := range recovered {
		b.WriteString(tr(lang, "✅ ", "✅ ") + eng + tr(lang, " 引擎已恢复", " engine recovered") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (r *Runner) loadState() map[string]bool {
	out := map[string]bool{}
	raw, _ := r.store.GetSetting(keyAlertState)
	if raw == "" {
		return out
	}
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) == nil {
		for _, id := range ids {
			out[id] = true
		}
	}
	return out
}

func (r *Runner) saveState(curFP map[string]alerts.Alert) {
	ids := make([]string, 0, len(curFP))
	for fp := range curFP {
		ids = append(ids, fp)
	}
	b, _ := json.Marshal(ids)
	_ = r.store.SetSetting(keyAlertState, string(b))
}

func (r *Runner) location() *time.Location {
	if tz, _ := r.store.GetSetting(keyDisplayTZ); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local
}

func (r *Runner) lang() string {
	if v, _ := r.store.GetSetting(keyDefaultLang); v == "zh" {
		return "zh"
	}
	return "en"
}

func tr(lang, zh, en string) string {
	if lang == "zh" {
		return zh
	}
	return en
}
