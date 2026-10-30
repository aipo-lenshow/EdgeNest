package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/argo"
	"github.com/aipo-lenshow/EdgeNest/internal/control/cfapi"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// The Argo tunnel feeds exactly one loopback WebSocket inbound (its "origin").
// Besides the dedicated Argo inbound the wizard creates, the CDN (edge)
// inbound can be the origin: one inbound then serves both entry paths, its
// users get CDN nodes plus a tunnel node, and the separate Argo inbound is no
// longer needed.

type argoOriginCandidate struct {
	ID      uint   `json:"id"`
	Tag     string `json:"tag"`
	Port    int    `json:"port"`
	Enabled bool   `json:"enabled"`
	Argo    bool   `json:"argo"` // the tunnel feeds it now
	Edge    bool   `json:"edge"` // also the CDN (edge) inbound
	Users   int    `json:"users"`
}

// GetArgoOrigin lists the inbounds the tunnel can feed and which one it does.
//
// GET /api/v1/argo/origin
func (h *Handler) GetArgoOrigin(c *gin.Context) {
	nodeID := h.parseLocalNodeID()
	ins, err := h.store.ListInbounds(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	out := []argoOriginCandidate{}
	for _, in := range ins {
		s := inboundSettingsMap(&in)
		argoB, edgeB := settingTruthy(s["argo_bound"]), settingTruthy(s["edge_bound"])
		if !(argoB || edgeB || settingTruthy(s[argoStandbyKey])) || argoOriginProblem(&in, s) != "" {
			continue
		}
		users := 0
		if cs, err := h.store.ListClients(in.ID); err == nil {
			users = len(cs)
		}
		out = append(out, argoOriginCandidate{
			ID: in.ID, Tag: in.Tag, Port: in.Port, Enabled: in.Enabled,
			Argo: argoB, Edge: edgeB, Users: users,
		})
	}
	mode := ""
	if cfg, _ := h.store.GetAdvanced(nodeID); cfg != nil {
		mode = cfg.ArgoMode
	}
	core.OK(c, gin.H{"candidates": out, "mode": mode})
}

type argoOriginBody struct {
	InboundID uint `json:"inbound_id"`
	// IngressDone: the operator changed the fixed tunnel's service URL in the
	// Cloudflare dashboard by hand (no API token stored to do it for them).
	IngressDone bool `json:"ingress_done"`
}

// SetArgoOrigin points the tunnel at another inbound. The previous origin
// stops being Argo-bound; if it was a dedicated Argo inbound it is disabled
// (kept, so switching back restores it) and its users are added to the new
// origin so nobody loses their tunnel node. A fixed tunnel's ingress lives in
// Cloudflare and is updated through the API when a token is stored; without
// one the call fails with ARGO_INGRESS_MANUAL and the operator changes the
// service URL in Cloudflare, then retries with ingress_done.
//
// PUT /api/v1/argo/origin
func (h *Handler) SetArgoOrigin(c *gin.Context) {
	var body argoOriginBody
	if err := c.ShouldBindJSON(&body); err != nil || body.InboundID == 0 {
		core.Fail(c, http.StatusBadRequest, "BAD_REQUEST", "inbound_id required")
		return
	}
	nodeID := h.parseLocalNodeID()
	target, err := h.store.GetInbound(body.InboundID)
	if err != nil || target == nil || target.NodeID != nodeID {
		core.Fail(c, http.StatusNotFound, "NOT_FOUND", "inbound not found")
		return
	}
	ts := inboundSettingsMap(target)
	if !settingTruthy(ts["argo_bound"]) && !settingTruthy(ts["edge_bound"]) && !settingTruthy(ts[argoStandbyKey]) {
		core.Fail(c, http.StatusBadRequest, "ARGO_ORIGIN_UNSUPPORTED", "only the Argo or CDN (edge) inbound can feed the tunnel")
		return
	}
	if msg := argoOriginProblem(target, ts); msg != "" {
		core.Fail(c, http.StatusBadRequest, "ARGO_ORIGIN_UNSUPPORTED", msg)
		return
	}

	var current *model.Inbound
	ins, _ := h.store.ListInbounds(nodeID)
	for i := range ins {
		if settingTruthy(inboundSettingsMap(&ins[i])["argo_bound"]) {
			current = &ins[i]
			break
		}
	}
	if current != nil && current.ID == target.ID && target.Enabled {
		core.OK(c, gin.H{"inbound_id": target.ID, "moved_users": 0, "ingress": "unchanged"})
		return
	}

	cfg, _ := h.store.GetAdvanced(nodeID)
	ingress := "none"
	if cfg != nil && cfg.ArgoMode == "fixed" && strings.TrimSpace(cfg.ArgoToken) != "" {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		err := h.repointFixedTunnel(ctx, cfg, target.Port)
		cancel()
		switch {
		case err == nil:
			ingress = "auto"
		case body.IngressDone:
			ingress = "manual"
		default:
			core.Fail(c, http.StatusConflict, "ARGO_INGRESS_MANUAL", err.Error())
			return
		}
	} else if cfg != nil && cfg.ArgoMode == "temp" {
		ingress = "temp"
	}

	// Move the flag (and the users) to the new origin.
	ts["argo_bound"] = "true"
	delete(ts, argoStandbyKey)
	if err := saveInboundSettings(h, target, ts, true); err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	var emails []string
	if current != nil && current.ID != target.ID {
		cs := inboundSettingsMap(current)
		delete(cs, "argo_bound")
		// A dedicated Argo inbound has no other entry path left: park it
		// disabled, marked so it can be picked to feed the tunnel again.
		keepOn := current.Enabled && settingTruthy(cs["edge_bound"])
		if !settingTruthy(cs["edge_bound"]) {
			cs[argoStandbyKey] = "true"
		}
		if err := saveInboundSettings(h, current, cs, keepOn); err != nil {
			core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
			return
		}
		emails = h.emailsMissingFrom(current.ID, target.ID)
	}
	moved := 0
	if len(emails) > 0 {
		res, err := h.userSvc(c).AddToInbound(c.Request.Context(), nodeID, target.ID, emails)
		if err != nil {
			failUserSvc(c, err)
			return
		}
		moved = len(res.Added)
	} else if err := h.applyAfterChange(c, nodeID); err != nil {
		core.Fail(c, http.StatusInternalServerError, "APPLY_FAILED", err.Error())
		return
	}

	// A quick tunnel is pointed at a port on the command line: restart it.
	if ingress == "temp" && globalArgo.status().State == argo.StateRunning {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
		if bin, err := globalArgo.binaryPath(ctx); err == nil {
			_ = globalArgo.supervisor(bin).StartTemp(ctx, target.Port, 30*time.Second)
		}
		cancel()
	}

	from := ""
	if current != nil {
		from = current.Tag
	}
	h.auditLog(c, "argo.origin", "argo", map[string]string{
		"from": from, "to": target.Tag, "ingress": ingress, "moved_users": intStr(moved),
	})
	core.OK(c, gin.H{"inbound_id": target.ID, "moved_users": moved, "ingress": ingress})
}

// argoStandbyKey marks a dedicated Argo inbound parked by SetArgoOrigin.
const argoStandbyKey = "argo_standby"

// argoOriginProblem is argoBindGate's shape check without the flag test:
// cloudflared speaks plain HTTP to a loopback WebSocket origin.
func argoOriginProblem(in *model.Inbound, s map[string]any) string {
	cp := map[string]any{"argo_bound": "true"}
	for k, v := range s {
		if k != "argo_bound" {
			cp[k] = v
		}
	}
	return argoBindGate(in.Type, in.Listen, cp)
}

// repointFixedTunnel sets the fixed tunnel's Cloudflare ingress to
// http://localhost:<port>. The run token carries the account and tunnel IDs.
func (h *Handler) repointFixedTunnel(ctx context.Context, cfg *model.AdvancedConfig, port int) error {
	account, tunnel, err := decodeRunToken(cfg.ArgoToken)
	if err != nil {
		return err
	}
	domain := strings.TrimSpace(cfg.ArgoDomain)
	if domain == "" {
		return errors.New("no tunnel domain saved")
	}
	var lastErr error = errors.New("no Cloudflare API token saved")
	for _, key := range []string{settingArgoCFToken, settingDNSCloudflareToken} {
		tok, _ := h.store.GetSetting(key)
		if strings.TrimSpace(tok) == "" {
			continue
		}
		if lastErr = cfapi.New(tok).SetIngress(ctx, account, tunnel, domain, "http://localhost:"+intStr(port)); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// decodeRunToken reads the account and tunnel IDs out of a cloudflared run
// token (base64 JSON {"a": account, "t": tunnel, "s": secret}).
func decodeRunToken(token string) (account, tunnel string, err error) {
	token = strings.TrimSpace(token)
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(token); err != nil {
			return "", "", errors.New("tunnel token is not readable")
		}
	}
	var t struct {
		A string `json:"a"`
		T string `json:"t"`
	}
	if json.Unmarshal(raw, &t) != nil || t.A == "" || t.T == "" {
		return "", "", errors.New("tunnel token is not readable")
	}
	return t.A, t.T, nil
}

func inboundSettingsMap(in *model.Inbound) map[string]any {
	s := map[string]any{}
	if in.Settings != "" {
		_ = json.Unmarshal([]byte(in.Settings), &s)
	}
	return s
}

func saveInboundSettings(h *Handler, in *model.Inbound, s map[string]any, enabled bool) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	in.Settings = string(b)
	in.Enabled = enabled
	return h.store.UpdateInbound(in)
}

// emailsMissingFrom lists the users on inbound from who aren't on inbound to.
func (h *Handler) emailsMissingFrom(from, to uint) []string {
	have := map[string]bool{}
	if cs, err := h.store.ListClients(to); err == nil {
		for _, c := range cs {
			have[c.Email] = true
		}
	}
	var out []string
	if cs, err := h.store.ListClients(from); err == nil {
		for _, c := range cs {
			if c.Email != "" && !have[c.Email] {
				have[c.Email] = true
				out = append(out, c.Email)
			}
		}
	}
	return out
}
