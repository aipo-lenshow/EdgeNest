package store

import (
	"os"
	"path/filepath"
	"testing"
)

// A staged pending file must replace the live DB and the old DB must be kept.
func TestApplyPendingRestore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "edgenest.db")
	if err := os.WriteFile(dbPath, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath+PendingRestoreSuffix, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}

	applied, err := ApplyPendingRestore(dbPath)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !applied {
		t.Fatal("expected applied=true")
	}
	got, _ := os.ReadFile(dbPath)
	if string(got) != "NEW" {
		t.Fatalf("db not swapped: %q", got)
	}
	if _, err := os.Stat(dbPath + PendingRestoreSuffix); !os.IsNotExist(err) {
		t.Fatal("pending file should be consumed")
	}
	// A pre-restore backup of the old DB must exist.
	entries, _ := filepath.Glob(dbPath + ".pre-restore-*")
	if len(entries) != 1 {
		t.Fatalf("want 1 pre-restore backup, got %d", len(entries))
	}
	if b, _ := os.ReadFile(entries[0]); string(b) != "OLD" {
		t.Fatalf("backup content wrong: %q", b)
	}
}

// No pending file → no-op.
func TestApplyPendingRestore_NoPending(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "edgenest.db")
	_ = os.WriteFile(dbPath, []byte("OLD"), 0o644)
	applied, err := ApplyPendingRestore(dbPath)
	if err != nil || applied {
		t.Fatalf("expected no-op, got applied=%v err=%v", applied, err)
	}
}
