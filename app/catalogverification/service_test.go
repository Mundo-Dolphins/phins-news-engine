// app/catalogverification/service_test.go
package catalogverification_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/catalogverification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeLLM struct {
	actions  []weblookup.AnalysisAction
	calls    int
	requests []appout.VerifyCandidateRequest
}

func (f *fakeLLM) Discover(context.Context, []appout.EntityDiscoveryArticle) (appout.EntityDiscoveryResult, error) {
	return appout.EntityDiscoveryResult{}, nil
}

func (f *fakeLLM) VerifyCandidate(_ context.Context, req appout.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	f.requests = append(f.requests, req)
	a := f.actions[f.calls]
	f.calls++
	return a, nil
}

type erroringLLM struct{ err error }

func (e erroringLLM) Discover(context.Context, []appout.EntityDiscoveryArticle) (appout.EntityDiscoveryResult, error) {
	return appout.EntityDiscoveryResult{}, nil
}

func (e erroringLLM) VerifyCandidate(context.Context, appout.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	return weblookup.AnalysisAction{}, e.err
}

type fakeSearcher struct {
	results []weblookup.SearchResult
	err     error
	calls   int
}

func (f *fakeSearcher) Search(_ context.Context, _ string, _ appout.SearchOptions) ([]weblookup.SearchResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

type fakeCache struct {
	store map[string][]weblookup.SearchResult
	gets  int
	sets  int
}

func newFakeCache() *fakeCache { return &fakeCache{store: map[string][]weblookup.SearchResult{}} }

func (c *fakeCache) Get(_ context.Context, queryHash string) ([]weblookup.SearchResult, bool, error) {
	c.gets++
	r, ok := c.store[queryHash]
	return r, ok, nil
}

func (c *fakeCache) Set(_ context.Context, queryHash, _, _ string, results []weblookup.SearchResult, _ time.Duration) error {
	c.sets++
	c.store[queryHash] = results
	return nil
}

type fakeLogger struct{ infos, warns []string }

func (f *fakeLogger) Debug(string, ...any)        {}
func (f *fakeLogger) Info(msg string, _ ...any)   { f.infos = append(f.infos, msg) }
func (f *fakeLogger) Warn(msg string, _ ...any)   { f.warns = append(f.warns, msg) }
func (f *fakeLogger) Error(string, error, ...any) {}

func testConfig() config.WebLookupConfig {
	return config.WebLookupConfig{
		Enabled: true, Provider: "test", TimeoutSeconds: 5,
		MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50,
		TrustedDomains: []string{"isotopes.example.com"},
	}
}

func keepFinal() weblookup.AnalysisAction {
	return weblookup.AnalysisAction{Action: weblookup.ActionFinal, Disposition: weblookup.DispositionKeep}
}

func TestVerify_FinalActionWithoutEvidenceIsUncertainWithNoSearch(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{keepFinal()}}
	searcher := &fakeSearcher{}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Jeff Harrowby", Type: "coach", Confidence: 0.9})
	if result.Resolved || result.Disposition != weblookup.DispositionUncertain {
		t.Fatalf("Verify() = %+v, want Uncertain: keep without trusted evidence must not resolve", result)
	}
	if searcher.calls != 0 {
		t.Fatalf("searcher.calls = %d, want 0 (LLM decided local evidence was enough)", searcher.calls)
	}
}

func TestVerify_WebSearchActionExecutesBoundedSearch(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: `"Jai'Rene' Mossbank" Isotopes`},
		keepFinal(),
	}}
	searcher := &fakeSearcher{results: []weblookup.SearchResult{
		{Title: "Isotopes Add CB Jai'Rene' Mossbank", Domain: "isotopes.example.com", URL: "https://isotopes.example.com/news/1", Trusted: true},
	}}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Jai'Rene' Mossbank", Type: "player", Confidence: 0.4})
	if !result.Resolved {
		t.Fatal("Verify() Resolved = false, want true")
	}
	if searcher.calls != 1 {
		t.Fatalf("searcher.calls = %d, want exactly 1", searcher.calls)
	}
	if len(result.WebSources) != 1 || result.WebSources[0].Domain != "isotopes.example.com" {
		t.Fatalf("WebSources = %+v, want one isotopes.example.com entry", result.WebSources)
	}
	if result.Confidence <= 0.4 {
		t.Fatalf("Confidence = %v, want an increase over the base 0.4 after a trusted source", result.Confidence)
	}
}

