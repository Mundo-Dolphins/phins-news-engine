package dedup

import (
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dedupeval"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func NewCommand() *cobra.Command {
	dedupCmd := &cobra.Command{Use: "dedup", Short: "Deduplication tooling"}
	dedupCmd.AddCommand(newEvaluateCommand())
	return dedupCmd
}

func newEvaluateCommand() *cobra.Command {
	var datasetPath string
	evaluateCmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Score a labeled dataset of article-title pairs against the configured dedup/clustering thresholds",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}

			dataset, err := dedupeval.LoadDataset(datasetPath)
			if err != nil {
				return err
			}

			nd := cfg.Deduplication.NearDuplicate
			thresholdCfg := deduplication.ThresholdConfig{
				DefaultThreshold:      nd.DefaultThreshold,
				ThresholdBySourceType: nd.ThresholdBySourceType,
				ShortTextMaxTokens:    nd.ShortTextMaxTokens,
				ShortTextThreshold:    nd.ShortTextThreshold,
				EventThreshold:        cfg.Clustering.EventThreshold,
				RulesVersion:          nd.RulesVersion,
			}
			report := dedupeval.Evaluate(dataset, cfg.Normalization.Text.Policy(), thresholdCfg, nd.Method)

			evaluated := len(dataset.Pairs) - report.Excluded
			cmd.Printf("pairs: %d evaluated, %d excluded (ambiguous)\n", evaluated, report.Excluded)
			cmd.Printf("overall: precision=%.3f recall=%.3f f1=%.3f\n", report.Overall.Precision(), report.Overall.Recall(), report.Overall.F1())
			for st, m := range report.BySourceType {
				cmd.Printf("source_type=%s: precision=%.3f recall=%.3f f1=%.3f\n", st, m.Precision(), m.Recall(), m.F1())
			}
			for ct, m := range report.ByContentType {
				cmd.Printf("content_type=%s: precision=%.3f recall=%.3f f1=%.3f\n", ct, m.Precision(), m.Recall(), m.F1())
			}
			cmd.Println("confusion matrix (predicted x gold):")
			for predicted, row := range report.Confusion {
				for gold, count := range row {
					cmd.Printf("  %s x %s: %d\n", predicted, gold, count)
				}
			}
			return nil
		},
	}
	evaluateCmd.Flags().StringVar(&datasetPath, "dataset", "testdata/dedup_pairs.yml", "path to the labeled dedup evaluation dataset")
	return evaluateCmd
}
