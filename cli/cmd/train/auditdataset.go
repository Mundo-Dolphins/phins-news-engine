package train

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetaudit"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func newAuditDatasetCommand() *cobra.Command {
	var datasetPath, outPath string
	cmd := &cobra.Command{
		Use:   "audit-dataset",
		Short: "Generate a prioritized CSV of heuristic dataset rows for interactive review",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() { datasetPath, outPath = "data/editorial_dataset.yml", "data/editorial_dataset-review.csv" }()
			dataset, err := editorialtrain.LoadDataset(datasetPath)
			if err != nil {
				return err
			}
			originalCount := len(dataset.Examples)
			excludedSources, err := modelBypassSourceSet()
			if err != nil {
				return err
			}
			dataset = dataset.ExcludeSources(excludedSources)
			candidates := datasetaudit.AuditDataset(dataset.Examples)
			if err := datasetaudit.WriteCSV(outPath, candidates); err != nil {
				return fmt.Errorf("train audit-dataset: write %s: %w", outPath, err)
			}
			cmd.Printf("audited %d eligible dataset row(s), excluding %d official-source row(s); %d candidate(s) written to %s\n", len(dataset.Examples), originalCount-len(dataset.Examples), len(candidates), outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", "data/editorial_dataset.yml", "dataset YAML to audit")
	cmd.Flags().StringVar(&outPath, "out", "data/editorial_dataset-review.csv", "CSV path to write prioritized candidates")
	return cmd
}
