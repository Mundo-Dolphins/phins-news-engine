// cli/cmd/editorial/editorialclassifyeval.go
package editorial

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

// newClassifyEvalCommand builds `editorial classify-eval`, which measures
// the LLM teacher prompt against human-labeled dataset examples without
// reading or writing the operational database. Use it to tune the
// publication policy text before a full `editorial classify` run.
func newClassifyEvalCommand() *cobra.Command {
	var (
		datasetPath, fold, policyFile, missesOut string
		sample, batchSize                        int
		seed                                     int64
		threshold                                float64
		includeImported                          bool
		llmBaseURL, llmModel                     string
	)
	cmd := &cobra.Command{
		Use:   "classify-eval",
		Short: "Measure the LLM teacher prompt against human-reviewed dataset labels (offline; writes nothing to the database)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return fmt.Errorf("editorial classify-eval: %w", err)
			}
			rep, err := trainwiring.ClassifyEval(cmd.Context(), cfg, trainwiring.ClassifyEvalFlags{
				DatasetPath: datasetPath, Fold: fold, PolicyFile: policyFile, MissesOut: missesOut,
				BaseURL: llmBaseURL, Model: llmModel, Sample: sample, BatchSize: batchSize,
				Seed: seed, Threshold: threshold, IncludeImported: includeImported,
			})
			out := cmd.OutOrStdout()
			// The report is filled in as far as the run got: Examples once
			// examples are selected, the summary once the evaluation ran.
			if rep.Examples > 0 {
				fmt.Fprintf(out, "evaluating %d examples (fold=%s) with %s\n", rep.Examples, rep.Fold, rep.Model)
			}
			if err != nil && !errors.Is(err, trainwiring.ErrWriteMisses) {
				return fmt.Errorf("editorial classify-eval: %w", err)
			}

			report, s := rep.Report, rep.Summary
			fmt.Fprintf(out, "batches: %d (failed: %d), labels missing: %d, uncertain: %d\n", report.BatchesTotal, report.BatchesFailed, s.Missing, s.Uncertain)
			fmt.Fprintf(out, "human labels: %d reject, %d publish\n", s.RejectTotal, s.PublishTotal)
			fmt.Fprintf(out, "discards (any confidence): %d, %d were human rejects\n", s.DiscardTotal, s.DiscardCorrect)
			fmt.Fprintf(out, "confident discards (>= %.2f): %d, %d were human rejects -> precision %.3f\n", threshold, s.ConfidentDiscardTotal, s.ConfidentDiscardCorrect, s.ConfidentDiscardPrecision())
			fmt.Fprintf(out, "human rejects caught confidently: %d/%d -> recall %.3f\n", s.ConfidentRejectsCaught, s.RejectTotal, s.RejectRecall())
			fmt.Fprintf(out, "human publishes confidently discarded: %d/%d\n", s.PublishDiscarded, s.PublishTotal)

			if err != nil {
				return fmt.Errorf("editorial classify-eval: %w", err)
			}
			if rep.MissesWritten != "" {
				fmt.Fprintf(out, "misses written to %s\n", rep.MissesWritten)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", "data/editorial_dataset-reviewed.yml", "dataset with human labels to evaluate against")
	cmd.Flags().StringVar(&fold, "fold", "dev", "dev, holdout (disjoint halves chosen by article id hash) or all; tune on dev, confirm on holdout")
	cmd.Flags().IntVar(&sample, "sample", 0, "evaluate a random sample of this many examples (0 = all in the fold)")
	cmd.Flags().Int64Var(&seed, "seed", 1, "seed for --sample")
	cmd.Flags().Float64Var(&threshold, "threshold", 0.95, "confidence at or above which a discard counts as confident (offline_training.llm.auto_label_threshold)")
	cmd.Flags().IntVar(&batchSize, "batch-size", 20, "articles per LLM request")
	cmd.Flags().StringVar(&policyFile, "policy-file", "", "evaluate this policy markdown instead of the embedded one, to iterate without rebuilding")
	cmd.Flags().StringVar(&missesOut, "misses-out", "", "write the confident discards that disagree with the human label to this CSV")
	cmd.Flags().BoolVar(&includeImported, "include-imported", false, "also evaluate telegramimport publish labels (posted to the channel, not human-reviewed)")
	cmd.Flags().StringVar(&llmBaseURL, "llm-base-url", "", "override offline_training.llm.base_url for this run")
	cmd.Flags().StringVar(&llmModel, "llm-model", "", "override offline_training.llm.model for this run")
	return cmd
}
