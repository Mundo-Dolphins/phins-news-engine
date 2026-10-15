// cli/cmd/entities/discover.go
package entities

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/catalogverification"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/websearch"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	sqlitewebsearchcache "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitewebsearchcache"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

// resolveSince parses --since into a lower time bound: a Go duration
// ("24h"), a bare day count ("7d", "30d" — time.ParseDuration has no "d"
// unit), an RFC3339 date, or "" (no lower bound). all forces the zero
// value regardless of since.
func resolveSince(since string, all bool) (time.Time, error) {
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

func newDiscoverCommand() *cobra.Command {
	var since string
	var all bool
	var force bool
	var batchSize int
	var limit int
	var verifyMinConfidence float64
	var verify bool
	var vflags verificationFlags
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

			discoveryCfg, llmMaxTokens, llmDisableThinking, err := resolveDiscoveryConfig(cmd, cfg, llmFlagValues{
				baseURL: llmBaseURL, model: llmModel, maxTokens: llmMaxTokens, disableThinking: llmDisableThinking,
			})
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}

			sinceTime, err := resolveSince(since, all)
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}

			manualRules, err := entitycatalog.LoadOne(manualPath)
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}
			generatedRules, err := entitycatalog.LoadOne(generatedPath)
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}
			allRules := make([]eventsignals.EntityRule, 0, len(manualRules)+len(generatedRules))
			allRules = append(allRules, manualRules...)
			allRules = append(allRules, generatedRules...)
			catalogIdx := entitydiscovery.BuildCatalogIndex(allRules)

			articlesRepo := sqliteadapter.NewArticleRepository(db)
			proposalsRepo := sqliteentityproposal.NewEntityProposalRepository(db)
			llm := newDiscoveryLLM(cmd, discoveryCfg, llmMaxTokens, llmDisableThinking)

			// --batch-size/--limit have their own flag defaults (20/500,
			// matching entity_discovery's YAML defaults) so the command
			// works with zero flags; but when the operator relies on
			// configs/settings.yml instead of passing a flag, the
			// configured value must win — otherwise entity_discovery.
			// batch_size/limit would be silently ignored, defeating the
			// point of making them configurable for small local models.
			if !cmd.Flags().Changed("batch-size") && discoveryCfg.BatchSize > 0 {
				batchSize = discoveryCfg.BatchSize
			}
			if !cmd.Flags().Changed("limit") && discoveryCfg.Limit > 0 {
				limit = discoveryCfg.Limit
			}

			webLookupCfg := cfg.CatalogMaintenance.WebLookup
			verifyEnabled := webLookupCfg.Enabled
			if cmd.Flags().Changed("verify") {
				verifyEnabled = verify
			}
			wikidataOn, fallbackOn := resolveVerificationStages(cfg.EntityDiscovery.Verification, verifyEnabled, vflags)
			if cmd.Flags().Changed("verify") && verify && !cfg.EntityDiscovery.Verification.Fallback && !vflags.fallback && !vflags.offline {
				fmt.Fprintln(cmd.OutOrStderr(), "warning: --verify has no effect unless --fallback is given, because entity_discovery.verification.fallback is false")
			}
			verifier, extras, err := buildChainVerifier(cmd, db, cfg, llm, catalogIdx, wikidataOn, fallbackOn, !vflags.noLLM && !vflags.offline)
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}

			opts := entitydiscovery.DiscoverOptions{
				Since: sinceTime, BatchSize: batchSize, Limit: limit,
				ExtractorVersion: discoveryCfg.ExtractorVersion, Force: force,
				VerifyMinConfidence: verifyMinConfidence,
			}
			if all {
				opts.Limit = 10_000_000
			}
			leagueStage, err := buildLeagueStage(cfg, catalogIdx, time.Now(), discoveryLogger{out: cmd.OutOrStderr(), component: "discover"})
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}
			if leagueStage != nil {
				opts.League = leagueStage.Index
			}

			result, err := entitydiscovery.Discover(cmd.Context(), articlesRepo, llm, proposalsRepo, catalogIdx, opts, time.Now().UTC(), verifier)
			if err != nil {
				return fmt.Errorf("entities discover: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "articles considered: %d\n", result.ArticlesConsidered)
			fmt.Fprintf(cmd.OutOrStdout(), "batches: %d (failed: %d)\n", result.BatchesTotal, result.BatchesFailed)
			fmt.Fprintf(cmd.OutOrStdout(), "proposals created/updated: %d\n", result.ProposalsUpserted)
			if opts.League != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "league: skipped %d as noise, %d already-known league entities\n", result.SkippedNoise, result.AlreadyKnownLeague)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "auto-rejected (non-NFL): %d\n", result.AutoRejected)
			fmt.Fprintf(cmd.OutOrStdout(), "uncertain (left pending): %d\n", result.Uncertain)
			fmt.Fprintf(cmd.OutOrStdout(), "duplicates (rejected, duplicate_of_existing): %d\n", result.Duplicates)
			v := result.Verification
			var x metricsExtras
			if extras != nil {
				x = extras()
			}
			out := cmd.OutOrStdout()
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

