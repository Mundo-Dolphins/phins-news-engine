package train

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetaudit"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

func newAuditGatesCommand() *cobra.Command {
	var datasetPath, outPath string
	var limit int
	cmd := &cobra.Command{
		Use:   "audit-gates",
		Short: "List publish-labeled dataset rows the current relevance, anchor and veto gates would block (review queue)",
		Long: `Re-runs every publish-labeled example of a dataset through the gates the
online pipeline applies today (veto rules, dictionary relevance, anchor policy)
and writes the ones that would now be blocked as a review queue. The positives
imported from the channel are whatever the bot published, noise included; this
finds that noise so it can be relabeled as reject with "train review-dataset"
before retraining. Read-only: it never changes labels.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() {
				datasetPath, outPath, limit = "data/editorial_dataset.yml", "data/editorial_dataset-gates-review.csv", 0
			}()
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			g, err := trainwiring.NewGateAuditor(cfg, time.Now())
			if err != nil {
				return err
			}
			dataset, err := editorialtrain.LoadDataset(datasetPath)
			if err != nil {
				return err
			}
			candidates := g.Audit(dataset.Examples)
			total := len(candidates)
			if limit > 0 && len(candidates) > limit {
				candidates = candidates[:limit]
			}
			if err := datasetaudit.WriteCSV(outPath, candidates); err != nil {
				return fmt.Errorf("train audit-gates: %w", err)
			}
			cmd.Printf("audited %d row(s); %d publish-labeled row(s) would now be blocked, %d written to %s\n", len(dataset.Examples), total, len(candidates), outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", "data/editorial_dataset.yml", "dataset YAML to audit")
	cmd.Flags().StringVar(&outPath, "out", "data/editorial_dataset-gates-review.csv", "CSV review queue to write (input of train review-dataset --audit)")
	cmd.Flags().IntVar(&limit, "limit", 0, "write at most this many rows, most certain first (0: all)")
	return cmd
}
