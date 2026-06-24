// Package alerts detects "needs attention" conditions — users approaching their
// quota, users/certs expiring soon — and renders them as localized lines. It is
// shared by the daily summary digest (notifyrunner) and, later, the proactive
// per-event alerter (push notifications): both run the same detection so the digest and the
// live alerts never disagree.
//
// These are EARLY warnings, distinct from quota enforcement (internal/control/
// quota), which disables a client at 100% quota or past expiry. A user already
// disabled by the enforcer drops out of these warnings naturally (we only flag
// users that still have an enabled client).
package alerts

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

type Kind string

const (
	KindQuota  Kind = "quota"  // a user's usage crossed the warn threshold
	KindExpiry Kind = "expiry" // a user expires within the warn window
	KindCert   Kind = "cert"   // a certificate expires within the warn window
	KindEngine Kind = "engine" // a proxy engine that serves inbounds is offline
	KindUpdate Kind = "update" // a newer EdgeNest release is available (Target = version)
)

// Thresholds bound the "needs attention" detection. Defaults are deliberately
// earlier than enforcement so the operator gets a heads-up before anything is
// auto-disabled or a cert lapses.
type Thresholds struct {
	QuotaPct   int // flag when a user's usage >= this % of quota
	ExpiryDays int // flag when a user expires within this many days
	CertDays   int // flag when a cert expires within this many days
}

// Default thresholds: warn at 90% quota, 7 days before a user expires, 14 days
// before a cert expires.
func Default() Thresholds { return Thresholds{QuotaPct: 90, ExpiryDays: 7, CertDays: 14} }

// Alert is one attention item. Target is the user email (quota/expiry) or cert
// domain (cert). The numeric fields back the localized Line renderer.
type Alert struct {
	Kind   Kind
	Target string
	Pct    int // KindQuota: used/quota percent
	Days   int // KindExpiry/KindCert: whole days until expiry; <0 = already past
}

// Detector reads the store (and node id) to find attention items. Construct one
// per scan; it holds no state between calls.
type Detector struct {
	store  *store.Store
	nodeID uint
}

func NewDetector(s *store.Store, nodeID string) *Detector {
	n, _ := strconv.ParseUint(nodeID, 10, 64)
	return &Detector{store: s, nodeID: uint(n)}
}

// Attention returns the full ordered "needs attention" set (quota → expiry →
// cert). now must already be in the operator's display timezone so day counts
// match the rest of the digest.
func (d *Detector) Attention(now time.Time, th Thresholds) []Alert {
	out := append(d.quotaWarnings(th.QuotaPct), d.expiringUsers(now, th.ExpiryDays)...)
	out = append(out, d.expiringCerts(now, th.CertDays)...)
	return out
}

// UserCounts returns total distinct users (by email) and how many are enabled
// (have at least one enabled client) — the digest's "👥 共 N · 启用 M" line.
func (d *Detector) UserCounts() (total, enabled int) {
	for _, a := range d.aggregate() {
		total++
		if a.enabled {
			enabled++
		}
	}
	return total, enabled
}

// ── detection ───────────────────────────────────────────────────────────────

type userAgg struct {
	used    int64
	quota   int64
	expiry  int64
	enabled bool
}

// aggregate collapses every client into one row per email: used = sum, quota /
// expiry = max non-zero, enabled = any client enabled. Matches the panel's and
// the bot's user-list view.
func (d *Detector) aggregate() map[string]*userAgg {
	m := map[string]*userAgg{}
	ibs, err := d.store.ListInbounds(d.nodeID)
	if err != nil {
		return m
	}
	for _, ib := range ibs {
		for _, c := range ib.Clients {
			a := m[c.Email]
			if a == nil {
				a = &userAgg{}
				m[c.Email] = a
			}
			a.used += c.TrafficUp + c.TrafficDown
			if c.QuotaBytes > a.quota {
				a.quota = c.QuotaBytes
			}
			if c.ExpiryAt > a.expiry {
				a.expiry = c.ExpiryAt
			}
			if c.Enabled {
				a.enabled = true
			}
		}
	}
	return m
}

