// Package node is the node execution plane. It consumes core.DesiredConfig and
// performs real work on the host (proxy engines, firewall, bbr, warp, stats).
//
// DISCIPLINE: this package and its subpackages must never import
// internal/control/* and must never touch HTTP. They only take DesiredConfig
// and return results/snapshots.
package node

import (
	"runtime"
	"strings"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/aipo-lenshow/EdgeNest/internal/node/engine"
	"github.com/aipo-lenshow/EdgeNest/internal/node/engine/singbox"
	"github.com/aipo-lenshow/EdgeNest/internal/node/engine/xray"
	"github.com/aipo-lenshow/EdgeNest/internal/node/firewall"
)

// LocalNode is the in-process node implementation used in standalone (Lite)
// mode. It owns one engine.Registry holding the concrete engines.
type LocalNode struct {
	id        string
	startedAt time.Time
	reg       *engine.Registry
}

// Options configure a LocalNode at construction time. Empty fields fall back to
// safe defaults (no engines registered → Apply will accept configs but produce
// no live proxy). Production / standalone callers should always pass a Singbox.
type Options struct {
	Singbox *singbox.Engine
	Xray    *xray.Engine
}

// NewLocalNode constructs the local node with the given node id and options.
// If opts.Singbox is nil, the registry is built without a sing-box engine and
// Apply will report "no engines registered" — useful for tests.
func NewLocalNode(id string, opts Options) *LocalNode {
	engines := []engine.ProxyEngine{}
	if opts.Singbox != nil {
		engines = append(engines, opts.Singbox)
	}
	if opts.Xray != nil {
		engines = append(engines, opts.Xray)
	}
	reg, _ := engine.NewRegistry(engines...) // err only on duplicate names; we control input
	return &LocalNode{id: id, startedAt: time.Now(), reg: reg}
}

// ID returns the node id.
func (n *LocalNode) ID() string { return n.id }

// Apply dispatches the DesiredConfig to each registered engine. Each engine
// independently renders, checks, swaps and rolls back on failure.
func (n *LocalNode) Apply(cfg core.DesiredConfig) (core.ApplyResult, error) {
	if n.reg == nil || len(n.reg.Names()) == 0 {
		return core.ApplyResult{
			OK:      false,
			Message: "no engines registered (likely missing sing-box binary)",
		}, nil
	}
	res, err := n.reg.ApplyAll(cfg)
	if res.OK {
		// Reconcile iptables INPUT to match the desired allow list. Failures
		// here are reported in the result message but don't fail the apply —
		// the engines have already swapped in the new config and we don't
		// want to roll that back over a firewall hiccup.
		if ferr := firewall.Apply(cfg.Firewall.AllowPorts); ferr != nil {
			res.Message = strings.TrimSpace(res.Message + " (firewall apply warning: " + ferr.Error() + ")")
		}
		// Reconcile nat PREROUTING REDIRECT rules for Hysteria2 port hopping
		// (v4+v6). Same best-effort policy as the INPUT reconciler above.
		if herr := firewall.ApplyPortHops(cfg.Firewall.PortHops); herr != nil {
			res.Message = strings.TrimSpace(res.Message + " (port-hop apply warning: " + herr.Error() + ")")
		}
	}
	return res, err
}

// Restart restarts every engine.
func (n *LocalNode) Restart() error {
	if n.reg == nil {
		return nil
	}
	return n.reg.RestartAll()
}

// Status reports the aggregate engine status (sing-box if present).
func (n *LocalNode) Status() core.EngineStatus {
	if n.reg == nil {
		return core.EngineStatus{Running: false, Version: "no-engines", Uptime: int64(time.Since(n.startedAt).Seconds())}
	}
	return n.reg.AggregateStatus()
}

// QueryStats aggregates per-client traffic across engines. v0: only sing-box,
// which currently returns empty (real impl in TASK-13).
func (n *LocalNode) QueryStats(reset bool) (map[string]core.Traffic, error) {
	out := map[string]core.Traffic{}
	if n.reg == nil {
		return out, nil
	}
	for _, name := range n.reg.Names() {
		stats, err := n.reg.Get(name).QueryStats(reset)
		if err != nil {
			return nil, err
		}
		for k, v := range stats {
			out[k] = v
		}
	}
	return out, nil
}

// Health returns a host + engine snapshot. CPU/Mem/Disk are read from /proc
// on Linux; on other OSes they stay at zero with an explanatory note in
// Errors. BBR detection lives on the control side (system.ReadBBRState) —
// node-side stays out of that to keep the dev laptop happy.
func (n *LocalNode) Health() core.HealthSnapshot {
	m := readHostMetrics()
	snap := core.HealthSnapshot{
		CPU:    m.CPULoad1,
		Mem:    m.MemUsed,
		Disk:   m.DiskUsed,
		BBR:    "unknown",
		Errors: m.Notes,
	}
	if n.reg != nil {
		if sb := n.reg.Get(core.EngineSingbox); sb != nil {
			snap.SingboxRunning = sb.Status().Running
		}
		if xr := n.reg.Get(core.EngineXray); xr != nil {
			snap.XrayRunning = xr.Status().Running
		}
	}
	return snap
}

// goVersion is a tiny helper to prove the build links correctly; harmless.
func goVersion() string { return runtime.Version() }
