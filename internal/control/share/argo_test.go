package share

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// seedArgoVMessWS creates the inbound the wizard's Argo path produces: a
// PLAINTEXT vmess-ws origin on 127.0.0.1 (no tls_cert_path) marked
// argo_bound=true. Returns the client ID for subscription seeding.
func seedArgoVMessWS(t *testing.T, st *store.Store, nodeID uint, email string) uint {
	t.Helper()
	in := &model.Inbound{
		NodeID: nodeID, Tag: "vmess-argo", Engine: "singbox", Type: "vmess-ws",
		Listen: "127.0.0.1", Port: 12345, Network: "tcp", Enabled: true,
		// No tls_cert_path — plaintext origin. argo_bound flags it for the
		// tunnel-edge rewrite. ws_path must survive to the client URI.
		Settings: `{"argo_bound":"true","ws_path":"/abcd1234"}`,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateClient(&model.Client{
		InboundID: in.ID, Email: email,
		UUID: "22222222-2222-2222-2222-222222222222", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	clients, _ := st.ListClients(in.ID)
	return clients[0].ID
}

func decodeVMess(t *testing.T, uri string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(uri, "vmess://") {
		t.Fatalf("not a vmess uri: %q", uri)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "vmess://"))
	if err != nil {
		t.Fatalf("decode vmess base64: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal vmess json: %v", err)
	}
	return m
}

// When an Argo tunnel is running, an argo_bound plaintext vmess-ws inbound must
// resolve to a client config that reaches the Cloudflare edge: server + SNI +
// ws Host = the tunnel hostname, TLS on, port 443 — even though the origin
// itself is plaintext loopback.
func TestResolve_ArgoBound_RewritesToTunnelEdge(t *testing.T) {
	st, nodeID := newStore(t)
	clientID := seedArgoVMessWS(t, st, nodeID, "alice@example.com")

	token := "argo-token"
	if err := st.CreateSubscription(&model.Subscription{
		Name: "alice", TokenHash: store.HashToken(token), ClientID: clientID,
	}); err != nil {
		t.Fatal(err)
	}

	const argoHost = "random-words-here.trycloudflare.com"
	r := NewResolver(st, "203.0.113.9", nil, argoHost, core.NodeCapability{})
	uris, err := r.Resolve(token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(uris) != 1 {
		t.Fatalf("want 1 URI, got %d: %v", len(uris), uris)
	}
	m := decodeVMess(t, uris[0])

	if got := m["add"]; got != argoHost {
		t.Errorf("add = %v, want tunnel host %q", got, argoHost)
	}
	// port is emitted as a JSON number → float64 after a round-trip.
	if got := m["port"]; got != float64(443) {
		t.Errorf("port = %v, want 443", got)
	}
	if got := m["tls"]; got != "tls" {
		t.Errorf("tls = %v, want \"tls\"", got)
	}
	if got := m["sni"]; got != argoHost {
		t.Errorf("sni = %v, want tunnel host %q", got, argoHost)
	}
	if got := m["host"]; got != argoHost {
		t.Errorf("ws host = %v, want tunnel host %q", got, argoHost)
	}
	if got := m["path"]; got != "/abcd1234" {
		t.Errorf("ws path = %v, want origin path /abcd1234", got)
	}
	// trycloudflare presents a publicly trusted cert → no skip-verify aliases.
	for _, k := range []string{"verify_cert", "skip-cert-verify", "allowInsecure", "insecure"} {
		if _, ok := m[k]; ok {
			t.Errorf("argo edge cert is trusted; unexpected insecure key %q present", k)
		}
	}
}

// Without a running tunnel (argoHost empty) the argo_bound inbound listens on
// 127.0.0.1 only, so a VPS:loopback-port URI would be a dead link. It must be
// OMITTED from the subscription entirely until the tunnel is up — the panel
// surfaces a "start the tunnel" badge so the operator knows why it's missing.
func TestResolve_ArgoBound_NoTunnel_Omitted(t *testing.T) {
	st, nodeID := newStore(t)
	clientID := seedArgoVMessWS(t, st, nodeID, "bob@example.com")

	token := "argo-off"
	if err := st.CreateSubscription(&model.Subscription{
		Name: "bob", TokenHash: store.HashToken(token), ClientID: clientID,
	}); err != nil {
		t.Fatal(err)
	}

	r := NewResolver(st, "203.0.113.9", nil, "", core.NodeCapability{})
	uris, err := r.Resolve(token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(uris) != 0 {
		t.Fatalf("argo_bound inbound with no tunnel must be omitted, got %d URIs: %v", len(uris), uris)
	}
}
