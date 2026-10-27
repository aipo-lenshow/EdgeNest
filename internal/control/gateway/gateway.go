// Package gateway is EdgeNest's built-in edge gateway: when the operator sets
// an edge domain (Settings → anti-blocking) it serves HTTPS on :443 itself, so
// no separate Caddy / Nginx is needed. It
//   - presents an ACME certificate for the edge domain (issued and renewed by
//     the cert manager like any other),
//   - hands /sub/* to the panel's own subscription handler,
//   - serves the panel itself to whoever opens its secret path (and from then
//     on carries the panel's gate cookie), so it is reachable over HTTPS
//     behind Cloudflare without exposing the panel port,
//   - forwards each edge_bound inbound's WebSocket path to its loopback port,
//   - answers everything else with a stock nginx 404,
//   - and by default only accepts connections from Cloudflare's ranges, so a
//     direct probe of the VPS address on :443 gets nothing at all.
//
// When :443 is already taken (another web server on the box) it stands aside
// and the panel keeps offering the reverse-proxy snippet instead.
package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	mrand "math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/portowner"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// Modes reported in Status.Mode.
const (
	ModeOff      = "off"      // no edge domain configured
	ModeBuiltin  = "builtin"  // this gateway serves the edge domain itself
	ModeExternal = "external" // the operator's own reverse proxy does
	// ModePending: an edge domain is set but nobody has chosen who serves it
	// yet. Auto mode never takes a port on its own — at boot it could beat
	// another web server to 443 and knock that server's sites offline — so it
	// reports who owns 443 (Owner) and a free Cloudflare port (AltPort) and
	// waits for the one-click setup or an explicit choice.
	ModePending = "pending"
)

// Status is what the panel shows next to the anti-blocking settings.
type Status struct {
	Mode        string `json:"mode"`
	Running     bool   `json:"running"`
	Domain      string `json:"domain,omitempty"`
	CertExpires int64  `json:"cert_expires,omitempty"`
	Issuing     bool   `json:"issuing,omitempty"`
	Error       string `json:"error,omitempty"`
	CFOnly      bool   `json:"cf_only"`
	// PanelURL is where the panel answers through the gateway (the edge
	// domain + the secret panel path), set while the gateway runs.
	PanelURL string `json:"panel_url,omitempty"`
	// Port is the public port the built-in gateway serves on.
	Port int `json:"port,omitempty"`
	// Backend is the loopback address an external reverse proxy forwards the
	// whole edge domain to (plain HTTP; EdgeNest does the routing).
	Backend string `json:"backend,omitempty"`
	// Owner is the program already listening on 443, when there is one.
	Owner *portowner.Owner `json:"owner,omitempty"`
	// AltPort is a free Cloudflare HTTPS port the gateway could use instead.
	AltPort int `json:"alt_port,omitempty"`
	// NeedCFToken: port 80 belongs to another program, so the certificate
	// can't be validated over HTTP; a Cloudflare API token (DNS validation)
	// is needed.
	NeedCFToken bool `json:"need_cf_token,omitempty"`
	// CertState says where the edge domain's certificate stands, for the
	// panel to explain in plain words: ok, issuing, no_dns (the name doesn't
	// resolve yet, so Let's Encrypt wasn't asked), failed (CertError has the
	// reason; NextRetry is when it is tried again), need_token.
	CertState string `json:"cert_state,omitempty"`
	CertError string `json:"cert_error,omitempty"`
	NextRetry int64  `json:"next_retry,omitempty"`
}

// Certificate states reported in Status.CertState.
const (
	CertOK        = "ok"
	CertIssuing   = "issuing"
	CertNoDNS     = "no_dns"
	CertFailed    = "failed"
	CertNeedToken = "need_token"
)

// Issuer is the slice of cert.Manager the gateway uses (a seam for tests).
type Issuer interface {
	Issue(ctx context.Context, nodeID uint, req cert.IssueRequest) (*model.Certificate, error)
}

// issueBackoff spaces ACME attempts for the edge domain: Let's Encrypt allows
// only a handful of failed validations per hour, and Reconcile runs often.
const issueBackoff = time.Hour

