package nodeapi

import (
	"context"

	"github.com/aipo-lenshow/EdgeNest/internal/core"
)

// RemoteNodeClient will satisfy NodeClient by talking to a remote node agent
// over gRPC + mTLS. It is reserved for Platform (v2); all methods return
// ErrNotImplemented in v1.
//
// When v2 lands, only this file (plus the node gRPC server and the join flow)
// needs real implementation — the control-plane logic that depends on
// NodeClient stays unchanged.
type RemoteNodeClient struct {
	endpoint string // e.g. "node.example.com:8443"
	// TODO(v2): grpc.ClientConn, mTLS credentials, node secret, etc.
}

// NewRemoteNodeClient constructs a remote client targeting the given endpoint.
func NewRemoteNodeClient(endpoint string) *RemoteNodeClient {
	return &RemoteNodeClient{endpoint: endpoint}
}

// compile-time assertion that RemoteNodeClient implements NodeClient.
var _ NodeClient = (*RemoteNodeClient)(nil)

func (r *RemoteNodeClient) Register(ctx context.Context, joinToken string) (string, string, error) {
	return "", "", ErrNotImplemented
}

func (r *RemoteNodeClient) Heartbeat(ctx context.Context, nodeID string) (core.HealthSnapshot, error) {
	return core.HealthSnapshot{}, ErrNotImplemented
}

func (r *RemoteNodeClient) ApplyConfig(ctx context.Context, nodeID string, desired core.DesiredConfig) (core.ApplyResult, error) {
	return core.ApplyResult{}, ErrNotImplemented
}

func (r *RemoteNodeClient) QueryStats(ctx context.Context, nodeID string, reset bool) (map[string]core.Traffic, error) {
	return nil, ErrNotImplemented
}

func (r *RemoteNodeClient) RestartEngine(ctx context.Context, nodeID string) error {
	return ErrNotImplemented
}

func (r *RemoteNodeClient) SelfCheck(ctx context.Context, nodeID string) (core.HealthSnapshot, error) {
	return core.HealthSnapshot{}, ErrNotImplemented
}

func (r *RemoteNodeClient) EngineStatus(ctx context.Context, nodeID string) (core.EngineStatus, error) {
	return core.EngineStatus{}, ErrNotImplemented
}
