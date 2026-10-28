package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/reality"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// serverIPv4 is the address the neighbour scan looks around.
func serverIPv4() string {
	cp := core.ReadNodeCapability(core.DefaultCapabilityPath)
	return reality.ServerIPv4(append([]string{cp.IPv4Addr}, cp.IPv4Addrs...)...)
}

// GetRealityNeighbors returns the cached neighbour scan (empty when none ran)
// plus the fallback the panel shows when nothing was found.
//
// GET /api/v1/reality/neighbors
func (h *Handler) GetRealityNeighbors(c *gin.Context) {
	raw, _ := h.store.GetSetting(reality.SettingKey)
	r := reality.Load(raw)
	if r == nil {
		r = &reality.Result{ServerIP: serverIPv4()}
	}
	if r.Candidates == nil {
		r.Candidates = []reality.Candidate{}
	}
	core.OK(c, gin.H{"result": r, "fallback": reality.FallbackSNI})
}

// ScanRealityNeighbors scans the server's /24 now (about half a minute) and
// returns the fresh result.
//
// POST /api/v1/reality/neighbors/scan
func (h *Handler) ScanRealityNeighbors(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	r, err := reality.Scan(ctx, h.store, serverIPv4())
	switch {
	case errors.Is(err, reality.ErrBusy):
		core.Fail(c, http.StatusConflict, "SCAN_BUSY", err.Error())
		return
	case errors.Is(err, reality.ErrNoIPv4):
		core.Fail(c, http.StatusBadRequest, "NO_IPV4", err.Error())
		return
	case err != nil:
		core.Fail(c, http.StatusInternalServerError, "SCAN_FAILED", err.Error())
		return
	}
	if r.Candidates == nil {
		r.Candidates = []reality.Candidate{}
	}
	h.auditLog(c, "reality.neighbors.scan", "reality", nil)
	core.OK(c, gin.H{"result": r, "fallback": reality.FallbackSNI})
}
