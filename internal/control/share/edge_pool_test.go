package share

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

func edgeURIs(t *testing.T, r *Resolver, token string) []string {
	t.Helper()
	var out []string
	for _, u := range strings.Split(joined(t, r, token), "\n") {
		if strings.Contains(u, "a1b2") {
			out = append(out, u)
		}
	}
	return out
}

func TestEdgePool_FansOutOneNodePerAddress(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	pool := []string{"103.31.4.230", "162.159.129.27"}
	r := NewResolver(st, "1.2.3.4", pool, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com"})
	got := edgeURIs(t, r, token)
	if len(got) != 3 {
		t.Fatalf("want domain node + 2 preferred nodes, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], "@cdn.example.com:443") {
		t.Errorf("first node should still dial the edge domain:\n%s", got[0])
	}
	names := map[string]bool{}
	for i, addr := range pool {
		u := got[i+1]
		for _, want := range []string{"@" + addr + ":443", "sni=cdn.example.com", "host=cdn.example.com", "security=tls"} {
			if !strings.Contains(u, want) {
				t.Errorf("preferred node %d missing %q:\n%s", i+1, want, u)
			}
		}
		if !strings.Contains(u, fmt.Sprintf("-CF%d", i+1)) {
			t.Errorf("preferred node %d not numbered:\n%s", i+1, u)
		}
		names[u[strings.LastIndex(u, "#"):]] = true
	}
	if len(names) != len(pool) {
		t.Errorf("preferred node names collide: %v", names)
	}
}

func TestEdgePool_KeptInLowProfileMode(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	r := NewResolver(st, "1.2.3.4", []string{"103.31.4.230"}, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com", Stealth: true})
	got := joined(t, r, token)
	if !strings.Contains(got, "@103.31.4.230:443") {
		t.Fatalf("low-profile mode dropped the preferred node:\n%s", got)
	}
	if strings.Contains(got, "security=reality") || strings.Contains(got, "hysteria2://") {
		t.Fatalf("low-profile mode leaked a direct inbound:\n%s", got)
	}
}

func TestEdgePool_NoPoolLeavesOutputUnchanged(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	ab := antiblock.Config{EdgeDomain: "cdn.example.com"}
	without := joined(t, NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{}).WithAntiBlock(ab), token)
	blank := joined(t, NewResolver(st, "1.2.3.4", []string{" ", ""}, "", core.NodeCapability{}).WithAntiBlock(ab), token)
	if without != blank {
		t.Fatalf("blank pool entries changed the subscription:\n%s\n---\n%s", without, blank)
	}
}

func TestEdgePool_DirectInboundsUntouched(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	got := joined(t, NewResolver(st, "1.2.3.4", []string{"103.31.4.230"}, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com"}), token)
	for _, u := range strings.Split(got, "\n") {
		if strings.Contains(u, "103.31.4.230") && !strings.Contains(u, "a1b2") {
			t.Errorf("a non-edge inbound picked up a preferred address:\n%s", u)
		}
	}
}

func TestEdgePool_Capped(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	var pool []string
	for i := 1; i <= maxEdgePoolNodes+4; i++ {
		pool = append(pool, fmt.Sprintf("104.16.0.%d", i))
	}
	r := NewResolver(st, "1.2.3.4", pool, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com"})
	if got := len(edgeURIs(t, r, token)); got != maxEdgePoolNodes+1 {
		t.Fatalf("want %d edge nodes, got %d", maxEdgePoolNodes+1, got)
	}
}

func TestEdgePool_BundlesCarryAddressAsServer(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedEdge(t, st, nodeID)
	r := NewResolver(st, "1.2.3.4", []string{"103.31.4.230"}, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com"})
	bs, err := r.ResolveBundles(token)
	if err != nil {
		t.Fatal(err)
	}
	var hosts []string
	for _, b := range bs {
		if b.Inbound.Tag == "ws-edge" {
			hosts = append(hosts, b.EffectiveHost+"|"+remarkOf(b.Inbound, b.Client))
		}
	}
	if len(hosts) != 2 || !strings.HasPrefix(hosts[0], "cdn.example.com|") || !strings.HasPrefix(hosts[1], "103.31.4.230|") {
		t.Fatalf("unexpected edge bundles: %v", hosts)
	}
	if hosts[0][strings.Index(hosts[0], "|"):] == hosts[1][strings.Index(hosts[1], "|"):] {
		t.Fatalf("bundle names collide: %v", hosts)
	}
}
