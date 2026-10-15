package entitydiscovery

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/catalogverification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeKnowledge struct {
	cands          []knowledge.Candidate
	entities       map[string]knowledge.Entity
	searchErr      error
	getErr         error
	searches, gets int
}

func (f *fakeKnowledge) Search(context.Context, string, int) ([]knowledge.Candidate, error) {
	f.searches++
	return f.cands, f.searchErr
}

func (f *fakeKnowledge) Get(_ context.Context, ids []string) ([]knowledge.Entity, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	var out []knowledge.Entity
	for _, id := range ids {
		if e, ok := f.entities[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

type fakeFallback struct {
	calls int
	res   VerifyResult
}

func (f *fakeFallback) Verify(context.Context, VerifyCandidate) VerifyResult {
	f.calls++
	return f.res
}

func stubResolve(s entityidentity.Status) func(entityidentity.Input) entityidentity.Result {
	return func(entityidentity.Input) entityidentity.Result {
		return entityidentity.Result{
			Status: s, Reason: "r", CanonicalName: "Pat Test", EntityType: entityproposal.EntityTypePlayer,
			Metadata: map[string]string{"provider": "wikidata", "provider_id": "Q1", "league": "NFL"},
		}
	}
}

func newChain(k *fakeKnowledge, fb *fakeFallback, s entityidentity.Status) *ChainVerifier {
	c := &ChainVerifier{Knowledge: k, Catalog: BuildCatalogIndex(nil), Resolve: stubResolve(s)}
	if fb != nil {
		c.Fallback = fb
	}
	return c
}

var cand = VerifyCandidate{Name: "Pat Test", Type: entityproposal.EntityTypePlayer}

func TestChain_DecisiveVerdictsSkipFallback(t *testing.T) {
	for _, tc := range []struct {
		status entityidentity.Status
		want   weblookup.Disposition
	}{
		{entityidentity.StatusVerifiedNFL, weblookup.DispositionKeep},
		{entityidentity.StatusVerifiedNonNFL, weblookup.DispositionReject},
	} {
		fb := &fakeFallback{}
		k := &fakeKnowledge{cands: []knowledge.Candidate{{ID: "Q1"}}, entities: map[string]knowledge.Entity{"Q1": {ID: "Q1"}}}
		vr := newChain(k, fb, tc.status).Verify(context.Background(), cand)
		if vr.Disposition != tc.want || !vr.Resolved || fb.calls != 0 {
			t.Fatalf("%s: %+v fallback calls=%d", tc.status, vr, fb.calls)
		}
		if vr.Provenance.Provider != "wikidata" || vr.Provenance.ProviderID != "Q1" || vr.Provenance.Version != WikidataVerificationVersion {
			t.Errorf("provenance = %+v", vr.Provenance)
		}
	}
}

func TestChain_AmbiguousAndNotFoundCallFallbackOnce(t *testing.T) {
	for _, s := range []entityidentity.Status{entityidentity.StatusAmbiguous, entityidentity.StatusNotFound} {
		fb := &fakeFallback{res: VerifyResult{Disposition: weblookup.DispositionKeep, Resolved: true}}
		c := newChain(&fakeKnowledge{}, fb, s)
		vr := c.Verify(context.Background(), cand)
		if fb.calls != 1 || vr.Disposition != weblookup.DispositionKeep {
			t.Fatalf("%s: calls=%d vr=%+v", s, fb.calls, vr)
		}
		if vr.Provenance.Provider != "llm_web" || vr.Provenance.Version != FallbackVerificationVersion {
			t.Errorf("fallback provenance = %+v", vr.Provenance)
		}
	}
}

func TestChain_WikidataErrorIsNonFatalAndFallsBack(t *testing.T) {
	for _, k := range []*fakeKnowledge{
		{searchErr: errors.New("boom")},
		{cands: []knowledge.Candidate{{ID: "Q1"}}, getErr: errors.New("boom")},
	} {
		fb := &fakeFallback{res: VerifyResult{Disposition: weblookup.DispositionUncertain}}
		c := newChain(k, fb, entityidentity.StatusVerifiedNFL)
		_ = c.Verify(context.Background(), cand)
		if fb.calls != 1 || c.ChainStats().WikidataErrors != 1 {
			t.Fatalf("calls=%d stats=%+v", fb.calls, c.ChainStats())
		}
	}
}

func TestChain_FallbackDisabledLeavesUncertain(t *testing.T) {
	c := newChain(&fakeKnowledge{}, nil, entityidentity.StatusAmbiguous)
	vr := c.Verify(context.Background(), cand)
	if vr.Disposition != weblookup.DispositionUncertain || vr.Resolved {
		t.Fatalf("got %+v", vr)
	}
}

func TestChain_WikidataOffGoesStraightToFallback(t *testing.T) {
	fb := &fakeFallback{res: VerifyResult{Disposition: weblookup.DispositionUncertain}}
	c := &ChainVerifier{Fallback: fb, Catalog: BuildCatalogIndex(nil)}
	c.Verify(context.Background(), cand)
	if fb.calls != 1 {
		t.Fatalf("calls = %d", fb.calls)
	}
}

func TestChain_StatsCountEachOutcome(t *testing.T) {
	fb := &fakeFallback{}
	c := newChain(&fakeKnowledge{}, fb, entityidentity.StatusVerifiedNFL)
	c.Verify(context.Background(), cand)
	c.Resolve = stubResolve(entityidentity.StatusVerifiedNonNFL)
	c.Verify(context.Background(), cand)
	c.Resolve = stubResolve(entityidentity.StatusAmbiguous)
	c.Verify(context.Background(), cand)
	c.Resolve = stubResolve(entityidentity.StatusNotFound)
	c.Verify(context.Background(), cand)
	want := ChainStats{Considered: 4, WikidataNFL: 1, WikidataNonNFL: 1, WikidataAmbiguous: 1, WikidataNotFound: 1, FallbackCalls: 2}
	if got := c.ChainStats(); got != want {
		t.Fatalf("stats = %+v, want %+v", got, want)
	}
}

func TestChain_EmptySearchAndMissingEntitiesDoNotPanic(t *testing.T) {
	k := &fakeKnowledge{cands: []knowledge.Candidate{{ID: "Q404"}}}    // Get omits Q404
	c := &ChainVerifier{Knowledge: k, Catalog: BuildCatalogIndex(nil)} // real Resolve
	vr := c.Verify(context.Background(), cand)
	if vr.Resolved {
		t.Fatalf("missing data must not resolve: %+v", vr)
	}
}

// --- #300 budget regression (case 14) ---

type budgetLLM struct{ n int }

func (b *budgetLLM) Discover(context.Context, []appout.EntityDiscoveryArticle) (appout.EntityDiscoveryResult, error) {
	return appout.EntityDiscoveryResult{}, nil
}

// VerifyCandidate always asks for a new (unique) search.
func (b *budgetLLM) VerifyCandidate(context.Context, appout.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	b.n++
	return weblookup.AnalysisAction{Action: weblookup.ActionWebSearch, Query: fmt.Sprintf("query number %d", b.n)}, nil
}

type budgetSearcher struct{ calls int }

func (s *budgetSearcher) Search(context.Context, string, appout.SearchOptions) ([]weblookup.SearchResult, error) {
	s.calls++
	return []weblookup.SearchResult{{Domain: "isotopes.example.com", URL: "https://isotopes.example.com/x", Trusted: true}}, nil
}

type budgetCache struct{}

func (budgetCache) Get(context.Context, string) ([]weblookup.SearchResult, bool, error) {
	return nil, false, nil
}

func (budgetCache) Set(context.Context, string, string, string, []weblookup.SearchResult, time.Duration) error {
	return nil
}

type budgetLogger struct{}

func (budgetLogger) Debug(string, ...any)        {}
func (budgetLogger) Info(string, ...any)         {}
func (budgetLogger) Warn(string, ...any)         {}
func (budgetLogger) Error(string, error, ...any) {}

// budgetAdapter mirrors verifierAdapter in cmd/.../entities/discover.go.
type budgetAdapter struct{ svc *catalogverification.Service }

func (a budgetAdapter) Verify(ctx context.Context, c VerifyCandidate) VerifyResult {
	r := a.svc.Verify(ctx, catalogverification.VerifyRequest{
		Name: c.Name, Type: string(c.Type), Metadata: c.Metadata,
		Confidence: c.Confidence, Sources: c.Sources, FallbackHistory: c.FallbackHistory,
	})
	return VerifyResult{
		Disposition: r.Disposition, CanonicalName: r.CanonicalName,
		EntityType: entityproposal.EntityType(r.EntityType), Metadata: r.Metadata,
		Confidence: r.Confidence, WebSources: r.WebSources, Reason: r.Reason,
		Resolved: r.Disposition != weblookup.DispositionUncertain,
	}
}

func TestChain_SearchBudgetsSpentOnlyOnFallbackCandidates(t *testing.T) {
	searcher := &budgetSearcher{}
	svc := &catalogverification.Service{
		LLM: &budgetLLM{}, Searcher: searcher, Cache: budgetCache{}, Logger: budgetLogger{},
		Config: config.WebLookupConfig{
			Enabled: true, Provider: "test", TimeoutSeconds: 5,
			MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 3,
			TrustedDomains: []string{"isotopes.example.com"},
		},
	}
	n := 0
	chain := &ChainVerifier{
		Knowledge: &fakeKnowledge{}, Fallback: budgetAdapter{svc: svc}, Catalog: BuildCatalogIndex(nil),
		Resolve: func(entityidentity.Input) entityidentity.Result {
			n++
			status := entityidentity.StatusAmbiguous
			if n <= 3 {
				status = entityidentity.StatusVerifiedNFL
			}
			return stubResolve(status)(entityidentity.Input{})
		},
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if vr := chain.Verify(ctx, cand); !vr.Resolved {
			t.Fatalf("decisive candidate %d not resolved: %+v", i, vr)
		}
	}
	if got := svc.Searches(); got != 0 {
		t.Fatalf("searches after 3 decisive candidates = %d, want 0", got)
	}
	for i := 0; i < 5; i++ {
		chain.Verify(ctx, cand)
	}
	if got := svc.Searches(); got == 0 || got > 3 {
		t.Fatalf("searches = %d, want 1..3 (run cap, spent only on fallback)", got)
	}
	if searcher.calls != svc.Searches() {
		t.Fatalf("searcher.calls = %d, Searches() = %d", searcher.calls, svc.Searches())
	}
	if fc := chain.ChainStats().FallbackCalls; fc != 5 {
		t.Fatalf("FallbackCalls = %d, want 5", fc)
	}
}

func assessCands() *fakeKnowledge {
	return &fakeKnowledge{
		cands:    []knowledge.Candidate{{ID: "Q1", Label: "Pat Test"}},
		entities: map[string]knowledge.Entity{"Q1": {ID: "Q1", Label: "Pat Test"}},
	}
}

func TestAssess_DecisiveNFLDoesNotCallFallback(t *testing.T) {
	fb := &fakeFallback{}
	c := newChain(assessCands(), fb, entityidentity.StatusVerifiedNFL)
	a := c.Assess(context.Background(), VerifyCandidate{Name: "Pat Test", Type: entityproposal.EntityTypePlayer})
	if !a.Decisive || a.Status != "verified_nfl" || a.Candidates != 1 || a.Result.Metadata["league"] != "NFL" {
		t.Fatalf("assessment = %+v", a)
	}
	if a.Result.Provenance.Provider != "wikidata" || a.Result.Provenance.Version != WikidataVerificationVersion {
		t.Fatalf("provenance = %+v", a.Result.Provenance)
	}
	if fb.calls != 0 {
		t.Fatalf("fallback calls = %d", fb.calls)
	}
}

func TestAssess_AmbiguousIsNotDecisiveAndNeverFallsBack(t *testing.T) {
	fb := &fakeFallback{}
	c := newChain(assessCands(), fb, entityidentity.StatusAmbiguous)
	a := c.Assess(context.Background(), VerifyCandidate{Name: "Pat Test"})
	if a.Decisive || a.Status != "ambiguous" || fb.calls != 0 {
		t.Fatalf("assessment = %+v fallback=%d", a, fb.calls)
	}
}

func TestAssess_SearchErrorReportsError(t *testing.T) {
	k := assessCands()
	k.searchErr = errors.New("boom")
	c := newChain(k, nil, entityidentity.StatusVerifiedNFL)
	a := c.Assess(context.Background(), VerifyCandidate{Name: "Pat Test"})
	if a.Decisive || a.Status != AssessmentError {
		t.Fatalf("assessment = %+v", a)
	}
	if c.ChainStats().WikidataErrors != 1 {
		t.Fatalf("stats = %+v", c.ChainStats())
	}
}

func TestVerify_ProviderErrorFlaggedOnlyWhenUnresolved(t *testing.T) {
	k := assessCands()
	k.searchErr = errors.New("boom")
	if vr := newChain(k, nil, entityidentity.StatusVerifiedNFL).Verify(context.Background(), VerifyCandidate{Name: "Pat Test"}); vr.Resolved || !vr.ProviderError {
		t.Fatalf("unresolved after error: %+v", vr)
	}
	fb := &fakeFallback{res: VerifyResult{Disposition: weblookup.DispositionKeep, Resolved: true}}
	if vr := newChain(k, fb, entityidentity.StatusVerifiedNFL).Verify(context.Background(), VerifyCandidate{Name: "Pat Test"}); !vr.Resolved || vr.ProviderError {
		t.Fatalf("fallback resolved: %+v", vr)
	}
	if vr := newChain(assessCands(), nil, entityidentity.StatusAmbiguous).Verify(context.Background(), VerifyCandidate{Name: "Pat Test"}); vr.ProviderError {
		t.Fatalf("ambiguous is not a provider error: %+v", vr)
	}
}
