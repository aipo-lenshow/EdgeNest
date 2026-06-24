package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/warp"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// warpDTO is the request/response body for /api/v1/warp. We deliberately do
// NOT echo PrivateKey back on GET — that field is write-only via the API.
type warpDTO struct {
	Enabled    bool   `json:"enabled"`
	PrivateKey string `json:"private_key,omitempty"`
	PublicKey  string `json:"public_key"`
	Address4   string `json:"address4"`
	Address6   string `json:"address6"`
	Reserved   []int  `json:"reserved"`
	Endpoint   string `json:"endpoint"`
	UpdatedAt  int64  `json:"updated_at"`
}

// GetWarp returns the current WARP outbound config for the local node, with
// the private key redacted (the field is omitted from the response).
//
// GET /api/v1/warp
func (h *Handler) GetWarp(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	w, err := h.store.GetWarp(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if w == nil {
		core.OK(c, warpDTO{}) // empty = not configured
		return
	}
	core.OK(c, warpDTO{
		Enabled:   w.Enabled,
		PublicKey: w.PublicKey,
		Address4:  w.Address4,
		Address6:  w.Address6,
		Reserved:  parseReservedJSON(w.Reserved),
		Endpoint:  w.Endpoint,
		UpdatedAt: w.UpdatedAt,
	})
}

// PutWarp upserts the WARP config. PrivateKey is required when Enabled=true.
// Passing an empty PrivateKey while Enabled=true is rejected so the engine
// never emits a half-configured WireGuard outbound.
//
// PUT /api/v1/warp
func (h *Handler) PutWarp(c *gin.Context) {
	var body warpDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()

	// private_key is write-only: GET never echoes it, so the normal "toggle
	// Enable + Save" round-trip arrives with an empty private_key. Preserve the
	// stored key (mirrors how advanced.go preserves ArgoToken) instead of
	// wiping it — otherwise enabling a registered WARP fails with "private_key
	// required" even though the key is right there in the DB.
	existing, _ := h.store.GetWarp(nodeID)
	privateKey := body.PrivateKey
	if strings.TrimSpace(privateKey) == "" && existing != nil {
		privateKey = existing.PrivateKey
	}

	if body.Enabled {
		if strings.TrimSpace(privateKey) == "" {
			core.Fail(c, http.StatusBadRequest, "MISSING_PRIVATE_KEY",
				"register WARP first — no private key on file")
			return
		}
		if strings.TrimSpace(body.PublicKey) == "" {
			core.Fail(c, http.StatusBadRequest, "MISSING_PUBLIC_KEY",
				"peer public_key is required when enabling WARP")
			return
		}
	}
	w := &model.WarpConfig{
		NodeID:     nodeID,
		Enabled:    body.Enabled,
		PrivateKey: privateKey,
		PublicKey:  body.PublicKey,
		Address4:   body.Address4,
		Address6:   body.Address6,
		Reserved:   encodeReservedJSON(body.Reserved),
		Endpoint:   defaultEndpoint(body.Endpoint),
	}
	if err := h.store.UpsertWarp(w); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "warp.upsert", "warp",
		map[string]string{"enabled": boolStr(body.Enabled)})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"enabled": body.Enabled})
}

// RegisterWarp provisions a fresh Cloudflare WARP account end-to-end so the
// operator does not need to run wgcf or wireguard-tools externally. Generates
// a WireGuard keypair, POSTs registration to Cloudflare, persists the result,
// and triggers a node apply. The freshly minted config is left disabled — the
// operator clicks "Enable" after reviewing it.
//
// POST /api/v1/warp/register
func (h *Handler) RegisterWarp(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	res, err := warp.NewClient().Register(ctx)
	if err != nil {
		core.Fail(c, http.StatusBadGateway, "WARP_REGISTER_FAILED", err.Error())
		return
	}

	nodeID := h.parseLocalNodeID()
	w := &model.WarpConfig{
		NodeID:     nodeID,
		Enabled:    false, // operator must opt-in after reviewing
		PrivateKey: res.PrivateKey,
		PublicKey:  res.PublicKey,
		Address4:   res.Address4,
		Address6:   res.Address6,
		Reserved:   encodeReservedJSON(res.Reserved),
		Endpoint:   res.Endpoint,
	}
	if err := h.store.UpsertWarp(w); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "warp.register", "warp", map[string]string{"address4": res.Address4})
	// Echo back the same shape GetWarp uses so the UI can refresh form fields
	// in one round-trip. Private key is omitted (write-only invariant).
	core.OK(c, warpDTO{
		Enabled:   false,
		PublicKey: res.PublicKey,
		Address4:  res.Address4,
		Address6:  res.Address6,
		Reserved:  res.Reserved,
		Endpoint:  res.Endpoint,
	})
}

// DeleteWarp removes the WARP config entirely.
//
// DELETE /api/v1/warp
func (h *Handler) DeleteWarp(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	if err := h.store.DeleteWarp(nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "warp.delete", "warp", nil)
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"deleted": true})
}

// defaultEndpoint falls back to Cloudflare's public WARP endpoint when the
// operator leaves the field blank — that's the value 99% of users want.
func defaultEndpoint(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "engage.cloudflareclient.com:2408"
	}
	return s
}

func parseReservedJSON(raw string) []int {
	if raw == "" {
		return nil
	}
	var v []int
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

func encodeReservedJSON(v []int) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func intStr(n int) string { return strconv.Itoa(n) }
