package entities

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/catalogverification"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type searchActionLLM struct{ appout.EntityDiscoveryLLM }

func (searchActionLLM) VerifyCandidate(context.Context, appout.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	return weblookup.AnalysisAction{Action: weblookup.ActionWebSearch, Query: "Some Player NFL"}, nil
}

type unavailableSearcher struct{}

func (unavailableSearcher) Search(context.Context, string, appout.SearchOptions) ([]weblookup.SearchResult, error) {
	return nil, appout.ErrSearchProviderUnavailable
}

type nopCache struct{}

func (nopCache) Get(context.Context, string) ([]weblookup.SearchResult, bool, error) {
	return nil, false, nil
}

func (nopCache) Set(context.Context, string, string, string, []weblookup.SearchResult, time.Duration) error {
	return nil
}

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)        {}
func (nopLogger) Info(string, ...any)         {}
func (nopLogger) Warn(string, ...any)         {}
func (nopLogger) Error(string, error, ...any) {}

func TestVerifierAdapter_PropagatesProviderError(t *testing.T) {
	a := verifierAdapter{svc: &catalogverification.Service{
		LLM: searchActionLLM{}, Searcher: unavailableSearcher{}, Cache: nopCache{}, Logger: nopLogger{},
		Config: config.WebLookupConfig{Provider: "test", MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 10},
	}}
	got := a.Verify(context.Background(), entitydiscovery.VerifyCandidate{Name: "Some Player", Type: "player"})
	if got.Resolved || !got.ProviderError {
		t.Fatalf("Verify() = %+v, want unresolved with ProviderError so the proposal is not marked attempted", got)
	}
}

func fallbackWebLookup(apiKey string) config.WebLookupConfig {
	return config.WebLookupConfig{
		Enabled: true, Provider: "searxng", BaseURL: "http://localhost:8081/search", APIKey: "searx-key",
		TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50,
		CacheTTL: time.Hour, TrustedDomains: []string{"league.example.com"},
		Fallback: config.WebLookupFallbackConfig{Provider: "tavily", MaxSearchesPerRun: 10, APIKey: apiKey},
	}
}

func TestBuildVerifier_FallbackProviderRequiresItsKeyWhereItIsUsed(t *testing.T) {
	if _, err := buildVerifier(&cobra.Command{}, nil, fallbackWebLookup(""), searchActionLLM{}); err == nil ||
		!strings.Contains(err.Error(), "TAVILY_API_KEY") {
		t.Fatalf("buildVerifier() error = %v, want one naming TAVILY_API_KEY", err)
	}
	if _, err := buildVerifier(&cobra.Command{}, nil, fallbackWebLookup("tvly-x"), searchActionLLM{}); err != nil {
		t.Fatalf("buildVerifier() with a key error = %v, want nil", err)
	}
}
