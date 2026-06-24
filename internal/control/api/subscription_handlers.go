package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/auth"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/share"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// KeyShareHost is the Settings key for the FQDN/IP clients should dial when
// an inbound has no per-inbound family preference. Acts as a global override
// (operator filled it deliberately) or fall-through default.
const KeyShareHost = "share_host"

// shareHost picks the fallback host for inbounds whose
// SubscriptionHost is unset (migrated legacy rows, mostly). Precedence:
//
//  1. Settings.share_host — operator explicitly set a FQDN/IP, that wins.
//  2. NodeCapability.IPv4Addr — dual-stack default (matches the wizard
//     Step1 default of "v4").
//  3. NodeCapability.IPv6Addr — only path on v6-only hosts.
//  4. local node PublicIP — legacy fallback for upgrades where
//     /etc/edgenest/network.json doesn't exist yet.
//
// Per-inbound family override lives inside the resolver, not here; this
// only chooses the resolver's `host` argument.
func (h *Handler) shareHost() string {
	if v, _ := h.store.GetSetting(KeyShareHost); v != "" {
		return v
	}
	cap := core.ReadNodeCapability(core.DefaultCapabilityPath)
	if cap.IPv4 && cap.IPv4Addr != "" {
		return cap.IPv4Addr
	}
	if cap.IPv6Global && cap.IPv6Addr != "" {
		return cap.IPv6Addr
	}
	if n, _ := h.store.GetLocalNode(); n != nil && n.PublicIP != "" {
		return n.PublicIP
	}
	return ""
}

// ---- Admin: subscription CRUD ----

type subscriptionCreateRequest struct {
	Name         string `json:"name"`
	ClientID     uint   `json:"client_id"`
	AllowedNodes []uint `json:"allowed_nodes"`
	// AllowedInbounds accepts either a list of inbound IDs ([]uint, modern
	// form) or tag strings ([]string, legacy form). The server normalises to
	// IDs before persisting, while parseAllowedInboundsField handles both
	// shapes so older panel builds calling the API don't break.
	AllowedInbounds json.RawMessage `json:"allowed_inbounds"`
	ExpiresAt       int64           `json:"expires_at"`
}

// parseAllowedInboundsField normalises the request's allowed_inbounds field
// to a JSON-encoded []uint string suitable for direct storage. Accepts:
//   - []uint  (modern: list of inbound IDs)
//   - []string (legacy: list of inbound tags — resolved against the store)
//   - empty/null/[]
func (h *Handler) parseAllowedInboundsField(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return "[]", nil
	}
	var ids []uint
	if err := json.Unmarshal(raw, &ids); err == nil {
		out, _ := json.Marshal(ids)
		return string(out), nil
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return "[]", err
	}
	resolved := make([]uint, 0, len(tags))
	for _, tag := range tags {
		in, err := h.store.GetInboundByTag(tag)
		if err == nil && in != nil {
			resolved = append(resolved, in.ID)
		}
	}
	out, _ := json.Marshal(resolved)
	return string(out), nil
}

// inboundLookup is a snapshot of the inbound table keyed by ID. The
// subscriptionView needs both the tag (for protocol chips) AND the
// SubscriptionHost (for absolute URL construction — the family the wizard's
// Step1 bound this inbound to). Doing the two lookups against the same
// snapshot guarantees both fields come from the same DB read.
type inboundLookup struct {
	tag  map[uint]string
	host map[uint]string
}

// buildInboundLookup builds the lookup once per request. Returns empty maps on
// DB error so subscriptionView can still emit the row (without enrichment).
func (h *Handler) buildInboundLookup() inboundLookup {
	out := inboundLookup{
		tag:  map[uint]string{},
		host: map[uint]string{},
	}
	ins, err := h.store.ListInbounds(h.parseLocalNodeID())
	if err != nil {
		return out
	}
	for _, in := range ins {
		out.tag[in.ID] = in.Tag
		out.host[in.ID] = in.SubscriptionHost
	}
	return out
}

// inboundTagLookup is a back-compat helper retained for callers that only
// need tags (currently unused after the lookup migration; keeps the API
// stable for in-flight refactors). buildInboundLookup is preferred.
func (h *Handler) inboundTagLookup() map[uint]string {
	return h.buildInboundLookup().tag
}

