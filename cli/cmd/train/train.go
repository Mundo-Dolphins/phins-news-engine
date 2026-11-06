package train

import (
	"fmt"
	"os"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func modelBypassSourceSet() (map[string]bool, error) {
	data, err := os.ReadFile(shared.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", shared.ConfigPath, err)
	}
	var cfg struct {
		Editorial config.EditorialConfig `yaml:"editorial"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse editorial source exclusions: %w", err)
	}
	ids := make(map[string]bool, len(cfg.Editorial.ModelBypassSourceIDs)+len(cfg.Editorial.UnconditionalPublishSourceIDs))
	for _, id := range cfg.Editorial.ModelBypassSourceIDs {
		ids[id] = true
	}
	for _, id := range cfg.Editorial.UnconditionalPublishSourceIDs {
		ids[id] = true
	}
	return ids, nil
}

func NewCommand() *cobra.Command {
	trainCmd := &cobra.Command{Use: "train", Short: "Train the editorial classifier offline"}
	trainCmd.AddCommand(newBuildCommand())
	trainCmd.AddCommand(newAuditDatasetCommand())
	trainCmd.AddCommand(newAuditGatesCommand())
	trainCmd.AddCommand(newTrainSeedV1Cmd())
	trainCmd.AddCommand(newAnalyzeCommand())
	trainCmd.AddCommand(newEvaluateCommand())
	trainCmd.AddCommand(newReviewCommand())
	trainCmd.AddCommand(newReviewDatasetCommand())
	trainCmd.AddCommand(newShadowCompareCommand())
	trainCmd.AddCommand(newPromoteCommand())
	trainCmd.AddCommand(newRollbackCommand())
	trainCmd.AddCommand(newRunCommand())
	trainCmd.AddCommand(newMineCommand())
	trainCmd.AddCommand(newBackfillRejectsCommand())
	trainCmd.AddCommand(newRecheckCorpusCommand())
	trainCmd.AddCommand(newImportLabelsCommand())
	return trainCmd
}

func newShadowCompareCommand() *cobra.Command {
	var candidatePath string
	var days, limit int
	cmd := &cobra.Command{
		Use:   "shadow-compare",
		Short: "Score the same recent stored articles with the production and a candidate model offline, storing every disagreement",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			result, err := trainwiring.ShadowCompare(cmd.Context(), db, cfg, candidatePath, days, limit, time.Now().UTC())
			if err != nil {
				return err
			}

			cmd.Printf("shadow-compare run %s: %d article(s) scored, %d agreement(s), %d disagreement(s) stored\n",
				result.RunID, result.TotalArticles, result.Agreements, len(result.Disagreements))
			return nil
		},
	}
	cmd.Flags().StringVar(&candidatePath, "candidate", "", "directory of the candidate model to compare against production (required)")
	cmd.Flags().IntVar(&days, "days", 7, "how many days of recent stored articles to score")
	cmd.Flags().IntVar(&limit, "limit", 5000, "maximum number of articles to score")
	_ = cmd.MarkFlagRequired("candidate")
	return cmd
}
