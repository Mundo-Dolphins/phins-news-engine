package dictionarywiring

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/app/regressioncheck"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysession"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ErrRollbackChecksum reports that a version's stored dictionary artifact no
// longer matches the content hash its ledger row recorded.
var ErrRollbackChecksum = errors.New("does not match its recorded checksum")

// ErrNoPreviousVersion reports that RollbackOptions.Previous found no recorded
// version other than the one currently on disk.
var ErrNoPreviousVersion = errors.New("no previous version found")

// RollbackOptions selects the version Rollback restores: exactly one of To (a
// recorded version) or Previous (the most recent recorded version other than
// the current one). DryRun computes the diff and writes nothing.
type RollbackOptions struct {
	To       string
	Previous bool
	DryRun   bool
}

// Validate reports the argument errors Rollback rejects before touching any
// file or table.
func (o RollbackOptions) Validate() error {
	if o.To == "" && !o.Previous {
		return fmt.Errorf("--to <version> or --previous is required")
	}
	if o.To != "" && o.Previous {
		return fmt.Errorf("--to and --previous are mutually exclusive")
	}
	return nil
}

// RollbackResult describes a rollback. From is the version on disk before it,
// RestoredFrom the version whose content is restored, and Written the new
// version (empty on a dry run). Regression holds the report-only regression
// check run after the write, or RegressionErr why it was skipped; both are
// nil on a dry run.
type RollbackResult struct {
	From          string
	Written       string
	RestoredFrom  string
	Proposals     []dictionaryupdate.Proposal
	DiffText      string
	Regression    *regressioncheck.Report
	RegressionErr error
	DryRun        bool
}

// Rollback restores a recorded version's dictionary content as a new version
// at cfg.Dictionary.Path. It never rewinds: rolling back v9 to v6's content
// produces v10 whose content matches v6, leaving every later version's ledger
// row and artifacts untouched. It refuses (ErrRollbackChecksum) when the
// stored artifact no longer matches its recorded checksum. Unless DryRun, it
// backs up the outgoing version, writes the restored content, runs a
// report-only regression check, then records and promotes the new version.
// An unknown To is a wrapped sql.ErrNoRows.
func Rollback(ctx context.Context, db *sql.DB, cfg *config.Config, opts RollbackOptions, now time.Time) (RollbackResult, error) {
	if err := opts.Validate(); err != nil {
		return RollbackResult{}, err
	}
	repo := sqliteadapter.NewDictionaryHistoryRepository(db)

	current, err := dictionaryfile.Load(cfg.Dictionary.Path)
	if err != nil {
		return RollbackResult{}, err
	}

	targetVersion := opts.To
	if opts.Previous {
		list, err := repo.List(ctx)
		if err != nil {
			return RollbackResult{}, err
		}
		// List is newest-first; the most recent version that isn't the one
		// currently on disk is "the previous version".
		for _, r := range list {
			if r.Version != current.Version {
				targetVersion = r.Version
				break
			}
		}
		if targetVersion == "" {
			return RollbackResult{}, ErrNoPreviousVersion
		}
	}

	targetRec, err := repo.Get(ctx, targetVersion)
	if err != nil {
		return RollbackResult{}, err
	}
	artifacts, err := dictionaryhistory.ReadVersion(dictionaryhistory.DefaultDir, targetVersion)
	if err != nil {
		return RollbackResult{}, err
	}
	// Refuse to restore content that has drifted from what the ledger
	// recorded for this version: a mismatch means the on-disk artifact was
	// altered or corrupted since it was written.
	if dictionary.ContentHash(artifacts.Dictionary) != targetRec.ContentHash {
		return RollbackResult{}, fmt.Errorf("stored content for version %s %w — refusing to restore", targetVersion, ErrRollbackChecksum)
	}

	proposals := dictionaryupdate.Compare(current, SourceEntitiesFromDictionary(artifacts.Dictionary), dictionaryupdate.AbsenceLog{}, dictionaryupdate.DeactivationPolicy{MinimumSuccessfulAbsences: 1}, now)
	var diffBuf bytes.Buffer
	_ = RenderDiffText(&diffBuf, proposals)
	res := RollbackResult{
		From: current.Version, RestoredFrom: targetVersion,
		Proposals: proposals, DiffText: diffBuf.String(), DryRun: opts.DryRun,
	}
	if opts.DryRun {
		return res, nil
	}

	changeCount := 0
	for _, p := range proposals {
		if p.Kind != dictionaryupdate.ProposalNoChange {
			changeCount++
		}
	}

	written, err := dictionaryupdate.WriteCandidate(current, artifacts.Dictionary, changeCount, nil, "rollback:"+targetVersion, now.UTC(),
		func(prev dictionary.Dictionary) error {
			return BackupPreviousDictionary(ctx, db, prev, res.DiffText, nil, now)
		},
		func(d dictionary.Dictionary) error { return dictionaryfile.Save(cfg.Dictionary.Path, d) },
	)
	if err != nil {
		return RollbackResult{}, err
	}
	res.Written = written.Version

	// Regression-check is report-only here — the write has already happened
	// by this point, so this never blocks a change.
	if report, rcErr := regressioncheck.Run(current, written); rcErr == nil {
		res.Regression = &report
	} else {
		res.RegressionErr = rcErr
	}

	// Record the newly written version in the same append-only ledger as the
	// outgoing one (backed up above), then promote it — mirroring the
	// post-WriteCandidate block in `dictionary update`, but tagged as a
	// rollback operation.
	if err := repo.Record(ctx, out.DictionaryHistoryRecord{
		Version: written.Version, ContentHash: dictionary.ContentHash(written), CreatedAt: now.UTC(),
		ChangeCount: changeCount, OperationKind: "rollback", PredecessorVersion: current.Version,
	}); err != nil {
		return RollbackResult{}, err
	}
	if err := repo.SetPromoted(ctx, written.Version); err != nil {
		return RollbackResult{}, err
	}
	return res, nil
}

// BackupPreviousDictionary preserves the outgoing dictionary version before
// it is overwritten: it writes the full artifact set (dictionary, diff,
// decision log, validation result) for previous under
// dictionaryhistory.DefaultDir via dictionaryhistory.WriteVersion, then
// records the version in the append-only dictionary_history SQLite ledger,
// stamped with now. It does not mark previous as promoted — that status moves
// to the newly written version once the write that follows this backup
// succeeds.
func BackupPreviousDictionary(ctx context.Context, db *sql.DB, previous dictionary.Dictionary, diff string, decisions []dictionarysession.Decision, now time.Time) error {
	validation := dictionary.Validate(previous)
	if err := dictionaryhistory.WriteVersion(dictionaryhistory.DefaultDir, dictionaryhistory.Artifacts{
		Version: previous.Version, Dictionary: previous, Diff: diff, Decisions: decisions, Validation: validation,
	}); err != nil {
		return err
	}
	return sqliteadapter.NewDictionaryHistoryRepository(db).Record(ctx, out.DictionaryHistoryRecord{
		Version: previous.Version, ContentHash: dictionary.ContentHash(previous), CreatedAt: now.UTC(),
		OperationKind: "update",
	})
}
