package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/cdnstats"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/share"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func postReport(t *testing.T, h *Handler, token, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/sub/"+token+"/cdn-report", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "token", Value: token}}
	h.CDNReport(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func fetchSubHeaders(t *testing.T, h *Handler, token, net string) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/sub/"+token, nil)
	if net != "" {
		c.Request.Header.Set("X-EdgeNest-Net", net)
	}
	c.Params = gin.Params{{Key: "token", Value: token}}
	h.PublicSubscription(c)
	out := map[string]string{}
	for _, k := range []string{"X-EdgeNest-CDN-Candidates", "X-EdgeNest-CDN-Recommended", "X-EdgeNest-CDN-Policy", "X-EdgeNest-CDN-Down"} {
		out[k] = w.Header().Get(k)
	}
	return out
}

func enableCDN(t *testing.T, h *Handler) {
	t.Helper()
	if code, env := callJSON(t, h.PutAdvancedCDN,
		`{"cdn_enabled":true,"cdn_preferred_ips":["198.51.100.9"]}`); code != 200 || !env.Success {
		t.Fatalf("enable CDN: code=%d err=%q", code, errCode(env))
	}
}

const goodRound = `{"net":"CN|联通|北方","net_label":"联通·北方","link":"wifi","control_ms":60,
 "results":[{"ip":"198.51.100.9","ok":true,"ms":300},{"ip":"103.31.4.230","ok":true,"ms":120}]}`

func TestCDNReport_RefusesUnknownOrRevokedToken(t *testing.T) {
	h := newAdvancedTestHandler(t)
	enableCDN(t, h)
	if code, _ := postReport(t, h, "nope", goodRound); code != 404 {
		t.Fatalf("unknown token: %d", code)
	}
	token := seedSubscription(t, h)
	sub, _ := h.store.GetSubscriptionByTokenHash(store.HashToken(token))
	if err := h.store.RevokeSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := postReport(t, h, token, goodRound); code != 404 {
		t.Fatalf("revoked token: %d", code)
	}
}

func TestCDNReport_PoolsAGoodRoundAndDropsAPoorOne(t *testing.T) {
	h := newAdvancedTestHandler(t)
	token := seedSubscription(t, h)
	if _, out := postReport(t, h, token, goodRound); out["reason"] != "cdn_off" {
		t.Fatalf("CDN off: %v", out)
	}
	enableCDN(t, h)
	poor := strings.Replace(goodRound, `"control_ms":60`, `"control_ms":0`, 1)
	if _, out := postReport(t, h, token, poor); out["accepted"] != false || out["reason"] != cdnstats.RejectNetworkPoor {
		t.Fatalf("poor network round: %v", out)
	}
	if _, out := postReport(t, h, token, goodRound); out["accepted"] != true {
		t.Fatalf("good round: %v", out)
	}
	rows, err := h.store.CDNStatsSince("2000-01-01", "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if _, out := postReport(t, h, token, goodRound); out["accepted"] != false || out["reason"] != "too_soon" {
		t.Fatalf("an immediate second automatic round should not pool: %v", out)
	}
}

// The pooled table must carry nothing that points back at a reporter.
func TestCDNStatDaily_HoldsNoReporterIdentity(t *testing.T) {
	want := []string{"ID", "Day", "NetKey", "IP", "NetLabel", "Samples", "OK", "MsSum", "UpdatedAt"}
	typ := reflect.TypeOf(model.CDNStatDaily{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, typ.Field(i).Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CDNStatDaily fields changed to %v; anything identifying a reporter needs a compliance review (retention, DSAR, disclosure)", got)
	}
}

func TestSubscription_CandidatesFollowTheClientsNetwork(t *testing.T) {
	h := newAdvancedTestHandler(t)
	token := seedSubscription(t, h)
	enableCDN(t, h)
	day := cdnstats.Day(time.Now())
	for i := 0; i < 3; i++ {
		if err := h.store.AddCDNSamples(day, "CN|联通|北方", "联通·北方", []store.CDNSample{
			{IP: "188.114.96.31", OK: true, Ms: 90},
			{IP: "198.51.100.9", OK: true, Ms: 400},
		}); err != nil {
			t.Fatal(err)
		}
	}
	hd := fetchSubHeaders(t, h, token, "CN|联通|北方")
	if !strings.HasPrefix(hd["X-EdgeNest-CDN-Candidates"], "188.114.96.31,198.51.100.9,") {
		t.Fatalf("candidates %q: want this network's ranking first", hd["X-EdgeNest-CDN-Candidates"])
	}
	if hd["X-EdgeNest-CDN-Recommended"] != "188.114.96.31,198.51.100.9" {
		t.Fatalf("recommended %q", hd["X-EdgeNest-CDN-Recommended"])
	}
	if hd["X-EdgeNest-CDN-Policy"] != cdnstats.DefaultPolicy.Header() {
		t.Fatalf("policy %q", hd["X-EdgeNest-CDN-Policy"])
	}
	other := fetchSubHeaders(t, h, token, "CN|电信|华南")
	if other["X-EdgeNest-CDN-Recommended"] != "" || !strings.HasPrefix(other["X-EdgeNest-CDN-Candidates"], "198.51.100.9,") {
		t.Fatalf("a network without data keeps the default order: %v", other)
	}
}

func TestEnablingCDNWithAnEmptyPoolStartsFromTheDefaults(t *testing.T) {
	h := newAdvancedTestHandler(t)
	if code, env := callJSON(t, h.PutAdvancedCDN, `{"cdn_enabled":true,"cdn_preferred_ips":[]}`); code != 200 || !env.Success {
		t.Fatalf("code=%d err=%q", code, errCode(env))
	}
	got := h.cdnPoolForLocalNode()
	if !reflect.DeepEqual(got, share.RecommendedCDNIPs()) {
		t.Fatalf("pool %v, want the defaults", got)
	}
}

func TestCDNPolicy_RoundTripAndBounds(t *testing.T) {
	h := newAdvancedTestHandler(t)
	if code, _ := callJSON(t, h.PutCDNPolicy, `{"interval_sec":5,"same_rounds":3,"cooldown_sec":60}`); code != 400 {
		t.Fatalf("interval below the floor accepted: %d", code)
	}
	if code, env := callJSON(t, h.PutCDNPolicy, `{"interval_sec":300,"same_rounds":2,"cooldown_sec":7200}`); code != 200 || !env.Success {
		t.Fatalf("code=%d err=%q", code, errCode(env))
	}
	if got := cdnstats.LoadPolicy(h.store); got != (cdnstats.Policy{IntervalSec: 300, SameRounds: 2, CooldownSec: 7200}) {
		t.Fatalf("stored %+v", got)
	}
}
