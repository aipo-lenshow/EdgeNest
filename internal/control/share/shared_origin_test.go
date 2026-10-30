package share

import (
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// seedSharedOrigin is seedEdge with the CDN inbound also feeding the tunnel.
func seedSharedOrigin(t *testing.T, st *store.Store, nodeID uint) string {
	t.Helper()
	token := seedEdge(t, st, nodeID)
	in, err := st.GetInboundByTag("ws-edge")
	if err != nil {
		t.Fatal(err)
	}
	in.Settings = `{"ws_path":"/a1b2","edge_bound":"true","argo_bound":"true"}`
	if err := st.UpdateInbound(in); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestSharedOrigin_CDNNodesPlusOneArgoNode(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedSharedOrigin(t, st, nodeID)
	pool := []string{"103.31.4.230", "162.159.129.27"}
	r := NewResolver(st, "1.2.3.4", pool, "tunnel.example.com", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com"})
	got := edgeURIs(t, r, token)
	if len(got) != 3 {
		t.Fatalf("want 2 CDN nodes + 1 Argo node, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i, addr := range pool {
		if !strings.Contains(got[i], "@"+addr+":443") || !strings.Contains(got[i], "sni=cdn.example.com") {
			t.Errorf("CDN node %d wrong:\n%s", i+1, got[i])
		}
	}
	argo := got[2]
	for _, want := range []string{"@tunnel.example.com:443", "sni=tunnel.example.com", "host=tunnel.example.com", "security=tls", "-Argo"} {
		if !strings.Contains(argo, want) {
			t.Errorf("Argo node missing %q:\n%s", want, argo)
		}
	}
}

func TestSharedOrigin_EitherPathAlone(t *testing.T) {
	st, nodeID := newStore(t)
	token := seedSharedOrigin(t, st, nodeID)

	// Tunnel down: just the CDN node.
	r := NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com"})
	got := edgeURIs(t, r, token)
	if len(got) != 1 || !strings.Contains(got[0], "@cdn.example.com:443") {
		t.Fatalf("tunnel down: want the CDN node only, got:\n%s", strings.Join(got, "\n"))
	}

	// No edge domain: just the Argo node.
	r = NewResolver(st, "1.2.3.4", nil, "tunnel.example.com", core.NodeCapability{})
	got = edgeURIs(t, r, token)
	if len(got) != 1 || !strings.Contains(got[0], "@tunnel.example.com:443") {
		t.Fatalf("no edge domain: want the Argo node only, got:\n%s", strings.Join(got, "\n"))
	}

	// Neither: the loopback origin must not leak.
	r = NewResolver(st, "1.2.3.4", nil, "", core.NodeCapability{})
	if got = edgeURIs(t, r, token); len(got) != 0 {
		t.Fatalf("no entry path: want nothing, got:\n%s", strings.Join(got, "\n"))
	}
}

func TestSharedOrigin_PreviewFlags(t *testing.T) {
	st, nodeID := newStore(t)
	seedSharedOrigin(t, st, nodeID)
	r := NewResolver(st, "1.2.3.4", nil, "tunnel.example.com", core.NodeCapability{}).
		WithAntiBlock(antiblock.Config{EdgeDomain: "cdn.example.com"})
	links, err := r.BuildBundleForClient(&model.Client{Email: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var cdn, argo int
	for _, l := range links {
		if !strings.Contains(l.URI, "a1b2") {
			continue
		}
		if l.Argo {
			argo++
		} else if l.CDN {
			cdn++
		}
	}
	if cdn != 1 || argo != 1 {
		t.Errorf("want one CDN and one Argo link, got cdn=%d argo=%d", cdn, argo)
	}
}