// dnsBackoff spaces the DNS pre-check while the name doesn't resolve yet; it
// costs nothing at Let's Encrypt, so a freshly added record is noticed soon.
const dnsBackoff = time.Minute

// Gateway owns the public listener and the loopback backend. Reconcile is the only entry point that
// changes state; it is safe to call at any time and from any goroutine.
type Gateway struct {
	store  *store.Store
	issuer Issuer
	sub    http.Handler // the panel router; gets /sub/* and panel-gated requests
	// panel reports whether a request belongs to the panel: its secret path,
	// or a later request carrying the gate cookie that path issued. Nil = the
	// panel is never served through the gateway.
	panel     func(*http.Request) bool
	panelPath func() string
	nodeID    uint
	// addr overrides the public listen address (tests); "" = ":<port>".
	addr string

	mu        sync.Mutex
	srv       *http.Server
	srvCFOnly bool // listener filter the running server was started with
	srvAddr   string
	backend   *http.Server // loopback plain-HTTP entry for an external proxy
	backAddr  string
	status    Status
	issuing   bool
	// The last certificate attempt: for which domain, how it ended, and when
	// the next one may start. Backoff is per domain, so changing the edge
	// domain retries at once.
	tryDomain string
	tryState  string // CertNoDNS / CertFailed after a failed attempt
	tryErr    string
	nextTry   time.Time
	// lookup resolves a host name (a seam for tests).
	lookup func(ctx context.Context, host string) ([]string, error)

	certMu   sync.Mutex
	certPath string
	certMod  time.Time
	certVal  *tls.Certificate

	routesMu sync.Mutex
	routes   map[string]string // ws path → loopback host:port
	routesAt time.Time
}

// New builds an idle gateway; call Reconcile (and Run for periodic upkeep).
func New(s *store.Store, issuer Issuer, sub http.Handler, nodeID uint) *Gateway {
	return &Gateway{store: s, issuer: issuer, sub: sub, nodeID: nodeID,
		status: Status{Mode: ModeOff}, lookup: net.DefaultResolver.LookupHost}
}

// SetPanel lets the gateway serve the panel: allowed decides which requests
// are the panel's (secret path or gate cookie), path yields the secret path
// for the URL shown to the operator.
func (g *Gateway) SetPanel(allowed func(*http.Request) bool, path func() string) {
	g.panel, g.panelPath = allowed, path
}

// Status returns a snapshot for the panel.
func (g *Gateway) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.status
	st.Issuing = g.issuing
	return st
}

// Run reconciles now and then every interval, so a renewed certificate, a
// finished issuance or a port that became free is picked up without a restart.
func (g *Gateway) Run(ctx context.Context, interval time.Duration) {
	g.Reconcile(ctx)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				g.stop()
				return
			case <-t.C:
				g.Reconcile(ctx)
			}
		}
	}()
}

