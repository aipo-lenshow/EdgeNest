package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/capture"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/share"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// captureTimeout bounds a single by-URL capture (fetch + parse) so a slow or
// hung target can't tie up the request indefinitely.
const captureTimeout = 20 * time.Second

// captureOutbounds are the outbounds the capture-apply flow accepts. Matches the
// three meaningful choices the UI offers for a captured service: send it abroad
// via WARP, force it direct, or block it.
var captureOutbounds = map[string]bool{"direct": true, "warp": true, "block": true}

// CaptureDomains fetches a URL's HTML and returns the registrable domains it
// references (static rough scan). For JS-loaded or login/playback-gated domains
// the operator uses live capture instead.
//
// POST /api/v1/routes/capture  {"url":"https://netflix.com"}
func (h *Handler) CaptureDomains(c *gin.Context) {
	var body struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	if strings.TrimSpace(body.URL) == "" {
		core.Fail(c, http.StatusBadRequest, "BAD_URL", "url is required")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), captureTimeout)
	defer cancel()
	res, err := capture.Capture(ctx, body.URL)
	if err != nil {
		core.Fail(c, http.StatusBadGateway, "CAPTURE_FAILED", err.Error())
		return
	}

	h.auditLog(c, "route.capture", "route", map[string]string{
		"url": strings.TrimSpace(body.URL), "engine": res.Engine,
		"groups": intStr(len(res.Domains)),
	})
	core.OK(c, gin.H{
		"engine":  res.Engine,
		"domains": res.Domains,
	})
}

// ApplyCapturedRoutes batch-creates domain_suffix rules from a captured domain
// list, all pointing at the chosen outbound and tagged source="captured" so the
// Routes page can filter them. Idempotent: a domain already routed to the same
// outbound is skipped, never duplicated.
//
// POST /api/v1/routes/capture/apply  {"domains":["netflix.com"],"outbound":"warp"}
func (h *Handler) ApplyCapturedRoutes(c *gin.Context) {
	var body struct {
		Domains  []string `json:"domains"`
		Outbound string   `json:"outbound"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	if len(body.Domains) == 0 {
		core.Fail(c, http.StatusBadRequest, "NO_DOMAINS", "domains must not be empty")
		return
	}
	outbound := strings.TrimSpace(body.Outbound)
	if outbound == "" {
		outbound = "warp" // capturing a service usually means routing it abroad
	}
	if !captureOutbounds[outbound] {
		core.Fail(c, http.StatusBadRequest, "BAD_OUTBOUND",
			"outbound must be one of: direct, warp, block")
		return
	}
	nodeID := h.parseLocalNodeID()

	existing, err := h.store.ListRouteRules(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	have := map[string]bool{}
	for _, r := range existing {
		have[r.Type+"|"+r.Value+"|"+r.Outbound] = true
	}

	added, skipped := 0, 0
	for _, raw := range body.Domains {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d == "" {
			continue
		}
		key := "domain_suffix|" + d + "|" + outbound
		if have[key] {
			skipped++
			continue
		}
		r := &model.RouteRule{
			NodeID:   nodeID,
			Type:     "domain_suffix",
			Value:    d,
			Outbound: outbound,
			Enabled:  true,
			Source:   share.SourceCaptured,
		}
		if err := h.store.CreateRouteRule(r); err != nil {
			core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
			return
		}
		have[key] = true
		added++
	}

	h.auditLog(c, "route.capture.apply", "route", map[string]string{
		"outbound": outbound, "added": intStr(added),
	})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"added": added, "skipped": skipped})
}
