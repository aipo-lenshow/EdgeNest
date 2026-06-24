package unlock

// Per-service unlock checks that the generic OKSub/BlockSub path can't express:
// three-state verdicts, POST requests, JSON parsing, multi-step flows and region
// extraction. Detection markers (URLs, status codes, body fields) are translated
// faithfully from the community de-facto standard MediaUnlockTest
// (github.com/HsukqiLee/MediaUnlockTest, the Go rewrite of lmc999's
// RegionRestrictionCheck). Per project discipline, the source function is named
// in each check's comment — do not change a marker without re-confirming upstream.

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// errStatus maps a transport error to a Status with the right state/detail code.
func errStatus(err error) Status {
	s := Status{State: classifyError(err), Detail: err.Error()}
	if s.State == "timeout" {
		s.DetailCode = "timeout"
	} else {
		s.DetailCode = "network_error"
	}
	return s
}

func pickStatus(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func locSegment(location string, idx int) string {
	parts := strings.Split(location, "/")
	if len(parts) > idx {
		return parts[idx]
	}
	return ""
}

// ── Netflix: full unlock vs originals-only vs banned ─────────────────────────
// Source: MediaUnlockTest checks/Netflix.go. Probes two NON-original (licensed)
// titles; 200/301 on either = full catalogue, both 404 = Originals only (Netflix
// reachable but licensed content geo-blocked), both 403 = banned. Region comes
// from the Location header of a third title.
const (
	nfTitleLicensed1 = "https://www.netflix.com/title/81280792" // LEGO Ninjago
	nfTitleLicensed2 = "https://www.netflix.com/title/70143836" // Breaking Bad
	nfTitleRegion    = "https://www.netflix.com/title/80018499"
)

func checkNetflix(ctx context.Context, p Prober) Status {
	st1, _, e1 := p.Probe(ctx, nfTitleLicensed1)
	st2, _, e2 := p.Probe(ctx, nfTitleLicensed2)
	if e1 != nil && e2 != nil {
		return errStatus(e1)
	}
	full := func(s int) bool { return s == 200 || s == 301 }
	if full(st1) || full(st2) {
		return Status{State: "unlocked", HTTPStatus: pickStatus(st1, st2), Region: netflixRegion(ctx, p)}
	}
	if st1 == 404 && st2 == 404 {
		return Status{State: "originals_only", HTTPStatus: 404, DetailCode: "netflix_originals_only"}
	}
	if st1 == 403 && st2 == 403 {
		return Status{State: "blocked", HTTPStatus: 403, DetailCode: "blocked_status"}
	}
	return Status{State: "error", HTTPStatus: pickStatus(st1, st2), DetailCode: "unexpected_status"}
}

func netflixRegion(ctx context.Context, p Prober) string {
	fp, ok := p.(FullProber)
	if !ok {
		return ""
	}
	r, err := fp.ProbeFull(ctx, ProbeRequest{URL: nfTitleRegion})
	if err != nil {
		return ""
	}
	loc := r.Header.Get("Location")
	if loc == "" {
		return "us" // 200 with no localisation redirect → US catalogue
	}
	// Localised URLs look like .../sg-en/title/... ; US has no locale segment.
	seg := locSegment(loc, 3)
	cc := strings.SplitN(seg, "-", 2)[0]
	if cc == "" || cc == "title" {
		return "us"
	}
	return strings.ToLower(cc)
}

// ── Amazon Prime Video ───────────────────────────────────────────────────────
// Source: MediaUnlockTest checks/PrimeVideo.go. The homepage 302-redirects to
// the localised catalogue, whose body carries "currentTerritory":"XX". We must
// FOLLOW the redirect (the landing 302 has no body) — a no-follow probe was
// false-negativing every region.
var rePrimeTerritory = regexp.MustCompile(`"currentTerritory":\s*"([A-Za-z]{2})"`)

func checkPrimeVideo(ctx context.Context, p Prober) Status {
	fp, ok := p.(FullProber)
	if !ok {
		st, body, err := p.Probe(ctx, "https://www.primevideo.com")
		if err != nil {
			return errStatus(err)
		}
		if m := rePrimeTerritory.FindStringSubmatch(body); m != nil {
			return Status{State: "unlocked", HTTPStatus: st, Region: strings.ToLower(m[1])}
		}
		return Status{State: "blocked", HTTPStatus: st, DetailCode: "region_unavailable"}
	}
	r, err := fp.ProbeFull(ctx, ProbeRequest{URL: "https://www.primevideo.com/", Follow: true})
	if err != nil {
		return errStatus(err)
	}
	if m := rePrimeTerritory.FindStringSubmatch(r.Body); m != nil {
		return Status{State: "unlocked", HTTPStatus: r.Status, Region: strings.ToLower(m[1])}
	}
	return Status{State: "blocked", HTTPStatus: r.Status, DetailCode: "region_unavailable"}
}

// ── Spotify ──────────────────────────────────────────────────────────────────
// Source: MediaUnlockTest checks/Spotify.go. POST to the signup endpoint; the
// JSON "status" field is 311 (with is_country_launched) when the region is live,
// 320 when not, HTTP 403 when banned. Body is form-encoded but sent with a JSON
// content-type exactly as upstream does.
const spotifyBody = "birth_day=11&birth_month=11&birth_year=2000&collect_personal_info=undefined&creation_flow=&creation_point=https%3A%2F%2Fwww.spotify.com%2Fhk-en%2F&displayname=Gay%20Lord&gender=male&iagree=1&key=a1e486e2729f46d6bb368d6b2bcda326&platform=www&referrer=&send-email=0&thirdpartyemail=0&identifier_token=AgE6YTvEzkReHNfJpO114514"

func checkSpotify(ctx context.Context, p Prober) Status {
	fp, ok := p.(FullProber)
	if !ok {
		return Status{State: "error", DetailCode: "probe_unsupported"}
	}
	r, err := fp.ProbeFull(ctx, ProbeRequest{
		Method: "POST",
		URL:    "https://spclient.wg.spotify.com/signup/public/v1/account",
		Headers: map[string]string{
			"Accept-Language": "en",
			"Content-Type":    "application/json",
			"Cache-Control":   "no-cache",
		},
		Body: spotifyBody,
	})
	if err != nil {
		return errStatus(err)
	}
	if r.Status == 403 {
		return Status{State: "blocked", HTTPStatus: 403, DetailCode: "blocked_status"}
	}
	var sp struct {
		Status            int    `json:"status"`
		Country           string `json:"country"`
		IsCountryLaunched bool   `json:"is_country_launched"`
	}
	_ = json.Unmarshal([]byte(r.Body), &sp)
	if sp.Status == 311 && sp.IsCountryLaunched {
		return Status{State: "unlocked", HTTPStatus: r.Status, Region: strings.ToLower(sp.Country)}
	}
	// Status 320 with a proxy error = Spotify flagged the egress as a
	// datacenter/proxy IP — that's not a region launch issue, so say so.
	if strings.Contains(r.Body, "proxy") {
		return Status{State: "blocked", HTTPStatus: r.Status, DetailCode: "datacenter_ip"}
	}
	return Status{State: "blocked", HTTPStatus: r.Status, DetailCode: "region_unavailable"}
}

// ── TikTok ───────────────────────────────────────────────────────────────────
// Source: MediaUnlockTest checks/TikTok.go. /explore body redirects to
// /hk/notfound when unavailable; otherwise "region":"XX" gives the geo.
var reTikTokRegion = regexp.MustCompile(`"region":"(\w+)"`)

func checkTikTok(ctx context.Context, p Prober) Status {
	st, body, err := p.Probe(ctx, "https://www.tiktok.com/explore")
	if err != nil {
		return errStatus(err)
	}
	if strings.Contains(body, "https://www.tiktok.com/hk/notfound") {
		return Status{State: "blocked", HTTPStatus: st, DetailCode: "region_unavailable"}
	}
	if m := reTikTokRegion.FindStringSubmatch(body); m != nil {
		return Status{State: "unlocked", HTTPStatus: st, Region: strings.ToLower(m[1])}
	}
	return Status{State: "blocked", HTTPStatus: st, DetailCode: "region_unavailable"}
}

// ── DAZN ─────────────────────────────────────────────────────────────────────
// Source: MediaUnlockTest checks/Dazn.go. POST to the Startup API; the nested
// Region.isAllowed boolean decides access, GeolocatedCountry gives the region.
const daznBody = `{"LandingPageKey":"generic","Languages":"zh-CN,zh,en","Platform":"web","PlatformAttributes":{},"Manufacturer":"","PromoCode":"","Version":"2"}`

func checkDazn(ctx context.Context, p Prober) Status {
	fp, ok := p.(FullProber)
	if !ok {
		return Status{State: "error", DetailCode: "probe_unsupported"}
	}
	r, err := fp.ProbeFull(ctx, ProbeRequest{
		Method:  "POST",
		URL:     "https://startup.core.indazn.com/misl/v5/Startup",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    daznBody,
	})
	if err != nil {
		return errStatus(err)
	}
	if r.Status == 403 {
		return Status{State: "blocked", HTTPStatus: 403, DetailCode: "blocked_status"}
	}
	var dz struct {
		Region struct {
			IsAllowed         bool   `json:"isAllowed"`
			GeolocatedCountry string `json:"GeolocatedCountry"`
		} `json:"Region"`
	}
	_ = json.Unmarshal([]byte(r.Body), &dz)
	if dz.Region.IsAllowed {
		return Status{State: "unlocked", HTTPStatus: r.Status, Region: strings.ToLower(dz.Region.GeolocatedCountry)}
	}
	return Status{State: "restricted", HTTPStatus: r.Status, Region: strings.ToLower(dz.Region.GeolocatedCountry), DetailCode: "region_unavailable"}
}

// ── AbemaTV ──────────────────────────────────────────────────────────────────
// Source: MediaUnlockTest checks/Abema.go. The IP-check API returns the egress
// country; only JP is fully unlocked, other countries are "oversea only".
func checkAbema(ctx context.Context, p Prober) Status {
	st, body, err := p.Probe(ctx, "https://api.abema.io/v1/ip/check?device=android")
	if err != nil {
		return errStatus(err)
	}
	if st == 403 {
		return Status{State: "blocked", HTTPStatus: 403, DetailCode: "blocked_status"}
	}
	var ab struct {
		IsoCountryCode string `json:"isoCountryCode"`
	}
	_ = json.Unmarshal([]byte(body), &ab)
	cc := strings.ToUpper(ab.IsoCountryCode)
	switch {
	case cc == "JP":
		return Status{State: "unlocked", HTTPStatus: st, Region: "jp"}
	case cc != "":
		return Status{State: "restricted", HTTPStatus: st, Region: strings.ToLower(cc), DetailCode: "oversea_only"}
	default:
		return Status{State: "blocked", HTTPStatus: st, DetailCode: "region_unavailable"}
	}
}

// ── Bilibili (port-area variants) ────────────────────────────────────────────
// Source: MediaUnlockTest checks/BiliBili.go. The bangumi playurl API returns
// code 0 when the title plays in that area, negative codes when geo-blocked,
// HTTP 412 when the request itself is rejected.
func bilibiliCheck(url, region string) func(ctx context.Context, p Prober) Status {
	return func(ctx context.Context, p Prober) Status {
		st, body, err := p.Probe(ctx, url)
		if err != nil {
			return errStatus(err)
		}
		if st == 412 {
			return Status{State: "blocked", HTTPStatus: 412, DetailCode: "blocked_status"}
		}
		var bl struct {
			Code int `json:"code"`
		}
		_ = json.Unmarshal([]byte(body), &bl)
		switch bl.Code {
		case 0:
			return Status{State: "unlocked", HTTPStatus: st, Region: region}
		case -10403, 10004001, 10003003:
			return Status{State: "blocked", HTTPStatus: st, DetailCode: "region_unavailable"}
		default:
			return Status{State: "error", HTTPStatus: st, DetailCode: "unexpected_status"}
		}
	}
}

const (
	biliHKURL = "https://api.bilibili.com/pgc/player/web/playurl?avid=473502608&cid=845838026&qn=0&type=&otype=json&ep_id=678506&fourk=1&fnver=0&fnval=16&module=bangumi"
	biliTWURL = "https://api.bilibili.com/pgc/player/web/playurl?avid=50762638&cid=100279344&qn=0&type=&otype=json&ep_id=268176&fourk=1&fnver=0&fnval=16&module=bangumi"
)

// ── Bahamut 動畫瘋 (Gamer Animad, Taiwan) ─────────────────────────────────────
// Source: MediaUnlockTest checks/BahamutAnime.go. getdeviceid → token (the anime
// is playable only from TW); a non-zero animeSn means full TW access, otherwise
// the cdn-cgi/trace loc reveals the (oversea) region.
var reTraceLoc = regexp.MustCompile(`loc=([A-Za-z]{2})`)

func checkBahamut(ctx context.Context, p Prober) Status {
	_, body1, err := p.Probe(ctx, "https://ani.gamer.com.tw/ajax/getdeviceid.php")
	if err != nil {
		return errStatus(err)
	}
	var d struct {
		Deviceid string `json:"deviceid"`
	}
	_ = json.Unmarshal([]byte(body1), &d)
	if d.Deviceid == "" {
		// Bahamut serves an HTML "系統異常回報" error page (not JSON) to
		// datacenter / non-TW IPs — i.e. it's refusing this egress, which is a
		// region block, not a probe malfunction.
		return Status{State: "blocked", DetailCode: "region_unavailable"}
	}
	animeSn := func(url string) int {
		_, b, e := p.Probe(ctx, url)
		if e != nil {
			return 0
		}
		var tk struct {
			AnimeSn int `json:"animeSn"`
		}
		_ = json.Unmarshal([]byte(b), &tk)
		return tk.AnimeSn
	}
	if animeSn("https://ani.gamer.com.tw/ajax/token.php?adID=89422&sn=37783&device="+d.Deviceid) != 0 ||
		animeSn("https://ani.gamer.com.tw/ajax/token.php?adID=89422&sn=38832&device="+d.Deviceid) != 0 {
		return Status{State: "unlocked", Region: "tw"}
	}
	// Not TW: report the detected region as restricted (oversea).
	_, b4, _ := p.Probe(ctx, "https://ani.gamer.com.tw/cdn-cgi/trace")
	if m := reTraceLoc.FindStringSubmatch(b4); m != nil {
		return Status{State: "restricted", Region: strings.ToLower(m[1]), DetailCode: "oversea_only"}
	}
	return Status{State: "blocked", DetailCode: "region_unavailable"}
}

// ── Disney+ Hotstar ──────────────────────────────────────────────────────────
// Source: MediaUnlockTest checks/HotStar.go. The page API returns 401 with a
// Location header that embeds the region when available, 472-474 when banned,
// 475 when not available in that region.
func checkHotstar(ctx context.Context, p Prober) Status {
	fp, ok := p.(FullProber)
	if !ok {
		return Status{State: "error", DetailCode: "probe_unsupported"}
	}
	r, err := fp.ProbeFull(ctx, ProbeRequest{
		URL: "https://api.hotstar.com/o/v1/page/1557?offset=0&size=20&tao=0&tas=20",
	})
	if err != nil {
		return errStatus(err)
	}
	switch r.Status {
	case 200:
		return Status{State: "unlocked", HTTPStatus: 200}
	case 401:
		loc := r.Header.Get("Location")
		if seg := locSegment(loc, 3); seg != "" {
			return Status{State: "unlocked", HTTPStatus: 401, Region: strings.ToLower(seg)}
		}
		return Status{State: "blocked", HTTPStatus: 401, DetailCode: "region_unavailable"}
	case 472, 473, 474:
		return Status{State: "blocked", HTTPStatus: r.Status, DetailCode: "blocked_status"}
	case 475:
		return Status{State: "blocked", HTTPStatus: 475, DetailCode: "region_unavailable"}
	default:
		return Status{State: "error", HTTPStatus: r.Status, DetailCode: "unexpected_status"}
	}
}
