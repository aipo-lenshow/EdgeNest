package api

import (
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/orchestrator"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/wizard"
	"github.com/aipo-lenshow/EdgeNest/internal/core/nodeapi"
)

// Handler holds the dependencies shared by all HTTP handlers. The control plane
// touches nodes ONLY through nodeClient (NodeClient interface) — never by
// importing node/engine directly.
type Handler struct {
	store        *store.Store
	node         nodeapi.NodeClient
	orch         *orchestrator.Orchestrator
	wiz          *wizard.Wizard
	certMgr      *cert.Manager
	jwtSecret    string
	localNodeID  string
	panelPort    int
	dataDir      string
	loginLimiter *rateLimiter
	// auditEnabled gates audit logging. Defaults on; a persisted
	// "audit_enabled=false" setting (toggled from the panel) disables it so
	// self-hosters who don't want an operation history on disk can opt out.
	// Events that occur while off are dropped, never back-filled.
	auditEnabled atomic.Bool
}

// HandlerDeps groups optional dependencies so adding new ones doesn't keep
// growing NewHandler's argument list.
type HandlerDeps struct {
	Orch      *orchestrator.Orchestrator
	Wizard    *wizard.Wizard
	CertMgr   *cert.Manager
	PanelPort int
	DataDir   string
}

// NewHandler constructs a Handler. deps fields may be nil in tests; the
// corresponding endpoints surface a clean 503 rather than panicking.
func NewHandler(s *store.Store, nc nodeapi.NodeClient, deps HandlerDeps, jwtSecret, localNodeID string) *Handler {
	h := &Handler{
		store:        s,
		node:         nc,
		orch:         deps.Orch,
		wiz:          deps.Wizard,
		certMgr:      deps.CertMgr,
		jwtSecret:    jwtSecret,
		localNodeID:  localNodeID,
		panelPort:    deps.PanelPort,
		dataDir:      deps.DataDir,
		loginLimiter: newRateLimiter(5, 60),
	}
	// Audit logging is on unless explicitly disabled in a prior session.
	h.auditEnabled.Store(true)
	if v, err := s.GetSetting("audit_enabled"); err == nil && v == "false" {
		h.auditEnabled.Store(false)
	}
	return h
}

// auditLog records a sensitive operation. Best-effort: failures are swallowed
// so audit logging never blocks a user action. No-op when audit logging is
// disabled (the panel toggle) — use writeAudit to record an event regardless of
// the toggle (e.g. the toggle change itself).
func (h *Handler) auditLog(c *gin.Context, action, resource string, meta map[string]string) {
	if !h.auditEnabled.Load() {
		return
	}
	h.writeAudit(c, action, resource, meta)
}

// writeAudit persists an audit row unconditionally, bypassing the enable toggle.
func (h *Handler) writeAudit(c *gin.Context, action, resource string, meta map[string]string) {
	actor := "system"
	if v, ok := c.Get("username"); ok {
		if s, ok := v.(string); ok {
			actor = s
		}
	}
	metaJSON := ""
	if meta != nil {
		if b, err := json.Marshal(meta); err == nil {
			metaJSON = string(b)
		}
	}
	row := &model.AuditLog{
		Actor:     actor,
		Action:    action,
		Resource:  resource,
		IP:        c.ClientIP(),
		Meta:      metaJSON,
		CreatedAt: time.Now().Unix(),
	}
	_ = h.store.DB().Create(row).Error
}