// llmFlagValues carries the --llm-* flag values shared by discover and
// proposals verify.
type llmFlagValues struct {
	baseURL, model  string
	maxTokens       int
	disableThinking bool
}

// resolveDiscoveryConfig layers the --llm-* flags over entity_discovery
// config (flags win only when set), enforces the enabled/override rule and
// validates. It returns the effective config plus max-tokens/thinking.
func resolveDiscoveryConfig(cmd *cobra.Command, cfg *config.Config, f llmFlagValues) (config.EntityDiscoveryConfig, int, bool, error) {
	discoveryCfg := cfg.EntityDiscovery
	if f.baseURL != "" {
		discoveryCfg.LLM.BaseURL = f.baseURL
	}
	if f.model != "" {
		discoveryCfg.LLM.Model = f.model
	}
	maxTokens, disableThinking := f.maxTokens, f.disableThinking
	if !cmd.Flags().Changed("llm-max-tokens") {
		maxTokens = discoveryCfg.LLM.MaxTokens
	}
	if !cmd.Flags().Changed("llm-disable-thinking") {
		disableThinking = discoveryCfg.LLM.DisableThinking
	}
	if !discoveryCfg.Enabled && f.baseURL == "" {
		return discoveryCfg, 0, false, fmt.Errorf("entity_discovery.enabled is false in config — pass --llm-base-url to override for a one-off run")
	}
	discoveryCfg.Enabled = true
	if err := discoveryCfg.Validate(); err != nil {
		return discoveryCfg, 0, false, err
	}
	return discoveryCfg, maxTokens, disableThinking, nil
}

// newDiscoveryLLM builds the LLM client from the effective discovery config.
func newDiscoveryLLM(cmd *cobra.Command, discoveryCfg config.EntityDiscoveryConfig, maxTokens int, disableThinking bool) *llmclient.Client {
	llmConfig := llmclient.Config{
		BaseURL: discoveryCfg.LLM.BaseURL, APIKey: discoveryCfg.LLM.APIKey,
		Model: discoveryCfg.LLM.Model, Temperature: discoveryCfg.LLM.Temperature,
		MaxTokens: maxTokens, Timeout: discoveryCfg.LLM.Timeout(),
		// Surface bounded transport retries and timeouts to the operator.
		// This command is an offline, interactive workflow, so a silent
		// logger turns a stalled local Ollama instance into an opaque list
		// of identical batch errors.
		Logger: discoveryLogger{out: cmd.OutOrStderr(), component: "llm"},
	}
	if disableThinking {
		llmConfig = llmConfig.WithThinkingDisabled()
	}
	return llmclient.New(llmConfig)
}

