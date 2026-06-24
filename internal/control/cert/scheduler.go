package cert

import (
	"context"
	"sync"
	"time"
)

// Scheduler wakes daily and runs Manager.CheckAndRenew. Intended to run in
// the background for the lifetime of the panel process.
type Scheduler struct {
	m        *Manager
	interval time.Duration
	stop     chan struct{}
	stopOnce sync.Once
}

// NewScheduler constructs one. interval defaults to 24h if zero.
func NewScheduler(m *Manager, interval time.Duration) *Scheduler {
	if interval == 0 {
		interval = 24 * time.Hour
	}
	return &Scheduler{m: m, interval: interval, stop: make(chan struct{})}
}

// Start launches the loop in its own goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	go s.run(ctx)
}

// Stop halts the loop (best-effort; current iteration finishes).
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *Scheduler) run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	// Run one pass immediately on startup so a panel restart catches near-
	// expiry certs without waiting a full day.
	_ = s.m.CheckAndRenew(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
			_ = s.m.CheckAndRenew(ctx)
		}
	}
}
