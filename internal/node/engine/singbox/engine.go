// Package singbox is the sing-box concrete engine. It renders DesiredConfig
// inbounds (engine == "singbox") into a sing-box v1.13.x JSON config, runs
// `sing-box check`, atomically swaps the live config, restarts the subprocess
// and rolls back on failure.
//
// DISCIPLINE: must not import internal/control/* and must not touch HTTP.
package singbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// SingboxPinnedVersion is the pinned sing-box major.minor.patch.
// ADR-003 (revised in v0.03): we now ride v1.13.x. The v1.10 → v1.13
// rewrite removed: legacy "dns" outbound (replaced by rule_action
// hijack-dns), inbound-level sniff (replaced by rule_action sniff). If the
// installed binary reports a different MAJOR.MINOR we refuse to apply
// because the renderer emits 1.13-only schema (rule_action, typed Hysteria2
// masquerade, anytls inbound).
const SingboxPinnedVersion = "1.13.13"

// Engine implements engine.ProxyEngine for sing-box. One per node.
type Engine struct {
	binPath    string // /usr/local/bin/sing-box
	configPath string // active config, e.g. /etc/edgenest/sing-box.json
	workDir    string // staging + backup dir (must exist)
	logDir     string // log dir (best-effort)

	mu        sync.Mutex
	proc      *process // nil when not running
	startedAt time.Time
	version   string // memoised result of `sing-box version`

	// supervisorStop signals the crash-watch goroutine to exit. nil before first Start.
	supervisorStop chan struct{}
}

// New constructs a sing-box engine. binPath is the sing-box executable, workDir
// is a writable staging directory (defaults to dirname of configPath if empty).
func New(binPath, configPath, workDir, logDir string) *Engine {
	if workDir == "" {
		workDir = filepath.Dir(configPath)
	}
	if logDir == "" {
		logDir = workDir
	}
	return &Engine{
		binPath:    binPath,
		configPath: configPath,
		workDir:    workDir,
		logDir:     logDir,
	}
}

// Name implements engine.ProxyEngine.
func (e *Engine) Name() string { return core.EngineSingbox }

// Apply implements engine.ProxyEngine. It is the full pipeline:
//
//  1. Pre-flight: binary exists, version pinned.
//  2. Render: DesiredConfig → sing-box JSON (only inbounds engine=="singbox").
//  3. Stage: write to <workDir>/sing-box.json.new.
//  4. Check: `sing-box check -c <new>`. Fail → return non-OK, no swap.
//  5. Backup: cp current configPath → <workDir>/sing-box.json.bak (if exists).
//  6. Swap: os.Rename(new, configPath) — atomic on same FS.
//  7. Reload: if proc nil → start; else stop + start.
//  8. Verify: sleep 2s, check proc still alive.
//  9. Rollback on verify failure: restore .bak, restart, mark RolledBack.
func (e *Engine) Apply(cfg core.DesiredConfig) (core.ApplyResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 1) Pre-flight.
	if err := e.preflight(); err != nil {
		return core.ApplyResult{OK: false, Message: err.Error()}, nil
	}

	// 2) Render. Filter inbounds + outbounds + routes for this engine.
	rendered, err := render(cfg)
	if err != nil {
		return core.ApplyResult{OK: false, Message: "render: " + err.Error()}, nil
	}

	// 3) Stage.
	newPath := filepath.Join(e.workDir, "sing-box.json.new")
	if err := writeFileAtomic(newPath, rendered, 0o600); err != nil {
		return core.ApplyResult{OK: false, Message: "stage: " + err.Error()}, nil
	}

	// 4) Check.
	if out, err := e.runCheck(newPath); err != nil {
		_ = os.Remove(newPath)
		return core.ApplyResult{OK: false, Message: "sing-box check failed:\n" + out}, nil
	}

	// 5) Backup.
	bakPath := filepath.Join(e.workDir, "sing-box.json.bak")
	if _, err := os.Stat(e.configPath); err == nil {
		if err := copyFile(e.configPath, bakPath); err != nil {
			_ = os.Remove(newPath)
			return core.ApplyResult{OK: false, Message: "backup: " + err.Error()}, nil
		}
	} else {
		_ = os.Remove(bakPath) // stale bak from a previous failed apply
	}

	// 6) Swap.
	if err := os.Rename(newPath, e.configPath); err != nil {
		return core.ApplyResult{OK: false, Message: "swap: " + err.Error()}, nil
	}

	// 7) Reload.
	if err := e.restartLocked(); err != nil {
		// 9) Rollback.
		if rbErr := e.rollbackLocked(bakPath); rbErr != nil {
			return core.ApplyResult{OK: false, RolledBack: false, Message: fmt.Sprintf("reload failed (%v) and rollback failed (%v)", err, rbErr)}, nil
		}
		return core.ApplyResult{OK: false, RolledBack: true, Message: "reload failed, rolled back: " + err.Error()}, nil
	}

	// 8) Verify.
	time.Sleep(verifyDelay)
	if !e.aliveLocked() {
		if rbErr := e.rollbackLocked(bakPath); rbErr != nil {
			return core.ApplyResult{OK: false, RolledBack: false, Message: "subprocess died after reload and rollback failed: " + rbErr.Error()}, nil
		}
		return core.ApplyResult{OK: false, RolledBack: true, Message: "subprocess died after reload, rolled back"}, nil
	}

	return core.ApplyResult{OK: true, Message: "applied"}, nil
}

// Restart implements engine.ProxyEngine. Restarts using the current configPath.
func (e *Engine) Restart() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.restartLocked()
}

// Stop implements engine.ProxyEngine.
func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopLocked()
}

// Status implements engine.ProxyEngine.
func (e *Engine) Status() core.EngineStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	running := e.aliveLocked()
	version := e.version
	if version == "" {
		// Best-effort: detect now. Failure is fine; we just report "unknown".
		if v, err := detectVersion(e.binPath); err == nil {
			e.version = v
			version = v
		}
	}
	st := core.EngineStatus{Running: running, Version: version}
	if running {
		st.Uptime = int64(time.Since(e.startedAt).Seconds())
	}
	if !running && e.proc != nil {
		st.Detail = "stopped"
	}
	if e.binPath == "" {
		st.Detail = "bin path not configured"
	} else if _, err := os.Stat(e.binPath); err != nil {
		st.Detail = "binary missing: " + e.binPath
	}
	return st
}

// QueryStats implements engine.ProxyEngine.
//
// Hook for TASK-13: real stats use the sing-box v2 stats API
// (clash_api / v2ray-api). We return empty until that lands.
func (e *Engine) QueryStats(reset bool) (map[string]core.Traffic, error) {
	return map[string]core.Traffic{}, nil
}

// preflight checks the binary is present and version-pinned.
func (e *Engine) preflight() error {
	if e.binPath == "" {
		return errors.New("sing-box binary path not configured")
	}
	if _, err := os.Stat(e.binPath); err != nil {
		return fmt.Errorf("sing-box binary not found at %s", e.binPath)
	}
	if e.version == "" {
		v, err := detectVersion(e.binPath)
		if err != nil {
			return fmt.Errorf("detect sing-box version: %w", err)
		}
		e.version = v
	}
	if !versionMatchesPin(e.version, SingboxPinnedVersion) {
		return fmt.Errorf("sing-box version %s does not match pinned %s (ADR-003)", e.version, SingboxPinnedVersion)
	}
	return nil
}
