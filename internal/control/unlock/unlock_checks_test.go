package unlock

import (
	"context"
	"net/http"
	"testing"
)

// fullStub implements both Prober and FullProber so the bespoke checks can be
// driven without real HTTP. Keyed by URL; ProbeFull ignores method/body.
type fullStub struct {
	resp map[string]ProbeResult
}

func (s *fullStub) Probe(_ context.Context, url string) (int, string, error) {
	r, ok := s.resp[url]
	if !ok {
		return 0, "", nil
	}
	return r.Status, r.Body, nil
}

func (s *fullStub) ProbeFull(_ context.Context, req ProbeRequest) (ProbeResult, error) {
	return s.resp[req.URL], nil
}

func hdr(loc string) http.Header {
	h := http.Header{}
	if loc != "" {
		h.Set("Location", loc)
	}
	return h
}

func TestCheckNetflix_ThreeState(t *testing.T) {
	ctx := context.Background()

	// Full unlock: a licensed title plays (200/301), region from third title.
	full := &fullStub{resp: map[string]ProbeResult{
		nfTitleLicensed1: {Status: 200},
		nfTitleLicensed2: {Status: 200},
		nfTitleRegion:    {Status: 301, Header: hdr("https://www.netflix.com/sg-en/title/80018499")},
	}}
	if s := checkNetflix(ctx, full); s.State != "unlocked" || s.Region != "sg" {
		t.Errorf("full: state=%q region=%q, want unlocked/sg", s.State, s.Region)
	}

	// Originals only: both licensed titles 404.
	orig := &fullStub{resp: map[string]ProbeResult{
		nfTitleLicensed1: {Status: 404},
		nfTitleLicensed2: {Status: 404},
	}}
	if s := checkNetflix(ctx, orig); s.State != "originals_only" {
		t.Errorf("originals: state=%q, want originals_only", s.State)
	}

	// Banned: both 403.
	ban := &fullStub{resp: map[string]ProbeResult{
		nfTitleLicensed1: {Status: 403},
		nfTitleLicensed2: {Status: 403},
	}}
	if s := checkNetflix(ctx, ban); s.State != "blocked" {
		t.Errorf("banned: state=%q, want blocked", s.State)
	}
}

func TestCheckPrimeVideo(t *testing.T) {
	ctx := context.Background()
	ok := &fullStub{resp: map[string]ProbeResult{
		"https://www.primevideo.com/": {Status: 200, Body: `...,"currentTerritory":"US",...`},
	}}
	if s := checkPrimeVideo(ctx, ok); s.State != "unlocked" || s.Region != "us" {
		t.Errorf("prime ok: %q/%q want unlocked/us", s.State, s.Region)
	}
	no := &fullStub{resp: map[string]ProbeResult{
		"https://www.primevideo.com/": {Status: 200, Body: "blocked page"},
	}}
	if s := checkPrimeVideo(ctx, no); s.State != "blocked" {
		t.Errorf("prime no: %q want blocked", s.State)
	}
}

func TestCheckSpotify(t *testing.T) {
	ctx := context.Background()
	url := "https://spclient.wg.spotify.com/signup/public/v1/account"
	ok := &fullStub{resp: map[string]ProbeResult{
		url: {Status: 200, Body: `{"status":311,"country":"HK","is_country_launched":true}`},
	}}
	if s := checkSpotify(ctx, ok); s.State != "unlocked" || s.Region != "hk" {
		t.Errorf("spotify ok: %q/%q want unlocked/hk", s.State, s.Region)
	}
	ban := &fullStub{resp: map[string]ProbeResult{url: {Status: 403}}}
	if s := checkSpotify(ctx, ban); s.State != "blocked" {
		t.Errorf("spotify 403: %q want blocked", s.State)
	}
}

func TestCheckTikTok(t *testing.T) {
	ctx := context.Background()
	url := "https://www.tiktok.com/explore"
	ok := &fullStub{resp: map[string]ProbeResult{
		url: {Status: 200, Body: `...,"region":"JP",...`},
	}}
	if s := checkTikTok(ctx, ok); s.State != "unlocked" || s.Region != "jp" {
		t.Errorf("tiktok ok: %q/%q want unlocked/jp", s.State, s.Region)
	}
	no := &fullStub{resp: map[string]ProbeResult{
		url: {Status: 200, Body: "https://www.tiktok.com/hk/notfound"},
	}}
	if s := checkTikTok(ctx, no); s.State != "blocked" {
		t.Errorf("tiktok notfound: %q want blocked", s.State)
	}
}