func (d *Detector) quotaWarnings(pct int) []Alert {
	if pct <= 0 {
		pct = 90
	}
	var out []Alert
	for email, a := range d.aggregate() {
		// Only still-enabled, capped users: a user already over 100% is disabled
		// by the enforcer and shows up as enabled=false, so this naturally yields
		// the warn band (e.g. 90–100%) rather than re-flagging enforced users.
		if !a.enabled || a.quota <= 0 {
			continue
		}
		p := int(float64(a.used) / float64(a.quota) * 100)
		if p >= pct {
			out = append(out, Alert{Kind: KindQuota, Target: email, Pct: p})
		}
	}
	sortAlerts(out)
	return out
}

func (d *Detector) expiringUsers(now time.Time, days int) []Alert {
	if days <= 0 {
		days = 7
	}
	cutoff := now.Add(time.Duration(days) * 24 * time.Hour).Unix()
	n := now.Unix()
	var out []Alert
	for email, a := range d.aggregate() {
		// Future expiry within the window, still enabled. Already-expired users
		// are disabled by the enforcer (enabled=false) and skipped here.
		if !a.enabled || a.expiry <= 0 || a.expiry > cutoff || a.expiry < n {
			continue
		}
		out = append(out, Alert{Kind: KindExpiry, Target: email, Days: daysUntil(a.expiry, n)})
	}
	sortAlerts(out)
	return out
}

func (d *Detector) expiringCerts(now time.Time, days int) []Alert {
	if days <= 0 {
		days = 14
	}
	cutoff := now.Add(time.Duration(days) * 24 * time.Hour).Unix()
	n := now.Unix()
	certs, err := d.store.ListCertificates(d.nodeID)
	if err != nil {
		return nil
	}
	var out []Alert
	for _, c := range certs {
		// Expiring within the window OR already expired (urgent — certs aren't
		// auto-disabled, a lapsed cert silently breaks TLS inbounds).
		if c.ExpiresAt <= 0 || c.ExpiresAt > cutoff {
			continue
		}
		out = append(out, Alert{Kind: KindCert, Target: c.Domain, Days: daysUntil(c.ExpiresAt, n)})
	}
	sortAlerts(out)
	return out
}

// ── rendering ────────────────────────────────────────────────────────────────

// Line renders one alert as a localized bullet (plain text — the daily digest
// is sent without HTML parse mode).
func Line(a Alert, lang string) string {
	switch a.Kind {
	case KindQuota:
		return "• " + a.Target + tr(lang,
			fmt.Sprintf(" — 配额 %d%%", a.Pct),
			fmt.Sprintf(" — quota %d%%", a.Pct))
	case KindExpiry:
		return "• " + a.Target + expiresIn(a.Days, lang)
	case KindCert:
		return "• " + tr(lang, "证书 ", "cert ") + a.Target + expiresIn(a.Days, lang)
	case KindEngine:
		return "• " + a.Target + tr(lang, " 引擎已掉线", " engine offline")
	case KindUpdate:
		return "• " + tr(lang, "有新版 ", "new version ") + a.Target + tr(lang, " 可升级", " available")
	}
	return ""
}

// Fingerprint is the dedup identity of an alert: kind + target, deliberately
// without the numeric severity so a user crossing 90→95% (or a cert ticking
// 14→13 days) doesn't re-fire. The proactive alerter (alertrunner) persists the
// set of fingerprints currently in alarm and only pushes newly-appeared ones.
func Fingerprint(a Alert) string { return string(a.Kind) + ":" + a.Target }

func expiresIn(days int, lang string) string {
	switch {
	case days < 0:
		return tr(lang, " — 已过期", " — expired")
	case days == 0:
		return tr(lang, " — 今天到期", " — expires today")
	default:
		return tr(lang,
			fmt.Sprintf(" — %d 天后到期", days),
			fmt.Sprintf(" — expires in %dd", days))
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// daysUntil returns whole days from n (unix) until ts (unix); negative if past.
func daysUntil(ts, n int64) int {
	return int((ts - n) / 86400)
}

// sortAlerts gives a stable order within a category (by target) so the digest
// doesn't reshuffle between runs.
func sortAlerts(a []Alert) {
	sort.Slice(a, func(i, j int) bool { return a[i].Target < a[j].Target })
}

func tr(lang, zh, en string) string {
	if lang == "zh" {
		return zh
	}
	return en
}
