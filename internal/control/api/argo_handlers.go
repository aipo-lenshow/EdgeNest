package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/argo"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// argoCoordinator is the panel-level singleton that owns the cloudflared
// binary manager + active tunnel supervisor. Lazy-initialised so cloudflared
// is only downloaded the first time the operator hits "Start tunnel".
type argoCoordinator struct {
	once sync.Once
	mu   sync.Mutex
	bm   *argo.BinaryManager
	sup  *argo.Supervisor
}

// global because the cloudflared process must outlive any single HTTP request
// and there is exactly one tunnel per panel.
var globalArgo = &argoCoordinator{}

// ArgoStatus returns the live tunnel state. The UI polls this every few
// seconds to render "starting…" → "running at https://x.trycloudflare.com".
//
// GET /api/v1/argo/status
func (h *Handler) ArgoStatus(c *gin.Context) {
	core.OK(c, globalArgo.status())
}

type argoStartBody struct {
	// LocalPort is the inbound port cloudflared should expose. Required for
	// temp mode; named mode also uses it so the operator can swap which
	// inbound is fronted by the tunnel without re-issuing a token.
	LocalPort int `json:"local_port"`
}

// ArgoStart provisions cloudflared (downloading it on first use) and launches
// a tunnel using the saved AdvancedConfig (mode / domain / token). For temp
// mode the supervisor captures the trycloudflare hostname; for fixed mode
// the operator's configured domain is the share host.
//
// POST /api/v1/argo/start
func (h *Handler) ArgoStart(c *gin.Context) {
	var body argoStartBody
	if err := c.ShouldBindJSON(&body); err != nil {
		core.Fail(c, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}

	nodeID := h.parseLocalNodeID()
	// local_port is optional: with the one-Argo-inbound-per-node constraint the
	// tunnel target is unambiguous, so the caller (wizard result page / Advanced
	// page) can omit it and we bind to the single argo_bound inbound's loopback
	// port. A non-zero value is still honoured for explicit control.
	if body.LocalPort <= 0 {
		if p, ok := h.argoInboundPort(nodeID); ok {
			body.LocalPort = p
		} else {
			core.Fail(c, http.StatusBadRequest, "ARGO_NO_INBOUND",
				"没有 Argo 入站可绑定。请先用创建入站向导建一个走 Argo 的 WebSocket 入站。")
			return
		}
	}
	if body.LocalPort <= 0 || body.LocalPort > 65535 {
		core.Fail(c, http.StatusBadRequest, "BAD_LOCAL_PORT",
			"local_port must be in 1..65535")
		return
	}

	cfg, err := h.store.GetAdvanced(nodeID)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "DB_ERROR", err.Error())
		return
	}
	if cfg == nil || strings.TrimSpace(cfg.ArgoMode) == "" {
		core.Fail(c, http.StatusBadRequest, "ARGO_NOT_CONFIGURED",
			"请先在「接入优化」选择 Argo 模式(临时/固定)并保存, 再启动隧道")
		return
	}
	// Starting the tunnel IS the enable intent — the dedicated "enable Argo"
	// toggle was removed from the panel, so persist the flag here to keep the
	// stored config truthful (a panel reload / other readers see argo_enabled).
	if !cfg.ArgoEnabled {
		cfg.ArgoEnabled = true
		_ = h.store.UpsertAdvanced(cfg)
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()

	bin, err := globalArgo.binaryPath(ctx)
	if err != nil {
		core.Fail(c, http.StatusInternalServerError, "ARGO_BINARY_FAILED", err.Error())
		return
	}

	sup := globalArgo.supervisor(bin)
	mode := strings.TrimSpace(cfg.ArgoMode)
	switch mode {
	case "temp":
		if err := sup.StartTemp(ctx, body.LocalPort, 30*time.Second); err != nil {
			core.Fail(c, http.StatusBadGateway, "ARGO_START_FAILED", err.Error())
			return
		}
	case "fixed":
		// Gate the two fixed-mode prerequisites with distinct codes so the UI
		// can localise them — StartNamed's raw "named tunnel requires a token"
		// error otherwise leaks untranslated Go jargon to the operator.
		if strings.TrimSpace(cfg.ArgoToken) == "" {
			core.Fail(c, http.StatusBadRequest, "ARGO_MISSING_TOKEN",
				"fixed Argo tunnel needs a token; save one on the Argo tab first")
			return
		}
		if strings.TrimSpace(cfg.ArgoDomain) == "" {
			core.Fail(c, http.StatusBadRequest, "ARGO_MISSING_DOMAIN",
				"fixed Argo tunnel needs a domain; save one on the Argo tab first")
			return
		}
		if err := sup.StartNamed(ctx, cfg.ArgoToken, cfg.ArgoDomain, 5*time.Second); err != nil {
			// A fast cloudflared exit on a named tunnel almost always means the
			// run token is wrong/expired — surface a distinct code the UI turns
			// into a clear "re-copy the tunnel token" message instead of leaking
			// "exit status 1 / cloudflared exited" jargon.
			code := "ARGO_START_FAILED"
			if strings.Contains(strings.ToLower(err.Error()), "token") {
				code = "ARGO_INVALID_TOKEN"
			}
			core.Fail(c, http.StatusBadGateway, code, err.Error())
			return
		}
	default:
		core.Fail(c, http.StatusBadRequest, "BAD_ARGO_MODE",
			"AdvancedConfig.ArgoMode must be 'temp' or 'fixed'")
		return
	}

	h.auditLog(c, "argo.start", "argo", map[string]string{
		"mode": mode,
	})
	core.OK(c, globalArgo.status())
}