func TestCheckDazn(t *testing.T) {
	ctx := context.Background()
	url := "https://startup.core.indazn.com/misl/v5/Startup"
	ok := &fullStub{resp: map[string]ProbeResult{
		url: {Status: 200, Body: `{"Region":{"isAllowed":true,"GeolocatedCountry":"JP"}}`},
	}}
	if s := checkDazn(ctx, ok); s.State != "unlocked" || s.Region != "jp" {
		t.Errorf("dazn ok: %q/%q want unlocked/jp", s.State, s.Region)
	}
	no := &fullStub{resp: map[string]ProbeResult{
		url: {Status: 200, Body: `{"Region":{"isAllowed":false,"GeolocatedCountry":"CN"}}`},
	}}
	if s := checkDazn(ctx, no); s.State != "restricted" {
		t.Errorf("dazn no: %q want restricted", s.State)
	}
}

func TestCheckAbema(t *testing.T) {
	ctx := context.Background()
	url := "https://api.abema.io/v1/ip/check?device=android"
	jp := &fullStub{resp: map[string]ProbeResult{url: {Status: 200, Body: `{"isoCountryCode":"JP"}`}}}
	if s := checkAbema(ctx, jp); s.State != "unlocked" || s.Region != "jp" {
		t.Errorf("abema jp: %q/%q want unlocked/jp", s.State, s.Region)
	}
	us := &fullStub{resp: map[string]ProbeResult{url: {Status: 200, Body: `{"isoCountryCode":"US"}`}}}
	if s := checkAbema(ctx, us); s.State != "restricted" {
		t.Errorf("abema us: %q want restricted", s.State)
	}
	ban := &fullStub{resp: map[string]ProbeResult{url: {Status: 403}}}
	if s := checkAbema(ctx, ban); s.State != "blocked" {
		t.Errorf("abema 403: %q want blocked", s.State)
	}
}

func TestCheckBilibili(t *testing.T) {
	ctx := context.Background()
	chk := bilibiliCheck(biliHKURL, "hk")
	ok := &fullStub{resp: map[string]ProbeResult{biliHKURL: {Status: 200, Body: `{"code":0}`}}}
	if s := chk(ctx, ok); s.State != "unlocked" || s.Region != "hk" {
		t.Errorf("bili ok: %q/%q want unlocked/hk", s.State, s.Region)
	}
	no := &fullStub{resp: map[string]ProbeResult{biliHKURL: {Status: 200, Body: `{"code":-10403}`}}}
	if s := chk(ctx, no); s.State != "blocked" {
		t.Errorf("bili geoblock: %q want blocked", s.State)
	}
	rej := &fullStub{resp: map[string]ProbeResult{biliHKURL: {Status: 412}}}
	if s := chk(ctx, rej); s.State != "blocked" {
		t.Errorf("bili 412: %q want blocked", s.State)
	}
}

func TestCheckHotstar(t *testing.T) {
	ctx := context.Background()
	url := "https://api.hotstar.com/o/v1/page/1557?offset=0&size=20&tao=0&tas=20"
	ok := &fullStub{resp: map[string]ProbeResult{
		url: {Status: 401, Header: hdr("https://www.hotstar.com/in/home")},
	}}
	if s := checkHotstar(ctx, ok); s.State != "unlocked" || s.Region != "in" {
		t.Errorf("hotstar ok: %q/%q want unlocked/in", s.State, s.Region)
	}
	ban := &fullStub{resp: map[string]ProbeResult{url: {Status: 473}}}
	if s := checkHotstar(ctx, ban); s.State != "blocked" {
		t.Errorf("hotstar ban: %q want blocked", s.State)
	}
	no := &fullStub{resp: map[string]ProbeResult{url: {Status: 475}}}
	if s := checkHotstar(ctx, no); s.State != "blocked" {
		t.Errorf("hotstar 475: %q want blocked", s.State)
	}
}

func TestCheckBahamut(t *testing.T) {
	ctx := context.Background()
	dev := "https://ani.gamer.com.tw/ajax/getdeviceid.php"
	tok1 := "https://ani.gamer.com.tw/ajax/token.php?adID=89422&sn=37783&device=dev123"
	tw := &fullStub{resp: map[string]ProbeResult{
		dev:  {Status: 200, Body: `{"deviceid":"dev123"}`},
		tok1: {Status: 200, Body: `{"animeSn":37783}`},
	}}
	if s := checkBahamut(ctx, tw); s.State != "unlocked" || s.Region != "tw" {
		t.Errorf("bahamut tw: %q/%q want unlocked/tw", s.State, s.Region)
	}
}
