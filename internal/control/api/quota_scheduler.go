package api

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/orchestrator"
	"github.com/aipo-lenshow/EdgeNest/internal/control/quota"
	"github.com/aipo-lenshow/EdgeNest/internal/control/trafficpoller"
	"github.com/aipo-lenshow/EdgeNest/internal/control/v2raystats"
)

const (
	trafficPollInterval = 10 * time.Second
	quotaEnforceInterval = 60 * time.Second
)

// StartQuotaEnforcement launches the two background loops that make per-user
// quotas and expiry actually take effect:
//
//   1. a traffic poller that samples sing-box's v2ray_api StatsService and
//      accumulates each user's (email's) cumulative bytes into the store, and
//   2. an enforcement loop that re-evaluates every user against its quota /
//      expiry and disables those over the line (then re-applies so live
//      connections drop).
//
// Both are best-effort and stop when ctx is cancelled. The manual
// POST /quota/enforce endpoint runs the same enforcement code on demand (the
// multi-user tab's "run check now" button), so an operator never has to wait
// for the next tick when testing.
func (h *Handler) StartQuotaEnforcement(ctx context.Context) {
	nodeID := h.parseLocalNodeID()

	// trigger coalesces enforcement requests from two producers: the periodic
	// ticker (a backstop that also catches expiry, which has no traffic to poll)
	// and the traffic poller (fires the instant a tick credits bytes). Buffered
	// size 1 + non-blocking send means overlapping requests collapse into a
	// single run, and a single consumer goroutine means the ticker and the
	// poller never run EnforceAll (and its config re-Apply) concurrently.
	trigger := make(chan struct{}, 1)
	signal := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	// Run once on startup so a panel restart re-checks users who are already
	// over quota/expired without waiting for the first tick. The buffered slot
	// holds this until the runner goroutine starts.
	signal()

	// Traffic poller: dials each engine's v2ray-style StatsService (loopback
	// gRPC, no secret) at the addresses the orchestrator renders. per-user
	// counters live there; clash_api can't attribute bytes to a user. Both
	// sources are polled and merged — a user can have inbounds on either engine,
	// and either engine may be absent (its source just errors and is skipped).
	// OnCredited makes enforcement event-driven: the moment a poll tick records
	// new bytes, we re-evaluate quotas, so a user that just crossed their cap is
	// disabled within one poll interval rather than up to a full enforce period
	// later (the gap that let a 10M cap overshoot to 28M).
	sources := []trafficpoller.Source{
		{Name: "singbox", Client: v2raystats.New(orchestrator.V2RayController)},
		{Name: "xray", Client: v2raystats.New(orchestrator.XRayController)},
	}
	poller := trafficpoller.New(sources, h.store, trafficPollInterval).
		OnCredited(func(context.Context) { signal() })
	go poller.Run(ctx)

	// Enforcement runner: one goroutine drains the trigger, fed by the periodic
	// ticker and the poller callback.
	go func() {
		t := time.NewTicker(quotaEnforceInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				signal()
			case <-trigger:
				enf := h.newEnforcer()
				res, err := enf.EnforceAll(ctx, nodeID)
				if err != nil {
					log.Printf("quota: enforce: %v", err)
					continue
				}
				if len(res.Disabled) > 0 {
					log.Printf("quota: auto-disabled %d user-client(s)", len(res.Disabled))
				}
			}
		}
	}()
}

// newEnforcer builds an Enforcer wired to the orchestrator (for re-apply) and a
// context-free "system" audit writer. Shared by the background loop and the
// manual endpoint so both behave identically.
func (h *Handler) newEnforcer() *quota.Enforcer {
	return &quota.Enforcer{
		Store: h.store,
		Apply: func(c context.Context, n uint) error {
			if h.orch == nil {
				return nil
			}
			res, err := h.orch.Apply(c, n)
			if err != nil {
				return err
			}
			if !res.OK {
				return errFromApply(res)
			}
			return nil
		},
		Audit: func(action, resource string, meta map[string]string) {
			metaJSON := ""
			if b, err := json.Marshal(meta); err == nil {
				metaJSON = string(b)
			}
			row := &model.AuditLog{
				Actor:     "system",
				Action:    action,
				Resource:  resource,
				Meta:      metaJSON,
				CreatedAt: time.Now().Unix(),
			}
			_ = h.store.DB().Create(row).Error
		},
	}
}
