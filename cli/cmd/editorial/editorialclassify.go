// cli/cmd/editorial/editorialclassify.go
package editorial

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/lookback"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// resolveClassifySince parses --since the same way `entities discover`
// does (see lookback.Parse); all forces no lower bound.
func resolveClassifySince(since string, all bool) (time.Time, error) {
	if all {
		return time.Time{}, nil
	}
	return lookback.Parse(since, time.Now().UTC())
}

func newClassifyCommand() *cobra.Command {
	var since string
	var all bool
	var force bool
	var batchSize int
	var limit int
	var llmBaseURL, llmModel string
	var llmMaxTokens int
	var llmDisableThinking bool

	cmd := &cobra.Command{
		Use:   "classify",
		Short: "Classify stored news publish/discard/uncertain via an independent LLM teacher (offline; never runs during ingestion)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}

			f := trainwiring.ClassifyFlags{All: all, Force: force, BaseURL: llmBaseURL, Model: llmModel}
			if cmd.Flags().Changed("llm-max-tokens") {
				f.MaxTokens = &llmMaxTokens
			}
			if cmd.Flags().Changed("llm-disable-thinking") {
				f.DisableThinking = &llmDisableThinking
			}
			// The LLM config checks run before --since is parsed, as they
			// always have, so a run with both wrong reports the LLM one.
			if _, err := trainwiring.ClassifyLLMConfig(cfg, f); err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}

			f.Since, err = resolveClassifySince(since, all)
			if err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}

			// --batch-size/--limit have their own flag defaults (20/500,
			// matching offline_training.llm's YAML defaults) so the
			// command works with zero flags; when the operator relies
			// on configs/settings.yml instead, the configured value
			// must win — so they are passed only when set (0 = configured).
			if cmd.Flags().Changed("batch-size") {
				f.BatchSize = batchSize
			}
			if cmd.Flags().Changed("limit") {
				f.Limit = limit
			}

			rep, err := trainwiring.Classify(cmd.Context(), db, cfg, f, time.Now().UTC())
			if err != nil && !errors.Is(err, trainwiring.ErrReviewQueueSync) {
				return fmt.Errorf("editorial classify: %w", err)
			}

			result := rep.Result
			fmt.Fprintf(cmd.OutOrStdout(), "articles considered: %d\n", result.ArticlesConsidered)
			fmt.Fprintf(cmd.OutOrStdout(), "batches: %d (failed: %d)\n", result.BatchesTotal, result.BatchesFailed)
			fmt.Fprintf(cmd.OutOrStdout(), "proposals saved: %d (skipped, pending exists: %d, labels missing: %d)\n", result.ProposalsSaved, result.ProposalsSkipped, result.LabelsMissing)
			for _, e := range result.BatchErrors {
				fmt.Fprintf(cmd.OutOrStderr(), "  batch error: %s\n", e)
			}
			for _, e := range result.LabelErrors {
				fmt.Fprintf(cmd.OutOrStderr(), "  label error: %s\n", e)
			}
			if err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}

			switch {
			case rep.SyncSkipped != "":
				fmt.Fprintf(cmd.OutOrStdout(), "review queue sync skipped: %s\n", rep.SyncSkipped)
			case rep.Sync != nil:
				fmt.Fprintf(cmd.OutOrStdout(), "review queue: %d considered, %d queued, %d obvious agreements skipped, %d orphaned (article purged)\n", rep.Sync.ProposalsConsidered, rep.Sync.Queued, rep.Sync.Skipped, rep.Sync.Orphaned)
			}

			if result.BatchesFailed > 0 {
				return fmt.Errorf("editorial classify: %d of %d batches failed", result.BatchesFailed, result.BatchesTotal)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "only consider articles fetched since this duration (24h, 7d, 30d) or RFC3339 date; default: no lower bound")
	cmd.Flags().BoolVar(&all, "all", false, "consider every stored article, with no --limit cap")
	cmd.Flags().BoolVar(&force, "force", false, "reprocess articles already marked processed for the current prompt version")
	cmd.Flags().IntVar(&batchSize, "batch-size", 20, "articles per LLM request")
	cmd.Flags().IntVar(&limit, "limit", 500, "maximum number of articles to consider in this run (ignored when --all is set)")
	cmd.Flags().StringVar(&llmBaseURL, "llm-base-url", "", "override offline_training.llm.base_url for this run")
	cmd.Flags().StringVar(&llmModel, "llm-model", "", "override offline_training.llm.model for this run")
	cmd.Flags().IntVar(&llmMaxTokens, "llm-max-tokens", 0, "override offline_training.llm.max_tokens for this run (0 uses the provider default)")
	cmd.Flags().BoolVar(&llmDisableThinking, "llm-disable-thinking", false, "override offline_training.llm.disable_thinking for this run")
	return cmd
}
