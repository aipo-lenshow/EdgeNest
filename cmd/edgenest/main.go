// Command edgenest is the single binary for all roles. In v1 only the
// standalone (Lite) role is functional: control plane and node run in one
// process, with the control plane talking to the node through an in-process
// NodeClient (the seam that lets Platform/v2 swap in a remote client later).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/alertrunner"
	"github.com/aipo-lenshow/EdgeNest/internal/control/api"
	"github.com/aipo-lenshow/EdgeNest/internal/control/argo"
	"github.com/aipo-lenshow/EdgeNest/internal/control/bootstrap"
	"github.com/aipo-lenshow/EdgeNest/internal/control/botrunner"
	"github.com/aipo-lenshow/EdgeNest/internal/control/cert"
	"github.com/aipo-lenshow/EdgeNest/internal/control/config"
	"github.com/aipo-lenshow/EdgeNest/internal/control/digest"
	"github.com/aipo-lenshow/EdgeNest/internal/control/health"
	"github.com/aipo-lenshow/EdgeNest/internal/control/notifyrunner"
	"github.com/aipo-lenshow/EdgeNest/internal/control/orchestrator"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/control/timesync"
	"github.com/aipo-lenshow/EdgeNest/internal/control/updatecheck"
	"github.com/aipo-lenshow/EdgeNest/internal/control/wizard"
	"github.com/aipo-lenshow/EdgeNest/internal/core/nodeapi"
	"github.com/aipo-lenshow/EdgeNest/internal/logredact"
	"github.com/aipo-lenshow/EdgeNest/internal/node"
	"github.com/aipo-lenshow/EdgeNest/internal/node/engine/singbox"
	"github.com/aipo-lenshow/EdgeNest/internal/node/engine/xray"
)

func main() {
	// Operator CLI: `edgenest status|menu|reset-pass|uninstall`, or a bare
	// `edgenest` on a root terminal with an existing install opens the menu.
	// Returns true only when it handled the invocation; the systemd service
	// (always launched with flags) and dev `go run` fall through to the server.
	if dispatchManage(os.Args[1:]) {
		return
	}

	cfg := config.Default()
	flag.StringVar(&cfg.Role, "role", cfg.Role, "run role: standalone | control | node")
	flag.StringVar(&cfg.Listen, "listen", cfg.Listen, "panel HTTP listen address")
	flag.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "data directory")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("edgenest", version)
		return
	}
	api.Version = version
	digest.AppVersion = version

	switch cfg.Role {
	case config.RoleStandalone:
		runStandalone(cfg)
	case config.RoleControl, config.RoleNode:
		log.Fatalf("role %q is reserved for Platform (v2) and not implemented yet", cfg.Role)
	default:
		log.Fatalf("unknown role %q", cfg.Role)
	}
}

// version is overridden by the linker via Makefile's -X main.version flag.
// The compiled-in default tracks the current source-tree release.
var version = "1.12.0624"

