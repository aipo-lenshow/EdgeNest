package api

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/cdnstats"
	"github.com/aipo-lenshow/EdgeNest/internal/control/share"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// maxReportBytes bounds a probe report; a full round is well under 4 KB.
const maxReportBytes = 16 << 10

// recommendedPerNetwork is how many of a network's ranked addresses go to the
// head of the candidate list and into X-EdgeNest-CDN-Recommended.
const recommendedPerNetwork = 3

// CDNReport pools one EdgeNest client's probe round of Cloudflare addresses.
// The subscription token only authorises the report and keys the in-memory
// rate limit; what is stored is the client's network label and the per-address
// results, nothing that identifies the reporter.
//
// POST /sub/:token/cdn-report
func (h *Handler) CDNReport(c *gin.Context) {
	sub, err := h.store.GetSubscriptionByTokenHash(store.HashToken(c.Param("token")))
	if err != nil || sub == nil || sub.Revoked || (sub.ExpiresAt > 0 && sub.ExpiresAt < time.Now().Unix()) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	pool := h.cdnPoolForLocalNode()
	if len(pool) == 0 {
		c.JSON(http.StatusOK, gin.H{"accepted": false, "reason": "cdn_off"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxReportBytes)
	var r cdnstats.Report
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"accepted": false, "reason": "bad_body"})
		return
	}
	policy := cdnstats.LoadPolicy(h.store)
	reply := gin.H{"accepted": false, "policy": policy}
	cleaned, reason := cdnstats.Clean(r, share.ClientCandidates(pool))
	if reason != "" {
		reply["reason"] = reason
		c.JSON(http.StatusOK, reply)
		return
	}
	h.cdnLimiterOnce.Do(func() { h.cdnLimiter = cdnstats.NewLimiter() })
	v := h.cdnLimiter.Check(sub.TokenHash, r, cleaned, policy)
	if !v.CoolingUntil.IsZero() {
		reply["cooling_until"] = v.CoolingUntil.Unix()
	}
	if !v.Accept {
		reply["reason"] = v.Reason
		c.JSON(http.StatusOK, reply)
		return
	}
	if err := h.store.AddCDNSamples(cdnstats.Day(time.Now()), r.NetKey, cdnstats.Label(r.NetLabel), cleaned); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	reply["accepted"] = true
	c.JSON(http.StatusOK, reply)
}

// cdnNetwork is the pooled view of one network, or nil when there is none.
func (h *Handler) cdnNetwork(netKey string) *cdnstats.Network {
	if !cdnstats.ValidNetKey(netKey) {
		return nil
	}
	rows, err := h.store.CDNStatsSince(cdnstats.Day(time.Now().Add(-cdnstats.Window)), netKey)
	if err != nil || len(rows) == 0 {
		return nil
	}
	nets := cdnstats.Summarise(rows)
	return &nets[0]
}

// clientNetKey reads X-EdgeNest-Net: percent-encoded by clients whose HTTP
// stack only sends ASCII header values, raw UTF-8 from others.
func clientNetKey(h string) string {
	h = strings.TrimSpace(h)
	if strings.Contains(h, "%") {
		if v, err := url.QueryUnescape(h); err == nil {
			return v
		}
	}
	return h
}

// setCDNSubHeaders adds the CDN headers to a subscription response: the
// candidates to probe — ordered for the client's network when that network has
// pooled results (X-EdgeNest-Net, sent by the client) — the addresses ranked
// there, and the manual re-optimise policy.
func (h *Handler) setCDNSubHeaders(c *gin.Context, pool []string) {
	cands := share.ClientCandidates(pool)
	if len(cands) == 0 {
		return
	}
	if n := h.cdnNetwork(clientNetKey(c.GetHeader("X-EdgeNest-Net"))); n != nil {
		rec := n.Recommended(recommendedPerNetwork)
		cands = cdnstats.Order(cands, rec)
		if len(rec) > 0 {
			c.Header("X-EdgeNest-CDN-Recommended", strings.Join(rec, ","))
		}
		if n.CDNDown {
			c.Header("X-EdgeNest-CDN-Down", "1")
		}
	}
	c.Header("X-EdgeNest-CDN-Candidates", strings.Join(cands, ","))
	c.Header("X-EdgeNest-CDN-Policy", cdnstats.LoadPolicy(h.store).Header())
}

// CDNNetworks lists what clients pooled per network over the window, plus the
// candidate set they are handed and how many networks each address works on.
//
// GET /api/v1/advanced/cdn/networks
func (h *Handler) CDNNetworks(c *gin.Context) {
	rows, err := h.store.CDNStatsSince(cdnstats.Day(time.Now().Add(-cdnstats.Window)), "")
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	nets := cdnstats.Summarise(rows)
	good := map[string]int{}
	for _, n := range nets {
		for _, ip := range n.Recommended(recommendedPerNetwork) {
			good[ip]++
		}
	}
	pool := h.cdnPoolForLocalNode()
	inPool := map[string]bool{}
	for _, ip := range pool {
		inPool[strings.TrimSpace(ip)] = true
	}
	type candidate struct {
		IP       string `json:"ip"`
		Source   string `json:"source"` // pool | builtin
		GoodNets int    `json:"good_nets"`
	}
	var cands []candidate
	for _, ip := range share.ClientCandidates(pool) {
		src := "builtin"
		if inPool[ip] {
			src = "pool"
		}
		cands = append(cands, candidate{IP: ip, Source: src, GoodNets: good[ip]})
	}
	core.OK(c, gin.H{
		"networks":       nets,
		"candidates":     cands,
		"window_days":    int(cdnstats.Window / (24 * time.Hour)),
		"retention_days": cdnstats.RetentionDays(),
	})
}

// GetCDNPolicy returns the manual re-optimise policy.
//
// GET /api/v1/advanced/cdn/policy
func (h *Handler) GetCDNPolicy(c *gin.Context) {
	core.OK(c, gin.H{"policy": cdnstats.LoadPolicy(h.store), "default": cdnstats.DefaultPolicy})
}

// PutCDNPolicy stores the manual re-optimise policy. It only shapes what the
// subscription response tells clients, so the engine is not reapplied.
//
// PUT /api/v1/advanced/cdn/policy
func (h *Handler) PutCDNPolicy(c *gin.Context) {
	var p cdnstats.Policy
	if err := c.ShouldBindJSON(&p); err != nil || !p.Valid() {
		core.Fail(c, http.StatusBadRequest, "BAD_POLICY",
			"interval 30-86400 s, same rounds 1-20, cooldown 0-604800 s")
		return
	}
	if err := cdnstats.SavePolicy(h.store, p); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "advanced.cdn.policy", "advanced", map[string]string{
		"interval": intStr(p.IntervalSec), "same": intStr(p.SameRounds), "cooldown": intStr(p.CooldownSec),
	})
	core.OK(c, gin.H{"policy": p})
}
