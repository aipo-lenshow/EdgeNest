package xray

import (
	"encoding/json"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

func xrayInbound() core.InboundSpec {
	return core.InboundSpec{
		Engine: core.EngineXray, Type: "vless-xhttp", Tag: "vx",
		Listen: "::", Port: 443,
		Settings: map[string]any{
			"sni": "www.microsoft.com", "reality_private_key": "PRIV",
			"short_ids": []string{"ab"}, "xhttp_path": "/p",
		},
		Clients: []core.ClientSpec{{Email: "u@x", UUID: "uuid-1", Flow: "xtls-rprx-vision"}},
	}
}

// stats/api/policy must appear only when XRayAPI is set, expose the StatsService
// on the loopback api.listen, enable level-0 per-user counters, and never
// perturb routing (behaviour-neutral telemetry).
func TestRender_XRayAPI(t *testing.T) {
	base := core.DesiredConfig{Inbounds: []core.InboundSpec{xrayInbound()}}

	// Absent by default.
	off := mustRender(t, base)
	for _, k := range []string{"api", "stats", "policy"} {
		if _, ok := off[k]; ok {
			t.Errorf("%s should be absent when XRayAPI is nil", k)
		}
	}

	// Present when configured.
	on := base
	on.XRayAPI = &core.XRayAPISpec{Controller: "127.0.0.1:9092"}
	doc := mustRender(t, on)

	api, ok := doc["api"].(map[string]any)
	if !ok {
		t.Fatalf("api missing when XRayAPI set")
	}
	if api["listen"] != "127.0.0.1:9092" {
		t.Errorf("api.listen = %v", api["listen"])
	}
	svcs, _ := api["services"].([]any)
	if len(svcs) != 1 || svcs[0] != "StatsService" {
		t.Errorf("api.services = %v, want [StatsService]", api["services"])
	}
	if _, ok := doc["stats"].(map[string]any); !ok {
		t.Errorf("stats missing")
	}
	pol, ok := doc["policy"].(map[string]any)
	if !ok {
		t.Fatalf("policy missing")
	}
	lvl0 := pol["levels"].(map[string]any)["0"].(map[string]any)
	if lvl0["statsUserUplink"] != true || lvl0["statsUserDownlink"] != true {
		t.Errorf("level-0 user stats not enabled: %+v", lvl0)
	}

	// Routing identical with/without the telemetry block.
	offRb, _ := json.Marshal(off["routing"]); offR := string(offRb)
	onRb, _ := json.Marshal(doc["routing"]); onR := string(onRb)
	if offR != onR {
		t.Errorf("routing changed when xray_api added:\noff=%s\non=%s", offR, onR)
	}
}
