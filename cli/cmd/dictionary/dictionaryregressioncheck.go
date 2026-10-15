package dictionary

import (
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/regressioncheck"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
)

func newRegressionCheckCommand() *cobra.Command {
	var previousPath, candidatePath string
	var flipThreshold int

	cmd := &cobra.Command{
		Use:   "regression-check",
		Short: "Compare classifier outcomes between two dictionary versions on the seed regression dataset",
		RunE: func(cmd *cobra.Command, args []string) error {
			defer func() { previousPath = ""; candidatePath = ""; flipThreshold = 0 }()

			prevPath := previousPath
			if prevPath == "" {
				cfg, err := config.Load(shared.ConfigPath)
				if err != nil {
					return fmt.Errorf("dictionary regression-check: %w", err)
				}
				prevPath = cfg.Dictionary.Path
			}
			if candidatePath == "" {
				return fmt.Errorf("dictionary regression-check: --candidate is required")
			}

			prevDict, err := dictionaryfile.Load(prevPath)
			if err != nil {
				return fmt.Errorf("dictionary regression-check: %w", err)
			}
			candDict, err := dictionaryfile.Load(candidatePath)
			if err != nil {
				return fmt.Errorf("dictionary regression-check: %w", err)
			}

			report, err := regressioncheck.Run(prevDict, candDict)
			if err != nil {
				return fmt.Errorf("dictionary regression-check: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "regression-check: %d/%d cases flipped\n", report.FlipCount, report.TotalCases)
			for _, f := range report.Flips {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s (score %.2f -> %.2f, rule %s -> %s)\n", f.CaseName, f.Direction, f.ScoreBefore, f.ScoreAfter, f.RuleBefore, f.RuleAfter)
			}

			if report.FlipCount > flipThreshold {
				return fmt.Errorf("dictionary regression-check: %d classification flip(s) exceeds threshold %d — review required before promotion", report.FlipCount, flipThreshold)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&previousPath, "previous", "", "previous dictionary file (default: dictionary.path from config)")
	cmd.Flags().StringVar(&candidatePath, "candidate", "", "candidate dictionary file to check")
	cmd.Flags().IntVar(&flipThreshold, "flip-threshold", 0, "maximum allowed classification flips before this command fails")
	return cmd
}
