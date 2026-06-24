package store

import (
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// TestCreateInbound_RespectsEnabledFalse locks in the fix for the GORM
// default:true gotcha: model.Inbound.Enabled has `gorm:"default:true"`, and
// without an explicit Select("*") the zero-value `false` gets silently
// overridden by the SQL DDL default on insert. We rely on enabled=false at
// create time when the API wants to stage an inbound for client population
// before flipping it on.
func TestCreateInbound_RespectsEnabledFalse(t *testing.T) {
	st := openTestStore(t)

	in := &model.Inbound{
		NodeID: 1, Tag: "stage-vless", Engine: "singbox", Type: "vless",
		Listen: "::", Port: 18443, Network: "tcp",
		Enabled: false,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.GetInbound(in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Enabled {
		t.Fatal("Enabled flipped from false to true (GORM default:true regression)")
	}

	in2 := &model.Inbound{
		NodeID: 1, Tag: "on-vless", Engine: "singbox", Type: "vless",
		Listen: "::", Port: 18444, Network: "tcp",
		Enabled: true,
	}
	if err := st.CreateInbound(in2); err != nil {
		t.Fatalf("create2: %v", err)
	}
	got2, _ := st.GetInbound(in2.ID)
	if !got2.Enabled {
		t.Fatal("Enabled true round-trip broken")
	}
}

// TestCreateClient_RespectsEnabledFalse mirrors the inbound test for clients
// (same model.Client field has the same gorm:"default:true" trap).
func TestCreateClient_RespectsEnabledFalse(t *testing.T) {
	st := openTestStore(t)

	in := &model.Inbound{
		NodeID: 1, Tag: "for-client", Engine: "singbox", Type: "vless",
		Listen: "::", Port: 18445, Network: "tcp", Enabled: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	c := &model.Client{
		InboundID: in.ID, Email: "off@smoke.test",
		UUID: "11111111-1111-1111-1111-111111111111",
		Enabled: false,
	}
	if err := st.CreateClient(c); err != nil {
		t.Fatalf("create client: %v", err)
	}
	got, err := st.GetClient(c.ID)
	if err != nil {
		t.Fatalf("get client: %v", err)
	}
	if got.Enabled {
		t.Fatal("client Enabled flipped from false to true")
	}
}

// TestNextSeqEmail walks the NNN@EdgeNest.Local sequence past the highest
// existing one and ignores non-sequence (custom) identifiers.
func TestNextSeqEmail(t *testing.T) {
	st := openTestStore(t)

	// Empty DB → 001.
	if got, _ := st.NextSeqEmail(); got != "001@EdgeNest.Local" {
		t.Fatalf("empty DB: want 001@EdgeNest.Local, got %q", got)
	}

	in := &model.Inbound{
		NodeID: 1, Tag: "vless-1", Engine: "singbox", Type: "vless",
		Listen: "::", Port: 19443, Network: "tcp", Enabled: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	for _, email := range []string{"002@EdgeNest.Local", "custom@example.com", "005@edgenest.local"} {
		if err := st.CreateClient(&model.Client{InboundID: in.ID, Email: email, Enabled: true}); err != nil {
			t.Fatalf("create client %s: %v", email, err)
		}
	}
	// Highest sequence is 005 (case-insensitive domain); custom@ ignored → 006.
	if got, _ := st.NextSeqEmail(); got != "006@EdgeNest.Local" {
		t.Errorf("want 006@EdgeNest.Local, got %q", got)
	}
}

// TestAddUserTraffic_LandsOnRepresentative confirms per-user bytes accumulate on
// the lowest-id client of an email, so summing the user's clients gives the
// correct total without double-counting.
func TestAddUserTraffic_LandsOnRepresentative(t *testing.T) {
	st := openTestStore(t)
	// One user (u@x) spread across two inbounds — one credential row per inbound.
	for i, port := range []int{19543, 19544} {
		in := &model.Inbound{
			NodeID: 1, Tag: "vless-t" + string(rune('a'+i)), Engine: "singbox", Type: "vless",
			Listen: "::", Port: port, Network: "tcp", Enabled: true,
		}
		if err := st.CreateInbound(in); err != nil {
			t.Fatalf("create inbound: %v", err)
		}
		if err := st.CreateClient(&model.Client{InboundID: in.ID, Email: "u@x", Enabled: true}); err != nil {
			t.Fatalf("create client: %v", err)
		}
	}
	if err := st.AddUserTraffic("u@x", 100, 50); err != nil {
		t.Fatalf("add traffic: %v", err)
	}
	rows, _ := st.ClientsByEmail("u@x")
	var total int64
	for _, c := range rows {
		total += c.TrafficUp + c.TrafficDown
	}
	if total != 150 {
		t.Errorf("want summed total 150 across user's clients, got %d", total)
	}
	// No clients with that email → silent no-op, not an error.
	if err := st.AddUserTraffic("ghost@x", 10, 10); err != nil {
		t.Errorf("AddUserTraffic for missing email should be nil, got %v", err)
	}
}