// TestVerify_MapsSourcesToLocalEvidence verifies that Verify maps
// VerifyRequest.Sources into the outgoing VerifyCandidateRequest's
// LocalEvidence field (final review finding I1) — without this mapping
// the LLM's LOCAL_EVIDENCE prompt section is always empty, defeating
// the "use local evidence before searching" instruction in
// llmclient.verifySystemPrompt.
func TestVerify_MapsSourcesToLocalEvidence(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{{Action: weblookup.ActionFinal}}}
	svc := &catalogverification.Service{LLM: llm, Searcher: &fakeSearcher{}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	sources := []entityproposal.Source{
		{ArticleID: "article-1", ArticleTitle: "Isotopes sign Jai'Rene' Mossbank"},
		{ArticleID: "article-2", ArticleTitle: "Isotopes camp roundup"},
	}
	svc.Verify(context.Background(), catalogverification.VerifyRequest{
		Name: "Jai'Rene' Mossbank", Type: "player", Confidence: 0.4, Sources: sources,
	})

	if len(llm.requests) != 1 {
		t.Fatalf("llm.requests count = %d, want 1", len(llm.requests))
	}
	got := llm.requests[0].LocalEvidence
	want := []appout.EntityDiscoveryArticle{
		{ArticleID: "article-1", Title: "Isotopes sign Jai'Rene' Mossbank"},
		{ArticleID: "article-2", Title: "Isotopes camp roundup"},
	}
	if len(got) != len(want) {
		t.Fatalf("LocalEvidence = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LocalEvidence[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestVerify_UntrustedDomainResultDropped(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "some unknown player"},
		{Action: weblookup.ActionFinal},
	}}
	searcher := &fakeSearcher{results: []weblookup.SearchResult{
		{Title: "Random Blog Post", Domain: "randomblog.example", URL: "https://randomblog.example/x", Trusted: false},
	}}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if len(result.WebSources) != 0 {
		t.Fatalf("WebSources = %+v, want none (untrusted result must be dropped before reaching evidence)", result.WebSources)
	}
	// The untrusted result must also never reach the LLM prompt itself,
	// not just be absent from the final WebSources (Task 10 finding 4).
	if len(llm.requests) != 2 {
		t.Fatalf("llm.requests = %d, want 2 (initial step + step after the search)", len(llm.requests))
	}
	if got := llm.requests[1].SearchResults; len(got) != 0 {
		t.Fatalf("SearchResults on the step after an untrusted-only search = %+v, want empty", got)
	}
}

func TestVerify_SearchProviderErrorDegradesToUnresolved(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{{Action: weblookup.ActionWebSearch, Query: "Some Player Isotopes"}}}
	searcher := &fakeSearcher{err: context.DeadlineExceeded}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if result.Resolved {
		t.Fatal("Verify() Resolved = true, want false when the search provider errors")
	}
}

func TestVerify_HallucinatedActionDegradesToUnresolved(t *testing.T) {
	svcLLM := &fakeLLM{actions: []weblookup.AnalysisAction{{Action: "browse_url", Query: "http://example.com"}}}
	svc := &catalogverification.Service{LLM: svcLLM, Searcher: &fakeSearcher{}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if result.Resolved {
		t.Fatal("Verify() Resolved = true, want false for a hallucinated action")
	}
}

func TestVerify_LLMErrorDegradesToUnresolved(t *testing.T) {
	svc := &catalogverification.Service{LLM: erroringLLM{err: context.DeadlineExceeded}, Searcher: &fakeSearcher{}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if result.Resolved {
		t.Fatal("Verify() Resolved = true, want false when the LLM call itself errors")
	}
}

func TestVerify_BudgetExhaustedStopsLoop(t *testing.T) {
	// Three web_search actions in a row exceed MaxSearchesPerCandidate (2).
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "query one"},
		{Action: weblookup.ActionWebSearch, Query: "query two"},
		{Action: weblookup.ActionWebSearch, Query: "query three"},
	}}
	searcher := &fakeSearcher{results: []weblookup.SearchResult{{Domain: "isotopes.example.com", URL: "https://isotopes.example.com/x", Trusted: true}}}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if searcher.calls > 2 {
		t.Fatalf("searcher.calls = %d, want at most MaxSearchesPerCandidate (2)", searcher.calls)
	}
	if result.Resolved {
		t.Fatal("Verify() Resolved = true, want false when the budget is exhausted without a final action")
	}
}

func TestVerify_RepeatedQueryHitsCache(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "Some Player Isotopes"},
		{Action: weblookup.ActionWebSearch, Query: "Some Player Isotopes"},
		{Action: weblookup.ActionFinal},
	}}
	searcher := &fakeSearcher{results: []weblookup.SearchResult{{Domain: "isotopes.example.com", URL: "https://isotopes.example.com/x", Trusted: true}}}
	cache := newFakeCache()
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: cache, Logger: &fakeLogger{}, Config: testConfig()}

	svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if searcher.calls != 1 {
		t.Fatalf("searcher.calls = %d, want exactly 1 (the second identical query must hit the cache)", searcher.calls)
	}
	if cache.sets != 1 {
		t.Fatalf("cache.sets = %d, want 1", cache.sets)
	}
}

func TestVerify_CacheHitDoesNotConsumeProviderSearchBudget(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSearchesPerCandidate = 1
	cache := newFakeCache()
	searcher := &fakeSearcher{results: trustedHit()}

	// Candidate one pays a provider search for "Some Player Isotopes" and caches it.
	first := &fakeLLM{actions: []weblookup.AnalysisAction{{Action: weblookup.ActionWebSearch, Query: "Some Player Isotopes"}, keepFinal()}}
	svc1 := &catalogverification.Service{LLM: first, Searcher: searcher, Cache: cache, Logger: &fakeLogger{}, Config: cfg}
	_ = svc1.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})

	// Candidate two reads that query from the cache and still has its single
	// provider-search slot for a different query.
	second := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "Some Player Isotopes"},
		{Action: weblookup.ActionWebSearch, Query: "Some Player NFL"},
		keepFinal(),
	}}
	svc2 := &catalogverification.Service{LLM: second, Searcher: searcher, Cache: cache, Logger: &fakeLogger{}, Config: cfg}
	result := svc2.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if searcher.calls != 2 {
		t.Fatalf("searcher.calls = %d, want 2 (one per distinct uncached query): the cache hit must not consume the provider-search slot", searcher.calls)
	}
	if !result.Resolved {
		t.Fatalf("Verify() = %+v, want a resolved result", result)
	}
}

