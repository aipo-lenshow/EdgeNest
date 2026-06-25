package api

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
	"github.com/aipo-lenshow/EdgeNest/internal/control/web"
)

// NewRouter builds the Gin engine: API under /api, everything else served by
// the embedded SPA (with client-side routing fallback to index.html).
func (h *Handler) NewRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// Single-binary panel: no reverse proxy in front. Tell Gin to ignore
	// X-Forwarded-For / X-Real-IP entirely so c.ClientIP() returns the actual
	// TCP source — otherwise a self-signed forwarded header from anywhere on
	// the host would override and audit logs would record the VPS's own IP.
	_ = r.SetTrustedProxies(nil)

	// Public health.
	r.GET("/api/health", h.Health)

	// Public subscription endpoint (no auth, no /api prefix).
	r.GET("/sub/:token", h.PublicSubscription)

	// Public API v1 (no auth).
	pub := r.Group("/api/v1")
	{
		pub.POST("/login", h.LoginRateLimit(), h.Login)
	}

	// Protected API v1.
	authed := r.Group("/api/v1")
	authed.Use(h.AuthMiddleware())
	{
		authed.POST("/logout", h.Logout)
		authed.GET("/me", h.Me)
		authed.POST("/password", h.ChangePassword)

		// Two-factor auth (TOTP) enrollment + management. Login itself stays on
		// the public /login endpoint (it takes the code in-band).
		authed.POST("/2fa/setup", h.TwoFASetup)
		authed.POST("/2fa/enable", h.TwoFAEnable)
		authed.POST("/2fa/disable", h.TwoFADisable)
		authed.POST("/2fa/recovery-codes", h.TwoFARegenCodes)
		authed.GET("/dashboard", h.Dashboard)
		authed.GET("/nodes", h.ListNodes)
		authed.GET("/engine/status", h.EngineStatus)
		authed.POST("/engine/restart", h.RestartEngine)

		// Self-upgrade: live "check now" + kick off (detached) + poll status.
		authed.GET("/version/check", h.VersionCheck)
		authed.POST("/upgrade", h.Upgrade)
		authed.GET("/upgrade/status", h.UpgradeStatus)

		// Inbound CRUD
		authed.GET("/inbounds", h.ListInbounds)
		authed.POST("/inbounds", h.CreateInbound)
		authed.GET("/inbounds/:id", h.GetInbound)
		authed.PUT("/inbounds/:id", h.UpdateInbound)
		authed.DELETE("/inbounds/:id", h.DeleteInbound)

		// Client CRUD (nested under inbound)
		authed.GET("/inbounds/:id/clients", h.ListClients)
		authed.POST("/inbounds/:id/clients", h.CreateClient)
		authed.PUT("/inbounds/:id/clients/:cid", h.UpdateClient)
		authed.DELETE("/inbounds/:id/clients/:cid", h.DeleteClient)

		// Firewall
		authed.GET("/firewall", h.ListFirewall)
		authed.POST("/firewall/resync", h.ResyncFirewall)

		// First-run wizard
		authed.GET("/wizard/status", h.WizardStatus)
		authed.POST("/wizard/complete", h.WizardComplete)
		authed.POST("/wizard/validate-domain", h.WizardValidateDomain)
		authed.POST("/wizard/create-funnel", h.WizardCreateFunnel)

		// Subscription CRUD (admin)
		authed.GET("/subscriptions", h.ListSubscriptions)
		authed.POST("/subscriptions", h.CreateSubscription)
		authed.GET("/subscriptions/:id", h.GetSubscription)
		authed.DELETE("/subscriptions/:id", h.DeleteSubscription)
		authed.POST("/subscriptions/:id/revoke", h.RevokeSubscription)
		authed.POST("/subscriptions/:id/rotate", h.RotateSubscription)

		// Preview a client's URI bundle (admin)
		authed.GET("/clients/:id/preview", h.PreviewClientBundle)

		// ACME certs
		authed.GET("/certs", h.ListCerts)
		authed.GET("/certs/dns-providers", h.ListDNSProviders)
		authed.POST("/certs", h.IssueCert)
		authed.POST("/certs/:id/renew", h.RenewCert)
		authed.PATCH("/certs/:id/auto-renew", h.SetCertAutoRenew)
		authed.DELETE("/certs/:id", h.DeleteCert)

		// System: BBR + firewall preview + host metrics
		authed.GET("/system/bbr/status", h.BBRStatus)
		authed.POST("/system/bbr/enable", h.BBREnable)
		authed.POST("/system/bbr/disable", h.BBRDisable)
		authed.GET("/system/info", h.SystemInfo)
		authed.GET("/system/ports/reserved", h.SystemPortsReserved)
		authed.GET("/system/xray/status", h.SystemXrayStatus)
		authed.POST("/system/xray/install", h.SystemXrayInstall)
		authed.GET("/firewall/preview", h.FirewallPreview)

		// Backup / restore: download the whole panel DB, or upload one to restore
		// (applied on the next restart).
		authed.POST("/system/backup", h.SystemBackup)
		authed.POST("/system/restore", h.SystemRestore)

		// Panel-editable settings (host, panel_path, notify)
		authed.GET("/settings", h.ListSettings)
		authed.PUT("/settings/host", h.UpdateHost)
		authed.PUT("/settings/panel-path", h.UpdatePanelPath)
		authed.PUT("/settings/timezone", h.UpdateTimezone)
		authed.PUT("/settings/language", h.UpdateLanguage)
		authed.PUT("/settings/notify", h.UpdateNotify)
		authed.POST("/settings/notify/test", h.TestNotify)
		authed.PUT("/admin/username", h.UpdateUsername)

		// WARP
		authed.GET("/warp", h.GetWarp)
		authed.PUT("/warp", h.PutWarp)
		authed.DELETE("/warp", h.DeleteWarp)
		authed.POST("/warp/register", h.RegisterWarp)

		// Advanced opt-in (CDN/Argo)
		authed.GET("/advanced", h.GetAdvanced)
		authed.PUT("/advanced", h.PutAdvanced)
		authed.DELETE("/advanced", h.DeleteAdvanced)

		// CDN preferred-IP helpers: curated candidate pool + server-side speed test
		authed.GET("/advanced/cdn/candidates", h.CDNCandidates)
		authed.POST("/advanced/cdn/candidates/refresh", h.CDNRefreshCandidates)
		authed.POST("/advanced/cdn/speedtest", h.CDNSpeedtest)
		// Granular per-feature saves so the inbound page's CDN / Argo / QUIC tabs
		// each persist independently (a CDN save never trips Argo's token check).
		authed.PUT("/advanced/cdn", h.PutAdvancedCDN)
		authed.PUT("/advanced/argo", h.PutAdvancedArgo)
		authed.PUT("/advanced/quic", h.PutAdvancedQUIC)
		authed.PUT("/advanced/logprivacy", h.PutAdvancedLogPrivacy)

		// Log privacy: wipe engine logs (sing-box.log / xray.log) in place
		authed.POST("/logs/clear", h.ClearLogs)
		authed.GET("/logs/size", h.LogsSize)

		// Argo tunnel control
		authed.GET("/argo/status", h.ArgoStatus)
		authed.POST("/argo/start", h.ArgoStart)
		authed.POST("/argo/stop", h.ArgoStop)

		// Argo via Cloudflare API: one-click create/select a named tunnel,
		// wire its ingress + DNS, and persist it as a fixed-mode config.
		authed.POST("/argo/cf/probe", h.ArgoCFProbe)
		authed.POST("/argo/cf/tunnels", h.ArgoCFListTunnels)
		authed.POST("/argo/cf/provision", h.ArgoCFProvision)

		// Routing rules
		authed.GET("/routes", h.ListRoutes)
		authed.GET("/routes/presets", h.RoutePresets)
		authed.POST("/routes/presets/apply", h.ApplyRoutePreset)
		authed.POST("/routes", h.CreateRoute)
		authed.PUT("/routes/:id", h.UpdateRoute)
		authed.DELETE("/routes/:id", h.DeleteRoute)
		authed.POST("/routes/bulk", h.BulkRoutes)
		authed.POST("/routes/reorder", h.ReorderRoutes)
		authed.POST("/routes/capture", h.CaptureDomains)
		authed.POST("/routes/capture/apply", h.ApplyCapturedRoutes)
		authed.POST("/routes/capture/live/start", h.StartLiveCapture)
		authed.GET("/routes/capture/live/status", h.LiveCaptureStatus)
		authed.POST("/routes/capture/live/clear", h.ClearLiveCapture)
		authed.POST("/routes/capture/live/stop", h.StopLiveCapture)

		// Unlock detection (Netflix/ChatGPT/etc.)
		authed.GET("/unlock/targets", h.UnlockTargets)
		authed.POST("/unlock/probe", h.UnlockProbe)
		authed.POST("/unlock/probe-warp", h.UnlockProbeWARP)

		// Observability: stats / quota enforcement / health snapshots / audit
		authed.GET("/stats/summary", h.StatsSummary)
		authed.GET("/users", h.ListUsers)
		authed.POST("/users", h.CreateUser)
		authed.PATCH("/users/:email", h.UpdateUser)
		authed.DELETE("/users/:email", h.DeleteUser)
		authed.POST("/quota/enforce", h.QuotaEnforce)
		authed.GET("/health/snapshots", h.HealthSnapshots)
		authed.POST("/health/check", h.HealthLatest)
		authed.GET("/audit", h.AuditList)
		authed.GET("/audit/actors", h.AuditActors)
		authed.GET("/audit/config", h.GetAuditConfig)
		authed.PUT("/audit/config", h.PutAuditConfig)
		authed.POST("/audit/clear", h.ClearAudit)
	}

	// Embedded SPA for all non-API routes.
	h.mountSPA(r)
	return r
}

