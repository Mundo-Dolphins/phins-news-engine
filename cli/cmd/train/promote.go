package train

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newPromoteCommand builds `train promote`, moved from the top-level
// `promote-model` (#253 part 22) — same underlying modelpromotion.Promote,
// now also checking offline_training.promotion criteria alongside
// editorial_gates.
func newPromoteCommand() *cobra.Command {
	var version string
	var force bool
	promoteCmd := &cobra.Command{
		Use:   "promote <candidate-dir>",
		Short: "Atomically promote a candidate editorial-classifier model to production, gated by quality thresholds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			candidateDir := args[0]
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if cfg.Model.Path == "" {
				return fmt.Errorf("train promote: model.path is not configured")
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}

			report, err := trainwiring.PromoteCandidate(cmd.Context(), db, cfg, candidateDir, version, force)
			for _, f := range report.GateFailures {
				cmd.Printf("gate failed: %s\n", f)
			}
			if report.Forced {
				cmd.Println("--force: promoting despite the above gate failure(s)")
			}
			if report.VocabularyOverlap != nil {
				cmd.Printf("vocabulary overlap with current promoted model: %.1f%% (added=%d removed=%d)\n", *report.VocabularyOverlap*100, report.Added, report.Removed)
			}
			if err != nil {
				return err
			}

			cmd.Printf("promoted %s to %s\n", report.Version, cfg.Model.Path)
			return nil
		},
	}
	promoteCmd.Flags().StringVar(&version, "version", "", "version to assign (default: the candidate's own manifest version)")
	promoteCmd.Flags().BoolVar(&force, "force", false, "promote even if the candidate fails one or more quality gates")
	return promoteCmd
}