// subscriptionView returns the JSON-friendly form of a Subscription row,
// enriching allowed_inbounds (stored as []uint of IDs) with the matching
// inbound tags AND the family-aware subscription_host so the panel UI can
// render protocol chips AND absolute family-correct URLs without extra
// lookups. `lookup` is built once with buildInboundLookup.
//
// `panelPort` and `fallbackHost` let the view emit an absolute subscription
// URL the front-end can copy/QR without prefixing window.location.origin.
// On a dual-stack node where the user picked v6 in Step1 but is logged into
// the panel via v4, the front-end's window.location.origin would always
// echo back v4 — silently breaking the v6 client. The backend has the
// inbound's SubscriptionHost in-hand, so it owns this concat.
func subscriptionView(sub *model.Subscription, lookup inboundLookup, panelPort int, fallbackHost string) gin.H {
	resp := gin.H{
		"id":               sub.ID,
		"name":             sub.Name,
		"token":            sub.Token,
		"client_id":        sub.ClientID,
		"allowed_nodes":    sub.AllowedNodes,
		"allowed_inbounds": sub.AllowedInbounds,
		"expires_at":       sub.ExpiresAt,
		"revoked":          sub.Revoked,
		"created_at":       sub.CreatedAt,
	}
	// Enrich: parse allowed_inbounds and emit a parallel tag list, regardless
	// of whether the stored shape is IDs or legacy tags. Same parse drives
	// the per-subscription host pick below.
	var ids []uint
	var tags []string
	if err := json.Unmarshal([]byte(sub.AllowedInbounds), &ids); err == nil {
		for _, id := range ids {
			if t, ok := lookup.tag[id]; ok {
				tags = append(tags, t)
			}
		}
	} else {
		_ = json.Unmarshal([]byte(sub.AllowedInbounds), &tags)
	}
	resp["allowed_inbound_tags"] = tags

	// Family pick: design says one wizard batch = one IP, so every
	// inbound in this subscription's allowed list shares a single
	// SubscriptionHost. Walk the IDs in order, pick the first non-empty
	// SubscriptionHost.
	//
	// Fallback discipline (BUG-1): the global shareHost prefers IPv4, so it
	// must only stand in where its family can't be wrong — earlier migrated
	// rows whose live inbounds carry no SubscriptionHost, and open-allow-list
	// rows (len(ids)==0 → "all inbounds"). A subscription whose every
	// referenced inbound has been DELETED gets an empty host + orphaned=true
	// instead: borrowing the node default here is how a v6 subscription got
	// rendered (and served) as v4.
	subHost := ""
	alive := 0
	for _, id := range ids {
		if h, ok := lookup.host[id]; ok {
			alive++
			if subHost == "" && h != "" {
				subHost = h
			}
		}
	}
	orphaned := len(ids) > 0 && alive == 0
	if subHost == "" && !orphaned {
		subHost = fallbackHost
	}
	resp["subscription_host"] = subHost
	resp["orphaned"] = orphaned
	if sub.Token != "" {
		resp["url"] = "/sub/" + sub.Token
		// Absolute URL — bracket v6 literals per RFC 3986 so QR codes /
		// copy buttons hand the client an importable URL straight away.
		// `panelPort == 0` (no panel-port hint configured) → fall back to
		// path-only so the front-end can still prefix origin.
		if subHost != "" && panelPort > 0 {
			hostSeg := subHost
			if isIPv6Literal(subHost) {
				hostSeg = "[" + subHost + "]"
			}
			resp["absolute_url"] = "http://" + hostSeg + ":" + strconv.Itoa(panelPort) + "/sub/" + sub.Token
		}
	}
	return resp
}

// isIPv6Literal reports whether s parses as an IPv6 address literal — used
// by the view to decide on [ ] bracketing in the absolute URL. Bare IPv4
// strings and DNS names parse as non-IPv6 (or fail to parse) and pass
// through unchanged.
func isIPv6Literal(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() == nil
}

type subscriptionCreateResponse struct {
	ID    uint   `json:"id"`
	Token string `json:"token"` // returned ONCE; only the hash is persisted
	URL   string `json:"url"`
}

// CreateSubscription issues a new subscription token bound to a client.
// The raw token is returned exactly once; only the SHA-256 is persisted.
//
// POST /api/v1/subscriptions
func (h *Handler) CreateSubscription(c *gin.Context) {
	var req subscriptionCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if req.ClientID == 0 {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "client_id is required")
		return
	}
	client, err := h.store.GetClient(req.ClientID)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "client not found")
		return
	}
	token, err := auth.RandomHex(24) // 48 hex chars
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "RANDOM", err.Error())
		return
	}
	allowedNodes := "[]"
	if len(req.AllowedNodes) > 0 {
		b, _ := json.Marshal(req.AllowedNodes)
		allowedNodes = string(b)
	}
	allowedInbounds, err := h.parseAllowedInboundsField(req.AllowedInbounds)
	if err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "invalid allowed_inbounds: "+err.Error())
		return
	}
	sub := &model.Subscription{
		Name:            req.Name,
		Token:           token,
		TokenHash:       store.HashToken(token),
		ClientID:        client.ID,
		AllowedNodes:    allowedNodes,
		AllowedInbounds: allowedInbounds,
		ExpiresAt:       req.ExpiresAt,
	}
	if err := h.store.CreateSubscription(sub); err != nil {
		core.Fail(c, http.StatusConflict, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "subscription.create", "subscription:"+strconv.Itoa(int(sub.ID)),
		map[string]string{"client_id": strconv.Itoa(int(client.ID))})

	// We don't know the panel's public scheme/host here — return a path-only
	// URL and let the front-end prefix the current origin.
	core.Created(c, subscriptionCreateResponse{
		ID:    sub.ID,
		Token: token,
		URL:   "/sub/" + token,
	})
}

