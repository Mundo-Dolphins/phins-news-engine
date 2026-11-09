package entitywiring

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteknowledgecache "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteknowledgecache"
	sqlitewebsearchcache "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitewebsearchcache"
	"github.com/Mundo-Dolphins/phins-news-engine/app/catalogverification"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/app/knowledgecache"
	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguerules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/websearch"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/wikidata"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Stages carries the per-run verification stage overrides (the CLI's
// --wikidata, --no-wikidata, --fallback, --no-fallback, --offline and
// --no-llm switches). The zero value defers to the config.
type Stages struct{ Wikidata, NoWikidata, Fallback, NoFallback, Offline, NoLLM bool }

// chooserOn reports whether the league LLM chooser may run.
func (s Stages) chooserOn() bool { return !s.NoLLM && !s.Offline }

// ResolveStages resolves the effective stage toggles. Offline wins;
// NoX beats X beats config. The fallback stage defaults on only when both
// verification.fallback and webLookupEnabled are true.
func ResolveStages(vc config.VerificationConfig, webLookupEnabled bool, s Stages) (wikidata, fallback bool) {
	if s.Offline {
		return false, false
	}
	wikidata = vc.WikidataFirst
	if s.Wikidata {
		wikidata = true
	}
	if s.NoWikidata {
		wikidata = false
	}
	fallback = vc.Fallback && webLookupEnabled
	if s.Fallback {
		fallback = true
	}
	if s.NoFallback {
		fallback = false
	}
	return wikidata, fallback
}

// Extras are run-end counters that live outside entitydiscovery.
type Extras struct{ CacheHits, SearXNGSearches int }

// ChainVerifier assembles the Wikidata-first verification chain (league
// gazetteer, Wikidata, LLM + web-search fallback). It returns a nil
// verifier and nil extras (and builds no network objects) when no stage is
// on. The extras func reads the cache-hit and search counters at run end.
func ChainVerifier(ctx context.Context, db *sql.DB, cfg *config.Config, llm out.EntityDiscoveryLLM, catalog entitydiscovery.CatalogIndex, wikidataOn, fallbackOn, chooserOn bool, logs Loggers) (entitydiscovery.Verifier, func() Extras, error) {
	leagueOn := cfg.EntityDiscovery.League.Enabled
	if !wikidataOn && !fallbackOn && !leagueOn {
		return nil, nil, nil
	}
	logger := logs("verify")

	var fb entitydiscovery.Verifier
	var searches func() int
	if fallbackOn {
		adapter, err := buildVerifier(db, cfg.CatalogMaintenance.WebLookup, llm, logs)
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
	stage, err := LeagueStage(cfg, catalog, time.Now(), logger)
	if err != nil {
		return nil, nil, err
	}
	if stage != nil {
		stage.Texts = articleTexts{finder: sqliteadapter.NewArticleRepository(db)}
		if lc := cfg.EntityDiscovery.League.Chooser; chooserOn && lc.Enabled {
			if ch, ok := llm.(out.CandidateChooser); ok {
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
		if _, err := cached.Purge(ctx, wcfg.CacheRetention); err != nil {
			logger.Warn("knowledge cache purge failed", "error", err)
		}
		chain.Knowledge = cached
	}

	extras := func() Extras {
		var x Extras
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

// buildVerifier validates the *effective* (enabled=true) web_lookup config
// and assembles the web searcher, cache and catalogverification.Service
// behind a verifierAdapter.
func buildVerifier(db *sql.DB, webLookupCfg config.WebLookupConfig, llm out.EntityDiscoveryLLM, logs Loggers) (verifierAdapter, error) {
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
		logger := logs("verify")
		searcher = websearch.NewFallbackChain(searcher, secondary, fb.MaxSearchesPerRun, func(err error) {
			logger.Warn("primary search provider unavailable; using fallback provider", "fallback", fb.Provider, "error", err)
		})
	}
	return verifierAdapter{svc: &catalogverification.Service{
		LLM:      llm,
		Searcher: searcher,
		Cache:    sqlitewebsearchcache.NewCache(db),
		Logger:   logs("verify"),
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
	var articles []out.EntityDiscoveryArticle
	for _, t := range c.Texts {
		articles = append(articles, out.EntityDiscoveryArticle{ArticleID: t.ID, Title: t.Title, Summary: t.Summary})
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

// LeagueStage assembles the league gazetteer stage. It returns nil when the
// league block is disabled; with it enabled, an invalid rules file or a
// rules team missing from the catalog is a start-up error (never a silent
// downgrade). A missing, stale or corrupt roster only disables player tiers
// (logged on log).
func LeagueStage(cfg *config.Config, catalog entitydiscovery.CatalogIndex, now time.Time, log out.Logger) (*entitydiscovery.LeagueStage, error) {
	if !cfg.EntityDiscovery.League.Enabled {
		return nil, nil
	}
	rules, err := leaguerules.Load(cfg.EntityDiscovery.League.RulesPath)
	if err != nil {
		return nil, fmt.Errorf("entity_discovery.league: %w", err)
	}
	idx, err := leaguegazetteer.New(loadLeaguePlayers(cfg.EntityDiscovery.Roster, now, log), rules)
	if err != nil {
		return nil, fmt.Errorf("entity_discovery.league: %w", err)
	}
	stage, err := entitydiscovery.NewLeagueStage(idx, catalog)
	if err != nil {
		return nil, fmt.Errorf("entity_discovery.league: %w", err)
	}
	return stage, nil
}

// loadLeaguePlayers reads the local roster. A disabled block, or a missing, stale
// or corrupt file, yields nil players and a warning: team logic and the noise
// filter still work without a roster.
func loadLeaguePlayers(cfg config.RosterConfig, now time.Time, logger out.Logger) []leaguegazetteer.Player {
	if !cfg.Enabled {
		return nil
	}
	info, err := os.Stat(cfg.Path)
	if err != nil {
		logger.Warn("nflverse roster unavailable; run `entities roster sync`", "path", cfg.Path, "error", err)
		return nil
	}
	if cfg.MaxAgeDays > 0 && now.Sub(info.ModTime()) > time.Duration(cfg.MaxAgeDays)*24*time.Hour {
		logger.Warn("nflverse roster is stale; player tiers disabled, run `entities roster sync`", "path", cfg.Path, "modified", info.ModTime().Format(time.RFC3339), "max_age_days", cfg.MaxAgeDays)
		return nil
	}
	f, err := os.Open(cfg.Path)
	if err != nil {
		logger.Warn("nflverse roster unreadable", "path", cfg.Path, "error", err)
		return nil
	}
	defer f.Close()
	rows, err := nflverse.ParseRoster(f)
	if err != nil {
		logger.Warn("nflverse roster invalid; player tiers disabled", "path", cfg.Path, "error", err)
		return nil
	}
	players := make([]leaguegazetteer.Player, len(rows))
	for i, p := range rows {
		players[i] = leaguegazetteer.Player{
			FullName: p.FullName, FirstName: p.FirstName, LastName: p.LastName, FootballName: p.FootballName,
			TeamAbbr: p.Team, Position: p.Position, Status: p.Status, GSISID: p.GSISID,
		}
	}
	return players
}

type articleFinder interface {
	FindByID(ctx context.Context, id string) (article.Article, error)
}

// articleTexts implements entitydiscovery.ArticleTextLoader over the article repository.
type articleTexts struct{ finder articleFinder }

var _ entitydiscovery.ArticleTextLoader = articleTexts{}

func (l articleTexts) Texts(ctx context.Context, ids []string) ([]entitydiscovery.ArticleText, error) {
	texts := make([]entitydiscovery.ArticleText, 0, len(ids))
	for _, id := range ids {
		a, err := l.finder.FindByID(ctx, id)
		if err != nil {
			continue
		}
		texts = append(texts, entitydiscovery.ArticleText{ID: a.ID, Title: a.Title, Summary: a.Summary})
	}
	return texts, nil
}
