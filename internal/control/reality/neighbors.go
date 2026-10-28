// Package reality picks the site a Reality inbound impersonates.
//
// Reality answers probes with a borrowed TLS handshake from a real website, so
// the server looks like that website. The borrowed site should therefore live
// where the server lives: a VPS in some data centre claiming to be a global
// brand hosted on that brand's own network is a mismatch anyone comparing the
// SNI with the address owner can spot. FindNeighbors scans the server's own
// /24 for an ordinary HTTPS site that also meets Reality's technical needs
// (TLS 1.3, X25519, HTTP/2, a publicly trusted certificate), and the result
// becomes the default disguise for new Reality inbounds.
package reality

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// FallbackSNI is used when no neighbour has been found (yet). It borrows
// reliably from VPS addresses; the neighbour scan replaces it when it can.
const FallbackSNI = "www.apple.com"

// SettingKey is where the last scan result is cached (JSON Result).
const SettingKey = "reality_neighbors"

// Candidate is one usable neighbour site.
type Candidate struct {
	Domain    string `json:"domain"`
	IP        string `json:"ip"`
	LatencyMs int    `json:"latency_ms"`
}

// Result is a finished scan.
type Result struct {
	ServerIP   string      `json:"server_ip"`
	ScannedAt  int64       `json:"scanned_at"`
	Candidates []Candidate `json:"candidates"`
}

var preferred atomic.Value // string

// SetResult publishes a scan result as the default for new Reality inbounds.
func SetResult(r *Result) {
	if r != nil && len(r.Candidates) > 0 {
		preferred.Store(r.Candidates[0].Domain)
	}
}

// DefaultSNI is the disguise a new Reality inbound gets when the operator
// didn't choose one: the best neighbour found so far, else FallbackSNI.
func DefaultSNI() string {
	if v, _ := preferred.Load().(string); v != "" {
		return v
	}
	return FallbackSNI
}

// Load decodes a cached Result; nil when empty or unreadable.
func Load(raw string) *Result {
	if raw == "" {
		return nil
	}
	var r Result
	if json.Unmarshal([]byte(raw), &r) != nil {
		return nil
	}
	return &r
}

// Options tune a scan. Zero values pick conservative defaults: the scan stays
// inside one /24, opens few connections at a time, and gives up quickly, so it
// looks like nothing more than a handful of HTTPS visits.
type Options struct {
	Concurrency int
	DialTimeout time.Duration
	Max         int // candidates to keep
}

func (o *Options) defaults() {
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 2 * time.Second
	}
	if o.Max <= 0 {
		o.Max = 5
	}
}

// ErrNoIPv4 means the server has no public IPv4 to scan around.
var ErrNoIPv4 = errors.New("no public IPv4 address to scan around")

// FindNeighbors scans serverIP's /24 and returns usable neighbour sites,
// fastest first.
func FindNeighbors(ctx context.Context, serverIP string, opt Options) (*Result, error) {
	opt.defaults()
	ip := net.ParseIP(serverIP).To4()
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return nil, ErrNoIPv4
	}
	prefix := ip.Mask(net.CIDRMask(24, 32))

	// Pass 1: who in the /24 speaks TLS 1.3 + X25519 + h2, and what names do
	// their default certificates carry?
	type hit struct {
		ip    string
		names []string
	}
	var (
		mu   sync.Mutex
		hits []hit
		wg   sync.WaitGroup
		sem  = make(chan struct{}, opt.Concurrency)
	)
	for i := 1; i < 255; i++ {
		addr := net.IPv4(prefix[0], prefix[1], prefix[2], byte(i)).String()
		if addr == ip.String() {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			cs, _, err := handshake(ctx, addr, "", opt.DialTimeout, false)
			if err != nil || len(cs.PeerCertificates) == 0 {
				return
			}
			if names := certNames(cs.PeerCertificates[0]); len(names) > 0 {
				mu.Lock()
				hits = append(hits, hit{addr, names})
				mu.Unlock()
			}
		}(addr)
	}
	wg.Wait()

	// Pass 2: a name only counts if it resolves back into this /24 and serves
	// a valid certificate for itself there — that is what Reality will dial.
	seen := map[string]bool{}
	var out []Candidate
	for _, h := range hits {
		for _, name := range h.names {
			if seen[name] || ctx.Err() != nil {
				continue
			}
			seen[name] = true
			if !resolvesInto(ctx, name, prefix) {
				continue
			}
			_, rtt, err := handshake(ctx, name, name, opt.DialTimeout, true)
			if err != nil || !servesPage(ctx, name, opt.DialTimeout) {
				continue
			}
			out = append(out, Candidate{Domain: name, IP: h.ip, LatencyMs: int(rtt.Milliseconds())})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].LatencyMs < out[b].LatencyMs })
	if len(out) > opt.Max {
		out = out[:opt.Max]
	}
	return &Result{ServerIP: ip.String(), ScannedAt: time.Now().Unix(), Candidates: out}, nil
}

