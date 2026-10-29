package cdnstats

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"
)

// DefaultRetentionDays bounds how long pooled results are kept.
const DefaultRetentionDays = 30

// RetentionDays reads EDGENEST_CDN_STATS_DAYS; <=0 turns pruning off.
func RetentionDays() int {
	if v := os.Getenv("EDGENEST_CDN_STATS_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return DefaultRetentionDays
}

type pruneStore interface {
	PruneCDNStatsBefore(day string) (int64, error)
}

// Pruner deletes pooled results past the retention window: once at start,
// then daily. A failed prune is logged and retried the next day.
type Pruner struct {
	st   pruneStore
	days int
	now  func() time.Time
}

// NewPruner returns a pruner keeping days of results.
func NewPruner(st pruneStore, days int) *Pruner {
	return &Pruner{st: st, days: days, now: time.Now}
}

// PruneOnce deletes the expired buckets and reports how many went.
func (p *Pruner) PruneOnce() (int64, error) {
	if p.days <= 0 {
		return 0, nil
	}
	return p.st.PruneCDNStatsBefore(Day(p.now().AddDate(0, 0, -p.days)))
}

// Start prunes now and then every 24 hours until ctx ends.
func (p *Pruner) Start(ctx context.Context) {
	go func() {
		tick := time.NewTicker(24 * time.Hour)
		defer tick.Stop()
		for {
			if n, err := p.PruneOnce(); err != nil {
				log.Printf("cdnstats: prune failed: %v", err)
			} else if n > 0 {
				log.Printf("cdnstats: pruned %d expired rows", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