// AutoStartArgoIfEnabled relaunches the Argo tunnel on panel startup when the
// stored config says it should be running, so a restart / binary hot-swap /
// reboot doesn't silently drop a tunnel the operator had enabled (KillStray at
// boot kills any orphan cloudflared, and the supervisor starts idle otherwise).
// Best-effort and non-fatal: every bail-out just logs and returns — the panel
// must come up regardless, and the operator can always start the tunnel from
// the UI. Call it AFTER the startup Apply so the Argo inbound's loopback
// listener exists for cloudflared to dial.
func (h *Handler) AutoStartArgoIfEnabled(ctx context.Context) {
	nodeID := h.parseLocalNodeID()
	cfg, err := h.store.GetAdvanced(nodeID)
	if err != nil || cfg == nil || !cfg.ArgoEnabled {
		return
	}
	mode := strings.TrimSpace(cfg.ArgoMode)
	if mode != "temp" && mode != "fixed" {
		return
	}
	port, ok := h.argoInboundPort(nodeID)
	if !ok {
		log.Printf("argo autostart: skipped (argo enabled but no argo_bound inbound)")
		return
	}
	if mode == "fixed" && strings.TrimSpace(cfg.ArgoToken) == "" {
		log.Printf("argo autostart: skipped (fixed mode but no stored token)")
		return
	}

	bin, err := globalArgo.binaryPath(ctx)
	if err != nil {
		log.Printf("argo autostart: binary unavailable: %v", err)
		return
	}
	sup := globalArgo.supervisor(bin)
	switch mode {
	case "temp":
		if err := sup.StartTemp(ctx, port, 30*time.Second); err != nil {
			log.Printf("argo autostart: temp start failed: %v", err)
			return
		}
	case "fixed":
		if err := sup.StartNamed(ctx, cfg.ArgoToken, cfg.ArgoDomain, 5*time.Second); err != nil {
			log.Printf("argo autostart: fixed start failed: %v", err)
			return
		}
	}
	log.Printf("argo autostart: %s tunnel started (local_port=%d)", mode, port)
}

// ArgoStop tears down the running tunnel. Idempotent.
//
// POST /api/v1/argo/stop
func (h *Handler) ArgoStop(c *gin.Context) {
	globalArgo.stop()
	h.auditLog(c, "argo.stop", "argo", nil)
	core.OK(c, globalArgo.status())
}

// argoHost returns the captured / configured Argo tunnel hostname, or empty
// when no tunnel is running. Used by the subscription handler to rewrite
// the `server` field for argo_bound inbounds.
func (h *Handler) argoHost() string {
	st := globalArgo.status()
	if st.State != argo.StateRunning {
		return ""
	}
	return st.Hostname
}

// argoInboundPort returns the loopback port of the node's single argo_bound
// inbound (the one the tunnel must point at), or false if none exists. The
// one-Argo-inbound-per-node constraint makes this unambiguous.
func (h *Handler) argoInboundPort(nodeID uint) (int, bool) {
	ins, err := h.store.ListInbounds(nodeID)
	if err != nil {
		return 0, false
	}
	for _, in := range ins {
		var s map[string]any
		if in.Settings != "" {
			_ = json.Unmarshal([]byte(in.Settings), &s)
		}
		if settingTruthy(s["argo_bound"]) {
			return in.Port, true
		}
	}
	return 0, false
}

// --- coordinator helpers --------------------------------------------------

func (a *argoCoordinator) binaryPath(ctx context.Context) (string, error) {
	a.once.Do(func() {
		// DefaultBinDir is the canonical install dir on Linux deployments;
		// bootstrap.go ensures it exists with the right owner. KillStray reaps
		// against the same path, so they can't drift.
		a.bm = argo.NewBinaryManager(argo.DefaultBinDir)
	})
	return a.bm.Path(ctx)
}

func (a *argoCoordinator) supervisor(bin string) *argo.Supervisor {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sup == nil {
		a.sup = argo.NewSupervisor(bin)
	}
	return a.sup
}

func (a *argoCoordinator) status() argo.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sup == nil {
		return argo.Status{State: argo.StateIdle}
	}
	return a.sup.Status()
}

func (a *argoCoordinator) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sup != nil {
		a.sup.Stop()
	}
}