// handshake does one TLS 1.3 handshake offering only X25519 and h2. With
// verify it also requires a publicly trusted certificate for sni.
func handshake(ctx context.Context, host, sni string, timeout time.Duration, verify bool) (tls.ConnectionState, time.Duration, error) {
	d := &net.Dialer{Timeout: timeout}
	cctx, cancel := context.WithTimeout(ctx, 2*timeout)
	defer cancel()
	start := time.Now()
	raw, err := d.DialContext(cctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		return tls.ConnectionState{}, 0, err
	}
	defer raw.Close()
	c := tls.Client(raw, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: !verify,
		MinVersion:         tls.VersionTLS13,
		CurvePreferences:   []tls.CurveID{tls.X25519},
		NextProtos:         []string{"h2", "http/1.1"},
	})
	if err := c.HandshakeContext(cctx); err != nil {
		return tls.ConnectionState{}, 0, err
	}
	cs := c.ConnectionState()
	if cs.NegotiatedProtocol != "h2" {
		return cs, 0, errors.New("no h2")
	}
	return cs, time.Since(start), nil
}

// servesPage reports whether https://name/ answers like a real website (a
// 2xx/3xx HTML page). Blank, parked and error-only hosts make a thin
// disguise and tend to vanish, so they are skipped.
func servesPage(ctx context.Context, name string, timeout time.Duration) bool {
	cctx, cancel := context.WithTimeout(ctx, 3*timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "https://"+name+"/", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	resp, err := pageClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
	return resp.StatusCode >= 200 && resp.StatusCode < 400 &&
		strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html")
}

// pageClient doesn't follow redirects: a 3xx from the site itself is fine,
// and following it could leave the neighbourhood.
var pageClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// certNames returns the concrete host names a certificate covers: no
// wildcards, no IPs, no single-label names.
func certNames(c *x509.Certificate) []string {
	var out []string
	add := func(n string) {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || strings.Contains(n, "*") || net.ParseIP(n) != nil || !strings.Contains(n, ".") {
			return
		}
		for _, x := range out {
			if x == n {
				return
			}
		}
		out = append(out, n)
	}
	for _, n := range c.DNSNames {
		add(n)
	}
	add(c.Subject.CommonName)
	return out
}

var lookupIP = net.DefaultResolver.LookupIP

// resolvesInto reports whether name has an IPv4 address inside prefix (/24).
func resolvesInto(ctx context.Context, name string, prefix net.IP) bool {
	ips, err := lookupIP(ctx, "ip4", name)
	if err != nil {
		return false
	}
	for _, a := range ips {
		if a4 := a.To4(); a4 != nil && a4.Mask(net.CIDRMask(24, 32)).Equal(prefix) {
			return true
		}
	}
	return false
}

// Store is the slice of the settings store a scan needs.
type Store interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
}

// ErrBusy means another scan is already running.
var ErrBusy = errors.New("a neighbour scan is already running")

var scanning sync.Mutex

// Scan runs FindNeighbors, caches the result in st and publishes it as the
// default. Only one scan runs at a time.
func Scan(ctx context.Context, st Store, serverIP string) (*Result, error) {
	if !scanning.TryLock() {
		return nil, ErrBusy
	}
	defer scanning.Unlock()
	r, err := FindNeighbors(ctx, serverIP, Options{})
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(r)
	if err := st.SetSetting(SettingKey, string(b)); err != nil {
		return r, err
	}
	SetResult(r)
	return r, nil
}

// Init publishes the cached result, and when there is none starts one scan in
// the background after delay so new Reality inbounds can default to a
// neighbour. Scans never repeat on their own; the operator rescans from the
// panel.
func Init(st Store, serverIP string, delay time.Duration) {
	raw, _ := st.GetSetting(SettingKey)
	if r := Load(raw); r != nil {
		SetResult(r)
		return
	}
	go func() {
		time.Sleep(delay)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		_, _ = Scan(ctx, st, serverIP)
	}()
}

// ServerIPv4 picks the address to scan around from the install-time network
// detection: the first public IPv4.
func ServerIPv4(addrs ...string) string {
	for _, a := range addrs {
		if ip := net.ParseIP(strings.TrimSpace(a)).To4(); ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
			return ip.String()
		}
	}
	return ""
}

// CheckTarget reports whether a Reality inbound can still borrow host:port's
// handshake: a TLS 1.3 + X25519 handshake with SNI sni must succeed.
func CheckTarget(ctx context.Context, host string, port int, sni string) error {
	d := &net.Dialer{Timeout: 5 * time.Second}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := d.DialContext(cctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer raw.Close()
	c := tls.Client(raw, &tls.Config{
		ServerName:       sni,
		MinVersion:       tls.VersionTLS13,
		CurvePreferences: []tls.CurveID{tls.X25519},
	})
	return c.HandshakeContext(cctx)
}

// Target is the site one Reality inbound impersonates.
type Target struct {
	SNI  string
	Host string
	Port int
}

// TargetOf returns the disguise of a Reality inbound (VLESS-Reality or
// VLESS-XHTTP with security=reality) from its type and settings.
func TargetOf(typ string, s map[string]any) (Target, bool) {
	str := func(k string) string { v, _ := s[k].(string); return strings.TrimSpace(v) }
	switch typ {
	case "vless":
		if str("reality_private_key") == "" {
			return Target{}, false
		}
	case "vless-xhttp":
		if sec := str("security"); sec != "" && sec != "reality" {
			return Target{}, false
		}
	default:
		return Target{}, false
	}
	t := Target{SNI: str("sni"), Host: str("server_name_target"), Port: 443}
	if t.SNI == "" {
		return Target{}, false
	}
	if t.Host == "" {
		t.Host = t.SNI
	}
	switch p := s["server_port_target"].(type) {
	case float64:
		t.Port = int(p)
	case string:
		if n, err := strconv.Atoi(p); err == nil {
			t.Port = n
		}
	}
	if t.Port <= 0 {
		t.Port = 443
	}
	return t, true
}
