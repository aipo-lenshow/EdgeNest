// Package timesync keeps the host system clock aligned with UTC so the
// replay-protected protocols (SS-2022 SIP022 / Hysteria2 / TUIC) don't reject
// legitimate clients. It runs inside the edgenest process so the operator
// doesn't have to think about it — if systemd-timesyncd or chrony work, fine;
// if the VPS provider blocks UDP/123 (common — happens on cheap regional VPS
// with strict egress ACLs), we still self-correct over HTTPS.
//
// Algorithm:
//   1. HEAD https://1.1.1.1/ and https://www.google.com/, parse the `Date`
//      response header (RFC 7231 — `Sun, 06 Nov 1994 08:49:37 GMT`). Cloudflare
//      and Google both stamp it from a strongly-synced source. Median across
//      successful responses defends against a single misbehaving CDN node.
//   2. Compute drift = remote_utc - local_utc, accounting for the HTTP RTT
//      (the response header was generated mid-RTT, so add rtt/2).
//   3. If |drift| > driftThreshold, call settimeofday(2) to step the clock.
//      We don't slew — drift this big means NTP is broken upstream, slewing
//      would take hours and meanwhile SS-2022 keeps rejecting clients.
//
// Anything below the threshold is logged but not acted on (avoid jitter
// fighting any NTP daemon that's actually working).
package timesync

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultInterval     = 6 * time.Hour
	httpTimeout         = 5 * time.Second
	defaultStepDrift    = 5 * time.Second
	requestPerEndpoint  = 1
	startupCheckGrace   = 30 * time.Second
)

// httpDateSources are intentionally diverse so a single blocked egress doesn't
// strand the sync. All return `Date` headers driven by tightly-NTP'd
// infrastructure.
var httpDateSources = []string{
	"https://1.1.1.1/",
	"https://www.cloudflare.com/cdn-cgi/trace",
	"https://www.google.com/generate_204",
	"https://www.apple.com/library/test/success.html",
}

// Manager runs the periodic sync loop. One per process.
type Manager struct {
	client       *http.Client
	stepDrift    time.Duration
	interval     time.Duration
	lastDriftMS  atomic.Int64 // last observed drift, milliseconds (signed)
	lastSyncTime atomic.Int64 // unix-nano of last successful probe
	mu           sync.Mutex
}

// New builds a Manager with sensible defaults. Pass interval=0 to use 6h.
func New(interval, stepDrift time.Duration) *Manager {
	if interval <= 0 {
		interval = defaultInterval
	}
	if stepDrift <= 0 {
		stepDrift = defaultStepDrift
	}
	return &Manager{
		client: &http.Client{
			Timeout: httpTimeout,
			// Don't follow redirects: the Date header on the first response is
			// what we want; following a redirect adds RTT noise.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		stepDrift: stepDrift,
		interval:  interval,
	}
}

// Start runs an immediate sync, then ticks every interval until ctx is done.
// Safe to call concurrently with other manager methods.
func (m *Manager) Start(ctx context.Context) {
	go func() {
		// Give the process a moment to settle before the first probe — DNS /
		// CGNAT routes are often not ready at the first second of boot.
		select {
		case <-time.After(startupCheckGrace):
		case <-ctx.Done():
			return
		}
		m.syncOnce(ctx)
		t := time.NewTicker(m.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.syncOnce(ctx)
			}
		}
	}()
}

// LastDrift returns the most recent measured drift (positive = local clock
// ahead of UTC). Useful for /api/health to expose to the panel.
func (m *Manager) LastDrift() time.Duration {
	return time.Duration(m.lastDriftMS.Load()) * time.Millisecond
}

// LastSync returns the wall-clock time of the last successful probe.
func (m *Manager) LastSync() time.Time {
	ns := m.lastSyncTime.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// syncOnce probes every source, takes the median, and steps the clock if the
// drift exceeds stepDrift. Errors are logged but never fatal — a quiet
// background fixer must never crash the panel.
func (m *Manager) syncOnce(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()

	samples := make([]time.Duration, 0, len(httpDateSources))
	for _, url := range httpDateSources {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if d, ok := m.probe(ctx, url); ok {
			samples = append(samples, d)
		}
	}
	if len(samples) == 0 {
		log.Printf("timesync: no probe succeeded; leaving clock alone")
		return
	}
	drift := median(samples)
	m.lastDriftMS.Store(drift.Milliseconds())
	m.lastSyncTime.Store(time.Now().UnixNano())

	abs := drift
	if abs < 0 {
		abs = -abs
	}
	if abs < m.stepDrift {
		log.Printf("timesync: drift %v within threshold %v, no action", drift, m.stepDrift)
		return
	}
	target := time.Now().Add(drift)
	if err := setSystemTime(target); err != nil {
		log.Printf("timesync: step failed (drift %v): %v", drift, err)
		return
	}
	log.Printf("timesync: stepped clock by %v to %v", drift, target.UTC().Format(time.RFC3339))
}

// probe HEADs the URL and returns (remoteUTC - localUTC) accounting for RTT.
// Falls back to GET if HEAD is rejected (some CDNs return 405 on HEAD).
func (m *Manager) probe(ctx context.Context, url string) (time.Duration, bool) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("User-Agent", "EdgeNest/timesync")
	resp, err := m.client.Do(req)
	if err != nil || resp.StatusCode == http.StatusMethodNotAllowed {
		if resp != nil {
			_ = resp.Body.Close()
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, false
		}
		req.Header.Set("User-Agent", "EdgeNest/timesync")
		resp, err = m.client.Do(req)
		if err != nil {
			return 0, false
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	}
	defer resp.Body.Close()
	rtt := time.Since(start)
	dateHdr := resp.Header.Get("Date")
	if dateHdr == "" {
		return 0, false
	}
	remote, err := http.ParseTime(dateHdr)
	if err != nil {
		return 0, false
	}
	// Date header was generated ~halfway through the round-trip.
	estimatedRemoteNow := remote.Add(rtt / 2)
	drift := estimatedRemoteNow.Sub(time.Now())
	return drift, true
}

func median(xs []time.Duration) time.Duration {
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

// setSystemTime steps the host clock. Requires CAP_SYS_TIME (edgenest runs as
// root in the supported install). On platforms where settimeofday isn't
// available we fall back to no-op and only update the drift gauge for the
// panel to surface.
func setSystemTime(t time.Time) error {
	tv := syscall.NsecToTimeval(t.UnixNano())
	if err := syscall.Settimeofday(&tv); err != nil {
		return fmt.Errorf("settimeofday: %w", err)
	}
	return nil
}
