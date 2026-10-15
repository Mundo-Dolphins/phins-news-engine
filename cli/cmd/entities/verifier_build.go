// cli/cmd/entities/verifier_build.go
package entities

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteknowledgecache "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteknowledgecache"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/app/knowledgecache"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/wikidata"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// verificationFlags carries the per-run verification stage overrides.
type verificationFlags struct{ wikidata, noWikidata, fallback, noFallback, offline, noLLM bool }

// metricsExtras are run-end counters that live outside entitydiscovery.
type metricsExtras struct{ CacheHits, SearXNGSearches int }

// addVerificationFlags registers the verification stage flags.
func addVerificationFlags(cmd *cobra.Command, f *verificationFlags) {
	cmd.Flags().BoolVar(&f.wikidata, "wikidata", false, "verify candidates against Wikidata first (overrides entity_discovery.verification.wikidata_first)")
	cmd.Flags().BoolVar(&f.noWikidata, "no-wikidata", false, "disable the Wikidata-first stage for this run")
	cmd.Flags().BoolVar(&f.fallback, "fallback", false, "enable the LLM + SearXNG fallback stage for this run")
	cmd.Flags().BoolVar(&f.noFallback, "no-fallback", false, "disable the LLM + SearXNG fallback stage for this run")
	cmd.Flags().BoolVar(&f.offline, "offline", false, "no network verification at all (disables both stages)")
	cmd.Flags().BoolVar(&f.noLLM, "no-llm", false, "do not call the LLM chooser for ambiguous league mentions in this run")
	cmd.MarkFlagsMutuallyExclusive("wikidata", "no-wikidata")
	cmd.MarkFlagsMutuallyExclusive("fallback", "no-fallback")
}

// resolveVerificationStages resolves the effective toggles. offline wins;
// --no-X beats --X beats config.
func resolveVerificationStages(cfg config.VerificationConfig, webLookupEnabled bool, f verificationFlags) (wikidata, fallback bool) {
	if f.offline {
		return false, false
	}
	wikidata = cfg.WikidataFirst
	if f.wikidata {
		wikidata = true
	}
	if f.noWikidata {
		wikidata = false
	}
	fallback = cfg.Fallback && webLookupEnabled
	if f.fallback {
		fallback = true
	}
	if f.noFallback {
		fallback = false
	}
	return wikidata, fallback
}

// buildChainVerifier assembles the Wikidata-first chain. It returns a nil
// verifier (and no network objects) when neither stage is on.
func buildChainVerifier(cmd *cobra.Command, db *sql.DB, cfg *config.Config, llm appout.EntityDiscoveryLLM, catalog entitydiscovery.CatalogIndex, wikidataOn, fallbackOn, chooserOn bool) (entitydiscovery.Verifier, func() metricsExtras, error) {
	leagueOn := cfg.EntityDiscovery.League.Enabled
	if !wikidataOn && !fallbackOn && !leagueOn {
		return nil, nil, nil
	}
	logger := discoveryLogger{out: cmd.OutOrStderr(), component: "verify"}

	var fb entitydiscovery.Verifier
	var searches func() int
	if fallbackOn {
		adapter, err := buildVerifier(cmd, db, cfg.CatalogMaintenance.WebLookup, llm)
		if err != nil {
			return nil, nil, err
		}
		fb = adapter
		searches = adapter.svc.Searches
	}

	chain := &entitydiscovery.ChainVerifier{
		Fallback: fb, Catalog: catalog,
		MaxCandidates: cfg.EntityDiscovery.Wikidata.MaxCandidates, Logger: logger,
	}
	stage, err := buildLeagueStage(cfg, catalog, time.Now(), logger)
	if err != nil {
		return nil, nil, err
	}
	if stage != nil {
		stage.Texts = articleTexts{finder: sqliteadapter.NewArticleRepository(db)}
		if lc := cfg.EntityDiscovery.League.Chooser; chooserOn && lc.Enabled {
			if ch, ok := llm.(appout.CandidateChooser); ok {
				stage.Chooser = ch
				stage.MaxArticles, stage.MaxCandidates = lc.MaxArticles, lc.MaxCandidates
			}
		}
	}
	chain.League = stage
	var cached *knowledgecache.Cached
	if wikidataOn {
		wcfg := cfg.EntityDiscovery.Wikidata
		wcfg.Enabled = true
		if err := wcfg.Validate(); err != nil {
			return nil, nil, fmt.Errorf("entity_discovery.wikidata: %w", err)
		}
		client, err := wikidata.New(wikidata.Config{
			BaseURL: wcfg.BaseURL, Language: wcfg.Language, UserAgent: wcfg.UserAgent,
			Timeout: wcfg.Timeout(), MaxCandidates: wcfg.MaxCandidates,
			MaxConcurrency: wcfg.Concurrency(), Properties: wcfg.Properties,
			AllowPrivateHosts: wcfg.AllowPrivateHosts,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("entity_discovery.wikidata: %w", err)
		}
		cached = knowledgecache.New(client, sqliteknowledgecache.NewCache(db), clock.Real{}, knowledgecache.Config{
			Provider: "wikidata", Language: wcfg.Language,
			PositiveTTL: wcfg.PositiveCacheTTL, NegativeTTL: wcfg.NegativeCacheTTL,
			AllowStaleOnError: wcfg.AllowStaleOnError,
		})
		if _, err := cached.Purge(cmd.Context(), wcfg.CacheRetention); err != nil {
			logger.Warn("knowledge cache purge failed", "error", err)
		}
		chain.Knowledge = cached
	}

	extras := func() metricsExtras {
		var x metricsExtras
		if cached != nil {
			st := cached.Stats()
			x.CacheHits = int(st.SearchHits + st.EntityHits)
		}
		if searches != nil {
			x.SearXNGSearches = searches()
		}
		return x
	}
	return chain, extras, nil
}