// mountSPA serves the embedded front-end build and falls back to index.html for
// client-side routes (so deep links work).
func (h *Handler) mountSPA(r *gin.Engine) {
	sub, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		// No embedded assets (dev without a build): serve a tiny placeholder.
		r.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				c.JSON(http.StatusNotFound, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "no such endpoint"}})
				return
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(devPlaceholderHTML))
		})
		return
	}
	fileServer := http.FileServer(http.FS(sub))
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "no such endpoint"}})
			return
		}
		if !h.checkPanelGate(c) {
			// Look like an empty port — no body, no headers tipping that
			// EdgeNest lives here. The operator must paste the full panel URL.
			c.Status(http.StatusNotFound)
			return
		}
		// Try to serve the static file; if missing, fall back to index.html.
		if _, err := fs.Stat(sub, strings.TrimPrefix(p, "/")); err == nil && p != "/" {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(devPlaceholderHTML))
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", h.injectDefaultLang(index))
	})
}

// injectDefaultLang inlines window.__EDGENEST_DEFAULT_LANG into the served
// index.html so the SPA picks the right language on first paint — no flash
// from English to Chinese while an async fetch resolves. Seeded from the
// default_lang setting (which install.sh primed via install.lang).
func (h *Handler) injectDefaultLang(index []byte) []byte {
	lang, _ := h.store.GetSetting(bootstrap.KeyDefaultLang)
	if !bootstrap.IsSupportedLang(lang) {
		return index
	}
	tag := []byte(fmt.Sprintf(`<script>window.__EDGENEST_DEFAULT_LANG=%q;</script></head>`, lang))
	return bytes.Replace(index, []byte("</head>"), tag, 1)
}

const devPlaceholderHTML = `<!doctype html><html><head><meta charset="utf-8"><title>EdgeNest</title></head>
<body style="font-family:system-ui;background:#0b0e14;color:#e6e6e6;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
<div style="text-align:center"><h1>EdgeNest</h1><p>Backend is running. Front-end build not embedded yet.</p>
<p style="opacity:.6">Run <code>make web</code> to build the UI.</p></div></body></html>`