// Reconcile brings the listener in line with the stored posture.
func (g *Gateway) Reconcile(ctx context.Context) {
	cfg := antiblock.Load(g.store)
	g.invalidateRoutes()

	if cfg.EdgeDomain == "" {
		g.stop()
		g.stopBackend()
		g.setStatus(Status{Mode: ModeOff})
		return
	}
	// The loopback backend runs whenever there is an edge domain, so an
	// external proxy can be pointed at it before or after the choice is made.
	backend := g.ensureBackend()

	if cfg.GatewayMode == antiblock.GatewayExternal {
		g.stop()
		g.setStatus(Status{Mode: ModeExternal, Domain: cfg.EdgeDomain, Backend: backend, Owner: portowner.Find(443)})
		return
	}

	if cfg.GatewayMode == antiblock.GatewayAuto {
		g.stop()
		g.setStatus(Status{Mode: ModePending, Domain: cfg.EdgeDomain, Backend: backend,
			Owner: portowner.Find(443), AltPort: g.freeAltPort()})
		return
	}

	port := cfg.GatewayPort()
	addr := g.addr
	if addr == "" {
		addr = fmt.Sprintf(":%d", port)
	}
	g.mu.Lock()
	stale := g.srv != nil && (g.srvAddr != addr || g.srvCFOnly != !cfg.OriginOpen)
	g.mu.Unlock()
	if stale {
		g.stop()
	}
	g.mu.Lock()
	running := g.srv != nil
	g.mu.Unlock()
	if !running {
		if err := g.start(addr, !cfg.OriginOpen); err != nil {
			g.setStatus(Status{Mode: ModeBuiltin, Domain: cfg.EdgeDomain, Backend: backend, Port: port,
				Error: fmt.Sprintf("内置网关无法监听 %d 端口: %v", port, err), Owner: portowner.Find(port),
				AltPort: g.freeAltPort()})
			return
		}
	}

	crt, _ := g.findCert(cfg.EdgeDomain)
	st := Status{Mode: ModeBuiltin, Running: true, Domain: cfg.EdgeDomain, CFOnly: !cfg.OriginOpen,
		Port: port, Backend: backend}
	valid := crt != nil && time.Until(time.Unix(crt.ExpiresAt, 0)) > 0
	if !valid {
		// HTTP-01 needs port 80; with another program on it the attempt can
		// only fail (and Let's Encrypt rate-limits failures), so ask for a
		// Cloudflare token instead and wait for it.
		if !g.hasCFToken() && portowner.Find(80) != nil {
			st.NeedCFToken = true
		} else {
			g.issueAsync(ctx, cfg.EdgeDomain)
		}
	}
	if g.panel != nil && g.panelPath != nil {
		if p := g.panelPath(); p != "" {
			st.PanelURL = cfg.EdgeOrigin() + p
		}
	}
	if crt != nil {
		st.CertExpires = crt.ExpiresAt
	}
	g.certStatus(&st, cfg.EdgeDomain, valid)
	g.setStatus(st)
}

// certStatus fills the certificate part of st.
func (g *Gateway) certStatus(st *Status, domain string, valid bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case valid:
		st.CertState = CertOK
	case g.issuing:
		st.CertState = CertIssuing
	case st.NeedCFToken:
		st.CertState = CertNeedToken
		if owner := portowner.Find(80); owner != nil {
			st.CertError = owner.Process
		}
	case g.tryDomain == domain && g.tryState != "":
		st.CertState, st.CertError, st.NextRetry = g.tryState, g.tryErr, g.nextTry.Unix()
	default:
		st.CertState = CertIssuing
	}
}

// FreeAltPort is freeAltPort for the one-click setup.
func (g *Gateway) FreeAltPort() int { return g.freeAltPort() }

// freeAltPort is the first Cloudflare HTTPS port other than 443 that no
// inbound uses and nothing on the box listens on.
func (g *Gateway) freeAltPort() int {
	used := map[int]bool{}
	if ins, err := g.store.ListInbounds(g.nodeID); err == nil {
		for _, in := range ins {
			used[in.Port] = true
		}
	}
	for _, p := range antiblock.CloudflareHTTPSPorts {
		if p == 443 || used[p] {
			continue
		}
		if ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p)); err == nil {
			_ = ln.Close()
			return p
		}
	}
	return 0
}

// ensureBackend starts the loopback plain-HTTP entry (same routing as the
// public gateway) on its persisted port, minting one on first use, and
// returns its address.
func (g *Gateway) ensureBackend() string {
	g.mu.Lock()
	if g.backend != nil {
		addr := g.backAddr
		g.mu.Unlock()
		return addr
	}
	g.mu.Unlock()
	port, _ := strconv.Atoi(mustSetting(g.store, antiblock.KeyBackendPort))
	var ln net.Listener
	var err error
	if port > 0 {
		ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	if ln == nil {
		for i := 0; i < 20 && ln == nil; i++ {
			port = 20000 + mrand.Intn(12000)
			ln, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		}
		if ln == nil {
			log.Printf("gateway: backend listener: %v", err)
			return ""
		}
		_ = g.store.SetSetting(antiblock.KeyBackendPort, strconv.Itoa(port))
	}
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	addr := ln.Addr().String()
	g.mu.Lock()
	g.backend, g.backAddr = srv, addr
	g.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gateway: backend: %v", err)
		}
	}()
	return addr
}

