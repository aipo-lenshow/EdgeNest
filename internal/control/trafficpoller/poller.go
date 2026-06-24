// Package trafficpoller turns the engines' v2ray-style StatsService into
// per-user (per Client.Email) cumulative traffic counters in the store, which
// the quota enforcer reads to disable users that exceed their byte allowance.
//
// Why v2ray-style stats and not clash_api: clash_api's /connections metadata
// never carries the matched inbound auth user (verified against upstream
// sing-box tracker.go for v1.13.12 and v1.13.13 — see v2raystats), so it cannot
// attribute bytes to a user on a shared multi-user inbound. The StatsService
// keeps cumulative `user>>>{email}>>>traffic>>>uplink|downlink` counters at the
// connection-tracker layer (short-lived connections and UDP/QUIC included), so
// this is an accurate counter rather than a poll-sampled soft cap.
//
// Multiple sources: a node can run BOTH engines — sing-box (its v2ray_api, the
// panel ships a `with_v2ray_api` build) and xray-core (its native stats API,
// stock). Both expose the same gRPC service. A user may even have credentials on
// inbounds hosted by both engines, so we poll every source, diff each source
// independently (so one engine restarting and zeroing its counters never makes
// us miscount the other), and sum the deltas per email before crediting them.
package trafficpoller

import (
	"context"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/v2raystats"
)

// statsClient is the read-only surface the poller needs. *v2raystats.Client
// satisfies it; the interface keeps the poller unit-testable without a live
// gRPC server.
type statsClient interface {
	QueryUserTraffic(ctx context.Context) (map[string]v2raystats.UserTraffic, error)
}

// Source is one named stats endpoint (e.g. "singbox", "xray").
type Source struct {
	Name   string
	Client statsClient
}

// Poller samples each stats source and writes per-user traffic deltas to the
// store.
type Poller struct {
	sources  []Source
	store    *store.Store
	interval time.Duration
	// last[i] maps email -> last-seen cumulative {upload, download} for
	// sources[i]. Per-source so a single engine restart (its counters reset to
	// 0) only re-baselines that source, not the others. In-memory only:
	// persisted totals live in the DB, so a panel restart re-baselines on the
	// next tick without double-counting.
	last []map[string][2]int64
	// onCredited, if set, is called after a tick that credited any traffic to
	// the store. The quota enforcer hooks this so a user who just crossed their
	// cap is disabled within one poll interval instead of waiting for the next
	// periodic enforce tick. Must be cheap and non-blocking — it runs inline in
	// the poll goroutine.
	onCredited func(context.Context)
}

// OnCredited registers a callback fired at the end of any tick that wrote
// non-zero traffic to the store. Returns the poller for chaining. Optional.
func (p *Poller) OnCredited(fn func(context.Context)) *Poller {
	p.onCredited = fn
	return p
}

// New builds a Poller over the given sources. interval <= 0 falls back to 15s.
func New(sources []Source, st *store.Store, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	last := make([]map[string][2]int64, len(sources))
	for i := range last {
		last[i] = map[string][2]int64{}
	}
	return &Poller{
		sources:  sources,
		store:    st,
		interval: interval,
		last:     last,
	}
}

// Run blocks until ctx is cancelled, sampling once per interval. Best-effort:
// a failed sample (engine restarting, stats service not up yet, engine not
// installed) is skipped for that source, never fatal.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	// Accumulate this tick's deltas across all sources, then credit once per
	// email so a user present on two engines gets a single combined update.
	perEmail := map[string][2]int64{}

	for i, src := range p.sources {
		cur, err := src.Client.QueryUserTraffic(ctx)
		if err != nil {
			// Source unavailable (engine down/restarting/not installed). Skip it
			// this tick; don't touch its baseline so it re-syncs cleanly when back.
			continue
		}
		last := p.last[i]
		seen := make(map[string]bool, len(cur))
		for email, t := range cur {
			seen[email] = true
			prev := last[email]
			du := t.Up - prev[0]
			dd := t.Down - prev[1]
			// Counter went backwards => this engine restarted and zeroed its
			// in-memory counters. Count from the current absolute value rather
			// than emit a negative delta.
			if du < 0 {
				du = t.Up
			}
			if dd < 0 {
				dd = t.Down
			}
			last[email] = [2]int64{t.Up, t.Down}
			if du == 0 && dd == 0 {
				continue
			}
			acc := perEmail[email]
			perEmail[email] = [2]int64{acc[0] + du, acc[1] + dd}
		}
		// Forget users this source no longer reports so its map doesn't grow
		// unbounded; they re-baseline cleanly if they reappear.
		for email := range last {
			if !seen[email] {
				delete(last, email)
			}
		}
	}

	// Key the daily bucket by the operator's display timezone, NOT raw server
	// local time — otherwise a VPS running in UTC buckets "today" on a different
	// calendar day than the digest/panel queries (which use display_tz), so the
	// "today" row never matches and traffic always reads 0.
	today := time.Now().In(p.displayLocation()).Format("2006-01-02")
	for email, d := range perEmail {
		_ = p.store.AddUserTraffic(email, d[0], d[1])
		// Same delta into the per-day bucket so month-to-date / range queries
		// stay consistent with the cumulative counter.
		_ = p.store.AddDailyTraffic(today, email, d[0], d[1])
	}

	// New bytes just landed in the store — let the enforcer re-check now rather
	// than wait up to a full enforce interval, so the overshoot past a quota is
	// bounded by one poll interval instead of the (much longer) enforce period.
	if len(perEmail) > 0 && p.onCredited != nil {
		p.onCredited(ctx)
	}
}

// displayLocation resolves the operator's display timezone (display_tz),
// falling back to server local when unset/invalid — same basis the digest and
// panel use, so daily buckets line up with how "today" is queried.
func (p *Poller) displayLocation() *time.Location {
	if tz, _ := p.store.GetSetting("display_tz"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.Local
}
