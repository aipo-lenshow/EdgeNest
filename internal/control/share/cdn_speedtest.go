package share

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// CDNSpeedResult is one candidate IP's measured reachability, latency, and
// (when a download test is requested) throughput.
type CDNSpeedResult struct {
	IP        string  `json:"ip"`
	Reachable bool    `json:"reachable"`
	LatencyMs int64   `json:"latency_ms"`        // TLS handshake round-trip; 0 when unreachable
	SpeedMbps float64 `json:"speed_mbps"`        // download throughput; 0 when not tested / unreachable
	Error     string  `json:"error,omitempty"`
}

// cdnProbeTimeout caps each candidate's TLS handshake. Cloudflare anycast edges
// answer in tens of ms from a healthy VPS; a 3s ceiling marks the dead ones as
// unreachable without stalling the whole sweep.
const cdnProbeTimeout = 3 * time.Second

// cdnDownloadTimeout caps the throughput probe. The download test is the slow
// dimension, so it gets its own, more generous ceiling.
const cdnDownloadTimeout = 8 * time.Second

// cdnProbeSNI is the default ServerName sent in the probe ClientHello. A
// Cloudflare edge rejects a no-SNI handshake on most of its anycast IPs (it
// needs SNI to select a certificate), so probing without one marks reachable,
// fast edges as "unreachable" — a false negative we hit in the field. Any
// CF-served hostname elicits a handshake; we use a stable, always-fronted one.
// Callers may override with the operator's real CDN domain for a faithful path.
const cdnProbeSNI = "cloudflare.com"

// cdnDownloadHost is Cloudflare's own public speed-test origin. `/__down?bytes=N`
// streams N bytes of incompressible filler from the nearest edge, so dialing a
// candidate IP and requesting it measures real edge→VPS throughput for that IP.
const cdnDownloadHost = "speed.cloudflare.com"

// defaultDownloadBytes is the payload size for the throughput probe. 4 MiB is
// enough to get past TCP slow-start and read a stable rate without making the
// whole sweep crawl.
const defaultDownloadBytes = 4 << 20

// SpeedtestOptions tunes a sweep. SNI overrides the probe ServerName; Download
// turns on the throughput dimension; DownloadBytes overrides the payload size.
type SpeedtestOptions struct {
	SNI           string
	Download      bool
	DownloadBytes int
}

// SpeedtestCDN measures each candidate IP's reachability + TLS-handshake latency
// on :443, and (when opts.Download is set) its download throughput. The returned
// slice is sorted best-first by latency (reachable before unreachable, then
// lowest handshake RTT). Latency is the ranking signal because it is highly
// reproducible; throughput is reported as a secondary, informational figure.
//
// Two phases:
//   - Latency: concurrent. Handshakes are cheap and don't contend for bandwidth,
//     so probing all IPs at once is both fast and stable.
//   - Throughput: SERIAL, best-latency-first. Running downloads concurrently
//     split the VPS uplink N ways — and N drifted as probes finished — so the
//     Mbps was neither stable nor comparable run-to-run (the "every test gives a
//     different number" report). One download at a time gives each candidate the
//     full pipe, so the figures are repeatable and rank-able.
func SpeedtestCDN(ctx context.Context, ips []string, opts SpeedtestOptions) []CDNSpeedResult {
	sni := opts.SNI
	if sni == "" {
		sni = cdnProbeSNI
	}
	dlBytes := opts.DownloadBytes
	if dlBytes <= 0 {
		dlBytes = defaultDownloadBytes
	}

	// Phase 1 — concurrent latency / reachability sweep.
	results := make([]CDNSpeedResult, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			results[i] = probeCDNIP(ctx, ip, sni)
		}(i, ip)
	}
	wg.Wait()

	// Rank by latency first so the serial download phase below tests the most
	// promising edges first (and, if ctx is cancelled mid-sweep, we still got
	// throughput for the ones that matter).
	sort.SliceStable(results, func(a, b int) bool {
		if results[a].Reachable != results[b].Reachable {
			return results[a].Reachable // reachable first
		}
		return results[a].LatencyMs < results[b].LatencyMs
	})

	// Phase 2 — serial throughput, reachable IPs in latency order.
	if opts.Download {
		for i := range results {
			if !results[i].Reachable {
				continue
			}
			if ctx.Err() != nil {
				break
			}
			if mbps, err := downloadProbeCDNIP(ctx, results[i].IP, dlBytes); err == nil {
				results[i].SpeedMbps = mbps
			}
		}
	}
	return results
}

func probeCDNIP(ctx context.Context, ip, sni string) CDNSpeedResult {
	c, cancel := context.WithTimeout(ctx, cdnProbeTimeout)
	defer cancel()

	d := &tls.Dialer{
		Config: &tls.Config{
			InsecureSkipVerify: true, // latency probe only — not validating identity
			ServerName:         sni,  // required: CF edges reject a no-SNI ClientHello
		},
	}
	start := time.Now()
	conn, err := d.DialContext(c, "tcp", net.JoinHostPort(ip, "443"))
	if err != nil {
		return CDNSpeedResult{IP: ip, Reachable: false, Error: err.Error()}
	}
	latency := time.Since(start).Milliseconds()
	_ = conn.Close()
	return CDNSpeedResult{IP: ip, Reachable: true, LatencyMs: latency}
}

// downloadProbeCDNIP measures download throughput (Mbps) by pulling `bytes` from
// speed.cloudflare.com/__down through the specified Cloudflare anycast IP. The
// transport's DialContext pins every connection to that IP while TLS SNI / the
// Host header stay the real speed-test hostname, so the edge serves normally.
func downloadProbeCDNIP(ctx context.Context, ip string, bytes int) (float64, error) {
	c, cancel := context.WithTimeout(ctx, cdnDownloadTimeout)
	defer cancel()

	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			// Ignore the request's host:port — always dial the chosen edge IP.
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip, "443"))
		},
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true, ServerName: cdnDownloadHost},
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: cdnProbeTimeout,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}

	url := "https://" + cdnDownloadHost + "/__down?bytes=" + strconv.Itoa(bytes)
	req, err := http.NewRequestWithContext(c, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Host = cdnDownloadHost

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Time only the body transfer — start the clock after the response headers
	// arrive, so dial + TLS handshake + time-to-first-byte don't inflate the
	// denominator. On a 4 MiB payload the handshake (tens of ms) otherwise
	// dominates and drags the reported Mbps down unevenly between candidates.
	start := time.Now()
	n, err := io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(start).Seconds()
	if err != nil && n == 0 {
		return 0, err
	}
	if elapsed <= 0 {
		return 0, nil
	}
	return float64(n*8) / elapsed / 1e6, nil // bits / seconds / 1e6 = Mbps
}

// TopNFastest returns just the IP strings of the fastest reachable results, in
// order. Callers feed this straight into the preferred-IP pool.
func TopNFastest(results []CDNSpeedResult, n int) []string {
	out := make([]string, 0, n)
	for _, r := range results {
		if !r.Reachable {
			continue
		}
		out = append(out, r.IP)
		if len(out) >= n {
			break
		}
	}
	return out
}
