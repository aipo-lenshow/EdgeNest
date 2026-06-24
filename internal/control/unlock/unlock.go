// Package unlock probes well-known streaming and AI endpoints to report
// whether they are reachable (and not region-locked) from the host running
// EdgeNest. This is the "解锁检测" feature: operators run it once to confirm
// their VPS region actually unblocks the services they care about, before
// handing subscription tokens to clients.
//
// Probes are direct (no outbound proxy). A future enhancement can route
// probes through a specific outbound (e.g., warp) by injecting an
// http.Transport with a SOCKS5 dialer to the local sing-box mixed inbound.
// We keep the probe contract narrow so that swap is a single Prober field.
package unlock

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Target is a single service endpoint we probe.
type Target struct {
	ID    string // stable identifier, e.g. "netflix"
	Name  string // human label, e.g. "Netflix"
	URL   string // probe URL (display + generic GET path)
	OKSub string // optional: response body must contain this substring when unlocked
	// BlockSub: when non-empty, presence of this substring in the response
	// signals an explicit block (e.g. Netflix's "Not Available" page).
	BlockSub string
	// Category groups targets in the UI: ai | streaming | music | social.
	Category string
	// Check, when set, fully determines the verdict — used by services that need
	// multi-request flows, POST, region extraction or status-code logic the
	// generic OKSub/BlockSub path can't express (Netflix three-state, Spotify,
	// DAZN, …). It receives the same Prober (direct or WARP-tunnelled) so the
	// "probe via WARP" comparison works identically. ID/Name/LatencyMS are
	// filled by probeOne; the func sets State/Region/HTTPStatus/Detail*.
	Check func(ctx context.Context, p Prober) Status
}

// Status is the per-target probe result.
type Status struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	State      string `json:"state"` // unlocked | originals_only | restricted | blocked | error | timeout
	HTTPStatus int    `json:"http_status"`
	LatencyMS  int64  `json:"latency_ms"`
	// Region is the detected egress country (lower-case ISO-ish code) when the
	// service exposes it (Netflix/Spotify/Prime/TikTok/…). Empty otherwise.
	Region string `json:"region,omitempty"`
	Detail string `json:"detail,omitempty"`
	// DetailCode is a stable, translatable reason key the UI maps to localised
	// text (the raw Detail stays as a technical fallback / tooltip). Avoids
	// shipping English reason strings to a non-English UI.
	DetailCode string `json:"detail_code,omitempty"`
}

// DefaultTargets is the baseline list. Operators can extend it by passing a
// custom slice to Run. Targets with a Check func use bespoke detection (three-
// state / region / POST); the rest use the generic OKSub/BlockSub path.
// Categories (ai | streaming | music | social) drive UI grouping.
var DefaultTargets = []Target{
	// ── AI ──────────────────────────────────────────────────────────────────
	{ID: "chatgpt", Name: "ChatGPT", Category: "ai",
		URL: "https://chat.openai.com/cdn-cgi/trace",
		// Cloudflare trace exposes the originating PoP location.
		OKSub: "loc=",
	},
	{ID: "openai_api", Name: "OpenAI API", Category: "ai",
		URL: "https://api.openai.com/v1/engines",
		// 401 from a valid region with no auth; 403 from a blocked region.
	},
	{ID: "gemini", Name: "Google Gemini", Category: "ai",
		URL: "https://gemini.google.com/",
	},
	{ID: "claude", Name: "Claude", Category: "ai",
		URL: "https://claude.ai/",
	},
	// ── Streaming ───────────────────────────────────────────────────────────
	{ID: "netflix", Name: "Netflix", Category: "streaming",
		URL: "https://www.netflix.com/", Check: checkNetflix,
	},
	{ID: "disneyplus", Name: "Disney+", Category: "streaming",
		URL: "https://www.disneyplus.com/",
	},
	{ID: "youtube_premium", Name: "YouTube Premium", Category: "streaming",
		URL:      "https://www.youtube.com/premium",
		BlockSub: "Premium is not available in your country",
	},
	{ID: "primevideo", Name: "Amazon Prime Video", Category: "streaming",
		URL: "https://www.primevideo.com/", Check: checkPrimeVideo,
	},
	{ID: "dazn", Name: "DAZN", Category: "streaming",
		URL: "https://www.dazn.com/", Check: checkDazn,
	},
	{ID: "hotstar", Name: "Disney+ Hotstar", Category: "streaming",
		URL: "https://www.hotstar.com/", Check: checkHotstar,
	},
	{ID: "abema", Name: "AbemaTV", Category: "streaming",
		URL: "https://abema.tv/", Check: checkAbema,
	},
	{ID: "bahamut", Name: "Bahamut 動畫瘋", Category: "streaming",
		URL: "https://ani.gamer.com.tw/", Check: checkBahamut,
	},
	{ID: "bilibili_hk", Name: "Bilibili 港澳", Category: "streaming",
		URL: "https://www.bilibili.com/", Check: bilibiliCheck(biliHKURL, "hk"),
	},
	{ID: "bilibili_tw", Name: "Bilibili 台湾", Category: "streaming",
		URL: "https://www.bilibili.com/", Check: bilibiliCheck(biliTWURL, "tw"),
	},
	// ── Music ───────────────────────────────────────────────────────────────
	{ID: "spotify", Name: "Spotify", Category: "music",
		URL: "https://www.spotify.com/", Check: checkSpotify,
	},
	// ── Social ──────────────────────────────────────────────────────────────
	{ID: "tiktok", Name: "TikTok", Category: "social",
		URL: "https://www.tiktok.com/", Check: checkTikTok,
	},
}

