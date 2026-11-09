// cli/cmd/entities/discover.go
package entities

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/lookback"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

// resolveSince parses --since into a lower time bound (see lookback.Parse).
// all forces the zero value regardless of since.
func resolveSince(since string, all bool) (time.Time, error) {
	if all {
		return time.Time{}, nil
	}
	return lookback.Parse(since, time.Now().UTC())
}

func newDiscoverCommand() *cobra.Command {
	var since string
	var all bool
	var force bool
	var batchSize int
	var limit int
	var verifyMinConfidence float64
	var verify bool
	var vflags entitywiring.Stages
	var dictionaryPath, manualPath, generatedPath string
	var llmBaseURL, llmModel string
	var llmMaxTokens int
	var llmDisableThinking bool

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Mine unknown entities and aliases from stored news via an LLM (offline; never runs during ingestion)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, cfg, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}
			defer db.Close()

			f := entitywiring.DiscoverFlags{
				All: all, Force: force, ManualPath: manualPath, GeneratedPath: generatedPath,
				LLM:                 llmOverrides(cmd, llmBaseURL, llmModel, llmMaxTokens, llmDisableThinking),
				VerifyMinConfidence: verifyMinConfidence, Stages: vflags,
			}
			// A discovery config error is reported before a --since error,
			// as it always has been.
			if _, _, _, err := entitywiring.DiscoveryConfig(cfg, f.LLM); err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}
			if f.Since, err = resolveSince(since, all); err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}
			// Unset --batch-size/--limit (0) let entity_discovery.batch_size/
			// limit win over the 20/500 defaults.
			if cmd.Flags().Changed("batch-size") {
				f.BatchSize = batchSize
			}
			if cmd.Flags().Changed("limit") {
				f.Limit = limit
			}
			if cmd.Flags().Changed("verify") {
				f.Verify = &verify
			}

			rep, err := entitywiring.Discover(cmd.Context(), db, cfg, f, time.Now(), cliLoggers(cmd))
			for _, w := range rep.Warnings {
				fmt.Fprintln(cmd.OutOrStderr(), w)
			}
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}

			result, x, out := rep.Result, rep.Extras, cmd.OutOrStdout()
			fmt.Fprintf(out, "articles considered: %d\n", result.ArticlesConsidered)
			fmt.Fprintf(out, "batches: %d (failed: %d)\n", result.BatchesTotal, result.BatchesFailed)
			fmt.Fprintf(out, "proposals created/updated: %d\n", result.ProposalsUpserted)
			if rep.LeagueOn {
				fmt.Fprintf(out, "league: skipped %d as noise, %d already-known league entities\n", result.SkippedNoise, result.AlreadyKnownLeague)
			}
			fmt.Fprintf(out, "auto-rejected (non-NFL): %d\n", result.AutoRejected)
			fmt.Fprintf(out, "uncertain (left pending): %d\n", result.Uncertain)
			fmt.Fprintf(out, "duplicates (rejected, duplicate_of_existing): %d\n", result.Duplicates)
			v := result.Verification
			fmt.Fprintf(out, "verification: candidates considered: %d, local catalog hits (counted per extracted mention): %d\n", v.CandidatesConsidered, v.CatalogHits)
			printLeagueStats(out, v.Chain.League)
			fmt.Fprintf(out, "wikidata: cache hits (lookups) %d, verified NFL %d, verified non-NFL %d, ambiguous %d, not found %d, errors %d\n", x.CacheHits, v.Chain.WikidataNFL, v.Chain.WikidataNonNFL, v.Chain.WikidataAmbiguous, v.Chain.WikidataNotFound, v.Chain.WikidataErrors)
			fmt.Fprintf(out, "fallback: LLM calls %d, SearXNG searches %d\n", v.Chain.FallbackCalls, x.SearXNGSearches)
			fmt.Fprintf(out, "verification uncertain (left pending): %d\n", v.LeftForReview)
			for _, e := range result.BatchErrors {
				fmt.Fprintf(cmd.OutOrStderr(), "  batch error: %s\n", e)
			}
			if result.BatchesFailed > 0 {
				return fmt.Errorf("entities discover: %d of %d batches failed", result.BatchesFailed, result.BatchesTotal)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "only consider articles fetched since this duration (24h, 7d, 30d) or RFC3339 date; default: no lower bound")
	cmd.Flags().BoolVar(&all, "all", false, "consider every stored article, with no --limit cap")
	cmd.Flags().BoolVar(&force, "force", false, "reprocess articles already marked processed for this extractor version")
	cmd.Flags().IntVar(&batchSize, "batch-size", 20, "articles per LLM request")
	cmd.Flags().IntVar(&limit, "limit", 500, "maximum number of articles to consider in this run (ignored when --all is set)")
	cmd.Flags().StringVar(&dictionaryPath, "dictionary", shared.DefaultDictionaryPath, "path to the team dictionary YAML file (unused directly by discover, accepted for symmetry with approve)")
	cmd.Flags().StringVar(&manualPath, "manual", "configs/entity_catalog_manual.yml", "path to the manual entity catalog YAML file")
	cmd.Flags().StringVar(&generatedPath, "generated", "configs/entity_catalog_generated.yml", "path to the generated entity catalog YAML file")
	cmd.Flags().StringVar(&llmBaseURL, "llm-base-url", "", "override entity_discovery.llm.base_url for this run")
	cmd.Flags().StringVar(&llmModel, "llm-model", "", "override entity_discovery.llm.model for this run")
	cmd.Flags().IntVar(&llmMaxTokens, "llm-max-tokens", 0, "maximum LLM response tokens for this run (0 uses the provider default)")
	cmd.Flags().BoolVar(&llmDisableThinking, "llm-disable-thinking", false, "ask compatible reasoning models (such as Ollama) not to generate thinking output")
	cmd.Flags().BoolVar(&verify, "verify", false, "allow bounded web-lookup verification for uncertain candidates (overrides catalog_maintenance.web_lookup.enabled for this run)")
	cmd.Flags().Float64Var(&verifyMinConfidence, "verify-min-confidence", 0.85, "confidence threshold below which a candidate is sent through --verify")
	addVerificationFlags(cmd, &vflags)
	return cmd
}

// discoveryLogger is a minimal appout.Logger adapter over the command's
// stderr writer: the entity commands have no existing logger dependency,
// and the verification wiring requires one per component.
type discoveryLogger struct {
	out       interface{ Write([]byte) (int, error) }
	component string
}

func (l discoveryLogger) Debug(string, ...any) {}
func (l discoveryLogger) Info(msg string, kv ...any) {
	fmt.Fprintf(l.out, "[%s] %s %v\n", l.component, msg, kv)
}

func (l discoveryLogger) Warn(msg string, kv ...any) {
	fmt.Fprintf(l.out, "[%s] WARN %s %v\n", l.component, msg, kv)
}

func (l discoveryLogger) Error(msg string, err error, kv ...any) {
	fmt.Fprintf(l.out, "[%s] ERROR %s: %v %v\n", l.component, msg, err, kv)
}