func TestVerify_MaxSearchesPerRunSurvivesAcrossVerifyCalls(t *testing.T) {
	// One Service instance, two separate Verify calls: the run-level
	// budget must be enforced across both, not reset per call
	// (Task 10 finding 3).
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "query one"},
		keepFinal(),
		{Action: weblookup.ActionWebSearch, Query: "query two"},
	}}
	searcher := &fakeSearcher{results: []weblookup.SearchResult{
		{Domain: "isotopes.example.com", URL: "https://isotopes.example.com/x", Trusted: true},
	}}
	cfg := testConfig()
	cfg.MaxSearchesPerRun = 1
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: cfg}

	first := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Player One", Type: "player", Confidence: 0.4})
	if !first.Resolved {
		t.Fatal("first Verify() Resolved = false, want true (within the run budget)")
	}
	if searcher.calls != 1 {
		t.Fatalf("after first Verify(), searcher.calls = %d, want 1", searcher.calls)
	}
	if svc.Searches() != 1 {
		t.Fatalf("Searches() = %d, want 1", svc.Searches())
	}

	second := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Player Two", Type: "player", Confidence: 0.4})
	if second.Resolved {
		t.Fatal("second Verify() Resolved = true, want false (run budget already exhausted by the first call)")
	}
	if searcher.calls != 1 {
		t.Fatalf("after second Verify(), searcher.calls = %d, want still 1 (run budget must refuse the second candidate's search)", searcher.calls)
	}
}

func TestVerify_InvalidQueryDegradesToUnresolvedWithoutSearching(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "<script>"},
	}}
	searcher := &fakeSearcher{}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if result.Resolved {
		t.Fatal("Verify() Resolved = true, want false for an invalid LLM-proposed query")
	}
	if searcher.calls != 0 {
		t.Fatalf("searcher.calls = %d, want 0 (an invalid query must never reach the searcher)", searcher.calls)
	}
}

// TestVerify_StaleCachedTrustedResultDroppedWhenDomainNoLongerTrusted
// verifies the cache-hit path re-checks TrustedDomains, not just the
// persisted Trusted flag: a result cached while a domain
// was trusted must be dropped once that domain is removed from
// TrustedDomains, even though it is still marked Trusted in the cache.
func TestVerify_StaleCachedTrustedResultDroppedWhenDomainNoLongerTrusted(t *testing.T) {
	cache := newFakeCache()

	// First Service instance: "randomblog.example" is (unusually) trusted,
	// so its result gets cached with Trusted: true.
	seedCfg := testConfig()
	seedCfg.TrustedDomains = []string{"randomblog.example"}
	seedLLM := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "Some Player Isotopes"},
		{Action: weblookup.ActionFinal},
	}}
	seedSearcher := &fakeSearcher{results: []weblookup.SearchResult{
		{Title: "Random Blog Post", Domain: "randomblog.example", URL: "https://randomblog.example/x", Trusted: true},
	}}
	seedSvc := &catalogverification.Service{LLM: seedLLM, Searcher: seedSearcher, Cache: cache, Logger: &fakeLogger{}, Config: seedCfg}
	seedResult := seedSvc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if len(seedResult.WebSources) != 1 {
		t.Fatalf("seed WebSources = %+v, want 1 (randomblog.example was trusted for this run)", seedResult.WebSources)
	}

	// Second Service instance, sharing the same cache: randomblog.example
	// is no longer in TrustedDomains. The identical query hits the cache,
	// but the stale Trusted:true entry must still be dropped.
	narrowCfg := testConfig() // TrustedDomains: []string{"isotopes.example.com"}
	narrowLLM := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "Some Player Isotopes"},
		{Action: weblookup.ActionFinal},
	}}
	narrowSvc := &catalogverification.Service{LLM: narrowLLM, Searcher: &fakeSearcher{}, Cache: cache, Logger: &fakeLogger{}, Config: narrowCfg}
	result := narrowSvc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})

	if len(result.WebSources) != 0 {
		t.Fatalf("WebSources = %+v, want none (cached result's domain is no longer trusted)", result.WebSources)
	}
	if got := narrowLLM.requests[1].SearchResults; len(got) != 0 {
		t.Fatalf("SearchResults on the step after the stale cache hit = %+v, want empty", got)
	}
	if cache.gets == 0 {
		t.Fatal("cache.gets = 0, want at least 1 (the second query must have hit the cache)")
	}
}

func searchAction(q string) weblookup.AnalysisAction {
	return weblookup.AnalysisAction{Action: weblookup.ActionWebSearch, Query: q}
}

func hasMsg(msgs []string, want string) bool {
	for _, m := range msgs {
		if m == want {
			return true
		}
	}
	return false
}

