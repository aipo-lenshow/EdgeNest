package singbox

import (
	"encoding/json"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// v2ray_api must appear under experimental only when V2RayAPI is set, must list
// exactly the rendered client emails (sorted, deduped), and must never perturb
// routing/outbounds (behaviour-neutral telemetry, like clash_api).
func TestRender_V2RayAPI(t *testing.T) {
	base := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(
				core.ClientSpec{Email: "bob@example.com", UUID: "22222222-2222-2222-2222-222222222222"},
				core.ClientSpec{Email: "alice@example.com", UUID: "11111111-1111-1111-1111-111111111111"},
			),
		},
	}

	// Absent by default.
	b, err := render(base)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var off map[string]any
	_ = json.Unmarshal(b, &off)
	if _, ok := off["experimental"]; ok {
		t.Errorf("experimental should be absent when V2RayAPI is nil")
	}

	// Present when configured.
	on := base
	on.V2RayAPI = &core.V2RayAPISpec{Controller: "127.0.0.1:9091"}
	b, err = render(on)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	exp, ok := doc["experimental"].(map[string]any)
	if !ok {
		t.Fatalf("experimental missing when V2RayAPI set")
	}
	va, ok := exp["v2ray_api"].(map[string]any)
	if !ok {
		t.Fatalf("v2ray_api missing")
	}
	if va["listen"] != "127.0.0.1:9091" {
		t.Errorf("listen wrong: %v", va["listen"])
	}
	stats, ok := va["stats"].(map[string]any)
	if !ok {
		t.Fatalf("stats missing")
	}
	if stats["enabled"] != true {
		t.Errorf("stats.enabled want true, got %v", stats["enabled"])
	}
	users, ok := stats["users"].([]any)
	if !ok {
		t.Fatalf("stats.users missing/not array: %v", stats["users"])
	}
	got := make([]string, len(users))
	for i, u := range users {
		got[i] = u.(string)
	}
	// Sorted + deduped across all rendered clients.
	want := []string{"alice@example.com", "bob@example.com"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("stats.users = %v, want %v (sorted)", got, want)
	}

	// Routing must be identical with/without v2ray_api (behaviour-neutral).
	offRoute, _ := json.Marshal(off["route"])
	onRoute, _ := json.Marshal(doc["route"])
	if string(offRoute) != string(onRoute) {
		t.Errorf("route changed when v2ray_api added:\noff=%s\non=%s", offRoute, onRoute)
	}
}

// clash_api and v2ray_api must coexist under experimental — neither clobbers the
// other (regression guard for the shared-map merge).
func TestRender_ClashAndV2RayCoexist(t *testing.T) {
	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(core.ClientSpec{Email: "u@x", UUID: "33333333-3333-3333-3333-333333333333"}),
		},
		ClashAPI: &core.ClashAPISpec{Controller: "127.0.0.1:9090", Secret: "s"},
		V2RayAPI: &core.V2RayAPISpec{Controller: "127.0.0.1:9091"},
	}
	b, err := render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	exp := doc["experimental"].(map[string]any)
	if _, ok := exp["clash_api"]; !ok {
		t.Errorf("clash_api missing when both set")
	}
	if _, ok := exp["v2ray_api"]; !ok {
		t.Errorf("v2ray_api missing when both set")
	}
}
