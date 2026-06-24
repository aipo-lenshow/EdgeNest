package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/share"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// validRouteTypes mirrors sing-box's accepted route-rule keys. Keep in sync
// with internal/node/engine/singbox/render.renderRoute.
var validRouteTypes = map[string]bool{
	"domain":         true,
	"domain_suffix":  true,
	"domain_keyword": true,
	"domain_regex":   true,
	"geosite":        true,
	"geoip":          true,
	"ip_cidr":        true,
	"process_name":   true,
}

type routeDTO struct {
	ID       uint   `json:"id"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	Outbound string `json:"outbound"`
	Enabled  bool   `json:"enabled"`
	Order    int    `json:"order"`
	Source   string `json:"source"` // "ai" | "streaming" | "custom"
}

func toRouteDTO(r model.RouteRule) routeDTO {
	src := r.Source
	if src == "" {
		// Legacy row from before the Source column existed — infer so the UI's
		// source filter still groups it sensibly.
		src = share.InferSource(r.Type, r.Value)
	}
	return routeDTO{
		ID: r.ID, Type: r.Type, Value: r.Value,
		Outbound: r.Outbound, Enabled: r.Enabled, Order: r.Order, Source: src,
	}
}

// ListRoutes returns all routing rules for the local node, in declared order.
//
// GET /api/v1/routes
func (h *Handler) ListRoutes(c *gin.Context) {
	rules, err := h.store.ListRouteRules(h.parseLocalNodeID())
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	out := make([]routeDTO, 0, len(rules))
	for _, r := range rules {
		out = append(out, toRouteDTO(r))
	}
	core.OK(c, out)
}

// CreateRoute appends a new routing rule.
//
// POST /api/v1/routes
func (h *Handler) CreateRoute(c *gin.Context) {
	var body routeDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	if !validRouteTypes[body.Type] {
		core.Fail(c, http.StatusBadRequest, "BAD_TYPE",
			"route type must be one of: domain, domain_suffix, domain_keyword, "+
				"domain_regex, geosite, geoip, ip_cidr, process_name")
		return
	}
	if strings.TrimSpace(body.Value) == "" {
		core.Fail(c, http.StatusBadRequest, "BAD_VALUE", "value is required")
		return
	}
	if strings.TrimSpace(body.Outbound) == "" {
		core.Fail(c, http.StatusBadRequest, "BAD_OUTBOUND", "outbound is required")
		return
	}
	nodeID := h.parseLocalNodeID()
	src := strings.TrimSpace(body.Source)
	if src == "" {
		src = share.SourceCustom // default tag for hand-entered rules
	}
	r := &model.RouteRule{
		NodeID:   nodeID,
		Type:     body.Type,
		Value:    strings.TrimSpace(body.Value),
		Outbound: strings.TrimSpace(body.Outbound),
		Enabled:  body.Enabled,
		Order:    body.Order,
		Source:   src,
	}
	if err := h.store.CreateRouteRule(r); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "route.create", "route",
		map[string]string{"type": r.Type, "value": r.Value, "outbound": r.Outbound})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, toRouteDTO(*r))
}

// UpdateRoute overwrites a routing rule in place.
//
// PUT /api/v1/routes/:id
func (h *Handler) UpdateRoute(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	existing, err := h.store.GetRouteRule(id)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}
	var body routeDTO
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	if !validRouteTypes[body.Type] {
		core.Fail(c, http.StatusBadRequest, "BAD_TYPE", "unsupported route type")
		return
	}
	existing.Type = body.Type
	existing.Value = strings.TrimSpace(body.Value)
	existing.Outbound = strings.TrimSpace(body.Outbound)
	existing.Enabled = body.Enabled
	existing.Order = body.Order
	// Let the operator re-tag a rule; keep the old tag if the field is omitted.
	if s := strings.TrimSpace(body.Source); s != "" {
		existing.Source = s
	}
	if err := h.store.UpdateRouteRule(existing); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "route.update", "route", map[string]string{"id": c.Param("id")})
	if err := h.applyAfterChange(c, existing.NodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, toRouteDTO(*existing))
}

// DeleteRoute removes a routing rule by id.
//
// DELETE /api/v1/routes/:id
func (h *Handler) DeleteRoute(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	existing, err := h.store.GetRouteRule(id)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}
	if err := h.store.DeleteRouteRule(id); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "route.delete", "route", map[string]string{"id": c.Param("id")})
	if err := h.applyAfterChange(c, existing.NodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"deleted": true})
}

// RoutePresets returns the curated preset catalogue (AI / streaming domain
// bundles) so the UI can offer one-click routing through WARP.
//
// GET /api/v1/routes/presets
func (h *Handler) RoutePresets(c *gin.Context) {
	core.OK(c, share.RoutePresets)
}

// ApplyRoutePreset batch-creates routing rules for a preset group, all pointing
// at the chosen outbound. Domains already present (same type+value+outbound) are
// skipped so re-applying is idempotent — the operator never ends up with
// duplicate rules. Returns how many were added vs skipped.
//
// POST /api/v1/routes/presets/apply  {"group":"ai","outbound":"warp"}
func (h *Handler) ApplyRoutePreset(c *gin.Context) {
	var body struct {
		Group    string `json:"group"`
		Outbound string `json:"outbound"`
		// Enabled controls whether the materialised rules start active. Nil/true
		// = enabled (one-click apply); false = create them switched off so the
		// operator can cherry-pick which to turn on in the table ("view rules"
		// pre-populates the group without routing anything yet).
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	enabled := body.Enabled == nil || *body.Enabled
	preset := share.RoutePresetByKey(strings.TrimSpace(body.Group))
	if preset == nil {
		core.Fail(c, http.StatusBadRequest, "BAD_GROUP", "unknown preset group")
		return
	}
	outbound := strings.TrimSpace(body.Outbound)
	if outbound == "" {
		outbound = "warp" // the whole point of presets is to send a group through WARP
	}
	nodeID := h.parseLocalNodeID()

	// Build a set of existing (type|value|outbound) so re-apply is idempotent.
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
	for _, d := range preset.Domains {
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
			Enabled:  enabled,
			Source:   preset.Key, // tag so the Routes page can filter by group
		}
		if err := h.store.CreateRouteRule(r); err != nil {
			core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
			return
		}
		have[key] = true
		added++
	}

	h.auditLog(c, "route.preset.apply", "route", map[string]string{
		"group": preset.Key, "outbound": outbound, "added": intStr(added),
	})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"added": added, "skipped": skipped})
}

// BulkRoutes applies one action to many rules in a single shot, so the engine
// re-renders once instead of once per rule. Action is delete | enable | disable.
// IDs must all belong to this node; unknown ids are rejected rather than
// silently skipped so the caller knows the batch was honoured in full.
//
// POST /api/v1/routes/bulk  {"action":"disable","ids":[1,2,3]}
func (h *Handler) BulkRoutes(c *gin.Context) {
	var body struct {
		Action string `json:"action"`
		IDs    []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	if body.Action != "delete" && body.Action != "enable" && body.Action != "disable" {
		core.Fail(c, http.StatusBadRequest, "BAD_ACTION",
			"action must be one of: delete, enable, disable")
		return
	}
	if len(body.IDs) == 0 {
		core.Fail(c, http.StatusBadRequest, "NO_IDS", "ids must not be empty")
		return
	}
	nodeID := h.parseLocalNodeID()
	existing, err := h.store.ListRouteRules(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	have := map[uint]bool{}
	for _, r := range existing {
		have[r.ID] = true
	}
	// Keep only ids that belong to this node. Stale ids (a row deleted between
	// the client building its selection and the request landing — a normal
	// refetch race) are silently skipped rather than failing the whole batch,
	// which previously surfaced as "one or more ids do not belong to this node".
	ids := make([]uint, 0, len(body.IDs))
	for _, id := range body.IDs {
		if have[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		core.OK(c, gin.H{"action": body.Action, "affected": 0})
		return
	}

	switch body.Action {
	case "delete":
		err = h.store.BulkDeleteRouteRules(nodeID, ids)
	default: // enable | disable
		err = h.store.BulkSetRouteEnabled(nodeID, ids, body.Action == "enable")
	}
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}

	h.auditLog(c, "route.bulk", "route", map[string]string{
		"action": body.Action, "count": intStr(len(ids)),
	})
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"action": body.Action, "affected": len(ids)})
}

// ReorderRoutes accepts {"ids":[...]} and assigns each id its index as Order.
// The list MUST contain every rule on the node; partial reorders are rejected
// because they'd leave the engine view ambiguous.
//
// POST /api/v1/routes/reorder
func (h *Handler) ReorderRoutes(c *gin.Context) {
	var body struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	nodeID := h.parseLocalNodeID()
	existing, err := h.store.ListRouteRules(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if len(body.IDs) != len(existing) {
		core.Fail(c, http.StatusBadRequest, "INCOMPLETE",
			"ids must include every route rule on this node")
		return
	}
	have := map[uint]bool{}
	for _, r := range existing {
		have[r.ID] = true
	}
	for _, id := range body.IDs {
		if !have[id] {
			core.Fail(c, http.StatusBadRequest, "UNKNOWN_ID",
				"one or more ids do not belong to this node")
			return
		}
	}
	if err := h.store.ReorderRouteRules(nodeID, body.IDs); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "route.reorder", "route", nil)
	if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{"reordered": len(body.IDs)})
}