func TestVerify_RepeatedQueryAfterZeroFreshResultsIsTerminal(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		searchAction("Malik Isotopes"), searchAction("Malik Isotopes"), {Action: weblookup.ActionFinal},
	}}
	searcher := &fakeSearcher{}
	cache := newFakeCache()
	logger := &fakeLogger{}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: cache, Logger: logger, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Malik", Type: "player", Confidence: 0.4})
	if result.Resolved {
		t.Fatal("Resolved = true, want false for a repeated no-evidence query")
	}
	if searcher.calls != 1 {
		t.Fatalf("searcher.calls = %d, want 1 (no second provider call)", searcher.calls)
	}
	if llm.calls != 2 {
		t.Fatalf("llm.calls = %d, want 2 (repeat must not consume more rounds)", llm.calls)
	}
	if cache.gets != 1 {
		t.Fatalf("cache.gets = %d, want 1 (repeat short-circuits before the cache)", cache.gets)
	}
	if !hasMsg(logger.warns, "catalog verification: repeated search has no new evidence") {
		t.Fatalf("warns = %v, want repeated-search reason", logger.warns)
	}
	if !hasMsg(logger.infos, "catalog verification: search executed") {
		t.Fatalf("infos = %v, want search executed log", logger.infos)
	}
	if cache.sets != 1 {
		t.Fatalf("cache.sets = %d, want 1 (empty results stay cacheable)", cache.sets)
	}
}

func TestVerify_CachedZeroResultQueryRepeatedIsTerminal(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		searchAction("Malik Isotopes"), searchAction("  malik   ISOTOPES "), {Action: weblookup.ActionFinal},
	}}
	searcher := &fakeSearcher{}
	cache := newFakeCache()
	svc0 := &catalogverification.Service{LLM: &fakeLLM{actions: []weblookup.AnalysisAction{searchAction("Malik Isotopes"), {Action: weblookup.ActionFinal}}}, Searcher: searcher, Cache: cache, Logger: &fakeLogger{}, Config: testConfig()}
	svc0.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Malik"}) // primes empty cache entry
	searcher.calls = 0

	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: cache, Logger: &fakeLogger{}, Config: testConfig()}
	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Malik", Type: "player"})
	if result.Resolved {
		t.Fatal("Resolved = true, want false")
	}
	if searcher.calls != 0 {
		t.Fatalf("searcher.calls = %d, want 0", searcher.calls)
	}
	if llm.calls != 2 {
		t.Fatalf("llm.calls = %d, want 2", llm.calls)
	}
}

func TestVerify_DifferentQueryAfterEmptyResultStillExecutes(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		searchAction("query one"), searchAction("query two"), keepFinal(),
	}}
	searcher := &fakeSearcher{}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Malik", Confidence: 0.4})
	if result.Resolved {
		t.Fatal("Resolved = true, want false: both searches were empty so a keep has no trusted evidence")
	}
	if searcher.calls != 2 {
		t.Fatalf("searcher.calls = %d, want 2", searcher.calls)
	}
}

func TestVerify_CachedNonEmptyEvidenceReusedWithoutDuplicates(t *testing.T) {
	res := []weblookup.SearchResult{{Domain: "isotopes.example.com", URL: "https://isotopes.example.com/x", Trusted: true}}
	cache := newFakeCache()
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		searchAction("Some Player"), searchAction("Some Player"), keepFinal(),
	}}
	searcher := &fakeSearcher{results: res}
	// Prime the cache so both requests are cache hits.
	prime := &catalogverification.Service{LLM: &fakeLLM{actions: []weblookup.AnalysisAction{searchAction("Some Player"), {Action: weblookup.ActionFinal}}}, Searcher: searcher, Cache: cache, Logger: &fakeLogger{}, Config: testConfig()}
	prime.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player"})
	searcher.calls = 0

	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: cache, Logger: &fakeLogger{}, Config: testConfig()}
	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Confidence: 0.4})
	if !result.Resolved {
		t.Fatal("Resolved = false, want true")
	}
	if len(result.WebSources) != 1 {
		t.Fatalf("WebSources = %+v, want exactly 1", result.WebSources)
	}
	if searcher.calls != 0 {
		t.Fatalf("searcher.calls = %d, want 0", searcher.calls)
	}
}

func TestVerify_DifferentRepeatedActionsStayBoundedByMaxSteps(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		searchAction("q1"), searchAction("q2"), searchAction("q3"), {Action: weblookup.ActionFinal},
	}}
	cfg := testConfig()
	cfg.MaxSearchesPerCandidate = 10
	logger := &fakeLogger{}
	svc := &catalogverification.Service{LLM: llm, Searcher: &fakeSearcher{}, Cache: newFakeCache(), Logger: logger, Config: cfg}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Malik"})
	if result.Resolved || llm.calls != 3 {
		t.Fatalf("Resolved=%v llm.calls=%d, want false/3", result.Resolved, llm.calls)
	}
	// The last step is final-only, so a model that still asks for a search is
	// reported as ignoring that instruction rather than as an exhausted loop.
	if !hasMsg(logger.warns, "catalog verification: model ignored the final-only instruction") {
		t.Fatalf("warns = %v, want the final-only warning", logger.warns)
	}
}

func src(domain string) []entityproposal.WebSource {
	return []entityproposal.WebSource{{Domain: domain, URL: "https://" + domain + "/x", Title: "t"}}
}

