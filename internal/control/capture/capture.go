// Package capture turns "this service uses these domains" into routing rules.
//
// Two complementary paths:
//
//   - by-URL (this file): fetches a page's HTML and parses src/href/action
//     attributes — a quick, server-side rough scan with no dependency. It can't
//     see JS-loaded or login/playback-gated domains, so results are flagged
//     incomplete and the UI points heavier needs at live capture.
//   - live (clashapi + api/live_capture_handlers): records the domains a real
//     client session actually reached. The complete, ground-truth path.
package capture

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Result is the outcome of one by-URL capture run.
type Result struct {
	// Engine is always "static" for by-URL capture (kept for the UI's incomplete
	// warning + forward-compat).
	Engine  string        `json:"engine"`
	Domains []DomainGroup `json:"domains"`
}

// DomainGroup collapses every observed host under one registrable domain
// (eTLD+1), e.g. all of www.netflix.com / nflxso.net... grouped so the operator
// checks one box to route the whole service.
type DomainGroup struct {
	Registrable string   `json:"registrable"`
	Hosts       []string `json:"hosts"`
	Count       int      `json:"count"`
	// Noise flags background/telemetry domains (push, analytics, browser
	// telemetry, IP lookups) so the UI can fold them away and pre-select only
	// the service-relevant ones. See noise.go.
	Noise bool `json:"noise"`
	// Bytes is the total traffic (up+down) the live session moved through this
	// group — the "you actually used this" signal. 0 for by-URL capture.
	Bytes int64 `json:"bytes"`
}

// Capture fetches rawurl's HTML and returns the registrable domains it
// references (static parse — quick rough scan). For domains that load via JS or
// only after login/playback, use live capture instead. The caller's ctx
// deadline bounds the run.
func Capture(ctx context.Context, rawurl string) (Result, error) {
	u, err := normalizeURL(rawurl)
	if err != nil {
		return Result{}, err
	}
	hosts, serr := staticCapture(ctx, u)
	if serr != nil {
		return Result{}, serr
	}
	return Result{Engine: "static", Domains: groupHosts(hosts)}, nil
}

// GroupHosts groups a host list into registrable-domain groups. Exported for
// the live-capture path, which accumulates hosts from sing-box's clash_api
// rather than from a single page visit, then reuses the same grouping.
func GroupHosts(hosts []string) []DomainGroup {
	set := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		set[h] = struct{}{}
	}
	return groupHosts(set)
}

// normalizeURL accepts a bare host ("netflix.com") or a full URL and returns a
// well-formed http(s) URL with a host.
func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("url must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("url has no host")
	}
	// Reject free text ("s送积分") that isn't a real domain: it must reduce to a
	// registrable name under a known public suffix. IPs are fine too.
	if net.ParseIP(host) == nil {
		if !strings.Contains(host, ".") {
			return "", fmt.Errorf("not a valid domain: %s", host)
		}
		if _, err := publicsuffix.EffectiveTLDPlusOne(host); err != nil {
			return "", fmt.Errorf("not a valid domain: %s", host)
		}
	}
	return u.String(), nil
}

// groupHosts collapses a host set into sorted DomainGroups keyed by eTLD+1.
// IPs and hosts with no registrable domain are kept under themselves so nothing
// silently vanishes.
func groupHosts(hosts map[string]struct{}) []DomainGroup {
	groups := map[string]map[string]struct{}{}
	for h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		key := registrable(h)
		if groups[key] == nil {
			groups[key] = map[string]struct{}{}
		}
		groups[key][h] = struct{}{}
	}
	out := make([]DomainGroup, 0, len(groups))
	for reg, hs := range groups {
		list := make([]string, 0, len(hs))
		for h := range hs {
			list = append(list, h)
		}
		sort.Strings(list)
		g := DomainGroup{Registrable: reg, Hosts: list, Count: len(list)}
		g.Noise = isNoiseGroup(g)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Registrable < out[j].Registrable })
	return out
}

// registrable returns the eTLD+1 for a host, or the host itself for IPs / hosts
// the public-suffix list can't reduce (so they remain selectable rather than
// being dropped).
func registrable(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return host
	}
	if reg, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil && reg != "" {
		return reg
	}
	return host
}
