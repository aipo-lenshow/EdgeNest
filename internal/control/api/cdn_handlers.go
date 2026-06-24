package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/share"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// CDNCandidates returns the curated Cloudflare anycast pool the UI offers for
// one-click "fill recommended". The full candidate set doubles as the input to
// the speed test; `recommended` is the shorter default subset to seed an empty
// preferred-IP pool.
//
// GET /api/v1/advanced/cdn/candidates
func (h *Handler) CDNCandidates(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"candidates":  share.CFCandidateIPs,
		"recommended": share.RecommendedCDNIPs(),
	}})
}

// cdnRefreshReq optionally tunes how many fresh hosts to sample; empty uses a
// sane default.
type cdnRefreshReq struct {
	N int `json:"n"`
}

// CDNRefreshCandidates resamples fresh Cloudflare anycast hosts from the
// published CIDR ranges (on top of the curated static reps) and returns the
// expanded candidate set for the speed test. The operator triggers it when too
// few of the fixed reps are reachable from their network — fresh random hosts
// surface live edges the /32 list misses. Nothing is persisted; the returned
// list is fed straight back into the next speed test as its `ips`.
//
// POST /api/v1/advanced/cdn/candidates/refresh
func (h *Handler) CDNRefreshCandidates(c *gin.Context) {
	var body cdnRefreshReq
	_ = c.ShouldBindJSON(&body)
	n := body.N
	if n <= 0 {
		n = 48
	}
	if n > 256 {
		n = 256
	}
	candidates := share.SampleCandidateIPs(n)
	h.auditLog(c, "advanced.cdn.refresh_candidates", "advanced", map[string]string{
		"count": intStr(len(candidates)),
	})
	core.OK(c, gin.H{"candidates": candidates})
}

// cdnSpeedtestReq lets the operator probe a custom pool; empty falls back to
// the full curated candidate set. SNI is the ServerName the probe sends — empty
// uses a default CF-served hostname (a non-empty SNI is mandatory or CF edges
// reject the handshake and every IP reads as unreachable).
type cdnSpeedtestReq struct {
	IPs  []string `json:"ips"`
	TopN int      `json:"top_n"`
	SNI  string   `json:"sni"`
	// Download turns on the throughput dimension (slower, ~MB pulled per IP via
	// Cloudflare's speed.cloudflare.com origin). When off, ranking is by TLS
	// handshake latency only — the fast default. DownloadBytes overrides the
	// per-IP payload size.
	Download      bool `json:"download"`
	DownloadBytes int  `json:"download_bytes"`
}

// CDNSpeedtest measures TLS-connect latency from this VPS to each candidate
// Cloudflare IP and returns them ranked fastest-first. The operator's "pick
// best N" then writes the top results into the preferred-IP pool. This is the
// "真·优选" step: the panel measures instead of asking the user to find IPs.
//
// POST /api/v1/advanced/cdn/speedtest
func (h *Handler) CDNSpeedtest(c *gin.Context) {
	var body cdnSpeedtestReq
	_ = c.ShouldBindJSON(&body) // empty body is fine — fall back to defaults

	ips := body.IPs
	if len(ips) == 0 {
		ips = share.CFCandidateIPs
	}
	topN := body.TopN
	if topN <= 0 {
		topN = 6
	}

	results := share.SpeedtestCDN(c.Request.Context(), ips, share.SpeedtestOptions{
		SNI:           body.SNI,
		Download:      body.Download,
		DownloadBytes: body.DownloadBytes,
	})
	best := share.TopNFastest(results, topN)

	h.auditLog(c, "advanced.cdn.speedtest", "advanced", map[string]string{
		"probed":   intStr(len(ips)),
		"download": boolStr(body.Download),
	})
	core.OK(c, gin.H{
		"results": results, // every candidate, ranked, with latency / reachability
		"best":    best,    // just the IP strings of the fastest N
	})
}
