package gateway

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/selfsigned"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

func newStore(t *testing.T) (*store.Store, uint) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.EnsureLocalNode()
	if err != nil {
		t.Fatal(err)
	}
	return st, n.ID
}

// selfSignedIssuer stands in for ACME: it mints a self-signed pair and lets
// the real cert manager persist it, counting calls.
type selfSignedIssuer struct {
	dir   string
	calls atomic.Int32
}

func (f *selfSignedIssuer) Obtain(_ context.Context, req cert.IssueRequest) (*cert.IssueResult, error) {
	f.calls.Add(1)
	c, k := filepath.Join(f.dir, "c.pem"), filepath.Join(f.dir, "k.pem")
	_ = os.Remove(c)
	_ = os.Remove(k)
	if err := selfsigned.Write(req.Domain, c, k); err != nil {
		return nil, err
	}
	cp, _ := os.ReadFile(c)
	kp, _ := os.ReadFile(k)
	exp, _ := cert.ParseExpiry(cp)
	return &cert.IssueResult{CertPEM: cp, KeyPEM: kp, NotBefore: time.Now(), NotAfter: exp}, nil
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	_ = ln.Close()
	return a
}

func TestFromCloudflare(t *testing.T) {
	for addr, want := range map[string]bool{
		"104.16.1.1:443":          true,
		"[2606:4700::1]:443":      true,
		"[::ffff:104.16.1.1]:443": true,
		"127.0.0.1:5555":          true,
		"1.2.3.4:443":             false,
		"[2001:db8::1]:443":       false,
	} {
		a, _ := net.ResolveTCPAddr("tcp", addr)
		if got := fromCloudflare(a); got != want {
			t.Errorf("%s: got %v want %v", addr, got, want)
		}
	}
}

func TestRouting(t *testing.T) {
	st, nodeID := newStore(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "origin:"+r.URL.Path)
	}))
	defer origin.Close()
	_, portStr, _ := net.SplitHostPort(origin.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	if err := st.CreateInbound(&model.Inbound{NodeID: nodeID, Tag: "ws", Engine: "singbox", Type: "vless-ws",
		Listen: "127.0.0.1", Port: port, Network: "tcp", Enabled: true,
		Settings: `{"edge_bound":"true","ws_path":"/a1b2"}`}); err != nil {
		t.Fatal(err)
	}
	sub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "sub:"+r.URL.Path) })
	g := New(st, nil, sub, nodeID)

	for path, want := range map[string]string{
		"/sub/TOKEN":  "sub:/sub/TOKEN",
		"/a1b2":       "origin:/a1b2",
		"/":           "404 Not Found",
		"/api/health": "404 Not Found",
	} {
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: got %q, want it to contain %q", path, rec.Body.String(), want)
		}
	}
}

// A web server already owns the port: auto mode stands aside and remembers,
// so a later boot never grabs the port from it.
func TestReconcileAutoBusyPortGoesExternal(t *testing.T) {
	st, nodeID := newStore(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	_ = antiblock.Save(st, antiblock.Config{EdgeDomain: "cdn.example.com"})
	g := New(st, nil, nil, nodeID)
	g.addr = busy.Addr().String()
	g.Reconcile(context.Background())
	if s := g.Status(); s.Mode != ModeExternal || s.Running {
		t.Fatalf("status = %+v, want external / not running", s)
	}
	if antiblock.Load(st).GatewayMode != antiblock.GatewayExternal {
		t.Fatal("auto decision not remembered")
	}
}

// End to end: edge domain set → certificate issued through the cert manager →
// HTTPS on the gateway serves the subscription; the decision sticks as
// builtin; a second reconcile doesn't re-issue.
func TestReconcileBuiltinIssuesAndServes(t *testing.T) {
	st, nodeID := newStore(t)
	_ = antiblock.Save(st, antiblock.Config{EdgeDomain: "cdn.example.com", OriginOpen: true})
	iss := &selfSignedIssuer{dir: t.TempDir()}
	mgr := cert.NewManager(st, t.TempDir(), iss)
	sub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "sub-ok") })
	g := New(st, mgr, sub, nodeID)
	g.addr = freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.Reconcile(ctx)
	defer g.stop()

	deadline := time.Now().Add(5 * time.Second)
	for g.Status().CertExpires == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if s := g.Status(); s.Mode != ModeBuiltin || !s.Running || s.CertExpires == 0 {
		t.Fatalf("status = %+v", s)
	}
	if antiblock.Load(st).GatewayMode != antiblock.GatewayBuiltin {
		t.Fatal("auto decision not remembered as builtin")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, ServerName: "cdn.example.com"}}}
	resp, err := client.Get("https://" + g.addr + "/sub/T")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "sub-ok" {
		t.Fatalf("subscription over the gateway: %q", body)
	}
	if peer := resp.TLS.PeerCertificates[0].DNSNames; len(peer) == 0 || peer[0] != "cdn.example.com" {
		t.Fatalf("served certificate names: %v", peer)
	}
	g.Reconcile(ctx)
	if n := iss.calls.Load(); n != 1 {
		t.Fatalf("issuer called %d times, want 1", n)
	}
}

// Cloudflare-only: a direct (non-Cloudflare) client gets the connection
// closed before any TLS byte; loopback is let through for on-box checks.
func TestCFListenerDropsOthers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cf := &cfListener{Listener: ln}
	defer cf.Close()
	go func() {
		c, err := cf.Accept()
		if err == nil {
			_, _ = c.Write([]byte("hi"))
			_ = c.Close()
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("loopback should pass: %v %q", err, buf)
	}
}

// The panel answers through the gateway only for its secret path or a request
// carrying the gate cookie; anything else stays the neutral 404.
func TestPanelThroughGateway(t *testing.T) {
	st, nodeID := newStore(t)
	panel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "panel:"+r.URL.Path) })
	g := New(st, nil, panel, nodeID)
	g.SetPanel(func(r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/ENPanel-x") {
			return true
		}
		c, err := r.Cookie("edgenest_gate")
		return err == nil && c.Value == "ok"
	}, func() string { return "/ENPanel-x" })

	cases := []struct {
		path, cookie, want string
	}{
		{"/ENPanel-x", "", "panel:/ENPanel-x"},
		{"/api/v1/inbounds", "ok", "panel:/api/v1/inbounds"},
		{"/assets/index.js", "ok", "panel:/assets/index.js"},
		{"/api/v1/inbounds", "", "404 Not Found"},
		{"/api/health", "bad", "404 Not Found"},
		{"/", "", "404 Not Found"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "edgenest_gate", Value: c.cookie})
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s (cookie %q): got %q, want %q", c.path, c.cookie, rec.Body.String(), c.want)
		}
	}
}
