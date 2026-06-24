package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/unlock"
	"github.com/aipo-lenshow/EdgeNest/internal/control/warpprobe"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// UnlockProbe runs the streaming/AI unlock detection panel against the
// host's direct egress. The overall request has a 10s deadline; individual
// probes have a 5s per-target deadline.
//
// POST /api/v1/unlock/probe
func (h *Handler) UnlockProbe(c *gin.Context) {
	// Generous overall deadline: multi-step checks (Netflix probes 3 titles,
	// Bahamut chains 4 requests) run within a single target slot, and all
	// targets run concurrently — so wall-clock is the slowest single chain.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	res := unlock.Run(ctx, unlock.NewHTTPProber(), unlock.DefaultTargets)
	h.auditLog(c, "unlock.probe", "unlock", nil)
	core.OK(c, gin.H{
		"targets": res,
		"ran_at":  time.Now().Unix(),
	})
}

// UnlockProbeWARP runs the same detection panel, but every probe egresses
// through a one-shot userspace WARP tunnel instead of the host's direct route.
// Paired with UnlockProbe it gives the operator a direct-vs-relay comparison:
// targets blocked direct but ok through WARP are the ones a WARP route fixes.
//
// POST /api/v1/unlock/probe-warp
func (h *Handler) UnlockProbeWARP(c *gin.Context) {
	w, err := h.store.GetWarp(h.parseLocalNodeID())
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if w == nil || w.PrivateKey == "" {
		core.Fail(c, http.StatusBadRequest, "WARP_NOT_CONFIGURED",
			"register WARP first (Outbound & relay → WARP)")
		return
	}

	tun, err := warpprobe.Open(w)
	if err != nil {
		core.Fail(c, http.StatusBadGateway, "WARP_TUNNEL_FAILED", err.Error())
		return
	}
	defer tun.Close()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 40*time.Second)
	defer cancel()
	// Probe through the WARP tunnel's dialer, with the same Chrome fingerprint
	// the direct prober uses — so the direct-vs-WARP comparison is apples to
	// apples and reflects real-browser reachability.
	prober := unlock.NewDialProber(tun.DialContext, 8*time.Second)
	res := unlock.Run(ctx, prober, unlock.DefaultTargets)

	h.auditLog(c, "unlock.probe_warp", "unlock", nil)
	core.OK(c, gin.H{
		"targets": res,
		"ran_at":  time.Now().Unix(),
		"via":     "warp",
	})
}

// UnlockTargets returns the default target catalogue (id + name + URL) so the
// UI can show what is being probed before the operator clicks Run.
//
// GET /api/v1/unlock/targets
func (h *Handler) UnlockTargets(c *gin.Context) {
	out := make([]gin.H, 0, len(unlock.DefaultTargets))
	for _, t := range unlock.DefaultTargets {
		out = append(out, gin.H{"id": t.ID, "name": t.Name, "url": t.URL, "category": t.Category})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": out})
}
