// Package nodeapi defines the contract line (seam) between the control plane
// and a node's execution plane.
//
// ARCHITECTURE DISCIPLINE (must hold for v1, enables v2 as additive work):
//   - The control plane operates on a node ONLY through NodeClient.
//     It must NEVER import or call internal/node/engine or internal/node/system
//     directly.
//   - In standalone (Lite) mode, NodeClient is satisfied by LocalNodeClient,
//     which calls the in-process node implementation — no network, no gRPC.
//   - In Platform (v2) mode, the same interface is satisfied by RemoteNodeClient
//     (gRPC + mTLS to a remote node). The control-plane logic does not change.
package nodeapi

import (
	"context"
	"errors"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// ErrNotImplemented is returned by stubs that are reserved for Platform (v2).
var ErrNotImplemented = errors.New("not implemented (reserved for Platform/v2)")

// NodeClient is the single interface through which the control plane manages a
// node. nodeID identifies which node to act on; in Lite there is exactly one
// node ("local").
type NodeClient interface {
	// Register joins a node to the control plane using a join token.
	// Lite: returns the auto-provisioned local node. Platform: real join flow.
	Register(ctx context.Context, joinToken string) (nodeID, secret string, err error)

	// Heartbeat returns a fresh health snapshot for the node.
	Heartbeat(ctx context.Context, nodeID string) (core.HealthSnapshot, error)

	// ApplyConfig renders, validates (check), backs up, applies, verifies and —
	// on failure — rolls back the node's proxy configuration.
	ApplyConfig(ctx context.Context, nodeID string, desired core.DesiredConfig) (core.ApplyResult, error)

	// QueryStats returns per-client traffic keyed by client email. If reset is
	// true, the engine counters are reset after reading.
	QueryStats(ctx context.Context, nodeID string, reset bool) (map[string]core.Traffic, error)

	// RestartEngine restarts the node's proxy engine(s).
	RestartEngine(ctx context.Context, nodeID string) error

	// SelfCheck runs node-side diagnostics (process alive, ports listening,
	// egress IP, etc.).
	SelfCheck(ctx context.Context, nodeID string) (core.HealthSnapshot, error)

	// EngineStatus reports whether the engine is running, its version and uptime.
	EngineStatus(ctx context.Context, nodeID string) (core.EngineStatus, error)
}