func TestNormalizeFinal(t *testing.T) {
	trusted := src("sportsnet.example.com")
	act := func(d weblookup.Disposition, typ string, md map[string]string) weblookup.AnalysisAction {
		return weblookup.AnalysisAction{Action: weblookup.ActionFinal, Disposition: d, EntityType: typ, Metadata: md}
	}
	tests := []struct {
		name    string
		req     catalogverification.VerifyRequest
		a       weblookup.AnalysisAction
		sources []entityproposal.WebSource
		want    weblookup.Disposition
		wantTyp string
		check   func(t *testing.T, r catalogverification.VerifyResult)
	}{
		{
			"mackerels reject MLB",
			catalogverification.VerifyRequest{Name: "Mackerels", Type: "team"},
			act(weblookup.DispositionReject, "team", map[string]string{"league": "MLB"}), trusted, weblookup.DispositionReject, "team", nil,
		},
		{
			"springfield tempests reject NCAA",
			catalogverification.VerifyRequest{Name: "Springfield Tempests", Type: "team"},
			act(weblookup.DispositionReject, "team", map[string]string{"league": "NCAA"}), trusted, weblookup.DispositionReject, "team", nil,
		},
		{
			"generic Springfield keep is uncertain",
			catalogverification.VerifyRequest{Name: "Springfield", Type: "team"},
			act(weblookup.DispositionKeep, "team", map[string]string{"league": "NFL", "sport": "football"}), trusted, weblookup.DispositionUncertain, "team", nil,
		},
		// NormalizeFinal cannot validate team_id (no catalog); a made-up or
		// existing-team id is turned back into uncertain by
		// entitydiscovery.ApplyVerification (see TestApply_TeamKeepGuards).
		{
			"team keep with team_id passes to catalog validation",
			catalogverification.VerifyRequest{Name: "Springfield", Type: "team"},
			act(weblookup.DispositionKeep, "team", map[string]string{"league": "NFL", "team_id": "springfield-isotopes"}), trusted, weblookup.DispositionKeep, "team", nil,
		},
		{
			"ostrander keep",
			catalogverification.VerifyRequest{Name: "Patrick Ostrander", Type: "player", Metadata: map[string]string{"position": "x"}},
			act(weblookup.DispositionKeep, "player", map[string]string{"position": "QB", "team": "Capital City Capitals", "league": "NFL", "sport": ""}), trusted, weblookup.DispositionKeep, "player",
			func(t *testing.T, r catalogverification.VerifyResult) {
				if r.Metadata["team"] != "Capital City Capitals" || r.Metadata["position"] != "QB" {
					t.Fatalf("Metadata = %v", r.Metadata)
				}
			},
		},
		{
			"fennow coach",
			catalogverification.VerifyRequest{Name: "Brian Fennow", Type: "player"},
			act(weblookup.DispositionKeep, "coach", map[string]string{"role": "defensive coordinator", "team": "Cypress Comets"}), trusted, weblookup.DispositionKeep, "coach", nil,
		},
		{
			"reject without sources is uncertain",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionReject, "player", nil), nil, weblookup.DispositionUncertain, "player",
			func(t *testing.T, r catalogverification.VerifyResult) {
				if r.Reason != "no trusted evidence" || r.Resolved {
					t.Fatalf("result = %+v", r)
				}
			},
		},
		{
			"reject claiming NFL is self-contradicting -> uncertain",
			catalogverification.VerifyRequest{Name: "Patrick Ostrander", Type: "player"},
			act(weblookup.DispositionReject, "player", map[string]string{"league": "nfl"}), trusted, weblookup.DispositionUncertain, "player",
			func(t *testing.T, r catalogverification.VerifyResult) {
				if r.Reason == "" {
					t.Fatal("want a reason noting the contradiction")
				}
			},
		},
		{
			"reject claiming football without league -> uncertain",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionReject, "player", map[string]string{"sport": "Football"}), trusted, weblookup.DispositionUncertain, "player", nil,
		},
		{
			"reject NCAA football still rejects",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionReject, "player", map[string]string{"league": "NCAA", "sport": "football"}), trusted, weblookup.DispositionReject, "player", nil,
		},
		{
			"keep MLB with sources rejects",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionKeep, "player", map[string]string{"league": "mlb"}), trusted, weblookup.DispositionReject, "player", nil,
		},
		{
			"keep MLB without sources uncertain",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionKeep, "player", map[string]string{"league": "MLB"}), nil, weblookup.DispositionUncertain, "player", nil,
		},
		{
			"keep NCAA non-prospect rejects",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionKeep, "player", map[string]string{"league": "NCAA"}), trusted, weblookup.DispositionReject, "player", nil,
		},
		{
			"keep NCAA draft prospect keeps",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionKeep, "player", map[string]string{"league": "NCAA", "nfl_context": "draft_prospect"}), trusted, weblookup.DispositionKeep, "player", nil,
		},
		{
			"keep non-football sport rejects",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionKeep, "player", map[string]string{"sport": "baseball"}), trusted, weblookup.DispositionReject, "player", nil,
		},
		{
			"invalid entity type keeps original",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act(weblookup.DispositionKeep, "mascot", nil), trusted, weblookup.DispositionKeep, "player", nil,
		},
		{
			"unknown disposition uncertain",
			catalogverification.VerifyRequest{Name: "X", Type: "player"},
			act("banana", "player", nil), trusted, weblookup.DispositionUncertain, "player", nil,
		},
		{
			"empty llm metadata values do not overwrite",
			catalogverification.VerifyRequest{Name: "X", Type: "player", Metadata: map[string]string{"team": "Springfield Isotopes"}},
			act(weblookup.DispositionKeep, "player", map[string]string{"team": ""}), trusted, weblookup.DispositionKeep, "player",
			func(t *testing.T, r catalogverification.VerifyResult) {
				if r.Metadata["team"] != "Springfield Isotopes" {
					t.Fatalf("Metadata = %v", r.Metadata)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := catalogverification.NormalizeFinal(tt.req, tt.a, tt.sources)
			if r.Disposition != tt.want {
				t.Fatalf("Disposition = %q, want %q (%+v)", r.Disposition, tt.want, r)
			}
			if r.Resolved != (tt.want != weblookup.DispositionUncertain) {
				t.Fatalf("Resolved = %v for %q", r.Resolved, r.Disposition)
			}
			if r.EntityType != tt.wantTyp {
				t.Fatalf("EntityType = %q, want %q", r.EntityType, tt.wantTyp)
			}
			if tt.check != nil {
				tt.check(t, r)
			}
		})
	}
}

