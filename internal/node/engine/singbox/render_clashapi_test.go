package singbox

import (
	"encoding/json"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// clash_api must appear under experimental only when ClashAPI is set, and must
// never perturb routing/outbounds (behaviour-neutral telemetry).
func TestRender_ClashAPI(t *testing.T) {
	base := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			vlessInbound(core.ClientSpec{
				Email: "alice@example.com",
				UUID:  "11111111-1111-1111-1111-111111111111",
			}),
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
		t.Errorf("experimental should be absent when ClashAPI is nil")
	}

	// Present when configured.
	on := base
	on.ClashAPI = &core.ClashAPISpec{Controller: "127.0.0.1:9090", Secret: "s3cr3t"}
	b, err = render(on)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	exp, ok := doc["experimental"].(map[string]any)
	if !ok {
		t.Fatalf("experimental missing when ClashAPI set")
	}
	ca, ok := exp["clash_api"].(map[string]any)
	if !ok {
		t.Fatalf("clash_api missing")
	}
	if ca["external_controller"] != "127.0.0.1:9090" || ca["secret"] != "s3cr3t" {
		t.Errorf("clash_api fields wrong: %v", ca)
	}
	// Routing must be identical with/without clash_api (behaviour-neutral).
	offRoute, _ := json.Marshal(off["route"])
	onRoute, _ := json.Marshal(doc["route"])
	if string(offRoute) != string(onRoute) {
		t.Errorf("route changed when clash_api added:\noff=%s\non=%s", offRoute, onRoute)
	}
}
