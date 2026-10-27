// Package gateway is EdgeNest's built-in edge gateway: when the operator sets
// an edge domain (Settings → anti-blocking) it serves HTTPS on :443 itself, so
// no separate Caddy / Nginx is needed. It
//   - presents an ACME certificate for the edge domain (issued and renewed by
//     the cert manager like any other),
//   - hands /sub/* to the panel's own subscription handler,
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
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/antiblock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// Modes reported in Status.Mode.
const (
	ModeOff      = "off"      // no edge domain configured
	ModeBuiltin  = "builtin"  // this gateway serves :443
	ModeExternal = "external" // the operator's own reverse proxy does (or must)
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
}

// Issuer is the slice of cert.Manager the gateway uses (a seam for tests).
type Issuer interface {
	Issue(ctx context.Context, nodeID uint, req cert.IssueRequest) (*model.Certificate, error)
}

// issueBackoff spaces ACME attempts for the edge domain: Let's Encrypt allows
// only a handful of failed validations per hour, and Reconcile runs often.
const issueBackoff = time.Hour

// Gateway owns the :443 listener. Reconcile is the only entry point that
// changes state; it is safe to call at any time and from any goroutine.
type Gateway struct {
	store  *store.Store
	issuer Issuer
	sub    http.Handler // the panel router; only /sub/* is ever passed to it
	nodeID uint
	addr   string // ":443" in production; tests use a free port

	mu        sync.Mutex
	srv       *http.Server
	srvCFOnly bool // listener filter the running server was started with
	status    Status
	issuing   bool
	lastIssue time.Time // last ACME attempt; failures back off (rate limits)

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
	return &Gateway{store: s, issuer: issuer, sub: sub, nodeID: nodeID, addr: ":443",
		status: Status{Mode: ModeOff}}
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

	if cfg.EdgeDomain == "" || cfg.GatewayMode == antiblock.GatewayExternal {
		g.stop()
		mode := ModeOff
		if cfg.EdgeDomain != "" {
			mode = ModeExternal
		}
		g.setStatus(Status{Mode: mode, Domain: cfg.EdgeDomain})
		return
	}

	crt, _ := g.findCert(cfg.EdgeDomain)
	if crt == nil || time.Until(time.Unix(crt.ExpiresAt, 0)) < 0 {
		g.issueAsync(ctx, cfg.EdgeDomain)
	}

	g.mu.Lock()
	running := g.srv != nil
	g.mu.Unlock()
	if !running {
		if err := g.start(!cfg.OriginOpen); err != nil {
			mode, msg := ModeExternal, "443 端口已被其他程序占用，内置网关未启动；请用你自己的反代转发（见下方配置）"
			if cfg.GatewayMode == antiblock.GatewayBuiltin {
				mode, msg = ModeBuiltin, "内置网关启动失败: "+err.Error()
			} else {
				// Auto resolves once and remembers: if a web server already owns
				// :443 here, a later boot that happens to start before it must
				// not grab the port and knock that server's sites offline.
				_ = g.store.SetSetting(antiblock.KeyGatewayMode, antiblock.GatewayExternal)
			}
			g.setStatus(Status{Mode: mode, Domain: cfg.EdgeDomain, Error: msg})
			return
		}
		if cfg.GatewayMode == antiblock.GatewayAuto {
			_ = g.store.SetSetting(antiblock.KeyGatewayMode, antiblock.GatewayBuiltin)
		}
	}
	st := Status{Mode: ModeBuiltin, Running: true, Domain: cfg.EdgeDomain, CFOnly: !cfg.OriginOpen}
	if crt != nil {
		st.CertExpires = crt.ExpiresAt
		if crt.LastError != "" && time.Until(time.Unix(crt.ExpiresAt, 0)) < 0 {
			st.Error = "证书申请失败: " + crt.LastError
		}
	} else if !g.isIssuing() {
		st.Error = "还没有证书，正在申请（需要 80 端口可用，或在证书页配置 Cloudflare DNS 令牌）"
	}
	g.setStatus(st)
	// The CF-only switch is a listener property; restart to apply a change.
	g.mu.Lock()
	needRestart := g.srv != nil && g.srvCFOnly != !cfg.OriginOpen
	g.mu.Unlock()
	if needRestart {
		g.stop()
		_ = g.start(!cfg.OriginOpen)
	}
}

func (g *Gateway) setStatus(s Status) {
	g.mu.Lock()
	g.status = s
	g.mu.Unlock()
}

func (g *Gateway) isIssuing() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.issuing
}

// issueAsync requests a certificate for domain unless one is in flight. The
// cert manager persists it and the scheduler renews it with every other cert.
// DNS-01 via Cloudflare is used when the operator stored a Cloudflare token on
// the certificates page (works with :80 closed); otherwise HTTP-01, which
// Cloudflare forwards to the origin's :80.
func (g *Gateway) issueAsync(ctx context.Context, domain string) {
	g.mu.Lock()
	if g.issuing || g.issuer == nil || time.Since(g.lastIssue) < issueBackoff {
		g.mu.Unlock()
		return
	}
	g.issuing, g.lastIssue = true, time.Now()
	g.mu.Unlock()
	go func() {
		defer func() {
			g.mu.Lock()
			g.issuing = false
			g.mu.Unlock()
		}()
		req := cert.IssueRequest{Domain: domain, Email: g.acmeEmail(domain), Mode: "http-01"}
		if tok, _ := g.store.GetSetting("dns_cloudflare_api_token"); tok != "" {
			req.Mode, req.DNSProvider = "dns-01", "cloudflare"
			req.DNSConfig = map[string]string{"api_token": tok}
		}
		ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if _, err := g.issuer.Issue(ictx, g.nodeID, req); err != nil {
			log.Printf("gateway: certificate for %s: %v", domain, err)
			return
		}
		log.Printf("gateway: certificate for %s issued", domain)
		g.Reconcile(ctx)
	}()
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

func (g *Gateway) start(cfOnly bool) error {
	ln, err := listenDual(g.addr)
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
	g.srv, g.srvCFOnly = srv, cfOnly
	g.mu.Unlock()
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gateway: serve: %v", err)
		}
	}()
	log.Printf("gateway: listening on %s (cloudflare-only=%v)", g.addr, cfOnly)
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
