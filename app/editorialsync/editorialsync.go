// Package editorialsync orchestrates editorial cycle's offline sync step:
// download-and-validate (via out.RemoteSync), schema compatibility,
// atomic promotion with a recoverable backup, opening a fresh connection,
// migrating the snapshot, and merging the previous local database's
// editorial-only tables forward -- stopping at the first failure and
// restoring the backup if anything fails after a successful promotion.
package editorialsync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Deps are the ports Run writes through.
type Deps struct {
	Sync out.RemoteSync
}

// Request configures one Run call.
type Request struct {
	Host, RemoteDBHint, LocalDBPath string
	RetentionDays                   int
	BusyTimeout                     time.Duration
	MigrationsFS                    embed.FS
}

// Result is the outcome of one Run call, assembled incrementally as each
// step completes. SourceHost, SourcePath, and SnapshotCreatedAt are the
// snapshot's provenance as reported by the transport (out.RemoteSync) —
// carried through unchanged so a caller can surface them to an operator,
// since nothing else records this once the sync script's old
// `.sync-metadata` sidecar was retired.
type Result struct {
	IncomingChecksumSHA256 string
	IncomingSizeBytes      int64
	SourceHost             string
	SourcePath             string
	SnapshotCreatedAt      time.Time
	Promoted               bool
	PreviousBackedUp       bool
	Merged                 bool
	FKWarnings             []string
	Migrated               bool
}

// Run downloads a validated snapshot (deps.Sync), verifies its checksum
// against what the transport reported (defense in depth beyond the
// script's own check), checks it is on a schema this binary understands,
// promotes it atomically into req.LocalDBPath (backing up whatever was
// there before into a single recoverable slot), opens a fresh connection
// against the promoted file, migrates it, and carries the previous local
// database's editorial-only tables forward into it — in that
// order, stopping at the first failure. On any failure after a
// successful promote, it restores req.LocalDBPath from the backup before
// returning a nil *sql.DB, so the local database is always left either
// fully advanced or fully rolled back, never partially merged or
// partially migrated.
func Run(ctx context.Context, deps Deps, req Request) (*sql.DB, Result, error) {
	var result Result

	syncResult, err := deps.Sync.Sync(ctx, out.RemoteSyncRequest{
		Host: req.Host, RemoteDBHint: req.RemoteDBHint, LocalDBPath: req.LocalDBPath, RetentionDays: req.RetentionDays,
	})
	if err != nil {
		return nil, result, fmt.Errorf("editorialsync: sync: %w", err)
	}
	result.IncomingChecksumSHA256 = syncResult.ChecksumSHA256
	result.IncomingSizeBytes = syncResult.SizeBytes
	result.SourceHost = syncResult.SourceHost
	result.SourcePath = syncResult.SourcePath
	result.SnapshotCreatedAt = syncResult.SnapshotCreatedAt

	// Guard against sql.Open's SQLite driver lazily creating an empty
	// database file at IncomingPath on first use if a transport bug ever
	// reports a bogus/nonexistent path — CheckSchemaCompatible below would
	// otherwise pass trivially against a freshly-created, empty database.
	if _, err := os.Stat(syncResult.IncomingPath); err != nil {
		return nil, result, fmt.Errorf("editorialsync: incoming snapshot: %w", err)
	}

	if syncResult.ChecksumSHA256 != "" {
		if err := verifyChecksum(syncResult.IncomingPath, syncResult.ChecksumSHA256); err != nil {
			return nil, result, fmt.Errorf("editorialsync: %w", err)
		}
	}

	if err := infrasqlite.CheckSchemaCompatible(ctx, syncResult.IncomingPath, req.MigrationsFS); err != nil {
		return nil, result, fmt.Errorf("editorialsync: %w", err)
	}

	backupPath, backedUp, err := infrasqlite.Promote(req.LocalDBPath, syncResult.IncomingPath)
	if err != nil {
		return nil, result, rollbackAfter(req.LocalDBPath, backupPath, backedUp, fmt.Errorf("editorialsync: promote: %w", err))
	}
	result.Promoted = true
	result.PreviousBackedUp = backedUp

	db, err := infrasqlite.Open(req.LocalDBPath, req.BusyTimeout)
	if err != nil {
		return nil, result, rollbackAfter(req.LocalDBPath, backupPath, backedUp, fmt.Errorf("editorialsync: open promoted database: %w", err))
	}

	// The remote snapshot can lag behind the local schema. Upgrade it before
	// copying local rows, which may already contain newly added columns.
	if err := infrasqlite.Migrate(ctx, db, req.MigrationsFS); err != nil {
		_ = db.Close()
		return nil, result, rollbackAfter(req.LocalDBPath, backupPath, backedUp, fmt.Errorf("editorialsync: migrate: %w", err))
	}
	result.Migrated = true

	if backedUp {
		warnings, err := infrasqlite.MergeLocalOwnedTables(ctx, db, backupPath)
		if err != nil {
			_ = db.Close()
			return nil, result, rollbackAfter(req.LocalDBPath, backupPath, backedUp, fmt.Errorf("editorialsync: merge: %w", err))
		}
		result.Merged = true
		result.FKWarnings = warnings
	}

	return db, result, nil
}

// verifyChecksum re-hashes the file at path and compares it to
// wantSHA256Hex, defense in depth beyond the sync script's own
// checksum check — relevant chiefly if a RemoteSync implementation other
// than the real script (e.g. a future transport, or a misbehaving fake)
// ever reports a result that disagrees with the bytes actually on disk.
func verifyChecksum(path, wantSHA256Hex string) error {
	f, err := os.Open(path) // #nosec G304 -- path is syncResult.IncomingPath, the just-downloaded snapshot's own temp path reported by the sync transport, not user input
	if err != nil {
		return fmt.Errorf("verify checksum: open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("verify checksum: hash %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantSHA256Hex {
		return fmt.Errorf("verify checksum: %s: got %s, want %s — discarding snapshot", path, got, wantSHA256Hex)
	}
	return nil
}

// rollbackAfter restores localDBPath from backupPath (if a backup was
// actually made) and wraps the restore outcome into cause — the promote
// step's failure or a downstream step's failure — so a rollback failure
// is never silently swallowed.
func rollbackAfter(localDBPath, backupPath string, backedUp bool, cause error) error {
	if !backedUp {
		return cause
	}
	if err := infrasqlite.Rollback(localDBPath, backupPath); err != nil {
		return fmt.Errorf("%w (additionally, rollback to %s failed: %v)", cause, backupPath, err)
	}
	return fmt.Errorf("%w (rolled back to the previous local database)", cause)
}
