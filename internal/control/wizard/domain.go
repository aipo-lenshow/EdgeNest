package wizard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DomainStatus is the four-way verdict reported by ValidateDomain. It tells
// the wizard which Step-4 toggles to surface (CDN visible vs hidden, default
// on vs off) without exposing raw DNS records to the operator.
type DomainStatus string

const (
	// DomainStatusOK — every resolved A/AAAA record points at the panel's
	// public IP (灰云 / DNS-only). Reality + WS+TLS + Argo all play.
	DomainStatusOK DomainStatus = "ok"

	// DomainStatusProxied — at least one record is inside a Cloudflare range
	// (橙云). CDN toggle defaults on; Reality still works because we pin
	// server_addr to the VPS IP for that protocol.
	DomainStatusProxied DomainStatus = "proxied"

	// DomainStatusMismatch — records resolve but to an IP that is neither the
	// panel nor Cloudflare. Wizard hides CDN/Argo for this state — the user is
	// pointing the domain somewhere else.
	DomainStatusMismatch DomainStatus = "mismatch"

	// DomainStatusNone — empty domain or NXDOMAIN. Wizard falls back to
	// IP-only protocols + offers the Argo temporary tunnel.
	DomainStatusNone DomainStatus = "none"
)

// DomainResult is the JSON the API hands back to the wizard front-end. We
// echo the resolved IPs and the VPS public IP so the user can read the call
// for themselves when it is "mismatch" — without that the verdict feels
// arbitrary.
type DomainResult struct {
	Status      DomainStatus `json:"status"`
	Domain      string       `json:"domain"`
	ResolvedIPs []string     `json:"resolved_ips"`
	VPSPublicIP string       `json:"vps_public_ip"`
}

// cloudflareIPv4URL / cloudflareIPv6URL are the public, contractually stable
// Cloudflare IP-range endpoints. Each returns plain text with one CIDR per
// line. They change rarely (years between rotations) and Cloudflare publishes
// them precisely so third parties can identify proxied DNS records.
const (
	cloudflareIPv4URL = "https://www.cloudflare.com/ips-v4"
	cloudflareIPv6URL = "https://www.cloudflare.com/ips-v6"

	// defaultCFCacheTTL is the cache lifetime for the Cloudflare CIDR list.
	// 24h matches the wizard memory's contract and keeps refresh load low.
	defaultCFCacheTTL = 24 * time.Hour
)

// vpsIPProvider abstracts "what are ALL the VPS's own public IPs?" — both
// families. A dual-stack node points A→v4 and AAAA→v6 at itself, so the
// verdict must accept either; knowing only the v4 made the validator flag the
// node's own v6 record as "some other IP" (mismatch). Production wires it to
// the node row + network.json capability; tests inject a fixture.
type vpsIPProvider func() []string

// DomainValidator runs the four-state check. It owns a lazily-refreshed cache
// of Cloudflare CIDRs and a custom *net.Resolver so tests can swap DNS for
// fixtures without touching the host's resolver.
type DomainValidator struct {
	mu        sync.Mutex
	cidrs     []*net.IPNet
	fetchedAt time.Time
	ttl       time.Duration

	httpClient *http.Client
	resolver   *net.Resolver
	vpsIP      vpsIPProvider

	// cfV4URL / cfV6URL are overridable for tests. Production uses the
	// constants above.
	cfV4URL string
	cfV6URL string
}

// NewDomainValidator wires the validator with production defaults. The
// vpsIPProvider must be non-nil; pass a closure over the local node row.
func NewDomainValidator(vps vpsIPProvider) *DomainValidator {
	if vps == nil {
		vps = func() []string { return nil }
	}
	return &DomainValidator{
		ttl:        defaultCFCacheTTL,
		httpClient: &http.Client{Timeout: 8 * time.Second},
		resolver:   net.DefaultResolver,
		vpsIP:      vps,
		cfV4URL:    cloudflareIPv4URL,
		cfV6URL:    cloudflareIPv6URL,
	}
}