// Prober runs an HTTP GET and returns the response. Injected so tests can
// stub the network without spinning up real servers.
type Prober interface {
	Probe(ctx context.Context, url string) (statusCode int, body string, err error)
}

// DialFunc dials a raw TCP connection. Swappable so the same probe logic runs
// against the host's direct egress or through a WARP tunnel.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// HTTPProber is the default implementation. It probes with a real Chrome TLS
// fingerprint (uTLS) — bot-management front ends (Cloudflare on claude.ai etc.)
// 403 the stock Go TLS fingerprint, which made the verdict a flaky false
// negative. Dial is swappable (direct or WARP).
type HTTPProber struct {
	Dial    DialFunc
	Timeout time.Duration
}

// NewHTTPProber returns a Prober that probes the host's direct egress with a
// browser fingerprint.
func NewHTTPProber() *HTTPProber {
	return &HTTPProber{
		Dial:    (&net.Dialer{Timeout: 8 * time.Second}).DialContext,
		Timeout: 8 * time.Second,
	}
}

// NewDialProber returns a Prober that egresses through a custom dialer (e.g. a
// WARP tunnel's DialContext) — used for the "probe via WARP" comparison.
func NewDialProber(dial DialFunc, timeout time.Duration) *HTTPProber {
	return &HTTPProber{Dial: dial, Timeout: timeout}
}

// Probe issues a GET with a Chrome fingerprint and returns status + body
// (capped at 64KB).
func (p *HTTPProber) Probe(ctx context.Context, url string) (int, string, error) {
	return utlsGet(ctx, url, p.Dial, p.Timeout)
}

// ProbeRequest is a richer request for per-service checks (POST, custom headers).
type ProbeRequest struct {
	Method  string // default GET
	URL     string
	Headers map[string]string
	Body    string
	// Follow makes ProbeFull chase 3xx redirects (as GET). Off by default
	// because Netflix/Hotstar need to see the raw redirect + Location header;
	// Prime Video needs it (its homepage 302s to the localised catalogue).
	Follow bool
}

// ProbeResult carries the response headers too (needed for Location-based region
// extraction on Netflix/Hotstar).
type ProbeResult struct {
	Status int
	Header http.Header
	Body   string
}

// FullProber is the richer probe surface. Both the direct and WARP-tunnelled
// HTTPProbers implement it; custom Check funcs type-assert to it (test stubs
// that only implement Prober simply skip the header/POST-dependent paths).
type FullProber interface {
	ProbeFull(ctx context.Context, req ProbeRequest) (ProbeResult, error)
}