// buildVerifier validates the *effective* (enabled=true) web_lookup config
// and assembles the web searcher, cache and catalogverification.Service
// behind a verifierAdapter. Shared by discover and proposals verify.
func buildVerifier(cmd *cobra.Command, db *sql.DB, webLookupCfg config.WebLookupConfig, llm appout.EntityDiscoveryLLM) (verifierAdapter, error) {
	effective := webLookupCfg
	effective.Enabled = true
	if err := effective.Validate(); err != nil {
		return verifierAdapter{}, fmt.Errorf("catalog_maintenance.web_lookup: %w", err)
	}
	searcher, err := websearch.New(effective.Provider, websearch.SearXNGConfig{
		BaseURL:      effective.BaseURL,
		APIKey:       effective.APIKey,
		Timeout:      effective.Timeout(),
		EngineGroups: parseEngineGroups(effective.EngineGroups),
	})
	if err != nil {
		return verifierAdapter{}, fmt.Errorf("catalog_maintenance.web_lookup: %w", err)
	}
	if fb := effective.Fallback; fb.Provider != "" {
		if fb.APIKey == "" {
			return verifierAdapter{}, fmt.Errorf("catalog_maintenance.web_lookup.fallback: TAVILY_API_KEY environment variable is required when fallback.provider is set")
		}
		secondary, err := websearch.New(fb.Provider, websearch.SearXNGConfig{BaseURL: fb.BaseURL, APIKey: fb.APIKey, Timeout: effective.Timeout()})
		if err != nil {
			return verifierAdapter{}, fmt.Errorf("catalog_maintenance.web_lookup.fallback: %w", err)
		}
		logger := discoveryLogger{out: cmd.OutOrStderr(), component: "verify"}
		searcher = websearch.NewFallbackChain(searcher, secondary, fb.MaxSearchesPerRun, func(err error) {
			logger.Warn("primary search provider unavailable; using fallback provider", "fallback", fb.Provider, "error", err)
		})
	}
	return verifierAdapter{svc: &catalogverification.Service{
		LLM:      llm,
		Searcher: searcher,
		Cache:    sqlitewebsearchcache.NewCache(db),
		Logger:   discoveryLogger{out: cmd.OutOrStderr(), component: "verify"},
		Config:   effective,
	}}, nil
}

// verifierAdapter satisfies entitydiscovery.Verifier by translating to/
// from catalogverification's own request/result types.
// catalogverification.Service.Verify deliberately uses its own
// VerifyRequest/VerifyResult (not entitydiscovery.VerifyCandidate/
// VerifyResult) so that package stays reusable outside entitydiscovery;
// this adapter is the one place the two type sets meet.
type verifierAdapter struct{ svc *catalogverification.Service }

func (a verifierAdapter) Verify(ctx context.Context, c entitydiscovery.VerifyCandidate) entitydiscovery.VerifyResult {
	var articles []appout.EntityDiscoveryArticle
	for _, t := range c.Texts {
		articles = append(articles, appout.EntityDiscoveryArticle{ArticleID: t.ID, Title: t.Title, Summary: t.Summary})
	}
	r := a.svc.Verify(ctx, catalogverification.VerifyRequest{
		Name: c.Name, Type: string(c.Type), Metadata: c.Metadata,
		Confidence: c.Confidence, Sources: c.Sources, Articles: articles, FallbackHistory: c.FallbackHistory,
	})
	return entitydiscovery.VerifyResult{
		Disposition: r.Disposition, CanonicalName: r.CanonicalName,
		EntityType: entityproposal.EntityType(r.EntityType), Metadata: r.Metadata,
		Confidence: r.Confidence, WebSources: r.WebSources, Reason: r.Reason,
		Resolved: r.Disposition != weblookup.DispositionUncertain, ProviderError: r.ProviderError,
	}
}

// discoveryLogger is a minimal appout.Logger adapter over the command's
// stderr writer; entities discover has no existing logger dependency,
// and catalogverification.Service requires one.
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

// parseEngineGroups turns "bing,yahoo" entries into engine name lists,
// dropping blanks.
func parseEngineGroups(groups []string) [][]string {
	var out [][]string
	for _, g := range groups {
		var names []string
		for _, n := range strings.Split(g, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			out = append(out, names)
		}
	}
	return out
}
