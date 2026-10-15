// cli/cmd/editorial/editorialclassify.go
package editorial

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialreview"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialteacher"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// resolveClassifySince parses --since the same way `entities discover`
// does: a Go duration ("24h"), a bare day count ("7d"), an RFC3339 date,
// or "" (no lower bound).
func resolveClassifySince(since string, all bool) (time.Time, error) {
	if all || since == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(since); err == nil {
		return time.Now().UTC().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, since); err == nil {
		return t.UTC(), nil
	}
	if strings.HasSuffix(since, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(since, "d")); err == nil && n > 0 {
			return time.Now().UTC().Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --since value %q: want a Go duration (24h), a day count (7d), or an RFC3339 date", since)
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

			llmCfg := cfg.OfflineTraining.LLM
			if llmBaseURL != "" {
				llmCfg.BaseURL = llmBaseURL
			}
			if llmModel != "" {
				llmCfg.Model = llmModel
			}
			if cmd.Flags().Changed("llm-max-tokens") {
				llmCfg.MaxTokens = llmMaxTokens
			}
			if cmd.Flags().Changed("llm-disable-thinking") {
				llmCfg.DisableThinking = llmDisableThinking
			}

			// OfflineTrainingConfig.Validate() intentionally checks only
			// AutoLabelThreshold (see config.go) — the LLM connection
			// fields below are required only when this command actually
			// runs, so this command performs its own explicit checks
			// rather than relying on a global Validate() to cover them.
			if llmCfg.BaseURL == "" {
				return fmt.Errorf("editorial classify: offline_training.llm.base_url is required (or pass --llm-base-url)")
			}
			if llmCfg.Model == "" {
				return fmt.Errorf("editorial classify: offline_training.llm.model is required (or pass --llm-model)")
			}
			if llmCfg.TimeoutSeconds <= 0 {
				return fmt.Errorf("editorial classify: offline_training.llm.timeout_seconds must be positive")
			}
			if err := cfg.OfflineTraining.Validate(); err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}

			sinceTime, err := resolveClassifySince(since, all)
			if err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}

			// --batch-size/--limit have their own flag defaults (20/500,
			// matching offline_training.llm's YAML defaults) so the
			// command works with zero flags; when the operator relies
			// on configs/settings.yml instead, the configured value
			// must win.
			if !cmd.Flags().Changed("batch-size") && llmCfg.BatchSize > 0 {
				batchSize = llmCfg.BatchSize
			}
			if !cmd.Flags().Changed("limit") && llmCfg.Limit > 0 {
				limit = llmCfg.Limit
			}

			articlesRepo := sqliteadapter.NewArticleRepository(db)
			proposalsRepo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db)
			llm := llmclient.New(shared.OfflineTrainingLLMClientConfig(llmCfg))

			opts := editorialteacher.ClassifyOptions{
				Since: sinceTime, BatchSize: batchSize, Limit: limit,
				Model: llmCfg.Model, Force: force,
			}
			if all {
				opts.Limit = 10_000_000
			}

			result, err := editorialteacher.Classify(cmd.Context(), articlesRepo, llm, proposalsRepo, opts, time.Now().UTC())
			if err != nil {
				return fmt.Errorf("editorial classify: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "articles considered: %d\n", result.ArticlesConsidered)
			fmt.Fprintf(cmd.OutOrStdout(), "batches: %d (failed: %d)\n", result.BatchesTotal, result.BatchesFailed)
			fmt.Fprintf(cmd.OutOrStdout(), "proposals saved: %d (skipped, pending exists: %d, labels missing: %d)\n", result.ProposalsSaved, result.ProposalsSkipped, result.LabelsMissing)
			for _, e := range result.BatchErrors {
				fmt.Fprintf(cmd.OutOrStderr(), "  batch error: %s\n", e)
			}
			for _, e := range result.LabelErrors {
				fmt.Fprintf(cmd.OutOrStderr(), "  label error: %s\n", e)
			}

			// The review-priority sync needs a promoted editorial model
			// to derive each article's NB zone/thresholds. That model
			// artifact is optional when editorial classification is
			// disabled (cfg.Editorial.Mode == "disabled"), matching the
			// guard in engine/run.go -- so skip the sync
			// entirely in that case, and also degrade gracefully (skip,
			// don't fail) if the model fails to load for any other
			// reason: this command's own LLM-classification work above
			// already succeeded and already spent the LLM calls, so
			// failing here would be wasteful and confusing.
			switch cfg.Editorial.Mode {
			case "disabled":
				fmt.Fprintln(cmd.OutOrStdout(), "review queue sync skipped: editorial.mode is disabled (no promoted editorial model expected)")
			default:
				editorialModel, loadErr := editorialmodel.Load(cfg.Model.Path)
				if loadErr != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "review queue sync skipped: failed to load editorial model at %s: %v\n", cfg.Model.Path, loadErr)
					break
				}
				sourcesRepo := sqliteadapter.NewSourceRepository(db)
				signalsRepo := sqlitereviewqueue.NewReviewSignalsRepository(db)
				queueRepo := sqlitereviewqueue.NewReviewQueueRepository(db)
				hardExamplesRepo := sqliteadapter.NewHardExampleRepository(db)
				rs := cfg.OfflineTraining.ReviewScoring
				scoringCfg := reviewpriority.Config{
					Weights: reviewpriority.Weights{
						Disagreement: rs.Weights.Disagreement, NBUncertainty: rs.Weights.NBUncertainty,
						LLMUncertainty: rs.Weights.LLMUncertainty, NovelVocabulary: rs.Weights.NovelVocabulary,
						UnknownEntities: rs.Weights.UnknownEntities, NewSource: rs.Weights.NewSource, Recency: rs.Weights.Recency,
						HardMining: rs.Weights.HardMining,
					},
					LLMConfidenceAutoLabelThreshold: rs.LLMConfidenceAutoLabelThreshold,
					LLMConfidenceUncertainThreshold: rs.LLMConfidenceUncertainThreshold,
					NewSourceWindowHours:            rs.NewSourceWindowHours,
					RecencyWindowHours:              rs.RecencyWindowHours,
				}
				syncResult, syncErr := editorialreview.Sync(cmd.Context(), proposalsRepo, articlesRepo, sourcesRepo, signalsRepo, queueRepo, hardExamplesRepo, editorialModel, scoringCfg, time.Now().UTC())
				if syncErr != nil {
					return fmt.Errorf("editorial classify: sync review queue: %w", syncErr)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "review queue: %d considered, %d queued, %d obvious agreements skipped, %d orphaned (article purged)\n", syncResult.ProposalsConsidered, syncResult.Queued, syncResult.Skipped, syncResult.Orphaned)
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
