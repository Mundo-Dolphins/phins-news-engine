package dictionary

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/app/regressioncheck"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// newRollbackCommand builds `dictionary rollback`, which restores a
// previous version's content as a *new* version. It never rewinds: rolling
// back v9 to v6's content produces v10 whose content matches v6, leaving every
// later version's ledger row and file artifacts untouched. Before restoring it
// verifies the target's stored content matches its recorded checksum, shows a
// diff, and (unless --force/--non-interactive) asks for confirmation.
func newRollbackCommand() *cobra.Command {
	var to string
	var previous, dryRun, force, nonInteractive bool

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Restore a previous version's dictionary content as a new version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// newRollbackCommand runs once at init, so these flag vars
			// live for the process lifetime; reset them so a value set by one
			// invocation never leaks into the next (the shared root command is
			// reused across tests and across repeated programmatic runs).
			defer func() { to = ""; previous = false; dryRun = false; force = false; nonInteractive = false }()

			if nonInteractive && to == "" {
				return fmt.Errorf("dictionary rollback: --non-interactive requires an explicit --to")
			}
			if to == "" && !previous {
				return fmt.Errorf("dictionary rollback: --to <version> or --previous is required")
			}
			if to != "" && previous {
				return fmt.Errorf("dictionary rollback: --to and --previous are mutually exclusive")
			}

			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}
			repo := sqliteadapter.NewDictionaryHistoryRepository(db)

			current, err := dictionaryfile.Load(cfg.Dictionary.Path)
			if err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}

			targetVersion := to
			if previous {
				list, err := repo.List(cmd.Context())
				if err != nil {
					return fmt.Errorf("dictionary rollback: %w", err)
				}
				// List is newest-first; the most recent version that isn't the
				// one currently on disk is "the previous version".
				for _, r := range list {
					if r.Version != current.Version {
						targetVersion = r.Version
						break
					}
				}
				if targetVersion == "" {
					return fmt.Errorf("dictionary rollback: no previous version found")
				}
			}

			targetRec, err := repo.Get(cmd.Context(), targetVersion)
			if err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}
			artifacts, err := dictionaryhistory.ReadVersion(dictionaryHistoryDir, targetVersion)
			if err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}
			// Refuse to restore content that has drifted from what the ledger
			// recorded for this version: a mismatch means the on-disk artifact
			// was altered or corrupted since it was written.
			if dictionary.ContentHash(artifacts.Dictionary) != targetRec.ContentHash {
				return fmt.Errorf("dictionary rollback: stored content for version %s does not match its recorded checksum — refusing to restore", targetVersion)
			}

			restoreProposals := dictionaryupdate.Compare(current, SourceEntitiesFromDictionary(artifacts.Dictionary), dictionaryupdate.AbsenceLog{}, dictionaryupdate.DeactivationPolicy{MinimumSuccessfulAbsences: 1}, time.Now())
			var diffBuf bytes.Buffer
			_ = RenderDiffText(&diffBuf, restoreProposals)
			fmt.Fprint(cmd.OutOrStdout(), diffBuf.String())

			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "dry-run: no changes written")
				return nil
			}
			if !nonInteractive && !force {
				fmt.Fprint(cmd.OutOrStdout(), "restore this content as a new version? [y/N] ")
				scanner := bufio.NewScanner(cmd.InOrStdin())
				if !scanner.Scan() || strings.TrimSpace(strings.ToLower(scanner.Text())) != "y" {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted, no changes written")
					return nil
				}
			}

			changeCount := 0
			for _, p := range restoreProposals {
				if p.Kind != dictionaryupdate.ProposalNoChange {
					changeCount++
				}
			}

			written, err := dictionaryupdate.WriteCandidate(current, artifacts.Dictionary, changeCount, nil, "rollback:"+targetVersion, time.Now().UTC(),
				func(prev dictionary.Dictionary) error {
					return BackupPreviousDictionary(cmd.Context(), db, prev, diffBuf.String(), nil)
				},
				func(d dictionary.Dictionary) error { return dictionaryfile.Save(cfg.Dictionary.Path, d) },
			)
			if err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}

			// Regression-check is report-only here — the write has already
			// happened by this point, so this never blocks a change; it
			// flags anything over threshold as requiring review.
			if report, rcErr := regressioncheck.Run(current, written); rcErr == nil {
				PrintRegressionReport(cmd.OutOrStdout(), report)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: dictionary rollback: regression check skipped: %v\n", rcErr)
			}

			// Record the newly written version in the same append-only ledger
			// as the outgoing one (backed up above), then promote it — mirroring
			// the post-WriteCandidate block in `dictionary update`, but tagged as
			// a rollback operation.
			if err := repo.Record(cmd.Context(), out.DictionaryHistoryRecord{
				Version: written.Version, ContentHash: dictionary.ContentHash(written), CreatedAt: time.Now().UTC(),
				ChangeCount: changeCount, OperationKind: "rollback", PredecessorVersion: current.Version,
			}); err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}
			if err := repo.SetPromoted(cmd.Context(), written.Version); err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "dictionary rolled back: %s -> %s (content restored from v%s)\n", current.Version, written.Version, targetVersion)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "version to restore content from")
	cmd.Flags().BoolVar(&previous, "previous", false, "restore the most recent version other than the current one")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the diff and validation without writing")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt (checks still run)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "no prompts; requires --to and all checks passing")
	return cmd
}
