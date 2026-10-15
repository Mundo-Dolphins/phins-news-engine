package backtest

import (
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/backtestcandidate"
)

func newCandidateCommand() *cobra.Command {
	var days int
	var dbPath, candidateDictPath, candidateModelPath, outPath string

	cmd := &cobra.Command{
		Use:   "candidate (--candidate-dictionary PATH | --candidate-model PATH) [flags]",
		Short: "Replay v2's own historical traffic through a candidate dictionary/editorial model and report what it would publish or reject",
		Long: `Replays the last --days days of v2's own operational.db articles through
v2's real ProcessCycle pipeline, wired against a candidate dictionary
(--candidate-dictionary) and/or a candidate editorial model
(--candidate-model) instead of the currently-promoted one, against a
scratch SQLite database that is discarded when the command exits. Ground
truth for "was this actually published" is the article's own already-
recorded Status in --db (the real operational database, opened read-only).

At least one of --candidate-dictionary or --candidate-model is required;
pass both to evaluate a coordinated dictionary+model change together.

Known limitation: v2's operational.db does not persist a per-cycle fetch-
batch log the way v1's does, so each historical article is replayed as its
own single-article cycle in chronological order rather than in its
original fetch batches — real dedup/clustering time-window behavior still
applies, but the exact original batch composition isn't reproduced.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dbPath == "" {
				return fmt.Errorf("backtest candidate: --db is required")
			}
			if candidateDictPath == "" && candidateModelPath == "" {
				return fmt.Errorf("backtest candidate: at least one of --candidate-dictionary or --candidate-model is required")
			}
			if days <= 0 {
				return fmt.Errorf("backtest candidate: --days must be positive")
			}

			report, err := backtestcandidate.Run(cmd.Context(), backtestcandidate.Config{
				Days: days, DBPath: dbPath, CandidateDictPath: candidateDictPath, CandidateModelPath: candidateModelPath,
				V2ConfigPath: shared.ConfigPath, OutPath: outPath,
				Progress: func(message string) {
					fmt.Fprintf(cmd.ErrOrStderr(), "backtest candidate progress: %s\n", message)
				},
			})
			if err != nil {
				return fmt.Errorf("backtest candidate: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "backtest candidate: analyzed %d articles over %d days\n", report.TotalArticles, days)
			fmt.Fprintf(cmd.OutOrStdout(), "  regressions (actually published, candidate would reject): %d\n", report.Regressions)
			fmt.Fprintf(cmd.OutOrStdout(), "  new passes (actually rejected, candidate would publish):  %d\n", report.NewPasses)
			fmt.Fprintf(cmd.OutOrStdout(), "  agree (both publish): %d   agree (both reject): %d\n", report.AgreePublish, report.AgreeReject)
			fmt.Fprintf(cmd.OutOrStdout(), "  detail written to %s\n", outPath)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "number of days of v2's own history to replay")
	cmd.Flags().StringVar(&dbPath, "db", "data/operational.db", "path to v2's operational.db (opened read-only)")
	cmd.Flags().StringVar(&candidateDictPath, "candidate-dictionary", "", "candidate dictionary YAML to evaluate in place of the currently-promoted one")
	cmd.Flags().StringVar(&candidateModelPath, "candidate-model", "", "candidate editorial model directory to evaluate in place of the currently-promoted one")
	cmd.Flags().StringVar(&outPath, "out", "data/backtest-candidate-report.csv", "path to write the per-article CSV detail")
	return cmd
}
