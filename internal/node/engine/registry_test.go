package engine

import (
	"errors"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// fakeEngine is a test double; records the InboundSpec tags it received.
type fakeEngine struct {
	name      string
	seen      []string
	applyErr  error
	applyRes  core.ApplyResult
}

func (f *fakeEngine) Name() string { return f.name }
func (f *fakeEngine) Apply(cfg core.DesiredConfig) (core.ApplyResult, error) {
	for _, in := range cfg.Inbounds {
		f.seen = append(f.seen, in.Tag)
	}
	if f.applyErr != nil {
		return f.applyRes, f.applyErr
	}
	return f.applyRes, nil
}
func (f *fakeEngine) Restart() error                                       { return nil }
func (f *fakeEngine) Stop() error                                          { return nil }
func (f *fakeEngine) Status() core.EngineStatus                            { return core.EngineStatus{Running: true} }
func (f *fakeEngine) QueryStats(reset bool) (map[string]core.Traffic, error) { return nil, nil }

func TestRegistry_DuplicateName(t *testing.T) {
	a := &fakeEngine{name: core.EngineSingbox, applyRes: core.ApplyResult{OK: true}}
	b := &fakeEngine{name: core.EngineSingbox, applyRes: core.ApplyResult{OK: true}}
	if _, err := NewRegistry(a, b); err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestRegistry_FiltersInboundsByEngine(t *testing.T) {
	sb := &fakeEngine{name: core.EngineSingbox, applyRes: core.ApplyResult{OK: true}}
	xr := &fakeEngine{name: core.EngineXray, applyRes: core.ApplyResult{OK: true}}
	reg, err := NewRegistry(sb, xr)
	if err != nil {
		t.Fatal(err)
	}

	cfg := core.DesiredConfig{
		Inbounds: []core.InboundSpec{
			{Tag: "sb-1", Engine: core.EngineSingbox, Type: "vless"},
			{Tag: "sb-2", Type: "hysteria2"},               // engine empty → routed by type → singbox
			{Tag: "sb-3", Type: "anytls"},                  // anytls moved to singbox (xray mainline lacks it)
			{Tag: "xr-1", Engine: core.EngineXray, Type: "vless-xhttp"},
			{Tag: "xr-2", Type: "vless-xhttp"},             // engine empty → routed by type → xray
		},
	}

	if _, err := reg.ApplyAll(cfg); err != nil {
		t.Fatal(err)
	}
	if got := sb.seen; !equal(got, []string{"sb-1", "sb-2", "sb-3"}) {
		t.Errorf("sing-box saw %v, want [sb-1 sb-2 sb-3]", got)
	}
	if got := xr.seen; !equal(got, []string{"xr-1", "xr-2"}) {
		t.Errorf("xray saw %v, want [xr-1 xr-2]", got)
	}
}

func TestRegistry_StopsOnFirstNonOK(t *testing.T) {
	sb := &fakeEngine{name: core.EngineSingbox, applyRes: core.ApplyResult{OK: false, Message: "broken"}}
	xr := &fakeEngine{name: core.EngineXray, applyRes: core.ApplyResult{OK: true}}
	reg, err := NewRegistry(sb, xr)
	if err != nil {
		t.Fatal(err)
	}
	res, err := reg.ApplyAll(core.DesiredConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.OK {
		t.Fatal("expected res.OK=false")
	}
	if len(xr.seen) != 0 {
		t.Errorf("xray should not have been called after singbox failed; saw %v", xr.seen)
	}
}

func TestRegistry_PropagatesErr(t *testing.T) {
	sb := &fakeEngine{name: core.EngineSingbox, applyErr: errors.New("boom")}
	reg, _ := NewRegistry(sb)
	_, err := reg.ApplyAll(core.DesiredConfig{})
	if err == nil || err.Error() == "" {
		t.Fatal("expected wrapped error from engine")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
