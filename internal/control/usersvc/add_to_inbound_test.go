package usersvc

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/share"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

func TestAddToInbound_MirrorsUserAndWidensSubscription(t *testing.T) {
	st, nodeID := newTestStore(t)
	reality := seedInbound(t, st, nodeID, "reality", "vless", 8443)
	applied, audited := 0, 0
	svc := newSvc(st, &applied, &audited)
	ctx := context.Background()

	alice, err := svc.Create(ctx, nodeID, CreateParams{Email: "alice@x", QuotaBytes: 5 << 30, ExpiryDays: 30, InboundIDs: []uint{reality.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, nodeID, CreateParams{Email: "bob@x", InboundIDs: []uint{reality.ID}}); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := svc.Update(ctx, nodeID, "bob@x", UpdateParams{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	argo := seedInbound(t, st, nodeID, "argo", "vless-ws", 2083)
	applied = 0

	res, err := svc.AddToInbound(ctx, nodeID, argo.ID, []string{"alice@x", "bob@x", "nobody@x", "alice@x", "seed@reality"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Added, []string{"alice@x", "bob@x", "seed@reality"}) || !reflect.DeepEqual(res.Unknown, []string{"nobody@x"}) {
		t.Fatalf("result = %+v", res)
	}
	if applied != 1 {
		t.Fatalf("applied %d times, want once", applied)
	}

	byInbound := func(email string) map[uint]bool {
		cs, _ := st.ClientsByEmail(email)
		m := map[uint]bool{}
		for _, c := range cs {
			if c.InboundID == argo.ID {
				m[argo.ID] = c.Enabled
				if email == "alice@x" && (c.QuotaBytes != 5<<30 || c.ExpiryAt == 0 || c.UUID == "") {
					t.Errorf("alice's new client didn't mirror quota/expiry/credentials: %+v", c)
				}
			}
		}
		return m
	}
	if en, ok := byInbound("alice@x")[argo.ID]; !ok || !en {
		t.Error("alice not enabled on the new inbound")
	}
	if en, ok := byInbound("bob@x")[argo.ID]; !ok || en {
		t.Error("disabled bob came back enabled on the new inbound")
	}

	// alice's subscription now includes the new inbound.
	sub, err := st.GetSubscription(alice.SubID)
	if err != nil {
		t.Fatal(err)
	}
	var allowed []uint
	_ = json.Unmarshal([]byte(sub.AllowedInbounds), &allowed)
	has := false
	for _, id := range allowed {
		has = has || id == argo.ID
	}
	if sub.AllowedInbounds != "" && !has {
		t.Fatalf("subscription scope %s misses the new inbound", sub.AllowedInbounds)
	}
	bs, err := share.NewResolver(st, "198.51.100.1", nil, "", coreCap()).ResolveBundles(alice.SubToken)
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]bool{}
	for _, b := range bs {
		tags[b.Inbound.Tag] = true
	}
	if !tags["argo"] || !tags["reality"] {
		t.Fatalf("alice's subscription tags = %v", tags)
	}

	// Running it again is a no-op.
	applied = 0
	again, err := svc.AddToInbound(ctx, nodeID, argo.ID, []string{"alice@x"})
	if err != nil || len(again.Added) != 0 || len(again.Already) != 1 || applied != 0 {
		t.Fatalf("second run = %+v %v (applied %d)", again, err, applied)
	}
}

func TestAddToInbound_RejectsUnsupported(t *testing.T) {
	st, nodeID := newTestStore(t)
	ss := seedInbound(t, st, nodeID, "ss", "shadowsocks", 8388)
	applied, audited := 0, 0
	if _, err := newSvc(st, &applied, &audited).AddToInbound(context.Background(), nodeID, ss.ID, []string{"seed@ss"}); err == nil {
		t.Fatal("shadowsocks accepted")
	}
	if _, err := newSvc(st, &applied, &audited).AddToInbound(context.Background(), nodeID, 9999, []string{"x"}); err == nil {
		t.Fatal("missing inbound accepted")
	}
}

func coreCap() core.NodeCapability { return core.NodeCapability{IPv4: true} }