// ListSubscriptions returns every subscription, including the plaintext token
// so the admin UI can render copy/QR controls without an extra round trip.
//
// GET /api/v1/subscriptions
func (h *Handler) ListSubscriptions(c *gin.Context) {
	subs, err := h.store.ListSubscriptions()
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	lookup := h.buildInboundLookup()
	fallback := h.shareHost()
	views := make([]gin.H, 0, len(subs))
	for i := range subs {
		v := subscriptionView(&subs[i], lookup, h.panelPort, fallback)
		views = append(views, h.withClientState(v, subs[i].ClientID))
	}
	core.OK(c, views)
}

// withClientState annotates a subscription view with its bound (seed) user's
// state. The share resolver rejects /sub/:token when the seed client is
// disabled (quota/expiry enforcement), so a subscription bound to a disabled
// user serves 404 even though its own revoked/expires_at look fine — the panel
// needs this to flag the dead link instead of presenting a copyable URL.
func (h *Handler) withClientState(view gin.H, clientID uint) gin.H {
	cl, err := h.store.GetClient(clientID)
	if err != nil || cl == nil {
		view["client_enabled"] = false
		view["client_email"] = ""
		return view
	}
	view["client_enabled"] = cl.Enabled
	view["client_email"] = cl.Email
	return view
}

// GetSubscription returns a single subscription including the plaintext token
// and a path-only URL the front-end can prefix with window.location.origin.
//
// GET /api/v1/subscriptions/:id
func (h *Handler) GetSubscription(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	sub, err := h.store.GetSubscription(id)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "subscription not found")
		return
	}
	core.OK(c, h.withClientState(
		subscriptionView(sub, h.buildInboundLookup(), h.panelPort, h.shareHost()),
		sub.ClientID,
	))
}

// RotateSubscription generates a new token for an existing subscription. The
// old URL stops resolving immediately; the new token is returned in the
// response (and persisted, so the admin can re-open it later).
//
// POST /api/v1/subscriptions/:id/rotate
func (h *Handler) RotateSubscription(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	token, err := auth.RandomHex(24)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "RANDOM", err.Error())
		return
	}
	sub, err := h.store.RotateSubscriptionToken(id, token, store.HashToken(token))
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "subscription.rotate", "subscription:"+strconv.Itoa(int(id)), nil)
	core.OK(c, gin.H{
		"id":    sub.ID,
		"token": sub.Token,
		"url":   "/sub/" + sub.Token,
	})
}

// DeleteSubscription removes a subscription row.
//
// DELETE /api/v1/subscriptions/:id
func (h *Handler) DeleteSubscription(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.store.DeleteSubscription(id); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "subscription.delete", "subscription:"+strconv.Itoa(int(id)), nil)
	core.OK(c, gin.H{"id": id})
}

// RevokeSubscription flips the revoked flag (token stops resolving immediately).
//
// POST /api/v1/subscriptions/:id/revoke
func (h *Handler) RevokeSubscription(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.store.RevokeSubscription(id); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	h.auditLog(c, "subscription.revoke", "subscription:"+strconv.Itoa(int(id)), nil)
	core.OK(c, gin.H{"id": id, "revoked": true})
}

// ---- Public: subscription endpoint ----

