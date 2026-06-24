package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/quota"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// jsonFlag reads a boolean-ish flag from an inbound's settings JSON. Values are
// written by the wizard as either real booleans or "true"/"false" strings, so
// accept both. Missing / unparseable → false.
func jsonFlag(raw, key string) bool {
	if raw == "" {
		return false
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return false
	}
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// StatsSummary aggregates traffic + quota usage BY USER (email), mirroring how
// the enforcer (quota.EvaluateByUser) actually applies quota/expiry: traffic is
// summed across a user's credentials, quota/expiry are the max non-zero value,
// and the whole user flips disabled when over quota or expired. A per-credential
// view would be misleading — traffic is credited to one representative
// credential per user, so the other rows always read 0.
//
// Each user row also carries the set of protocols (one per inbound the user has
// a credential on) so the panel can tag VLESS / Hysteria2(UDP) / CDN / Argo etc.
//
// GET /api/v1/stats/summary
func (h *Handler) StatsSummary(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	inbounds, err := h.store.ListInbounds(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	type protoTag struct {
		Type    string `json:"type"`
		Engine  string `json:"engine"`
		Network string `json:"network"`
		Port    int    `json:"port"`
		CDN     bool   `json:"cdn"`
		Argo    bool   `json:"argo"`
	}
	type userRow struct {
		Email        string     `json:"email"`
		Enabled      bool       `json:"enabled"` // any credential still enabled
		TrafficUp    int64      `json:"traffic_up"`
		TrafficDown  int64      `json:"traffic_down"`
		QuotaBytes   int64      `json:"quota_bytes"`
		QuotaUsedPct float64    `json:"quota_used_pct"` // -1 = unlimited
		ExpiryAt     int64      `json:"expiry_at"`
		OverQuota    bool       `json:"over_quota"`
		Expired      bool       `json:"expired"`
		Protocols    []protoTag `json:"protocols"`
	}

	now := time.Now().Unix()
	byEmail := map[string]*userRow{}
	var order []string
	var totalUp, totalDown int64

	for _, ib := range inbounds {
		tag := protoTag{
			Type:    ib.Type,
			Engine:  ib.Engine,
			Network: ib.Network,
			Port:    ib.Port,
			CDN:     jsonFlag(ib.Settings, "cdn_mode"),
			Argo:    jsonFlag(ib.Settings, "argo_bound"),
		}
		for _, cl := range ib.Clients {
			totalUp += cl.TrafficUp
			totalDown += cl.TrafficDown
			u, ok := byEmail[cl.Email]
			if !ok {
				u = &userRow{Email: cl.Email, QuotaUsedPct: -1}
				byEmail[cl.Email] = u
				order = append(order, cl.Email)
			}
			u.TrafficUp += cl.TrafficUp
			u.TrafficDown += cl.TrafficDown
			if cl.QuotaBytes > u.QuotaBytes {
				u.QuotaBytes = cl.QuotaBytes
			}
			if cl.ExpiryAt > u.ExpiryAt {
				u.ExpiryAt = cl.ExpiryAt
			}
			if cl.Enabled {
				u.Enabled = true
			}
			u.Protocols = append(u.Protocols, tag)
		}
	}

	enabledUsers, overQuotaUsers, expiredUsers := 0, 0, 0
	rows := make([]userRow, 0, len(order))
	for _, email := range order {
		u := byEmail[email]
		used := u.TrafficUp + u.TrafficDown
		if u.QuotaBytes > 0 {
			u.QuotaUsedPct = float64(used) / float64(u.QuotaBytes) * 100.0
			u.OverQuota = used >= u.QuotaBytes
		}
		u.Expired = u.ExpiryAt > 0 && u.ExpiryAt < now
		if u.Enabled {
			enabledUsers++
		}
		if u.OverQuota {
			overQuotaUsers++
		}
		if u.Expired {
			expiredUsers++
		}
		rows = append(rows, *u)
	}
	// Heaviest users first so the operator sees top consumers at the top.
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].TrafficUp+rows[i].TrafficDown > rows[j].TrafficUp+rows[j].TrafficDown
	})

	core.OK(c, gin.H{
		"users":         rows,
		"total_up":      totalUp,
		"total_down":    totalDown,
		"enabled_users": enabledUsers,
		"over_quota":    overQuotaUsers,
		"expired":       expiredUsers,
	})
}

