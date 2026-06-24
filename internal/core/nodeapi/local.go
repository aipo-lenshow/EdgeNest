package nodeapi

import (
	"context"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/aipo-lenshow/EdgeNest/internal/node"
)

// LocalNodeClient satisfies NodeClient by calling the in-process node directly.
// Used in standalone (Lite) mode. No network, no auth — same process.
type LocalNodeClient struct {
	local *node.LocalNode
}

// NewLocalNodeClient wraps a LocalNode as a NodeClient.
func NewLocalNodeClient(local *node.LocalNode) *LocalNodeClient {
	return &LocalNodeClient{local: local}
}

// compile-time assertion that LocalNodeClient implements NodeClient.
var _ NodeClient = (*LocalNodeClient)(nil)

func (l *LocalNodeClient) Register(ctx context.Context, joinToken string) (string, string, error) {
	// Lite: the local node is auto-provisioned; there is no join handshake.
	return l.local.ID(), "", nil
}

func (l *LocalNodeClient) Heartbeat(ctx context.Context, nodeID string) (core.HealthSnapshot, error) {
	return l.local.Health(), nil
}

func (l *LocalNodeClient) ApplyConfig(ctx context.Context, nodeID string, desired core.DesiredConfig) (core.ApplyResult, error) {
	return l.local.Apply(desired)
}

func (l *LocalNodeClient) QueryStats(ctx context.Context, nodeID string, reset bool) (map[string]core.Traffic, error) {
	return l.local.QueryStats(reset)
}

func (l *LocalNodeClient) RestartEngine(ctx context.Context, nodeID string) error {
	return l.local.Restart()
}

func (l *LocalNodeClient) SelfCheck(ctx context.Context, nodeID string) (core.HealthSnapshot, error) {
	return l.local.Health(), nil
}

func (l *LocalNodeClient) EngineStatus(ctx context.Context, nodeID string) (core.EngineStatus, error) {
	return l.local.Status(), nil
}
