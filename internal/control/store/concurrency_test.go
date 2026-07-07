package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aipo-lenshow/EdgeNest/internal/control/model"
)

// TestConcurrentWriters hammers the store with the same write mix the live
// process produces (traffic credits, audit rows, setting upserts) from many
// goroutines at once. The process has 7+ background writers (trafficpoller,
// quota scheduler, audit log on every mutating request, alert/notify runners,
// update check), so concurrent writes are the normal case, not a stress
// scenario. Any "database is locked" here is a real-deployment failure: those
// writers swallow errors, so a lock means silently lost traffic accounting or
// audit rows.
func TestConcurrentWriters(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// The DSN pragmas must actually take effect — if the _pragma syntax ever
	// silently stops working, the lock errors below come back.
	var journalMode string
	if err := st.DB().Raw("PRAGMA journal_mode").Scan(&journalMode).Error; err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal (DSN _pragma not applied?)", journalMode)
	}
	var busyTimeout int
	if err := st.DB().Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout < 1000 {
		t.Fatalf("busy_timeout = %d, want >= 1000 (DSN _pragma not applied?)", busyTimeout)
	}

	const workers = 16
	const iters = 200

	errCh := make(chan error, workers*iters*3)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			email := fmt.Sprintf("user%d", w)
			for i := 0; i < iters; i++ {
				if err := st.AddUserTraffic(email, 10, 20); err != nil {
					errCh <- fmt.Errorf("AddUserTraffic: %w", err)
				}
				if err := st.DB().Create(&model.AuditLog{
					Actor:    email,
					Action:   "test",
					Resource: fmt.Sprintf("row-%d", i),
				}).Error; err != nil {
					errCh <- fmt.Errorf("audit Create: %w", err)
				}
				if err := st.SetSetting(fmt.Sprintf("k%d", w), fmt.Sprintf("v%d", i)); err != nil {
					errCh <- fmt.Errorf("SetSetting: %w", err)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)

	locked, other := 0, 0
	for err := range errCh {
		if strings.Contains(err.Error(), "database is locked") ||
			strings.Contains(err.Error(), "SQLITE_BUSY") {
			locked++
		} else {
			other++
			t.Errorf("unexpected write error: %v", err)
		}
	}
	if locked > 0 {
		t.Fatalf("%d writes failed with database-is-locked (silently dropped in production)", locked)
	}
}
