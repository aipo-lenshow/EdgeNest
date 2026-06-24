// Package xray is the xray-core concrete engine. It renders DesiredConfig
// inbounds (engine == "xray") into an xray-core v1.8+ JSON config, runs
// `xray -test -config`, atomically swaps the live config, restarts the
// subprocess and rolls back on failure.
//
// DISCIPLINE: must not import internal/control/* and must not touch HTTP.
package xray

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// XrayPinnedMajor is the xray-core MAJOR version we pin to. xray-core switched
// to date-based versioning in 2025 (v25.x.x = January 2025, v26.x.x = current).
// The renderer was empirically validated against v26.3.27; any same-major
// build (26.x.x) is accepted.
const XrayPinnedMajor = "26"

// Engine implements engine.ProxyEngine for xray-core. One per node.
type Engine struct {
	binPath    string // /usr/local/bin/xray
	configPath string // active config, e.g. /etc/edgenest/xray.json
	workDir    string // staging + backup dir
	logDir     string // log dir (best-effort)

	mu        sync.Mutex
	proc      *process
	startedAt time.Time
	version   string // memoised `xray version`

	supervisorStop chan struct{}
}

// New constructs an xray engine.
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
func (e *Engine) Name() string { return core.EngineXray }

// Apply implements engine.ProxyEngine — same 9-step pipeline as sing-box.
func (e *Engine) Apply(cfg core.DesiredConfig) (core.ApplyResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Short-circuit before preflight: a deployment with zero xray-engine
	// inbounds shouldn't require the xray binary at all. Without this guard,
	// any sing-box-only install fails Apply just because /usr/local/bin/xray
	// is missing.
	if !hasOwnedInbound(cfg) {
		if e.proc != nil {
			_ = e.stopLocked()
		}
		return core.ApplyResult{OK: true, Message: "no xray inbounds, engine idle"}, nil
	}

	if err := e.preflight(); err != nil {
		return core.ApplyResult{OK: false, Message: err.Error()}, nil
	}

	rendered, err := render(cfg)
	if err != nil {
		return core.ApplyResult{OK: false, Message: "render: " + err.Error()}, nil
	}

	newPath := filepath.Join(e.workDir, "xray.json.new")
	if err := writeFileAtomic(newPath, rendered, 0o600); err != nil {
		return core.ApplyResult{OK: false, Message: "stage: " + err.Error()}, nil
	}

	if out, err := e.runCheck(newPath); err != nil {
		_ = os.Remove(newPath)
		return core.ApplyResult{OK: false, Message: "xray test failed:\n" + out}, nil
	}

	bakPath := filepath.Join(e.workDir, "xray.json.bak")
	if _, err := os.Stat(e.configPath); err == nil {
		if err := copyFile(e.configPath, bakPath); err != nil {
			_ = os.Remove(newPath)
			return core.ApplyResult{OK: false, Message: "backup: " + err.Error()}, nil
		}
	} else {
		_ = os.Remove(bakPath)
	}

	if err := os.Rename(newPath, e.configPath); err != nil {
		return core.ApplyResult{OK: false, Message: "swap: " + err.Error()}, nil
	}

	if err := e.restartLocked(); err != nil {
		if rbErr := e.rollbackLocked(bakPath); rbErr != nil {
			return core.ApplyResult{OK: false, RolledBack: false, Message: fmt.Sprintf("reload failed (%v) and rollback failed (%v)", err, rbErr)}, nil
		}
		return core.ApplyResult{OK: false, RolledBack: true, Message: "reload failed, rolled back: " + err.Error()}, nil
	}

	time.Sleep(verifyDelay)
	if !e.aliveLocked() {
		if rbErr := e.rollbackLocked(bakPath); rbErr != nil {
			return core.ApplyResult{OK: false, RolledBack: false, Message: "subprocess died after reload and rollback failed: " + rbErr.Error()}, nil
		}
		return core.ApplyResult{OK: false, RolledBack: true, Message: "subprocess died after reload, rolled back"}, nil
	}

	return core.ApplyResult{OK: true, Message: "applied"}, nil
}

func (e *Engine) Restart() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.restartLocked()
}

func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopLocked()
}

func (e *Engine) Status() core.EngineStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	running := e.aliveLocked()
	version := e.version
	if version == "" {
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

// QueryStats returns per-client traffic. Real impl uses xray's stats API
// (statsservice over gRPC); we return empty here and let sing-box provide
// the stats hook in v1.
func (e *Engine) QueryStats(reset bool) (map[string]core.Traffic, error) {
	return map[string]core.Traffic{}, nil
}

func (e *Engine) preflight() error {
	if e.binPath == "" {
		return errors.New("xray binary path not configured")
	}
	if _, err := os.Stat(e.binPath); err != nil {
		return fmt.Errorf("xray binary not found at %s", e.binPath)
	}
	if e.version == "" {
		v, err := detectVersion(e.binPath)
		if err != nil {
			return fmt.Errorf("detect xray version: %w", err)
		}
		e.version = v
	}
	if !versionMatchesMajor(e.version, XrayPinnedMajor) {
		return fmt.Errorf("xray version %s does not match pinned major %s", e.version, XrayPinnedMajor)
	}
	return nil
}

// hasOwnedInbound is true if any inbound in cfg routes to xray. Registry
// pre-filters to xray-owned inbounds before calling Apply, so in production
// any non-empty inbound list here is for us.
func hasOwnedInbound(cfg core.DesiredConfig) bool {
	return len(cfg.Inbounds) > 0
}
