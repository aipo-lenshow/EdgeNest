// Package updatecheck periodically asks GitHub for EdgeNest's latest release tag
// and caches it in settings. The bot (/help, /status), the proactive alerter,
// and the panel's About page read the cache to tell the operator when a newer
// version is available — without any of them making a network call on the hot
// path. It only ever reads public release metadata (tag name); nothing about the
// host is reported. Self-host operators can disable it (update_check_enabled).
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

const (
	keyEnabled   = "update_check_enabled" // "false" = opt out; default on
	keyLatest    = "latest_version"       // cached tag (no leading v)
	keyCheckedAt = "latest_checked_at"    // unix seconds of last successful check

	// releasesURL is the public latest-release endpoint. Anonymous access is
	// rate-limited (60 req/h/IP); the cache keeps us far below that. While the
	// repo is private the call 404s and we silently keep the last cache.
	releasesURL = "https://api.github.com/repos/aipo-lenshow/EdgeNest/releases/latest"

	checkInterval = 1 * time.Hour
	startupDelay  = 30 * time.Second
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

type Runner struct {
	store   *store.Store
	current string // running version, for an immediate up-to-date short-circuit log
}

func New(s *store.Store, current string) *Runner {
	return &Runner{store: s, current: current}
}

func (r *Runner) Start(ctx context.Context) { go r.loop(ctx) }

func (r *Runner) loop(ctx context.Context) {
	// Stagger the first check so startup isn't competing for the network.
	if sleep(ctx, startupDelay) {
		return
	}
	r.checkOnce(ctx)
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.checkOnce(ctx)
		}
	}
}

func (r *Runner) checkOnce(ctx context.Context) {
	if v, _ := r.store.GetSetting(keyEnabled); v == "false" {
		return
	}
	tag, err := fetchLatest(ctx)
	if err != nil || tag == "" {
		return // network/private-repo/rate-limit → keep last cache silently
	}
	_ = r.store.SetSetting(keyLatest, tag)
	_ = r.store.SetSetting(keyCheckedAt, strconv.FormatInt(time.Now().Unix(), 10))
}

// fetchLatest returns the latest release tag with any leading "v" stripped.
func fetchLatest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("releases: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(body.TagName), "v"), nil
}

// Status reads the cache and reports whether a newer version than current is
// available. latest is the cached tag ("" if never fetched / disabled).
func Status(s *store.Store, current string) (latest string, available bool) {
	if v, _ := s.GetSetting(keyEnabled); v == "false" {
		return "", false
	}
	latest, _ = s.GetSetting(keyLatest)
	if latest == "" {
		return "", false
	}
	return latest, Newer(current, latest)
}

// StatusLive is Status with a forced fresh GitHub check first, for explicit
// operator-initiated upgrade actions (CLI menu, panel button, bot /upgrade).
// The passive cache only refreshes every checkInterval (1h), so right after a
// release it still holds the previous tag — gating an upgrade on it tells the
// operator "already on the latest" for up to 1h after a newer version shipped
// (observed on a box that had started before the release was published). An
// explicit "upgrade now" must reflect reality, so we fetch live; on a network /
// rate-limit failure we fall back to the cached value so the action degrades
// gracefully instead of erroring. A successful fetch also refreshes the cache
// for the passive readers. Unlike Status it ignores keyEnabled: that flag
// governs the passive periodic nag, not a check the operator just asked for.
func StatusLive(s *store.Store, current string) (latest string, available bool) {
	ctx, cancel := context.WithTimeout(context.Background(), httpClient.Timeout)
	defer cancel()
	if tag, err := fetchLatest(ctx); err == nil && tag != "" {
		_ = s.SetSetting(keyLatest, tag)
		_ = s.SetSetting(keyCheckedAt, strconv.FormatInt(time.Now().Unix(), 10))
		return tag, Newer(current, tag)
	}
	cached, _ := s.GetSetting(keyLatest)
	if cached == "" {
		return "", false
	}
	return cached, Newer(current, cached)
}

// Newer reports whether latest is a strictly newer EdgeNest version than
// current. Versions are "<major>.<MM>.<DDDD>"; compared field-by-field as
// integers (major, then batch, then the MMDD date). A non-conforming or equal
// version yields false (never nags about a same/older/garbage tag).
func Newer(current, latest string) bool {
	c, okc := parseVersion(current)
	l, okl := parseVersion(latest)
	if !okc || !okl {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func sleep(ctx context.Context, d time.Duration) (cancelled bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
