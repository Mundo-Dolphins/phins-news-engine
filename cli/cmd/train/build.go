package train

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newBuildCommand builds `train build`, which assembles the Gold+Silver
// training dataset — renamed from `train export` (#253 part 22).
func newBuildCommand() *cobra.Command {
	var outPath string
	var maxPerCluster int
	var includeDB []string
	buildCmd := &cobra.Command{
		Use:   "build",
		Short: "Build a training dataset from Gold and approved Silver examples",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if cfg.OfflineTraining.LLM.AutoLabelThreshold <= 0 {
				return fmt.Errorf("editorialtrain: offline_training.llm.auto_label_threshold must be configured above 0 before Silver examples can be included in a training dataset")
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}

			labelRepo := sqliteadapter.NewHumanLabelRepository(db)
			articleRepo := sqliteadapter.NewArticleRepository(db)
			proposalRepo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db)

			trustedSourceIDs := make(map[string]bool, len(cfg.Classification.TrustedSourceIDs))
			for _, id := range cfg.Classification.TrustedSourceIDs {
				trustedSourceIDs[id] = true
			}
			excludedSources := make(map[string]bool, len(cfg.Editorial.ModelBypassSourceIDs)+len(cfg.Editorial.UnconditionalPublishSourceIDs))
			for _, id := range cfg.Editorial.ModelBypassSourceIDs {
				excludedSources[id] = true
			}
			for _, id := range cfg.Editorial.UnconditionalPublishSourceIDs {
				excludedSources[id] = true
			}

			gold, err := editorialtrain.ExportDataset(cmd.Context(), labelRepo, trustedSourceIDs, excludedSources)
			if err != nil {
				return err
			}
			goldGroups := [][]editorialtrain.TrainingExample{gold}
			for _, path := range includeDB {
				archive, openErr := infrasqlite.OpenReadOnly(path, cfg.SQLite.BusyTimeout())
				if openErr != nil {
					return openErr
				}
				archiveLabels := sqliteadapter.NewHumanLabelRepository(archive)
				archived, exportErr := editorialtrain.ExportDataset(cmd.Context(), archiveLabels, trustedSourceIDs, excludedSources)
				closeErr := archive.Close()
				if exportErr != nil {
					return exportErr
				}
				if closeErr != nil {
					return closeErr
				}
				goldGroups = append(goldGroups, archived)
			}
			gold, err = editorialtrain.MergeTrainingExamples(goldGroups...)
			if err != nil {
				return err
			}
			silver, skippedMissingArticle, err := editorialtrain.FetchSilverExamples(
				cmd.Context(), proposalRepo, labelRepo, articleRepo, trustedSourceIDs, cfg.OfflineTraining.LLM.AutoLabelThreshold,
			)
			if err != nil {
				return err
			}
			silverDataset := (editorialtrain.Dataset{Examples: silver}).ExcludeSources(excludedSources)
			silver = silverDataset.Examples

			examples := editorialtrain.CapPerCluster(append(gold, silver...), maxPerCluster)
			if len(examples) == 0 {
				return fmt.Errorf("editorialtrain: no Gold or Silver examples found — use `label` to add Gold examples, or `editorial classify` to generate Silver proposals, first")
			}

			if err := editorialtrain.SaveDataset(outPath, examples); err != nil {
				return err
			}

			unresolved, err := labelRepo.CountUnresolvedConflicts(cmd.Context())
			if err != nil {
				return err
			}
			if unresolved > 0 {
				cmd.Printf("WARNING: %d article(s) have an unresolved legacy label conflict and are excluded from this export — see `label conflicts` and `label resolve-conflict`\n", unresolved)
			}

			reloaded, err := editorialtrain.LoadDataset(outPath)
			if err != nil {
				return err
			}
			if err := editorialtrain.WriteManifest(outPath, examples, reloaded.Hash(), time.Now().UTC(), unresolved, skippedMissingArticle); err != nil {
				return err
			}

			dropped := len(gold) + len(silver) - len(examples)
			cmd.Printf("built %d examples to %s (%d gold, %d silver; %d dropped by --max-per-cluster)\n",
				len(examples), outPath, len(gold), len(silver), dropped)
			return nil
		},
	}
	buildCmd.Flags().StringVar(&outPath, "out", "data/editorial_dataset.yml", "path to write the built dataset")
	buildCmd.Flags().IntVar(&maxPerCluster, "max-per-cluster", 1, "maximum number of examples kept from any one event cluster, to prevent one story from dominating word frequencies; <= 0 disables capping")
	buildCmd.Flags().StringSliceVar(&includeDB, "include-db", nil, "read-only SQLite snapshot whose human labels should be merged into Gold (repeatable)")
	return buildCmd
}
