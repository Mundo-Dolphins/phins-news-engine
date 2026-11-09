package dictionary

import (
	"bufio"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarywiring"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newRollbackCommand builds `dictionary rollback`, which restores a
// previous version's content as a *new* version (see
// dictionarywiring.Rollback). It shows the diff from a dry run first and
// (unless --force/--non-interactive) asks for confirmation before the real
// run, which recomputes and verifies everything again.
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
			opts := dictionarywiring.RollbackOptions{To: to, Previous: previous}
			if err := opts.Validate(); err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
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

			opts.DryRun = true
			preview, err := dictionarywiring.Rollback(cmd.Context(), db, cfg, opts, time.Now())
			if err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}
			fmt.Fprint(cmd.OutOrStdout(), preview.DiffText)

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

			opts.DryRun = false
			res, err := dictionarywiring.Rollback(cmd.Context(), db, cfg, opts, time.Now())
			if err != nil {
				return fmt.Errorf("dictionary rollback: %w", err)
			}
			if res.Regression != nil {
				PrintRegressionReport(cmd.OutOrStdout(), *res.Regression)
			} else if res.RegressionErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: dictionary rollback: regression check skipped: %v\n", res.RegressionErr)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dictionary rolled back: %s -> %s (content restored from v%s)\n", res.From, res.Written, res.RestoredFrom)
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
