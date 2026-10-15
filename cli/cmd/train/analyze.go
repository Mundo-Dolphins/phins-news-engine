package train

import (
	"github.com/spf13/cobra"

	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainanalyze"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newAnalyzeCommand builds `train analyze`, a new read-only diagnostic
// command reporting LLM label counts and review
// queue composition — distinct from the pre-existing, interactive
// `train review`, which this command does not touch or replace.
func newAnalyzeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Report LLM label counts and review-queue composition (read-only)",
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

			proposalRepo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db)
			queueRepo := sqlitereviewqueue.NewReviewQueueRepository(db)

			result, err := trainanalyze.Analyze(cmd.Context(), proposalRepo, queueRepo, cfg.OfflineTraining.LLM.AutoLabelThreshold, 100000)
			if err != nil {
				return err
			}

			cmd.Printf("LLM labels: %d proposed, %d auto-labeled, %d sent to review\n",
				result.ProposedCount, result.AutoLabeledCount, result.SentToReviewCount)
			cmd.Printf("Review queue: %d items (%d disagreement, %d novel vocabulary, %d other)\n",
				result.QueueSize, result.QueueDisagreementCount, result.QueueNovelVocabularyCount, result.QueueOtherCount)
			return nil
		},
	}
	return cmd
}
