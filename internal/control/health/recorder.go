// Package health captures periodic node health snapshots into the DB so the
// dashboard can show recent trends. The scheduler is intentionally minimal:
// one ticker, one node (Lite mode), best-effort.
package health

import (
	"context"
	"sync"
	"time"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
	"github.com/aipo-lenshow/EdgeNest/internal/control/store"
	"github.com/aipo-lenshow/EdgeNest/internal/core"
	"github.com/aipo-lenshow/EdgeNest/internal/core/nodeapi"
)

// Recorder samples the node and persists.
type Recorder struct {
	Store *store.Store
	Node  nodeapi.NodeClient
	// NodeID is the local node's model.Node.ID as a string (NodeClient API
	// uses string ids since v2 will route over the network).
	NodeID string
	// NumericNodeID is the same value as uint, used to set HealthSnapshot.NodeID.
	NumericNodeID uint
	// Retain keeps the most recent N samples; older ones get pruned. 0 = keep all.
	Retain int
}

// SampleOnce queries the node, persists a row, and prunes old rows beyond
// Retain. Returns the saved snapshot for the caller (Heartbeat handler reuses
// this to avoid double-querying).
func (r *Recorder) SampleOnce(ctx context.Context) (core.HealthSnapshot, error) {
	snap, err := r.Node.Heartbeat(ctx, r.NodeID)
	if err != nil {
		return core.HealthSnapshot{}, err
	}
	row := &model.HealthSnapshot{
		NodeID:         r.NumericNodeID,
		CPU:            snap.CPU,
		Mem:            snap.Mem,
		Disk:           snap.Disk,
		PublicIP:       snap.PublicIP,
		Country:        snap.Country,
		SingboxRunning: snap.SingboxRunning,
		BBR:            snap.BBR,
		Errors:         snap.Errors,
		CreatedAt:      time.Now().Unix(),
	}
	if err := r.Store.DB().Create(row).Error; err != nil {
		return snap, err
	}
	if r.Retain > 0 {
		r.pruneOldest()
	}
	return snap, nil
}

func (r *Recorder) pruneOldest() {
	// Find the cutoff id (the (Retain+1)th most recent sample) and delete
	// everything older. Two queries beat loading all rows.
	var cutoff model.HealthSnapshot
	err := r.Store.DB().
		Where("node_id = ?", r.NumericNodeID).
		Order("id desc").
		Offset(r.Retain).
		Limit(1).
		First(&cutoff).Error
	if err != nil {
		return // fewer than Retain rows, nothing to prune
	}
	_ = r.Store.DB().
		Where("node_id = ? AND id <= ?", r.NumericNodeID, cutoff.ID).
		Delete(&model.HealthSnapshot{}).Error
}

// Scheduler runs SampleOnce on a ticker. Start launches a goroutine that
// runs immediately, then every Interval. Stop is idempotent.
type Scheduler struct {
	Recorder *Recorder
	Interval time.Duration
	stop     chan struct{}
	once     sync.Once
}

// NewScheduler builds a scheduler; defaultInterval falls back to 5m.
func NewScheduler(rec *Recorder, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Scheduler{Recorder: rec, Interval: interval, stop: make(chan struct{})}
}

// Start kicks off the sampler in a goroutine. Errors are swallowed (sampling
// is best-effort observability — we don't want a transient DB hiccup to
// crash the whole panel).
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		_, _ = s.Recorder.SampleOnce(ctx)
		t := time.NewTicker(s.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-t.C:
				_, _ = s.Recorder.SampleOnce(ctx)
			}
		}
	}()
}

// Stop signals the goroutine to exit. Safe to call multiple times.
func (s *Scheduler) Stop() {
	s.once.Do(func() { close(s.stop) })
}
