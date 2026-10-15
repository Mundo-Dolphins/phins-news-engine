// cli/cmd/entities/proposals_verify.go
package entities

import (
	"fmt"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func newProposalsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "proposals", Short: "Maintenance of stored entity proposals"}
	cmd.AddCommand(newProposalsVerifyCommand())
	return cmd
}

// applyProviderFlag maps --provider onto the verification stage flags.
// wikidata means Wikidata only: no LLM/SearXNG fallback.
func applyProviderFlag(provider string, f *verificationFlags) error {
	switch provider {
	case "":
		return nil
	case "wikidata":
		if f.noWikidata || f.fallback || f.offline {
			return fmt.Errorf("--provider wikidata conflicts with --no-wikidata, --fallback and --offline")
		}
		f.wikidata, f.noFallback = true, true
		return nil
	}
	return fmt.Errorf("--provider must be wikidata (got %q)", provider)
}

func newProposalsVerifyCommand() *cobra.Command {
	var provider string
	var limit int
	var retryAttempted bool
	var entityType string
	var dryRun bool
	var manualPath, generatedPath string
	var llmBaseURL, llmModel string
	var llmMaxTokens int
	var llmDisableThinking bool
	var vflags verificationFlags

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify the pending proposal backlog via web lookup using stored sources (idempotent, resumable, batched with --limit)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch entityproposal.EntityType(entityType) {
			case "", entityproposal.EntityTypePlayer, entityproposal.EntityTypeCoach, entityproposal.EntityTypeExecutive, entityproposal.EntityTypeTeam:
			default:
				return fmt.Errorf("entities proposals verify: --type must be one of player, coach, executive, team (got %q)", entityType)
			}
			if err := applyProviderFlag(provider, &vflags); err != nil {
				return fmt.Errorf("entities proposals verify: %w", err)
			}
			db, cfg, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities proposals verify: %w", err)
			}
			defer db.Close()

			discoveryCfg, maxTokens, disableThinking, err := resolveDiscoveryConfig(cmd, cfg, llmFlagValues{
				baseURL: llmBaseURL, model: llmModel, maxTokens: llmMaxTokens, disableThinking: llmDisableThinking,
			})
			if err != nil {
				return fmt.Errorf("entities proposals verify: %w", err)
			}
			llm := newDiscoveryLLM(cmd, discoveryCfg, maxTokens, disableThinking)
			// This command always verifies; web lookup config must be valid.

			catalogIdx, err := loadCatalogIndex(manualPath, generatedPath)
			if err != nil {
				return fmt.Errorf("entities proposals verify: %w", err)
			}

			// webLookupEnabled is true: this command always verifies, so the
			// fallback stage defaults on (subject to verification.fallback).
			wikidataOn, fallbackOn := resolveVerificationStages(cfg.EntityDiscovery.Verification, true, vflags)
			verifier, extras, err := buildChainVerifier(cmd, db, cfg, llm, catalogIdx, wikidataOn, fallbackOn, !vflags.noLLM && !vflags.offline)
			if err != nil {
				return fmt.Errorf("entities proposals verify: %w", err)
			}
			if verifier == nil {
				return fmt.Errorf("entities proposals verify: no verification stage enabled (--offline, or --no-wikidata with --no-fallback; check entity_discovery.verification.wikidata_first and entity_discovery.verification.fallback)")
			}

			repo := sqliteentityproposal.NewEntityProposalRepository(db)
			result, err := entitydiscovery.ReverifyPending(cmd.Context(), repo, verifier, catalogIdx, entitydiscovery.ReverifyOptions{
				Limit: limit, EntityType: entityType, DryRun: dryRun, RetryAttempted: retryAttempted, Wikidata: wikidataOn,
				Texts: articleTexts{finder: sqliteadapter.NewArticleRepository(db)},
			})
			if err != nil {
				return fmt.Errorf("entities proposals verify: %w", err)
			}
			out := cmd.OutOrStdout()
			if dryRun {
				fmt.Fprintln(out, "dry run: no changes written")
			}
			fmt.Fprintf(out, "processed: %d\n", result.Processed)
			fmt.Fprintf(out, "kept: %d\n", result.Kept)
			fmt.Fprintf(out, "auto-rejected (non-NFL): %d\n", result.AutoRejected)
			fmt.Fprintf(out, "uncertain (left pending): %d\n", result.Uncertain)
			fmt.Fprintf(out, "conflicts (left pending): %d\n", result.Conflicts)
			fmt.Fprintf(out, "duplicates (rejected, duplicate_of_existing): %d\n", result.Duplicates)
			fmt.Fprintf(out, "provider errors (left unmarked, retried next run): %d\n", result.ProviderErrors)
			fmt.Fprintf(out, "skipped (already verified/attempted): %d\n", result.Skipped)
			if sr, ok := verifier.(entitydiscovery.StatsReporter); ok {
				cs := sr.ChainStats()
				x := extras()
				printLeagueStats(out, cs.League)
				fmt.Fprintf(out, "wikidata: cache hits (lookups) %d, verified NFL %d, verified non-NFL %d, ambiguous %d, not found %d, errors %d\n", x.CacheHits, cs.WikidataNFL, cs.WikidataNonNFL, cs.WikidataAmbiguous, cs.WikidataNotFound, cs.WikidataErrors)
				fmt.Fprintf(out, "fallback: LLM calls %d, SearXNG searches %d\n", cs.FallbackCalls, x.SearXNGSearches)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum proposals to verify in this run (0 = no cap)")
	cmd.Flags().BoolVar(&retryAttempted, "retry-attempted", false, "re-verify proposals already attempted by an earlier run (e.g. uncertain ones), oldest attempt first")
	cmd.Flags().StringVar(&entityType, "type", "", "only verify proposals of this entity type (player, coach, executive, team)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report outcomes without changing any proposal (the verifier still runs and may write the web-search cache)")
	cmd.Flags().StringVar(&manualPath, "manual", "configs/entity_catalog_manual.yml", "path to the manual entity catalog YAML file")
	cmd.Flags().StringVar(&generatedPath, "generated", "configs/entity_catalog_generated.yml", "path to the generated entity catalog YAML file")
	cmd.Flags().StringVar(&llmBaseURL, "llm-base-url", "", "override entity_discovery.llm.base_url for this run")
	cmd.Flags().StringVar(&llmModel, "llm-model", "", "override entity_discovery.llm.model for this run")
	cmd.Flags().IntVar(&llmMaxTokens, "llm-max-tokens", 0, "maximum LLM response tokens for this run (0 uses the provider default)")
	cmd.Flags().BoolVar(&llmDisableThinking, "llm-disable-thinking", false, "ask compatible reasoning models (such as Ollama) not to generate thinking output")
	cmd.Flags().StringVar(&provider, "provider", "", "verify with a single provider: wikidata (Wikidata only, no LLM/SearXNG fallback)")
	addVerificationFlags(cmd, &vflags)
	return cmd
}