func runStandalone(cfg config.Config) {
	if err := cfg.EnsureDataDir(); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// Reap any cloudflared left running by a prior process (deploy/crash/reboot):
	// the Argo supervisor's handle is in-memory, so a stray child would desync
	// the reported tunnel state and let a second tunnel spawn. WARP/CDN need no
	// equivalent — they're config-only, rebuilt from the DB on startup.
	argo.KillStray()

	// Apply a backup staged by the in-panel restore flow, if any. Done before
	// the DB is opened so the swap is race-free (the live process can't hold the
	// file open yet). The previous DB is preserved as <db>.pre-restore-<unix>.
	if applied, err := store.ApplyPendingRestore(cfg.DBPath()); err != nil {
		log.Fatalf("apply pending restore: %v", err)
	} else if applied {
		log.Printf("restore: applied staged backup, previous DB kept as %s.pre-restore-*", cfg.DBPath())
	}

	// Data layer.
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	// First-run provisioning (idempotent).
	res, err := bootstrap.Ensure(st)
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}
	if err := bootstrap.ImportInstallerLang(st, cfg.DataDir); err != nil {
		log.Printf("import installer lang hint: %v (continuing)", err)
	}
	bootstrap.EmitCredentials(cfg.DataDir, cfg.Listen, res)

	// Node execution plane (in-process) + the seam.
	localNode, err := st.GetLocalNode()
	if err != nil {
		log.Fatalf("get local node: %v", err)
	}
	localNodeID := strconv.FormatUint(uint64(localNode.ID), 10)
	sb := singbox.New(cfg.SingboxBin, cfg.SingboxConfig, cfg.DataDir, cfg.DataDir)
	xr := xray.New(cfg.XrayBin, cfg.XrayConfig, cfg.DataDir, cfg.DataDir)
	ln := node.NewLocalNode(localNodeID, node.Options{Singbox: sb, Xray: xr})
	var nodeClient nodeapi.NodeClient = nodeapi.NewLocalNodeClient(ln)

	// Restore the "don't log client IP" privacy choice from the persisted setting
	// so it survives restarts. The engines wrap their log writers with
	// logredact.Writer; this sets the global gate those writers read.
	if adv, _ := st.GetAdvanced(localNode.ID); adv != nil {
		logredact.SetEnabled(adv.RedactClientIP)
	}

	// JWT secret.
	secret, err := st.GetSetting(bootstrap.KeyJWTSecret)
	if err != nil || secret == "" {
		log.Fatalf("jwt secret missing")
	}

	// Orchestrator: aggregates DB → DesiredConfig → NodeClient. We pass the
	// panel port so safe-mode (I7) always keeps the admin port reachable.
	orch := orchestrator.NewWithPanelPort(st, nodeClient, parsePanelPort(cfg.Listen))

	// ACME cert manager + daily renewal scheduler. Staging (LE's rate-limit-free
	// test CA) is opt-in via the EDGENEST_ACME_STAGING env var or the
	// acme_use_staging setting; read once at startup, so flipping it needs a
	// restart (which the renewal-test flow does anyway). Issue + renew share
	// this issuer, so staging applies to both. Built before the wizard because
	// the wizard issues a real cert synchronously when a grey-cloud domain is
	// supplied.
	certsDir := filepath.Join(cfg.DataDir, "certs")
	issuer := cert.NewLegoIssuer()
	if useACMEStaging(st) {
		issuer.DirectoryURL = cert.LEDirectoryStaging
		log.Printf("ACME: using Let's Encrypt STAGING directory (test certs, not browser-trusted)")
	}
	certMgr := cert.NewManager(st, certsDir, issuer)
	cert.NewScheduler(certMgr, 0).Start(context.Background())

	// First-run wizard. Takes certMgr so a grey-cloud domain batch gets a real
	// ACME cert instead of the bootstrap self-signed pair.
	wiz := wizard.New(st, orch, certMgr)

	// Health snapshot recorder (every 5min, retain last 1000 samples).
	healthRec := &health.Recorder{
		Store:         st,
		Node:          nodeClient,
		NodeID:        localNodeID,
		NumericNodeID: localNode.ID,
		Retain:        1000,
	}
	health.NewScheduler(healthRec, 0).Start(context.Background())

	// Daily notify bot (sends VPS + traffic summary to Telegram at the
	// configured local hour, if notify_enabled=true and the bot is configured).
	notifyrunner.New(st, nodeClient, localNodeID).Start(context.Background())

	// Proactive alerter (pushes quota/expiry/cert/engine-offline alerts as they
	// appear, deduped; opt out via alerts_enabled=false). Shares the notify token
	// + chat target.
	alertrunner.New(st, nodeClient, localNodeID).Start(context.Background())

	// Interactive Telegram management bot (long-polls getUpdates; runs only when
	// bot_enabled=true). Shares the notify bot token; admin chat-ID allowlist
	// gates every command.
	botrunner.New(st, nodeClient, orch, localNodeID, parsePanelPort(cfg.Listen)).Start(context.Background())

	// Periodic update check (caches the latest GitHub release tag; bot + About
	// page read the cache to flag a newer version). Opt out via
	// update_check_enabled=false. Public release metadata only — no host info.
	updatecheck.New(st, version).Start(context.Background())

	// HTTP / control plane.
	h := api.NewHandler(st, nodeClient, api.HandlerDeps{
		Orch:      orch,
		Wizard:    wiz,
		CertMgr:   certMgr,
		PanelPort: parsePanelPort(cfg.Listen),
		DataDir:   cfg.DataDir,
	}, secret, localNodeID)
	r := h.NewRouter()

	// Background clock sync. SS-2022 / Hysteria2 / TUIC all carry a client
	// timestamp the server checks against its own clock with a ±30 s replay
	// window. Many VPS images ship with systemd-timesyncd active but never
	// actually synced (provider blocks UDP/123, or NTP pool is unreachable
	// from the region), and the symptom is silent rejection of every client.
	// The timesync goroutine self-corrects over HTTPS so the operator doesn't
	// have to think about it.
	timesync.New(0, 0).Start(context.Background())

	// Background quota/expiry enforcement: a traffic poller (sing-box v2ray_api
	// StatsService -> per-user byte counters) plus a loop that disables users
	// over their cap or past expiry. See internal/control/trafficpoller.
	// Best-effort; never fatal.
	h.StartQuotaEnforcement(context.Background())

	// One-shot startup migration: rewrite legacy tag-string allowed_inbounds
	// rows to the modern inbound_id form. Idempotent (skips already-migrated
	// rows). Logged + ignored on error so a bad migration can't keep the
	// panel down.
	if n, err := st.MigrateAllowedInboundsToIDs(); err != nil {
		log.Printf("migrate allowed_inbounds: %d row(s) rewritten, first error: %v", n, err)
	} else if n > 0 {
		log.Printf("migrated %d subscription(s) allowed_inbounds tag→id", n)
	}

	// Best-effort startup Apply: if the DB already has inbounds (panel restart,
	// hot-swap of binary, etc.), push them through the orchestrator so the
	// proxy engine reconciles to the current desired state. Errors are logged
	// but never fatal — the panel must come up even if a render bug is
	// blocking apply, so the operator can use the UI to fix it.
	go func() {
		res, err := orch.Apply(context.Background(), localNode.ID)
		if err != nil {
			log.Printf("startup apply: error: %v", err)
			return
		}
		if !res.OK {
			log.Printf("startup apply: not OK: %s (rolled_back=%v)", res.Message, res.RolledBack)
			return
		}
		log.Printf("startup apply: OK (%s)", res.Message)

		// Relaunch the Argo tunnel if the operator had it enabled — apply has
		// now reconciled sing-box so the inbound's loopback listener is up for
		// cloudflared to dial. Best-effort; never blocks panel availability.
		actx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		h.AutoStartArgoIfEnabled(actx)
	}()

	log.Printf("EdgeNest v%s standalone listening on %s (role=%s)", version, cfg.Listen, cfg.Role)
	httpListener, err := listenWithFamilyFallback(cfg.Listen)
	if err != nil {
		log.Fatalf("http server: listen %s: %v", cfg.Listen, err)
	}
	if err := r.RunListener(httpListener); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

