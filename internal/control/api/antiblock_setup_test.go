package api

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func TestEnsureEdgeInbound(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := st.EnsureLocalNode()
	h := &Handler{store: st, localNodeID: strconv.Itoa(int(n.ID))}

	reality := &model.Inbound{NodeID: n.ID, Tag: "r", Engine: "singbox", Type: "vless", Listen: "::", Port: 8443, Network: "tcp", Enabled: true, Settings: `{}`}
	hy2 := &model.Inbound{NodeID: n.ID, Tag: "h", Engine: "singbox", Type: "hysteria2", Listen: "::", Port: 41020, Network: "udp", Enabled: true, Settings: `{}`}
	for _, in := range []*model.Inbound{reality, hy2} {
		if err := st.CreateInbound(in); err != nil {
			t.Fatal(err)
		}
	}
	// alice on both (UUID on Reality), bob only on Hy2 (no UUID), carol disabled.
	add := func(in *model.Inbound, email, id string, enabled bool) uint {
		c := &model.Client{InboundID: in.ID, Email: email, UUID: id, Password: "p", Enabled: enabled, QuotaBytes: 7}
		if err := st.CreateClient(c); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	aliceID := add(reality, "alice", "11111111-1111-1111-1111-111111111111", true)
	add(hy2, "alice", "", true)
	bobID := add(hy2, "bob", "", true)
	add(reality, "carol", "33333333-3333-3333-3333-333333333333", false)
	scoped, _ := json.Marshal([]uint{reality.ID, hy2.ID})
	_ = st.CreateSubscription(&model.Subscription{Name: "a", TokenHash: "h1", ClientID: aliceID, AllowedInbounds: string(scoped)})
	_ = st.CreateSubscription(&model.Subscription{Name: "b", TokenHash: "h2", ClientID: bobID, AllowedInbounds: "[]"})

	created, added, err := h.ensureEdgeInbound()
	if err != nil || !created || added != 3 {
		t.Fatalf("created=%v added=%d err=%v", created, added, err)
	}
	ins, _ := st.ListInbounds(n.ID)
	edge := ins[len(ins)-1]
	if edge.Type != "vless-ws" || edge.Listen != "127.0.0.1" || edge.Port < 20000 {
		t.Fatalf("edge inbound: %+v", edge)
	}
	var s map[string]any
	_ = json.Unmarshal([]byte(edge.Settings), &s)
	if s["edge_bound"] != "true" || s["ws_path"] == "" {
		t.Fatalf("settings: %v", s)
	}
	cl, _ := st.ListClients(edge.ID)
	byEmail := map[string]model.Client{}
	for _, c := range cl {
		byEmail[c.Email] = c
	}
	if byEmail["alice"].UUID != "11111111-1111-1111-1111-111111111111" {
		t.Error("alice should keep her UUID")
	}
	if byEmail["bob"].UUID == "" || byEmail["carol"].Enabled || byEmail["alice"].QuotaBytes != 7 {
		t.Errorf("seeding wrong: %+v", byEmail)
	}
	subs, _ := st.ListSubscriptions()
	for _, sub := range subs {
		var ids []uint
		_ = json.Unmarshal([]byte(sub.AllowedInbounds), &ids)
		switch sub.Name {
		case "a":
			if len(ids) != 3 || ids[2] != edge.ID {
				t.Errorf("scoped subscription not extended: %v", ids)
			}
		case "b":
			if len(ids) != 0 {
				t.Errorf("all-inbounds subscription must stay empty: %v", ids)
			}
		}
	}
	if created, _, _ := h.ensureEdgeInbound(); created {
		t.Fatal("second run created another CDN node")
	}
}