// Validate resolves the domain and returns the four-state verdict. An empty
// or whitespace-only domain returns DomainStatusNone with no error — that's
// the "Step 2: I skip" path, not a failure.
func (v *DomainValidator) Validate(ctx context.Context, domain string) (DomainResult, error) {
	domain = strings.TrimSpace(domain)
	vpsIPs := v.vpsIP()
	res := DomainResult{
		Domain: domain,
		// Show every VPS IP (v4 + v6) so a dual-stack verdict reads correctly —
		// otherwise a v6-also domain looked like it "resolved elsewhere".
		VPSPublicIP: strings.Join(vpsIPs, ", "),
	}
	if domain == "" {
		res.Status = DomainStatusNone
		return res, nil
	}

	ips, err := v.resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		// NXDOMAIN / SERVFAIL / no records — treat as "none" so the wizard
		// guides the user to the no-domain branch instead of erroring out.
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			res.Status = DomainStatusNone
			return res, nil
		}
		return res, fmt.Errorf("resolve %s: %w", domain, err)
	}
	if len(ips) == 0 {
		res.Status = DomainStatusNone
		return res, nil
	}

	resolved := make([]string, 0, len(ips))
	for _, ip := range ips {
		resolved = append(resolved, ip.IP.String())
	}
	res.ResolvedIPs = resolved

	cidrs, err := v.cloudflareCIDRs(ctx)
	if err != nil {
		// Cloudflare endpoint unreachable: fall back to ok/mismatch only —
		// can't claim "proxied" without the range list. Don't fail the call
		// outright; the wizard still has useful info.
		cidrs = nil
	}

	vpsParsed := make([]net.IP, 0, len(vpsIPs))
	for _, s := range vpsIPs {
		if p := net.ParseIP(s); p != nil {
			vpsParsed = append(vpsParsed, p)
		}
	}
	resolvedIPs := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		resolvedIPs = append(resolvedIPs, ip.IP)
	}
	res.Status = classifyDomain(resolvedIPs, vpsParsed, cidrs)
	return res, nil
}

// classifyDomain is the pure four-state verdict. A resolved record "matches the
// VPS" if it equals ANY of the node's own IPs (vpsIPs holds both families), so
// a dual-stack domain (A→v4, AAAA→v6 both at this node) returns OK instead of
// mismatch. anyCF wins (proxied) since a Cloudflare record means the edge, not
// the origin, terminates TLS.
func classifyDomain(resolved, vpsIPs []net.IP, cidrs []*net.IPNet) DomainStatus {
	allMatchVPS := len(vpsIPs) > 0
	anyCF := false
	for _, ip := range resolved {
		matchesAny := false
		for _, vp := range vpsIPs {
			if ip.Equal(vp) {
				matchesAny = true
				break
			}
		}
		if !matchesAny {
			allMatchVPS = false
		}
		if ipInCIDRs(ip, cidrs) {
			anyCF = true
		}
	}
	switch {
	case anyCF:
		return DomainStatusProxied
	case allMatchVPS:
		return DomainStatusOK
	default:
		return DomainStatusMismatch
	}
}

// cloudflareCIDRs returns the cached CIDR list, refetching when the cache is
// older than ttl. Safe to call concurrently.
func (v *DomainValidator) cloudflareCIDRs(ctx context.Context) ([]*net.IPNet, error) {
	v.mu.Lock()
	if v.cidrs != nil && time.Since(v.fetchedAt) < v.ttl {
		defer v.mu.Unlock()
		return v.cidrs, nil
	}
	v.mu.Unlock()

	v4, err := v.fetchCIDRList(ctx, v.cfV4URL)
	if err != nil {
		return nil, err
	}
	v6, err := v.fetchCIDRList(ctx, v.cfV6URL)
	if err != nil {
		// v4 list alone is still useful for the common IPv4-only deploys.
		v6 = nil
	}
	merged := append(v4, v6...)

	v.mu.Lock()
	v.cidrs = merged
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return merged, nil
}

func (v *DomainValidator) fetchCIDRList(ctx context.Context, url string) ([]*net.IPNet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "EdgeNest/wizard")
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	var out []*net.IPNet
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, cidr, err := net.ParseCIDR(line)
		if err != nil {
			continue
		}
		out = append(out, cidr)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: empty CIDR list", url)
	}
	return out, nil
}

func ipInCIDRs(ip net.IP, cidrs []*net.IPNet) bool {
	for _, c := range cidrs {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}