func verifyWithSearch(t *testing.T, final weblookup.AnalysisAction, results []weblookup.SearchResult, req catalogverification.VerifyRequest) catalogverification.VerifyResult {
	t.Helper()
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{searchAction("q one"), final}}
	cfg := testConfig()
	cfg.TrustedDomains = []string{"isotopes.example.com", "sportsnet.example.com"}
	svc := &catalogverification.Service{LLM: llm, Searcher: &fakeSearcher{results: results}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: cfg}
	return svc.Verify(context.Background(), req)
}

func TestVerify_UntrustedOnlyResultsMakeKeepAndRejectUncertain(t *testing.T) {
	untrusted := []weblookup.SearchResult{{Domain: "example.com", URL: "https://example.com/x", Trusted: false}}
	for _, d := range []weblookup.Disposition{weblookup.DispositionKeep, weblookup.DispositionReject} {
		r := verifyWithSearch(t, weblookup.AnalysisAction{Action: weblookup.ActionFinal, Disposition: d}, untrusted,
			catalogverification.VerifyRequest{Name: "X", Type: "player", Confidence: 0.4})
		if r.Disposition != weblookup.DispositionUncertain || len(r.WebSources) != 0 {
			t.Fatalf("%s: result = %+v, want Uncertain with no sources", d, r)
		}
	}
}

func TestVerify_TrustedNonNFLEvidenceRejects(t *testing.T) {
	res := []weblookup.SearchResult{{Domain: "sportsnet.example.com", URL: "https://sportsnet.example.com/mlb/x", Title: "MLB team", Trusted: true}}
	final := weblookup.AnalysisAction{Action: weblookup.ActionFinal, Disposition: weblookup.DispositionReject, Metadata: map[string]string{"league": "MLB"}}
	r := verifyWithSearch(t, final, res, catalogverification.VerifyRequest{Name: "Mackerels", Type: "team", Confidence: 0.4})
	if r.Disposition != weblookup.DispositionReject || !r.Resolved || len(r.WebSources) != 1 {
		t.Fatalf("result = %+v, want resolved Reject with one source", r)
	}
	if r.Confidence <= 0.4 {
		t.Fatalf("Confidence = %v, want AdjustConfidence uplift", r.Confidence)
	}
}

func TestVerify_SearchProviderFailureIsFlaggedAndNotCached(t *testing.T) {
	for name, searchErr := range map[string]error{
		"unavailable": appout.ErrSearchProviderUnavailable,
		"timeout":     context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			llm := &fakeLLM{actions: []weblookup.AnalysisAction{{Action: weblookup.ActionWebSearch, Query: "Some Player NFL"}}}
			cache := newFakeCache()
			svc := &catalogverification.Service{LLM: llm, Searcher: &fakeSearcher{err: searchErr}, Cache: cache, Logger: &fakeLogger{}, Config: testConfig()}

			result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
			if result.Resolved || !result.ProviderError {
				t.Fatalf("Verify() = %+v, want unresolved with ProviderError", result)
			}
			if cache.sets != 0 {
				t.Fatalf("cache sets = %d, want 0: a failed search must never be cached", cache.sets)
			}
		})
	}
}

func TestVerify_NoEvidenceIsNotAProviderError(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{{Action: weblookup.ActionWebSearch, Query: "Some Player NFL"}, keepFinal()}}
	svc := &catalogverification.Service{LLM: llm, Searcher: &fakeSearcher{}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if result.ProviderError {
		t.Fatal("ProviderError = true for a genuine empty search, want false")
	}
}

// scriptedSearcher returns errs[i] (or a nil-error empty answer when errs[i]
// is nil) on the i-th call.
type scriptedSearcher struct {
	errs  []error
	calls int
}