// QuotaEnforce runs the quota+expiry evaluator now and disables matching
// clients. The scheduler calls the same code on a timer; this endpoint lets
// operators trigger it manually after editing a quota.
//
// POST /api/v1/quota/enforce
func (h *Handler) QuotaEnforce(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	enf := &quota.Enforcer{
		Store: h.store,
		Apply: func(ctx context.Context, n uint) error {
			if h.orch == nil {
				return nil
			}
			return h.applyAfterChange(c, n)
		},
		Audit: func(action, resource string, meta map[string]string) {
			h.auditLog(c, action, resource, meta)
		},
	}
	res, err := enf.EnforceAll(c.Request.Context(), nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "ENFORCE_FAILED", err.Error())
		return
	}
	core.OK(c, res)
}

// HealthSnapshots returns the N most recent persisted health rows. Default
// limit 50, max 500.
//
// GET /api/v1/health/snapshots?limit=N
func (h *Handler) HealthSnapshots(c *gin.Context) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	var rows []model.HealthSnapshot
	if err := h.store.DB().
		Where("node_id = ?", h.parseLocalNodeID()).
		Order("id desc").
		Limit(limit).
		Find(&rows).Error; err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, rows)
}

// HealthLatest forces a fresh sample (calls SelfCheck on the node) and
// returns it without persisting — useful for the "Run check now" button.
//
// POST /api/v1/health/check
func (h *Handler) HealthLatest(c *gin.Context) {
	snap, err := h.node.SelfCheck(c.Request.Context(), h.localNodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "SELFCHECK_FAILED", err.Error())
		return
	}
	core.OK(c, snap)
}

// AuditList returns the N most recent audit rows, optionally filtered by
// action (exact match) and actor.
//
// GET /api/v1/audit?limit=N&action=X&actor=Y
func (h *Handler) AuditList(c *gin.Context) {
	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	q := h.store.DB().Model(&model.AuditLog{}).Order("id desc").Limit(limit)
	if a := c.Query("action"); a != "" {
		q = q.Where("action = ?", a)
	}
	if a := c.Query("actor"); a != "" {
		q = q.Where("actor = ?", a)
	}
	var rows []model.AuditLog
	if err := q.Find(&rows).Error; err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, rows)
}

// AuditActors returns the distinct actor values present in the audit log so the
// panel can offer them as a filter dropdown instead of free-text exact match.
//
// GET /api/v1/audit/actors
func (h *Handler) AuditActors(c *gin.Context) {
	var actors []string
	if err := h.store.DB().Model(&model.AuditLog{}).
		Distinct().Order("actor").Pluck("actor", &actors).Error; err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, gin.H{"actors": actors})
}

// ClearAudit deletes all audit rows. A privacy / housekeeping action mirroring
// the engine-log "clear" button: it leaves the table empty and does NOT write a
// trailing "cleared" row (honouring the operator's intent to wipe history).
//
// POST /api/v1/audit/clear
func (h *Handler) ClearAudit(c *gin.Context) {
	res := h.store.DB().Where("1 = 1").Delete(&model.AuditLog{})
	if res.Error != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", res.Error.Error())
		return
	}
	core.OK(c, gin.H{"cleared": res.RowsAffected})
}

// GetAuditConfig reports whether audit logging is currently on.
//
// GET /api/v1/audit/config
func (h *Handler) GetAuditConfig(c *gin.Context) {
	core.OK(c, gin.H{"enabled": h.auditEnabled.Load()})
}

// PutAuditConfig toggles audit logging on/off. The change itself is always
// recorded (via writeAudit, bypassing the toggle) so a disable is captured as
// the final entry; events that occur while off are never back-filled.
//
// PUT /api/v1/audit/config
func (h *Handler) PutAuditConfig(c *gin.Context) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	h.writeAudit(c, "audit.config", "audit", map[string]string{"enabled": boolStr(body.Enabled)})
	h.auditEnabled.Store(body.Enabled)
	if err := h.store.SetSetting("audit_enabled", boolStr(body.Enabled)); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	core.OK(c, gin.H{"enabled": body.Enabled})
}
