package engine

import (
	"fmt"
	"sort"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// Registry groups one ProxyEngine per engine name and dispatches DesiredConfig
// to each in a deterministic order. The node execution plane owns one Registry.
type Registry struct {
	engines map[string]ProxyEngine
}

// NewRegistry constructs a registry from the given engines. Duplicate Name()
// returns an error so misconfiguration fails fast.
func NewRegistry(engines ...ProxyEngine) (*Registry, error) {
	r := &Registry{engines: map[string]ProxyEngine{}}
	for _, e := range engines {
		if e == nil {
			continue
		}
		name := e.Name()
		if _, dup := r.engines[name]; dup {
			return nil, fmt.Errorf("duplicate engine %q", name)
		}
		r.engines[name] = e
	}
	return r, nil
}

// Get returns the engine by name, or nil if absent.
func (r *Registry) Get(name string) ProxyEngine { return r.engines[name] }

// Names returns the registered engine names in sorted order. Useful for
// deterministic Apply ordering and status reporting.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.engines))
	for k := range r.engines {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ApplyAll dispatches cfg to each engine in name-sorted order. It splits
// cfg.Inbounds by EngineForType (so a sing-box engine only sees inbounds it
// can render) and shares outbounds/routes across engines (each engine renders
// what it understands and ignores the rest).
//
// Behaviour: stops at the first engine that returns OK=false. The earlier
// engines are NOT rolled back individually here (each engine already rolled
// itself back internally if it failed mid-apply); a higher layer can decide
// to re-Apply a known-good config to roll everything back if needed.
func (r *Registry) ApplyAll(cfg core.DesiredConfig) (core.ApplyResult, error) {
	for _, name := range r.Names() {
		eng := r.engines[name]
		sub := filterFor(cfg, name)
		res, err := eng.Apply(sub)
		if err != nil {
			return res, fmt.Errorf("engine %s apply error: %w", name, err)
		}
		if !res.OK {
			return res, nil
		}
	}
	return core.ApplyResult{OK: true, Message: "all engines applied"}, nil
}

// filterFor returns a copy of cfg with Inbounds reduced to those owned by name.
func filterFor(cfg core.DesiredConfig, name string) core.DesiredConfig {
	out := cfg // shallow copy; we replace Inbounds only
	filtered := make([]core.InboundSpec, 0, len(cfg.Inbounds))
	for _, in := range cfg.Inbounds {
		eng := in.Engine
		if eng == "" {
			eng = EngineForType(in.Type)
		}
		if eng == name {
			filtered = append(filtered, in)
		}
	}
	out.Inbounds = filtered
	return out
}

// RestartAll calls Restart on every engine, returning the first error.
func (r *Registry) RestartAll() error {
	for _, name := range r.Names() {
		if err := r.engines[name].Restart(); err != nil {
			return fmt.Errorf("restart %s: %w", name, err)
		}
	}
	return nil
}

// StopAll calls Stop on every engine. Collects errors and returns the last one;
// continues even if some fail (shutdown path should be best-effort).
func (r *Registry) StopAll() error {
	var last error
	for _, name := range r.Names() {
		if err := r.engines[name].Stop(); err != nil {
			last = err
		}
	}
	return last
}

// AggregateStatus returns the status of the primary engine (sing-box) for now.
// TASK-13 will expand this to a per-engine slice.
func (r *Registry) AggregateStatus() core.EngineStatus {
	if e := r.engines[core.EngineSingbox]; e != nil {
		return e.Status()
	}
	for _, name := range r.Names() {
		return r.engines[name].Status()
	}
	return core.EngineStatus{Running: false, Detail: "no engines registered"}
}
