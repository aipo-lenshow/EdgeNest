package store

import (
	"fmt"
	"os"
	"time"
)

// PendingRestoreSuffix is appended to the DB path to stage an uploaded backup
// that should replace the live database on the next startup. The restore is
// applied at boot (before the DB is opened) rather than hot-swapped, because
// the running process holds the SQLite file open — renaming over it would leave
// the old inode live until restart anyway. Staging + boot-time swap is the
// clean, race-free path.
const PendingRestoreSuffix = ".restore-pending"

// ApplyPendingRestore swaps a staged backup into place if one exists. Called
// from main BEFORE store.Open. The current DB is preserved as
// <dbPath>.pre-restore-<unix> so a bad restore is recoverable. Returns whether
// a restore was applied.
func ApplyPendingRestore(dbPath string) (bool, error) {
	pending := dbPath + PendingRestoreSuffix
	if _, err := os.Stat(pending); err != nil {
		return false, nil // nothing staged
	}
	// Back up the current DB (if any) before overwriting it.
	if _, err := os.Stat(dbPath); err == nil {
		backup := fmt.Sprintf("%s.pre-restore-%d", dbPath, time.Now().Unix())
		if err := os.Rename(dbPath, backup); err != nil {
			return false, fmt.Errorf("back up current db: %w", err)
		}
	}
	// Move the staged file into place. Also clear any stale SQLite WAL/SHM
	// sidecars from the previous DB so they can't corrupt the restored one.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	if err := os.Rename(pending, dbPath); err != nil {
		return false, fmt.Errorf("apply staged restore: %w", err)
	}
	return true, nil
}