// PublicSubscription serves the subscription body for a token. The output
// format is picked from the User-Agent: Clash family gets YAML, sing-box gets
// JSON, Quantumult X gets its KV format, everyone else gets the V2RayN-style
// base64 URI list (which Shadowrocket / Hiddify / V2RayN / NekoBox consume).
//
// Override with ?fmt=clash|singbox|qx|v2ray for clients with quirky UAs.
//
// Returns 404 for any failure (unknown / revoked / expired) — never leak which.
//
// GET /sub/:token   (no auth, no /api prefix)
func (h *Handler) PublicSubscription(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.Status(http.StatusNotFound)
		return
	}
	host := h.shareHost()
	if host == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"error": gin.H{
				"code":    "SHARE_HOST_UNSET",
				"message": "share_host setting is empty",
			},
		})
		return
	}
	resolver := share.NewResolver(h.store, host, h.cdnPoolForLocalNode(), h.argoHost(), core.ReadNodeCapability(core.DefaultCapabilityPath))

	format := pickSubFormat(c.Query("fmt"), c.GetHeader("User-Agent"))

	var (
		body        string
		contentType = "text/plain; charset=utf-8"
	)
	switch format {
	case "clash":
		bundles, err := resolver.ResolveBundles(token)
		if err != nil {
			respondResolveErr(c, err)
			return
		}
		body = share.EncodeClash(bundles, host)
		contentType = "text/yaml; charset=utf-8"
	case "stash":
		bundles, err := resolver.ResolveBundles(token)
		if err != nil {
			respondResolveErr(c, err)
			return
		}
		body = share.EncodeStash(bundles, host)
		contentType = "text/yaml; charset=utf-8"
	case "singbox":
		bundles, err := resolver.ResolveBundles(token)
		if err != nil {
			respondResolveErr(c, err)
			return
		}
		body = share.EncodeSingbox(bundles, host)
		contentType = "application/json; charset=utf-8"
	case "qx":
		bundles, err := resolver.ResolveBundles(token)
		if err != nil {
			respondResolveErr(c, err)
			return
		}
		body = share.EncodeQuantumultX(bundles, host)
	case "surge":
		bundles, err := resolver.ResolveBundles(token)
		if err != nil {
			respondResolveErr(c, err)
			return
		}
		body = share.EncodeSurge(bundles, host)
	case "loon":
		bundles, err := resolver.ResolveBundles(token)
		if err != nil {
			respondResolveErr(c, err)
			return
		}
		body = share.EncodeLoon(bundles, host)
	default: // "v2ray"
		uris, err := resolver.Resolve(token)
		if err != nil {
			respondResolveErr(c, err)
			return
		}
		body = share.EncodeSubscriptionBody(uris)
	}

	c.Header("Subscription-Userinfo", "upload=0; download=0; total=0; expire=0")
	c.Header("Profile-Update-Interval", "24")
	c.Header("Cache-Control", "no-store, max-age=0")
	c.Data(http.StatusOK, contentType, []byte(body))
}

func respondResolveErr(c *gin.Context, err error) {
	if errors.Is(err, share.ErrNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false,
		"error":   gin.H{"code": "RESOLVE_FAILED", "message": err.Error()},
	})
}

// pickSubFormat picks the subscription body format. Explicit ?fmt= overrides
// the User-Agent sniff. Defaults to "v2ray" (base64 URI list) which is what
// Shadowrocket / Hiddify / V2RayN / NekoBox / NekoRay all consume.
func pickSubFormat(fmtParam, ua string) string {
	switch fmtParam {
	case "clash", "stash", "singbox", "qx", "surge", "loon", "v2ray":
		return fmtParam
	}
	u := toLower(ua)
	switch {
	case contains(u, "stash"):
		// Stash forked from Clash Premium but ships its own strict YAML
		// validator. The Hy2 entry uses `auth:` (Stash) vs `password:`
		// (Mihomo); routing Stash UA to the Stash encoder avoids silent
		// Hy2 node-drop on import.
		return "stash"
	case contains(u, "clash") || contains(u, "mihomo") || contains(u, "meta"):
		return "clash"
	case contains(u, "sing-box") || contains(u, "sfa") || contains(u, "sfi") || contains(u, "sft") || contains(u, "sfm"):
		return "singbox"
	case contains(u, "quantumult"):
		return "qx"
	case contains(u, "surge"):
		return "surge"
	case contains(u, "loon"):
		return "loon"
	default:
		return "v2ray"
	}
}

func toLower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ---- Admin: preview a client's URI bundle (no token needed) ----

// PreviewClientBundle returns the URIs a subscription for this client would
// resolve to. Used by the admin UI to show "the link this client would get"
// before issuing a real token.
//
// GET /api/v1/clients/:id/preview
func (h *Handler) PreviewClientBundle(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	client, err := h.store.GetClient(id)
	if err != nil {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "client not found")
		return
	}
	host := h.shareHost()
	if host == "" {
		core.Fail(c, http.StatusServiceUnavailable, "SHARE_HOST_UNSET",
			"share_host setting is empty")
		return
	}
	resolver := share.NewResolver(h.store, host, h.cdnPoolForLocalNode(), h.argoHost(), core.ReadNodeCapability(core.DefaultCapabilityPath))
	links, err := resolver.BuildBundleForClient(client)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "RESOLVE_FAILED", err.Error())
		return
	}
	core.OK(c, gin.H{
		"client_id": client.ID,
		"email":     client.Email,
		"host":      host,
		"links":     links,
	})
}