func (g *Gateway) stopBackend() {
	g.mu.Lock()
	srv := g.backend
	g.backend, g.backAddr = nil, ""
	g.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func mustSetting(s *store.Store, k string) string {
	v, _ := s.GetSetting(k)
	return v
}

func (g *Gateway) setStatus(s Status) {
	g.mu.Lock()
	g.status = s
	g.mu.Unlock()
}

// issueAsync requests a certificate for domain unless one is in flight. The
// cert manager persists it and the scheduler renews it with every other cert.
// DNS-01 via Cloudflare is used when the operator stored a Cloudflare token on
// the certificates page (works with :80 closed); otherwise HTTP-01, which
// Cloudflare forwards to the origin's :80.
func (g *Gateway) issueAsync(ctx context.Context, domain string) {
	g.mu.Lock()
	if g.issuing || g.issuer == nil || (g.tryDomain == domain && time.Now().Before(g.nextTry)) {
		g.mu.Unlock()
		return
	}
	g.issuing = true
	if g.tryDomain != domain {
		g.tryDomain, g.tryState, g.tryErr = domain, "", ""
	}
	g.mu.Unlock()
	fail := func(state, msg string, wait time.Duration) {
		g.mu.Lock()
		g.tryState, g.tryErr, g.nextTry = state, msg, time.Now().Add(wait)
		g.mu.Unlock()
	}
	go func() {
		defer func() {
			g.mu.Lock()
			g.issuing = false
			g.mu.Unlock()
			// Publish the outcome (failure reason / new certificate) now
			// rather than at the next periodic pass.
			g.Reconcile(ctx)
		}()
		// A name that doesn't resolve can only fail validation, and failed
		// validations count against Let's Encrypt's hourly limit; check first.
		lctx, lcancel := context.WithTimeout(ctx, 10*time.Second)
		_, lerr := g.lookup(lctx, domain)
		lcancel()
		if lerr != nil {
			fail(CertNoDNS, lerr.Error(), dnsBackoff)
			return
		}
		req := cert.IssueRequest{Domain: domain, Email: g.acmeEmail(domain), Mode: "http-01"}
		if tok, _ := g.store.GetSetting(CFTokenSetting); tok != "" {
			req.Mode, req.DNSProvider = "dns-01", "cloudflare"
			req.DNSConfig = map[string]string{"api_token": tok}
		}
		ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if _, err := g.issuer.Issue(ictx, g.nodeID, req); err != nil {
			log.Printf("gateway: certificate for %s: %v", domain, err)
			fail(CertFailed, err.Error(), issueBackoff)
			return
		}
		log.Printf("gateway: certificate for %s issued", domain)
		g.mu.Lock()
		g.tryState, g.tryErr, g.nextTry = "", "", time.Time{}
		g.mu.Unlock()
	}()
}

// CFTokenSetting is where the cert manager keeps the Cloudflare DNS token
// (the same key the certificates page writes), so renewals reuse it.
const CFTokenSetting = "dns_cloudflare_api_token"

func (g *Gateway) hasCFToken() bool {
	v, _ := g.store.GetSetting(CFTokenSetting)
	return v != ""
}

// RetryCertificate clears the failure backoff so the next Reconcile asks for
// the certificate again (a token was supplied, or the operator pressed
// "request again" after fixing DNS).
func (g *Gateway) RetryCertificate() {
	g.mu.Lock()
	g.nextTry = time.Time{}
	g.mu.Unlock()
}

func (g *Gateway) acmeEmail(domain string) string {
	if v, _ := g.store.GetSetting("acme_email"); v != "" {
		return v
	}
	return "admin@" + domain
}

func (g *Gateway) findCert(domain string) (*model.Certificate, error) {
	certs, err := g.store.ListCertificates(g.nodeID)
	if err != nil {
		return nil, err
	}
	for i := range certs {
		if strings.EqualFold(certs[i].Domain, domain) && certs[i].CertPath != "" {
			return &certs[i], nil
		}
	}
	return nil, nil
}

// getCertificate serves the edge domain's certificate, re-reading the PEM
// files when the cert manager has replaced them (renewal).
func (g *Gateway) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cfg := antiblock.Load(g.store)
	crt, err := g.findCert(cfg.EdgeDomain)
	if err != nil || crt == nil {
		return nil, errors.New("no certificate yet")
	}
	fi, err := os.Stat(crt.CertPath)
	if err != nil {
		return nil, err
	}
	g.certMu.Lock()
	defer g.certMu.Unlock()
	if g.certVal != nil && g.certPath == crt.CertPath && g.certMod.Equal(fi.ModTime()) {
		return g.certVal, nil
	}
	pair, err := tls.LoadX509KeyPair(crt.CertPath, crt.KeyPath)
	if err != nil {
		return nil, err
	}
	g.certPath, g.certMod, g.certVal = crt.CertPath, fi.ModTime(), &pair
	return &pair, nil
}