// ProbeFull issues an arbitrary request with the Chrome fingerprint and returns
// status + response headers + body. With req.Follow it chases 3xx redirects (as
// GET) up to maxRedirects; otherwise it's a single round-trip.
func (p *HTTPProber) ProbeFull(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	method, target, body := req.Method, req.URL, req.Body
	for i := 0; ; i++ {
		st, hdr, b, err := utlsDo(ctx, method, target, req.Headers, body, p.Dial, p.Timeout)
		if err != nil {
			return ProbeResult{}, err
		}
		redirect := st == 301 || st == 302 || st == 303 || st == 307 || st == 308
		if req.Follow && redirect && i < maxRedirects {
			loc := hdr.Get("Location")
			if loc != "" {
				if next := resolveLocation(target, loc); next != "" {
					target, method, body = next, "GET", ""
					continue
				}
			}
		}
		return ProbeResult{Status: st, Header: hdr, Body: b}, nil
	}
}

// resolveLocation turns a (possibly relative) Location header into an absolute
// URL against the request URL. Returns "" if it can't be parsed.
func resolveLocation(base, loc string) string {
	bu, err := url.Parse(base)
	if err != nil {
		return ""
	}
	lu, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	return bu.ResolveReference(lu).String()
}

// Run probes every target concurrently and returns the result in the same
// order as the input. ctx controls the overall deadline; each probe also
// has the prober's own per-request timeout.
func Run(ctx context.Context, prober Prober, targets []Target) []Status {
	if prober == nil {
		prober = NewHTTPProber()
	}
	out := make([]Status, len(targets))
	type result struct {
		idx int
		s   Status
	}
	ch := make(chan result, len(targets))
	for i, t := range targets {
		go func(idx int, tgt Target) {
			ch <- result{idx: idx, s: probeOne(ctx, prober, tgt)}
		}(i, t)
	}
	for i := 0; i < len(targets); i++ {
		r := <-ch
		out[r.idx] = r.s
	}
	return out
}

func probeOne(ctx context.Context, prober Prober, t Target) Status {
	// Custom multi-step / POST / region checks own the whole verdict.
	if t.Check != nil {
		start := time.Now()
		s := t.Check(ctx, prober)
		s.ID, s.Name = t.ID, t.Name
		if s.LatencyMS == 0 {
			s.LatencyMS = time.Since(start).Milliseconds()
		}
		return s
	}
	start := time.Now()
	status, body, err := prober.Probe(ctx, t.URL)
	elapsed := time.Since(start).Milliseconds()
	s := Status{ID: t.ID, Name: t.Name, HTTPStatus: status, LatencyMS: elapsed}
	if err != nil {
		s.State = classifyError(err)
		s.Detail = err.Error()
		if s.State == "timeout" {
			s.DetailCode = "timeout"
		} else {
			s.DetailCode = "network_error"
		}
		return s
	}
	// Block detection takes priority — a 200 with a "Not Available" body
	// should still be marked blocked.
	if t.BlockSub != "" && strings.Contains(body, t.BlockSub) {
		s.State = "blocked"
		s.Detail = "block marker present: " + t.BlockSub
		s.DetailCode = "block_marker"
		return s
	}
	// OKSub means the body must contain the marker to count as unlocked.
	if t.OKSub != "" {
		if strings.Contains(body, t.OKSub) {
			s.State = "unlocked"
		} else {
			s.State = "blocked"
			s.Detail = "ok marker missing: " + t.OKSub
			s.DetailCode = "ok_marker_missing"
		}
		return s
	}
	// No body markers: 2xx/3xx → unlocked, 4xx/5xx → blocked.
	switch {
	case status >= 200 && status < 400:
		s.State = "unlocked"
	case status == 401:
		// Many APIs (OpenAI etc.) return 401 to anonymous requests — that's
		// still proof we reach the right region.
		s.State = "unlocked"
		s.Detail = "401 anonymous (region OK)"
		s.DetailCode = "region_ok_401"
	case status == 403 || status == 451:
		s.State = "blocked"
		s.DetailCode = "blocked_status"
	default:
		s.State = "error"
		s.Detail = "unexpected status"
		s.DetailCode = "unexpected_status"
	}
	return s
}

func classifyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	msg := err.Error()
	if strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "context deadline exceeded") {
		return "timeout"
	}
	return "error"
}