// listenWithFamilyFallback opens the panel TCP socket with one retry. The
// configured addr is usually "[::]:2087" (dual-stack wildcard), which the
// kernel rejects on a v4-only host where install.sh wrote
// net.ipv6.conf.all.disable_ipv6=1 (a v4-only directive). When the v6
// bind fails for that reason, retry with "0.0.0.0:port" so the panel still
// comes up — without this fallback, a fresh install on a v4-only VPS would
// leave the operator locked out of the UI with only a log line to show for it.
//
// The mirror case (v4-only "0.0.0.0:port" failing on a v6-only kernel) is
// rare in practice; install.sh's smart listen picks the right family at
// install time. This fallback covers the operator who started the binary
// by hand or whose capability changed after install.
func listenWithFamilyFallback(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}
	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, err
	}
	var alt string
	switch host {
	case "", "::", "[::]":
		alt = net.JoinHostPort("0.0.0.0", port)
	case "0.0.0.0":
		alt = net.JoinHostPort("::", port)
	default:
		return nil, err
	}
	if alt == addr {
		return nil, err
	}
	log.Printf("http server: listen %s failed (%v) — retrying %s", addr, err, alt)
	return net.Listen("tcp", alt)
}

// parsePanelPort extracts the TCP port from a "host:port" listen string so the
// orchestrator can guarantee the admin port stays in the firewall allow-list
// (Invariant I7 safe-mode). Returns 0 if parsing fails; orchestrator treats 0
// as "no panel port hint" and falls back to the inbound-derived list.
func parsePanelPort(listen string) int {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return p
}

// useACMEStaging decides whether ACME issuance/renewal targets Let's Encrypt's
// staging CA. The env var wins (handy for a one-off restart without touching
// the DB); otherwise the acme_use_staging setting. Staging certs aren't
// browser-trusted but don't count against production rate limits.
func useACMEStaging(st *store.Store) bool {
	if v := os.Getenv("EDGENEST_ACME_STAGING"); v == "1" || v == "true" {
		return true
	}
	v, _ := st.GetSetting("acme_use_staging")
	return v == "true"
}
