// Package engine defines the proxy engine abstraction and the protocol→engine
// routing used by the node execution plane. Concrete engines live in
// subpackages (singbox, xray). This package must not import control/* code.
package engine

import "github.com/aipo-lenshow/EdgeNest/internal/core"

// ProxyEngine is implemented by each concrete proxy engine (sing-box, xray).
type ProxyEngine interface {
	// Name returns the engine identifier (core.EngineSingbox / EngineXray).
	Name() string

	// Apply renders the given inbounds/outbounds/routes for THIS engine,
	// validates the config, applies it atomically and rolls back on failure.
	Apply(cfg core.DesiredConfig) (core.ApplyResult, error)

	// Restart restarts the engine subprocess.
	Restart() error

	// Stop stops the engine subprocess.
	Stop() error

	// Status reports running state, version and uptime.
	Status() core.EngineStatus

	// QueryStats returns per-client traffic keyed by client email.
	QueryStats(reset bool) (map[string]core.Traffic, error)
}

// engineForType maps a protocol type to the engine that should render it.
// The map below is the authoritative protocol-to-engine matrix.
var engineForType = map[string]string{
	"vless":       core.EngineSingbox, // VLESS-Reality-Vision
	"hysteria2":   core.EngineSingbox,
	"trojan":      core.EngineSingbox,
	"shadowsocks": core.EngineSingbox,
	"tuic":        core.EngineSingbox,
	"vmess":       core.EngineSingbox, // VMess-WS
	"vless-ws":    core.EngineSingbox,
	"socks":       core.EngineSingbox,
	"anytls":      core.EngineSingbox, // v1.12+ native; xray-core mainline doesn't have it
	"vless-xhttp": core.EngineXray,    // XHTTP / XHTTP-Reality / XHTTP-ENC
}

// EngineForType returns the engine responsible for a protocol type. Unknown
// types default to sing-box.
func EngineForType(protoType string) string {
	if e, ok := engineForType[protoType]; ok {
		return e
	}
	return core.EngineSingbox
}
