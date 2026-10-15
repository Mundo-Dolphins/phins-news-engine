// Package sqlite (this file): atomic promotion of a validated snapshot
// into place, with a single recoverable backup slot.
package sqlite

import (
	"fmt"
	"os"
)

// walSuffixes are the sidecar files SQLite's WAL journal mode may leave
// beside a database file. Promote and Rollback always move or clear them
// together with their main file, so a promoted or restored database is
// never left paired with a stale WAL/SHM from a different generation of
// the file.
var walSuffixes = []string{"-wal", "-shm"}

// Promote backs up the database at targetPath (and any -wal/-shm
// sidecars) to a single fixed backupPath slot — targetPath + ".previous"
// — overwriting whatever was backed up there before, matching this
// project's existing "one last-good slot, not a retained history"
// convention used by the deploy rollback. It then atomically renames
// incomingPath into targetPath. If targetPath does not exist yet (the
// first-ever sync), no backup is made and backedUp is false; callers
// must not attempt a merge or rollback in that case.
func Promote(targetPath, incomingPath string) (backupPath string, backedUp bool, err error) {
	backupPath = targetPath + ".previous"
	var backupErr error

	if _, statErr := os.Stat(targetPath); statErr == nil {
		mainRenamed, err := renameWithSidecars(targetPath, backupPath)
		if err != nil {
			// If the main file was successfully renamed to backup, report that
			// state accurately so the caller can rollback if needed, even if
			// sidecar operations failed. Continue to promote incoming.
			if mainRenamed {
				backupErr = fmt.Errorf("sqlite: promote: back up %s: %w", targetPath, err)
				backedUp = true
			} else {
				return "", false, fmt.Errorf("sqlite: promote: back up %s: %w", targetPath, err)
			}
		} else {
			backedUp = true
		}
	} else if !os.IsNotExist(statErr) {
		return "", false, fmt.Errorf("sqlite: promote: stat %s: %w", targetPath, statErr)
	}

	if err := os.Rename(incomingPath, targetPath); err != nil {
		return backupPath, backedUp, fmt.Errorf("sqlite: promote: rename %s to %s: %w", incomingPath, targetPath, err)
	}

	// If there was an error during backup sidecar handling but the main file
	// was moved and incoming was promoted, return both the accurate backup info
	// and the sidecar error so the caller can be aware of the partial failure.
	if backupErr != nil {
		return backupPath, backedUp, backupErr
	}
	return backupPath, backedUp, nil
}

// Rollback restores targetPath (and its -wal/-shm sidecars) from
// backupPath, undoing a prior Promote — including clearing any -wal/-shm
// a failed attempt left next to targetPath after promotion but before
// the failure. It is a no-op returning nil if backupPath does not exist
// (nothing to roll back to, e.g. Promote itself never ran or backedUp
// was false).
func Rollback(targetPath, backupPath string) error {
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("sqlite: rollback: stat %s: %w", backupPath, err)
	}
	_, err := renameWithSidecars(backupPath, targetPath)
	if err != nil {
		return fmt.Errorf("sqlite: rollback: restore %s: %w", targetPath, err)
	}
	return nil
}

// renameWithSidecars renames from->to, then does the same for any -wal/-shm
// sidecar of from — removing any stale sidecar already sitting at to when
// from has none of its own, so neither Promote nor Rollback ever leaves a
// sidecar from a different generation of the file behind.
// It returns (mainRenamed, err): mainRenamed is true iff the main file rename
// succeeded. If mainRenamed is false, err is the reason the main rename failed
// and no sidecars were touched. If mainRenamed is true, err is non-nil only if
// a sidecar operation failed after the main rename succeeded.
func renameWithSidecars(from, to string) (bool, error) {
	if err := os.Rename(from, to); err != nil {
		return false, err
	}
	for _, suffix := range walSuffixes {
		src, dst := from+suffix, to+suffix
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, dst); err != nil {
				return true, fmt.Errorf("rename sidecar %s: %w", src, err)
			}
			continue
		} else if !os.IsNotExist(err) {
			return true, fmt.Errorf("stat sidecar %s: %w", src, err)
		}
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return true, fmt.Errorf("remove stale sidecar %s: %w", dst, err)
		}
	}
	return true, nil
}
