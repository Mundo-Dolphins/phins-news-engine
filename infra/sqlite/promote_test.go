package sqlite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestPromote_FirstSync_NoExistingTarget_PromotesWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "local.db")
	incoming := filepath.Join(dir, "local.db.incoming.abc123")
	writeFile(t, incoming, "snapshot-B")

	backupPath, backedUp, err := sqlite.Promote(target, incoming)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if backedUp {
		t.Error("backedUp = true, want false (no prior target existed)")
	}
	if got := readFile(t, target); got != "snapshot-B" {
		t.Errorf("target content = %q, want %q", got, "snapshot-B")
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Errorf("backup file exists at %s, want none created", backupPath)
	}
	if _, err := os.Stat(incoming); !os.IsNotExist(err) {
		t.Errorf("incoming file %s still exists, want it renamed away", incoming)
	}
}

func TestPromote_ExistingTarget_BacksItUpThenPromotesIncoming(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "local.db")
	incoming := filepath.Join(dir, "local.db.incoming.abc123")
	writeFile(t, target, "snapshot-A")
	writeFile(t, incoming, "snapshot-B")

	backupPath, backedUp, err := sqlite.Promote(target, incoming)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if !backedUp {
		t.Error("backedUp = false, want true")
	}
	if want := target + ".previous"; backupPath != want {
		t.Errorf("backupPath = %q, want %q", backupPath, want)
	}
	if got := readFile(t, target); got != "snapshot-B" {
		t.Errorf("target content = %q, want %q (the promoted incoming file)", got, "snapshot-B")
	}
	if got := readFile(t, backupPath); got != "snapshot-A" {
		t.Errorf("backup content = %q, want %q (the previous target)", got, "snapshot-A")
	}
}

func TestPromote_MovesWalAndShmSidecarsWithTheBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "local.db")
	incoming := filepath.Join(dir, "local.db.incoming.abc123")
	writeFile(t, target, "snapshot-A")
	writeFile(t, target+"-wal", "wal-A")
	writeFile(t, target+"-shm", "shm-A")
	writeFile(t, incoming, "snapshot-B")

	backupPath, _, err := sqlite.Promote(target, incoming)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}
	if got := readFile(t, backupPath+"-wal"); got != "wal-A" {
		t.Errorf("backup -wal content = %q, want %q", got, "wal-A")
	}
	if got := readFile(t, backupPath+"-shm"); got != "shm-A" {
		t.Errorf("backup -shm content = %q, want %q", got, "shm-A")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(target + suffix); !os.IsNotExist(err) {
			t.Errorf("stale %s left next to the newly promoted target", suffix)
		}
	}
}

func TestPromote_SidecarRenameFailureAfterMainRename_ReturnsAccurateBackupPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "local.db")
	incoming := filepath.Join(dir, "local.db.incoming.abc123")
	backupPath := target + ".previous"
	writeFile(t, target, "snapshot-A")
	writeFile(t, target+"-wal", "wal-A")
	writeFile(t, incoming, "snapshot-B")

	// Create a directory at the backup WAL path to force the sidecar rename to fail
	if err := os.Mkdir(backupPath+"-wal", 0o755); err != nil {
		t.Fatalf("setup: mkdir %s: %v", backupPath+"-wal", err)
	}

	// Promote should fail due to the sidecar issue, but return the correct backupPath
	// and backedUp=true because the main database file was already moved.
	returnedBackupPath, backedUp, err := sqlite.Promote(target, incoming)
	if err == nil {
		t.Error("Promote() error = nil, want non-nil (sidecar rename failed)")
	}
	if !backedUp {
		t.Error("backedUp = false, want true (main file was renamed to backup)")
	}
	if want := backupPath; returnedBackupPath != want {
		t.Errorf("backupPath = %q, want %q", returnedBackupPath, want)
	}
	// Verify the main database file really is at the backup location despite the error
	if got := readFile(t, backupPath); got != "snapshot-A" {
		t.Errorf("backup content = %q, want %q (should be there despite sidecar failure)", got, "snapshot-A")
	}
	// Verify incoming was successfully promoted into the target slot
	if got := readFile(t, target); got != "snapshot-B" {
		t.Errorf("target content = %q, want %q (promoted after backup)", got, "snapshot-B")
	}
}

func TestRollback_RestoresTargetFromBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "local.db")
	backup := target + ".previous"
	writeFile(t, target, "bad-merge-result")
	writeFile(t, backup, "snapshot-A")

	if err := sqlite.Rollback(target, backup); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if got := readFile(t, target); got != "snapshot-A" {
		t.Errorf("target content = %q, want %q (restored)", got, "snapshot-A")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("backup file %s still exists after rollback consumed it", backup)
	}
}

func TestRollback_RemovesStaleSidecarLeftByFailedPromotedAttempt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "local.db")
	backup := target + ".previous"
	writeFile(t, target, "bad-merge-result")
	writeFile(t, target+"-wal", "wal-from-failed-open") // Open() under WAL mode created this
	writeFile(t, backup, "snapshot-A")                  // backup itself has no -wal (it was never opened)

	if err := sqlite.Rollback(target, backup); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if _, err := os.Stat(target + "-wal"); !os.IsNotExist(err) {
		t.Error("stale -wal from the failed attempt was not cleaned up by rollback")
	}
}

func TestRollback_NoBackup_IsANoOp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "local.db")
	writeFile(t, target, "unchanged")

	if err := sqlite.Rollback(target, target+".previous"); err != nil {
		t.Fatalf("Rollback() error = %v, want nil when there is nothing to roll back to", err)
	}
	if got := readFile(t, target); got != "unchanged" {
		t.Errorf("target content = %q, want unchanged", got)
	}
}
