package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/usersvc"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// User-centric view over the (inbound, client) credential rows.
//
// A "user" is identified by the shared Client.Email — the same email on
// multiple inbounds is one logical person whose traffic is summed and whose
// quota/expiry are enforced together (the share resolver already fans a
// subscription out across every same-email client). The multi-user tab turns
// that email-keyed reality into a first-class list instead of forcing the
// operator to edit credentials inbound by inbound.
//
// The create/update/delete mutations live in package usersvc so the HTTP API
// and the Telegram management bot drive the exact same code path. These handlers
// stay thin: translate the request, call the service, map the coded error.

// userSvc builds a request-scoped service: Apply re-renders + pushes config
// (using the request context), Audit records under the authenticated operator.
func (h *Handler) userSvc(c *gin.Context) *usersvc.Service {
	return &usersvc.Service{
		Store: h.store,
		Apply: func(ctx context.Context, nodeID uint) error {
			if h.orch == nil {
				return nil
			}
			res, err := h.orch.Apply(ctx, nodeID)
			if err != nil {
				return err
			}
			if !res.OK {
				return errFromApply(res)
			}
			return nil
		},
		Audit: func(action, resource string, meta map[string]string) {
			h.auditLog(c, action, resource, meta)
		},
	}
}

// userSvcStatus maps a usersvc.Error code to its HTTP status.
func userSvcStatus(code string) int {
	switch code {
	case "EMAIL_EXISTS", "APPLY_FAILED", "MEMBERSHIP":
		return http.StatusConflict
	case "NO_INBOUND":
		return http.StatusUnprocessableEntity
	case "NOT_FOUND":
		return http.StatusNotFound
	case "BAD_REQUEST":
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// failUserSvc surfaces a service error with its code + status; non-service
// errors fall back to a generic 500.
func failUserSvc(c *gin.Context, err error) {
	if e, ok := err.(*usersvc.Error); ok {
		core.Fail(c, userSvcStatus(e.Code), e.Code, e.Msg)
		return
	}
	core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
}

// userRow is one aggregated user in the multi-user list.
type userRow struct {
	Email        string   `json:"email"`
	InboundTags  []string `json:"inbound_tags"`
	InboundIDs   []uint   `json:"inbound_ids"`
	InboundCount int      `json:"inbound_count"`
	TrafficUp    int64    `json:"traffic_up"`
	TrafficDown  int64    `json:"traffic_down"`
	QuotaBytes   int64    `json:"quota_bytes"`    // 0 = unlimited
	QuotaUsedPct float64  `json:"quota_used_pct"` // -1 = unlimited
	ExpiryAt     int64    `json:"expiry_at"`      // 0 = never
	Enabled      bool     `json:"enabled"`        // true only when every client is enabled
	OverQuota    bool     `json:"over_quota"`
	SubID        uint     `json:"sub_id"` // 0 = no bundle subscription
}

// ListUsers aggregates clients by email into the user-centric view.
//
// GET /api/v1/users
func (h *Handler) ListUsers(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	inbounds, err := h.store.ListInbounds(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}

	// email -> running aggregate (insertion order preserved via `order`).
	agg := map[string]*userRow{}
	var order []string
	for _, ib := range inbounds {
		for _, cl := range ib.Clients {
			if cl.Email == "" {
				continue
			}
			u, ok := agg[cl.Email]
			if !ok {
				u = &userRow{Email: cl.Email, Enabled: true, QuotaUsedPct: -1}
				agg[cl.Email] = u
				order = append(order, cl.Email)
			}
			u.InboundTags = append(u.InboundTags, ib.Tag)
			u.InboundIDs = append(u.InboundIDs, ib.ID)
			u.InboundCount++
			u.TrafficUp += cl.TrafficUp
			u.TrafficDown += cl.TrafficDown
			// Quota/expiry are per-user in the user-centric model; the largest
			// non-zero value across the email's clients wins so a stray 0 row
			// (e.g. added before a quota was set) doesn't hide the real cap.
			if cl.QuotaBytes > u.QuotaBytes {
				u.QuotaBytes = cl.QuotaBytes
			}
			if cl.ExpiryAt > u.ExpiryAt {
				u.ExpiryAt = cl.ExpiryAt
			}
			if !cl.Enabled {
				u.Enabled = false
			}
		}
	}

	// Attach the bundle subscription id (if any) by matching the seed client's
	// email. A user can have at most one bundle here (the create flow makes one).
	subs, _ := h.store.ListSubscriptions()
	for _, sub := range subs {
		cl, err := h.store.GetClient(sub.ClientID)
		if err != nil || cl == nil {
			continue
		}
		if u, ok := agg[cl.Email]; ok && u.SubID == 0 {
			u.SubID = sub.ID
		}
	}

	rows := make([]userRow, 0, len(order))
	for _, email := range order {
		u := agg[email]
		if u.QuotaBytes > 0 {
			used := u.TrafficUp + u.TrafficDown
			u.QuotaUsedPct = float64(used) / float64(u.QuotaBytes) * 100.0
			u.OverQuota = u.QuotaUsedPct >= 100.0
		}
		rows = append(rows, *u)
	}
	core.OK(c, gin.H{"users": rows})
}

type createUserRequest struct {
	Email      string `json:"email"`       // optional; blank → next NNN@EdgeNest.Local
	QuotaBytes int64  `json:"quota_bytes"` // 0 = unlimited
	ExpiryDays int    `json:"expiry_days"` // 0 = never; N = end of (today+N)th day; negatives allowed (test)
	InboundIDs []uint `json:"inbound_ids"` // empty = every eligible enabled inbound
}

// CreateUser provisions one logical user across the chosen inbounds.
//
// POST /api/v1/users
func (h *Handler) CreateUser(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	res, err := h.userSvc(c).Create(c.Request.Context(), h.parseLocalNodeID(), usersvc.CreateParams{
		Email:      req.Email,
		QuotaBytes: req.QuotaBytes,
		ExpiryDays: req.ExpiryDays,
		InboundIDs: req.InboundIDs,
	})
	if err != nil {
		failUserSvc(c, err)
		return
	}
	core.Created(c, gin.H{
		"email":         res.Email,
		"sub_id":        res.SubID,
		"sub_token":     res.SubToken,
		"sub_url":       "/sub/" + res.SubToken,
		"inbound_tags":  res.InboundTags,
		"inbound_count": len(res.InboundTags),
		"skipped":       res.Skipped,
	})
}

type updateUserRequest struct {
	NewEmail   *string `json:"new_email"` // rename the user across every inbound
	QuotaBytes *int64  `json:"quota_bytes"`
	ExpiryDays *int    `json:"expiry_days"` // re-derived to end-of-day, server TZ
	Enabled    *bool   `json:"enabled"`
	ResetUsage *bool   `json:"reset_usage"`
	InboundIDs *[]uint `json:"inbound_ids"` // reconcile which inbounds this user is on
}

// UpdateUser patches quota / expiry / enabled across every client of one user.
//
// PATCH /api/v1/users/:email
func (h *Handler) UpdateUser(c *gin.Context) {
	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	res, err := h.userSvc(c).Update(c.Request.Context(), h.parseLocalNodeID(), c.Param("email"), usersvc.UpdateParams{
		NewEmail:   req.NewEmail,
		QuotaBytes: req.QuotaBytes,
		ExpiryDays: req.ExpiryDays,
		Enabled:    req.Enabled,
		ResetUsage: req.ResetUsage,
		InboundIDs: req.InboundIDs,
	})
	if err != nil {
		failUserSvc(c, err)
		return
	}
	core.OK(c, gin.H{"email": res.Email, "clients": res.Clients})
}

// DeleteUser removes every client of a user and any bundle subscription.
//
// DELETE /api/v1/users/:email
func (h *Handler) DeleteUser(c *gin.Context) {
	res, err := h.userSvc(c).Delete(c.Request.Context(), h.parseLocalNodeID(), c.Param("email"))
	if err != nil {
		failUserSvc(c, err)
		return
	}
	core.OK(c, gin.H{"email": res.Email, "deleted_clients": res.DeletedClients})
}
