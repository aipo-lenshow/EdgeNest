package quota

import (
	"context"
	"fmt"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
)

// ApplyTrigger is the function the enforcer calls after disabling a batch of
// clients so the engine actually drops their existing connections. The
// orchestrator's Apply method satisfies this shape — we use a function type
// instead of importing the orchestrator package to keep this layer testable
// without DB-backed orchestrator setup.
type ApplyTrigger func(ctx context.Context, nodeID uint) error

// AuditSink records one disable action. Optional; nil disables audit writes.
type AuditSink func(action string, resource string, meta map[string]string)

// Enforcer wires the pure Evaluate decisions to the DB writes + apply call.
type Enforcer struct {
	Store   *store.Store
	Apply   ApplyTrigger
	Audit   AuditSink
	NowFunc func() time.Time // injectable for tests; defaults to time.Now
}

// EnforceResult is what the API returns to the caller.
type EnforceResult struct {
	Disabled []DisabledClient `json:"disabled"`
	NodeIDs  []uint           `json:"node_ids_applied"`
}

// DisabledClient describes one client we just disabled.
type DisabledClient struct {
	ClientID uint   `json:"client_id"`
	Email    string `json:"email"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

// EnforceAll walks every client on the local node, applies Evaluate, then for
// each flagged client: sets Enabled=false, writes an audit row (if Audit !=
// nil) and accumulates the node ids that need Apply. After the loop, Apply is
// called once per affected node so we don't re-render the engine config N
// times during a big sweep.
func (e *Enforcer) EnforceAll(ctx context.Context, nodeID uint) (EnforceResult, error) {
	inbounds, err := e.Store.ListInbounds(nodeID)
	if err != nil {
		return EnforceResult{}, fmt.Errorf("list inbounds: %w", err)
	}
	var clients []model.Client
	for _, ib := range inbounds {
		clients = append(clients, ib.Clients...)
	}

	now := time.Now()
	if e.NowFunc != nil {
		now = e.NowFunc()
	}
	decisions := EvaluateByUser(clients, now)
	if len(decisions) == 0 {
		return EnforceResult{}, nil
	}

	res := EnforceResult{}
	for _, d := range decisions {
		if err := e.Store.SetClientEnabled(d.ClientID, false); err != nil {
			return res, fmt.Errorf("disable client %d: %w", d.ClientID, err)
		}
		if e.Audit != nil {
			e.Audit("client.auto_disable", fmt.Sprintf("client/%d", d.ClientID),
				map[string]string{
					"email":  d.Email,
					"reason": string(d.Reason),
					"detail": d.Detail,
				})
		}
		res.Disabled = append(res.Disabled, DisabledClient{
			ClientID: d.ClientID, Email: d.Email,
			Reason: string(d.Reason), Detail: d.Detail,
		})
	}
	res.NodeIDs = []uint{nodeID}
	if e.Apply != nil {
		if err := e.Apply(ctx, nodeID); err != nil {
			return res, fmt.Errorf("apply after enforcement: %w", err)
		}
	}
	return res, nil
}
