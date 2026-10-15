// cli/cmd/editorial/editorialclassifyeval.go
package editorial

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialteacher"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
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
			llmCfg := cfg.OfflineTraining.LLM
			if llmBaseURL != "" {
				llmCfg.BaseURL = llmBaseURL
			}
			if llmModel != "" {
				llmCfg.Model = llmModel
			}
			if llmCfg.BaseURL == "" || llmCfg.Model == "" || llmCfg.TimeoutSeconds <= 0 {
				return fmt.Errorf("editorial classify-eval: offline_training.llm base_url, model and timeout_seconds are required")
			}

			policy := editorialteacher.PublicationPolicy
			if policyFile != "" {
				raw, err := os.ReadFile(policyFile) // #nosec G304 -- operator-provided CLI flag
				if err != nil {
					return fmt.Errorf("editorial classify-eval: read --policy-file: %w", err)
				}
				policy = string(raw)
			}

			dataset, err := editorialtrain.LoadDataset(datasetPath)
			if err != nil {
				return fmt.Errorf("editorial classify-eval: %w", err)
			}
			// telegramimport labels mean "was posted to the channel", not a
			// human editorial judgment, so they are excluded by default.
			var examples []editorialteacher.EvalExample
			for _, ex := range dataset.Examples {
				if !includeImported && ex.LabelSource == "telegramimport" {
					continue
				}
				if ex.Label != "publish" && ex.Label != "reject" {
					continue
				}
				examples = append(examples, editorialteacher.EvalExample{
					ArticleID: ex.ArticleID, Title: ex.Title, Summary: ex.Body,
					SourceType: ex.SourceType, Domain: ex.Domain, Truth: ex.Label,
				})
			}
			examples = editorialteacher.SelectEvalExamples(examples, fold, sample, seed)
			if len(examples) == 0 {
				return fmt.Errorf("editorial classify-eval: no examples selected from %s", datasetPath)
			}

			llm := llmclient.New(shared.OfflineTrainingLLMClientConfig(llmCfg))
			fmt.Fprintf(cmd.OutOrStdout(), "evaluating %d examples (fold=%s) with %s\n", len(examples), fold, llmCfg.Model)
			report, err := editorialteacher.Evaluate(cmd.Context(), llm, policy, examples, batchSize)
			if err != nil {
				return fmt.Errorf("editorial classify-eval: %w", err)
			}

			s := report.Summarize(threshold)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "batches: %d (failed: %d), labels missing: %d, uncertain: %d\n", report.BatchesTotal, report.BatchesFailed, s.Missing, s.Uncertain)
			fmt.Fprintf(out, "human labels: %d reject, %d publish\n", s.RejectTotal, s.PublishTotal)
			fmt.Fprintf(out, "discards (any confidence): %d, %d were human rejects\n", s.DiscardTotal, s.DiscardCorrect)
			fmt.Fprintf(out, "confident discards (>= %.2f): %d, %d were human rejects -> precision %.3f\n", threshold, s.ConfidentDiscardTotal, s.ConfidentDiscardCorrect, s.ConfidentDiscardPrecision())
			fmt.Fprintf(out, "human rejects caught confidently: %d/%d -> recall %.3f\n", s.ConfidentRejectsCaught, s.RejectTotal, s.RejectRecall())
			fmt.Fprintf(out, "human publishes confidently discarded: %d/%d\n", s.PublishDiscarded, s.PublishTotal)

			if missesOut != "" {
				if err := writeEvalMisses(missesOut, report.Predictions, threshold); err != nil {
					return fmt.Errorf("editorial classify-eval: %w", err)
				}
				fmt.Fprintf(out, "misses written to %s\n", missesOut)
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

func writeEvalMisses(path string, preds []editorialteacher.EvalPrediction, threshold float64) error {
	var misses []editorialteacher.EvalPrediction
	for _, p := range preds {
		if p.Label == "discard" && p.Confidence >= threshold && p.Truth == "publish" {
			misses = append(misses, p)
		}
	}
	sort.Slice(misses, func(i, j int) bool { return misses[i].Confidence > misses[j].Confidence })
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- operator-provided CLI flag
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"article_id", "confidence", "domain", "title", "llm_reason"})
	for _, p := range misses {
		_ = w.Write([]string{p.ArticleID, fmt.Sprintf("%.2f", p.Confidence), p.Domain, p.Title, p.Reason})
	}
	w.Flush()
	return w.Error()
}