func (s *scriptedSearcher) Search(context.Context, string, appout.SearchOptions) ([]weblookup.SearchResult, error) {
	err := s.errs[s.calls]
	s.calls++
	return nil, err
}

func searchActions(n int) []weblookup.AnalysisAction {
	actions := make([]weblookup.AnalysisAction, n)
	for i := range actions {
		actions[i] = weblookup.AnalysisAction{Action: weblookup.ActionWebSearch, Query: "Player NFL"}
	}
	return actions
}

func TestVerify_CircuitOpensAfterConsecutiveProviderFailures(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConsecutiveProviderFailures = 2
	searcher := &fakeSearcher{err: appout.ErrSearchProviderUnavailable}
	svc := &catalogverification.Service{LLM: &fakeLLM{actions: searchActions(4)}, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: cfg}

	for i, name := range []string{"A", "B", "C", "D"} {
		res := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: name, Type: "player"})
		if !res.ProviderError {
			t.Fatalf("candidate %d: ProviderError = false, want true", i)
		}
	}
	if searcher.calls != 2 {
		t.Fatalf("provider calls = %d, want 2: the circuit must stop hitting a failing provider", searcher.calls)
	}
}

func TestVerify_SuccessResetsConsecutiveFailureCount(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConsecutiveProviderFailures = 2
	searcher := &scriptedSearcher{errs: []error{appout.ErrSearchProviderUnavailable, nil, appout.ErrSearchProviderUnavailable}}
	// Distinct queries so the per-candidate query dedup and cache never short-circuit.
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "Alpha NFL"},
		{Action: weblookup.ActionWebSearch, Query: "Bravo NFL"},
		keepFinal(),
		{Action: weblookup.ActionWebSearch, Query: "Charlie NFL"},
	}}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: cfg}

	svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "A", Type: "player"}) // fail (1)
	svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "B", Type: "player"}) // ok -> reset, then final
	svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "C", Type: "player"}) // fail (1), circuit still closed
	if searcher.calls != 3 {
		t.Fatalf("provider calls = %d, want 3: a success in between must reset the failure streak", searcher.calls)
	}
}

func TestVerify_PacesSearchesByMinInterval(t *testing.T) {
	cfg := testConfig()
	cfg.MinSearchIntervalMS = 1000
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	var waits []time.Duration
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "Alpha NFL"},
		keepFinal(),
		{Action: weblookup.ActionWebSearch, Query: "Bravo NFL"},
		keepFinal(),
	}}
	svc := &catalogverification.Service{
		LLM: llm, Searcher: &fakeSearcher{}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: cfg,
		Now:    func() time.Time { return now },
		Wait:   func(_ context.Context, d time.Duration) { waits = append(waits, d) },
		Jitter: func(time.Duration) time.Duration { return 0 },
	}

	svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "A", Type: "player"})
	svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "B", Type: "player"})
	if len(waits) != 1 || waits[0] != time.Second {
		t.Fatalf("waits = %v, want exactly one wait of 1s before the second search", waits)
	}
}

func TestVerify_PacingAddsJitterBoundedByThirtyPercent(t *testing.T) {
	cfg := testConfig()
	cfg.MinSearchIntervalMS = 1000
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	var waits []time.Duration
	var jitterMax time.Duration
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "Alpha NFL"},
		keepFinal(),
		{Action: weblookup.ActionWebSearch, Query: "Bravo NFL"},
		keepFinal(),
	}}
	svc := &catalogverification.Service{
		LLM: llm, Searcher: &fakeSearcher{}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: cfg,
		Now:    func() time.Time { return now },
		Wait:   func(_ context.Context, d time.Duration) { waits = append(waits, d) },
		Jitter: func(max time.Duration) time.Duration { jitterMax = max; return 200 * time.Millisecond },
	}

	svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "A", Type: "player"})
	svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "B", Type: "player"})
	if len(waits) != 1 || waits[0] != 1200*time.Millisecond {
		t.Fatalf("waits = %v, want one wait of 1.2s (1s interval + 200ms jitter)", waits)
	}
	if jitterMax != 300*time.Millisecond {
		t.Fatalf("jitter max = %v, want 300ms (30%% of the interval)", jitterMax)
	}
}

