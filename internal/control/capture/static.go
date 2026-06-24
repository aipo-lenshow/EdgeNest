package capture

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// staticBodyCap bounds how much HTML we parse — enough for any real page's
// <head> + body markup without letting a hostile/huge response exhaust memory.
const staticBodyCap = 4 * 1024 * 1024

// staticUA presents a browser User-Agent so edges that 403 the stock Go client
// are more likely to return real markup. Static capture is best-effort anyway.
const staticUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// staticCapture fetches the page HTML and extracts hosts from URL-bearing
// attributes (src/href/action/srcset). It cannot see runtime (JS) requests, so
// the result is incomplete — the UI says so and points heavier needs at live
// capture. The page's own host is always included.
func staticCapture(ctx context.Context, rawurl string) (map[string]struct{}, error) {
	base, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	hosts := map[string]struct{}{base.Hostname(): {}}

	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", staticUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		// Network error fetching the page still leaves us the page's own host.
		return hosts, nil
	}
	defer resp.Body.Close()

	tok := html.NewTokenizer(io.LimitReader(resp.Body, staticBodyCap))
	for {
		switch tok.Next() {
		case html.ErrorToken:
			return hosts, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := tok.TagName()
			tag := string(name)
			for hasAttr {
				var key, val []byte
				key, val, hasAttr = tok.TagAttr()
				switch string(key) {
				case "src", "poster", "data-src":
					// Loaded sub-resources (script/img/iframe/video/source...).
					addHost(hosts, base, string(val))
				case "href":
					// Only <link> href is a fetched resource (stylesheet /
					// preload / icon). <a>/<area>/<base> href are navigation
					// targets, NOT resources the page loads — collecting them
					// pulls in footer & social links (e.g. a music site's
					// Instagram / TikTok / YouTube / X), which are false
					// positives for "domains this service actually uses".
					if tag == "link" {
						addHost(hosts, base, string(val))
					}
				case "action":
					// Form submission endpoint — a backend the service posts to.
					addHost(hosts, base, string(val))
				case "srcset":
					for _, cand := range strings.Split(string(val), ",") {
						if f := strings.Fields(strings.TrimSpace(cand)); len(f) > 0 {
							addHost(hosts, base, f[0])
						}
					}
				}
			}
		}
	}
}

// addHost resolves ref against base and records its host. Relative URLs resolve
// to base's host; data:/about:/mailto: and the like have no host and are skipped.
func addHost(hosts map[string]struct{}, base *url.URL, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	u, err := base.Parse(ref)
	if err != nil {
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return
	}
	if h := u.Hostname(); h != "" {
		hosts[h] = struct{}{}
	}
}
