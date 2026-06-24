package store

import (
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// TestDeleteInbound_ScrubsSubscriptionRefs locks the BUG-1 cascade: deleting
// an inbound removes its ID from every subscription's allowed_inbounds —
// EXCEPT when it is the last entry, which stays dangling on purpose. An
// empty allowed_inbounds means "all inbounds allowed" (decodeAllowedInbounds
// semantics), so scrubbing to "[]" would silently widen a curated bundle.
func TestDeleteInbound_ScrubsSubscriptionRefs(t *testing.T) {
	st := openTestStore(t)

	mk := func(tag string, port int) uint {
		in := &model.Inbound{
			NodeID: 1, Tag: tag, Engine: "singbox", Type: "vless",
			Listen: "127.0.0.1", Port: port, Network: "tcp", Enabled: true,
		}
		if err := st.CreateInbound(in); err != nil {
			t.Fatalf("create %s: %v", tag, err)
		}
		return in.ID
	}
	id1 := mk("scrub-a", 18443)
	id2 := mk("scrub-b", 18444)

	sub := &model.Subscription{
		Name: "scrub-test", Token: "tok", TokenHash: HashToken("tok"),
		ClientID: 1, AllowedNodes: "[]",
		AllowedInbounds: mustIDsJSON(t, id1, id2),
	}
	if err := st.CreateSubscription(sub); err != nil {
		t.Fatalf("create sub: %v", err)
	}
	// An unrelated open-allow-list subscription must come through untouched.
	open := &model.Subscription{
		Name: "open", Token: "tok2", TokenHash: HashToken("tok2"),
		ClientID: 1, AllowedNodes: "[]", AllowedInbounds: "[]",
	}
	if err := st.CreateSubscription(open); err != nil {
		t.Fatalf("create open sub: %v", err)
	}

	if err := st.DeleteInbound(id1); err != nil {
		t.Fatalf("delete id1: %v", err)
	}
	got, _ := st.GetSubscription(sub.ID)
	if got.AllowedInbounds != mustIDsJSON(t, id2) {
		t.Errorf("after deleting id1: allowed_inbounds = %s, want %s",
			got.AllowedInbounds, mustIDsJSON(t, id2))
	}

	// Deleting the LAST referenced inbound keeps the dangling ID (never "[]").
	if err := st.DeleteInbound(id2); err != nil {
		t.Fatalf("delete id2: %v", err)
	}
	got, _ = st.GetSubscription(sub.ID)
	if got.AllowedInbounds == "[]" {
		t.Fatal("scrub emptied allowed_inbounds — this silently flips the bundle to all-inbounds")
	}
	if got.AllowedInbounds != mustIDsJSON(t, id2) {
		t.Errorf("after deleting id2: allowed_inbounds = %s, want dangling %s",
			got.AllowedInbounds, mustIDsJSON(t, id2))
	}

	gotOpen, _ := st.GetSubscription(open.ID)
	if gotOpen.AllowedInbounds != "[]" {
		t.Errorf("open subscription mutated: %s", gotOpen.AllowedInbounds)
	}
}

func mustIDsJSON(t *testing.T, ids ...uint) string {
	t.Helper()
	out := "["
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += uitoa(id)
	}
	return out + "]"
}

func uitoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