func TestNormalizeFinal_SportSpellingsOfFootball(t *testing.T) {
	src := []entityproposal.WebSource{{Domain: "league.example.com", URL: "https://www.league.example.com/players/x/", Title: "x"}}
	req := catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.5}
	action := func(d weblookup.Disposition, md map[string]string) weblookup.AnalysisAction {
		return weblookup.AnalysisAction{Action: weblookup.ActionFinal, Disposition: d, Metadata: md}
	}
	cases := []struct {
		name string
		a    weblookup.AnalysisAction
		want weblookup.Disposition
	}{
		{"keep with 'American Football' stays keep", action(weblookup.DispositionKeep, map[string]string{"league": "NFL", "sport": "American Football"}), weblookup.DispositionKeep},
		{"keep with 'american_football' stays keep", action(weblookup.DispositionKeep, map[string]string{"league": "NFL", "sport": "american_football"}), weblookup.DispositionKeep},
		{"keep with 'tackle football' and no league stays keep", action(weblookup.DispositionKeep, map[string]string{"sport": "tackle football"}), weblookup.DispositionKeep},
		{"keep with league NFL and sport 'NFL' stays keep", action(weblookup.DispositionKeep, map[string]string{"league": "NFL", "sport": "NFL"}), weblookup.DispositionKeep},
		{"keep with league NFL and an odd sport label stays keep", action(weblookup.DispositionKeep, map[string]string{"league": "nfl", "sport": "gridiron"}), weblookup.DispositionKeep},
		{"keep with no league and sport 'NFL' stays keep", action(weblookup.DispositionKeep, map[string]string{"sport": "NFL"}), weblookup.DispositionKeep},
		{"keep with baseball is rejected", action(weblookup.DispositionKeep, map[string]string{"sport": "baseball"}), weblookup.DispositionReject},
		{"keep with Canadian football is rejected", action(weblookup.DispositionKeep, map[string]string{"sport": "Canadian Football"}), weblookup.DispositionReject},
		{"keep with association football is rejected", action(weblookup.DispositionKeep, map[string]string{"sport": "association football"}), weblookup.DispositionReject},
		{"reject that says American Football is not honoured", action(weblookup.DispositionReject, map[string]string{"sport": "American Football"}), weblookup.DispositionUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := catalogverification.NormalizeFinal(req, tc.a, src)
			if got.Disposition != tc.want {
				t.Fatalf("Disposition = %q (reason %q), want %q", got.Disposition, got.Reason, tc.want)
			}
		})
	}
}

func trustedHit() []weblookup.SearchResult {
	return []weblookup.SearchResult{{Title: "Player", Domain: "isotopes.example.com", URL: "https://isotopes.example.com/p", Trusted: true}}
}

func TestVerify_RepeatedUsefulQueryForcesAFinalOnlyStep(t *testing.T) {
	q := weblookup.AnalysisAction{Action: weblookup.ActionWebSearch, Query: "Some Player NFL team position"}
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{q, q, keepFinal()}}
	searcher := &fakeSearcher{results: trustedHit()}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if !result.Resolved {
		t.Fatalf("Verify() = %+v, want a resolved result once the model is told to finalize", result)
	}
	if len(llm.requests) != 3 {
		t.Fatalf("LLM requests = %d, want 3 (search, repeat, forced final)", len(llm.requests))
	}
	if llm.requests[0].FinalOnly || llm.requests[1].FinalOnly {
		t.Fatal("FinalOnly must stay false until a query is repeated")
	}
	if !llm.requests[2].FinalOnly {
		t.Fatal("after a repeated useful query the next request must be FinalOnly: an identical prompt would just repeat the same answer")
	}
	if searcher.calls != 1 {
		t.Fatalf("provider searches = %d, want 1", searcher.calls)
	}
}

func TestVerify_FinalOnlyOnceTheSearchBudgetForTheCandidateIsSpent(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSearchesPerCandidate = 2
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{
		{Action: weblookup.ActionWebSearch, Query: "Some Player NFL"},
		{Action: weblookup.ActionWebSearch, Query: "Some Player roster"},
		keepFinal(),
	}}
	svc := &catalogverification.Service{LLM: llm, Searcher: &fakeSearcher{results: trustedHit()}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: cfg}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if !result.Resolved || len(llm.requests) != 3 || !llm.requests[2].FinalOnly || llm.requests[1].FinalOnly {
		t.Fatalf("result %+v, requests %d, FinalOnly flags %v; want the third request alone to be FinalOnly",
			result, len(llm.requests), []bool{llm.requests[0].FinalOnly, llm.requests[1].FinalOnly, llm.requests[len(llm.requests)-1].FinalOnly})
	}
}

func TestVerify_ModelIgnoringFinalOnlyIsUncertainWithoutAnotherSearch(t *testing.T) {
	q := weblookup.AnalysisAction{Action: weblookup.ActionWebSearch, Query: "Some Player NFL team position"}
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{q, q, q}}
	searcher := &fakeSearcher{results: trustedHit()}
	svc := &catalogverification.Service{LLM: llm, Searcher: searcher, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}

	result := svc.Verify(context.Background(), catalogverification.VerifyRequest{Name: "Some Player", Type: "player", Confidence: 0.4})
	if result.Resolved || result.ProviderError {
		t.Fatalf("Verify() = %+v, want unresolved and not a provider error", result)
	}
	if searcher.calls != 1 {
		t.Fatalf("provider searches = %d, want 1: a FinalOnly step must never trigger another search", searcher.calls)
	}
}

func TestVerify_LocalEvidenceCarriesSummariesWhenArticlesAreProvided(t *testing.T) {
	llm := &fakeLLM{actions: []weblookup.AnalysisAction{keepFinal()}}
	svc := &catalogverification.Service{LLM: llm, Searcher: &fakeSearcher{}, Cache: newFakeCache(), Logger: &fakeLogger{}, Config: testConfig()}
	svc.Verify(context.Background(), catalogverification.VerifyRequest{
		Name: "Burns", Type: "player",
		Sources:  []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "Herons win"}},
		Articles: []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "Herons win", Summary: "Burns threw two touchdowns"}},
	})
	if got := llm.requests[0].LocalEvidence; len(got) != 1 || got[0].Summary != "Burns threw two touchdowns" {
		t.Fatalf("LocalEvidence = %+v, want the provided summary", got)
	}
}