func (g *Gateway) start(addr string, cfOnly bool) error {
	ln, err := listenDual(addr)
	if err != nil {
		return err
	}
	if cfOnly {
		ln = &cfListener{Listener: ln}
	}
	srv := &http.Server{
		Handler:           g,
		TLSConfig:         &tls.Config{GetCertificate: g.getCertificate, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}},
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.New(discard{}, "", 0), // handshake noise from scanners
	}
	g.mu.Lock()
	g.srv, g.srvCFOnly, g.srvAddr = srv, cfOnly, addr
	g.mu.Unlock()
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gateway: serve: %v", err)
		}
	}()
	log.Printf("gateway: listening on %s (cloudflare-only=%v)", addr, cfOnly)
	return nil
}

func (g *Gateway) stop() {
	g.mu.Lock()
	srv := g.srv
	g.srv = nil
	g.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

// ServeHTTP routes one request: subscription, an edge WebSocket path, or 404.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/sub/") && g.sub != nil {
		g.sub.ServeHTTP(w, r)
		return
	}
	if target := g.route(r.URL.Path); target != "" {
		proxyTo(target).ServeHTTP(w, r)
		return
	}
	if g.panel != nil && g.sub != nil && g.panel(r) {
		g.sub.ServeHTTP(w, r)
		return
	}
	notFound(w)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Server", "nginx")
	w.WriteHeader(core.NeutralMasqueradeStatus)
	_, _ = w.Write([]byte(core.NeutralMasquerade))
}

func proxyTo(hostport string) *httputil.ReverseProxy {
	u := &url.URL{Scheme: "http", Host: hostport}
	p := httputil.NewSingleHostReverseProxy(u)
	p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { notFound(w) }
	return p
}

// route maps a request path to the loopback origin of the edge inbound that
// owns it. The table is rebuilt from the store at most every few seconds.
func (g *Gateway) route(path string) string {
	g.routesMu.Lock()
	defer g.routesMu.Unlock()
	if g.routes == nil || time.Since(g.routesAt) > 5*time.Second {
		g.routes = g.loadRoutes()
		g.routesAt = time.Now()
	}
	return g.routes[path]
}

func (g *Gateway) invalidateRoutes() {
	g.routesMu.Lock()
	g.routes = nil
	g.routesMu.Unlock()
}

func (g *Gateway) loadRoutes() map[string]string {
	out := map[string]string{}
	ins, err := g.store.ListInbounds(g.nodeID)
	if err != nil {
		return out
	}
	for _, in := range ins {
		if !in.Enabled {
			continue
		}
		var s map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &s)
		}
		if v, _ := s["edge_bound"].(string); v != "true" {
			if b, _ := s["edge_bound"].(bool); !b {
				continue
			}
		}
		if p, _ := s["ws_path"].(string); strings.HasPrefix(p, "/") {
			out[p] = net.JoinHostPort("127.0.0.1", fmt.Sprint(in.Port))
		}
	}
	return out
}

// listenDual binds addr on all families, falling back to IPv4 on kernels with
// IPv6 disabled (same reasoning as the panel listener). A port that is simply
// in use is reported as such — retrying on another family could squeeze in
// beside the web server that owns it.
func listenDual(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil || errors.Is(err, syscall.EADDRINUSE) {
		return ln, err
	}
	host, port, e := net.SplitHostPort(addr)
	if e != nil || (host != "" && host != "::") {
		return nil, err
	}
	return net.Listen("tcp4", net.JoinHostPort("0.0.0.0", port))
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
