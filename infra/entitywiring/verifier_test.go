package entitywiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/catalogverification"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

type nopLogger struct{}

func (nopLogger) Debug(string, ...any)        {}
func (nopLogger) Info(string, ...any)         {}
func (nopLogger) Warn(string, ...any)         {}
func (nopLogger) Error(string, error, ...any) {}

type searchActionLLM struct{ out.EntityDiscoveryLLM }

func (searchActionLLM) VerifyCandidate(context.Context, out.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	return weblookup.AnalysisAction{Action: weblookup.ActionWebSearch, Query: "Some Player NFL"}, nil
}

type unavailableSearcher struct{}

func (unavailableSearcher) Search(context.Context, string, out.SearchOptions) ([]weblookup.SearchResult, error) {
	return nil, out.ErrSearchProviderUnavailable
}

type nopCache struct{}

func (nopCache) Get(context.Context, string) ([]weblookup.SearchResult, bool, error) {
	return nil, false, nil
}

func (nopCache) Set(context.Context, string, string, string, []weblookup.SearchResult, time.Duration) error {
	return nil
}

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
	if _, err := buildVerifier(nil, fallbackWebLookup(""), searchActionLLM{}, nopLoggers); err == nil ||
		!strings.Contains(err.Error(), "TAVILY_API_KEY") {
		t.Fatalf("buildVerifier() error = %v, want one naming TAVILY_API_KEY", err)
	}
	if _, err := buildVerifier(nil, fallbackWebLookup("tvly-x"), searchActionLLM{}, nopLoggers); err != nil {
		t.Fatalf("buildVerifier() with a key error = %v, want nil", err)
	}
}

func TestResolveStages(t *testing.T) {
	on := config.VerificationConfig{WikidataFirst: true, Fallback: true}
	tests := []struct {
		name    string
		cfg     config.VerificationConfig
		webLook bool
		s       Stages
		wd, fb  bool
	}{
		{"config defaults, web_lookup on", config.VerificationConfig{Fallback: true}, true, Stages{}, false, true},
		{"config defaults, web_lookup off", config.VerificationConfig{Fallback: true}, false, Stages{}, false, false},
		{"wikidata_first from config", on, true, Stages{}, true, true},
		{"--no-wikidata beats config", on, true, Stages{NoWikidata: true}, false, true},
		{"--wikidata enables", config.VerificationConfig{Fallback: true}, false, Stages{Wikidata: true}, true, false},
		{"--no-fallback", on, true, Stages{NoFallback: true}, true, false},
		{"--fallback forces web path on", config.VerificationConfig{}, false, Stages{Fallback: true}, false, true},
		{"--offline wins over everything", on, true, Stages{Offline: true, Wikidata: true, Fallback: true}, false, false},
	}
	for _, tc := range tests {
		wd, fb := ResolveStages(tc.cfg, tc.webLook, tc.s)
		if wd != tc.wd || fb != tc.fb {
			t.Errorf("%s: got wd=%v fb=%v, want wd=%v fb=%v", tc.name, wd, fb, tc.wd, tc.fb)
		}
	}
}

func leagueTestCatalog(t *testing.T) entitydiscovery.CatalogIndex {
	t.Helper()
	rules, err := entitycatalog.LoadOne(repofs.ExampleConfig(t, "entity_catalog_manual.yml"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return entitydiscovery.BuildCatalogIndex(rules)
}

func TestLeagueStage_DisabledIsNil(t *testing.T) {
	stage, err := LeagueStage(&config.Config{}, entitydiscovery.CatalogIndex{}, time.Now(), nopLogger{})
	if err != nil || stage != nil {
		t.Fatalf("LeagueStage() = %v, %v; want nil, nil when disabled", stage, err)
	}
}

func TestLeagueStage_InvalidRulesFileFailsAtStartup(t *testing.T) {
	cfg := &config.Config{}
	cfg.EntityDiscovery.League = config.LeagueConfig{Enabled: true, RulesPath: "does/not/exist.yml"}
	_, err := LeagueStage(cfg, leagueTestCatalog(t), time.Now(), nopLogger{})
	if err == nil || !strings.Contains(err.Error(), "entity_discovery.league") {
		t.Fatalf("LeagueStage() error = %v, want a start-up error naming entity_discovery.league", err)
	}
}

func TestLeagueStage_BuildsFromTheRepoRulesWithoutARoster(t *testing.T) {
	cfg := &config.Config{}
	cfg.EntityDiscovery.League = config.LeagueConfig{Enabled: true, RulesPath: repofs.ExampleConfig(t, "league_alias_rules.yml")}
	stage, err := LeagueStage(cfg, leagueTestCatalog(t), time.Now(), nopLogger{})
	if err != nil || stage == nil {
		t.Fatalf("LeagueStage() = %v, %v; teams and noise must work without a roster", stage, err)
	}
	res, decided := stage.Verify(context.Background(), entitydiscovery.VerifyCandidate{Name: "Northern", Type: entityproposal.EntityTypeTeam})
	if !decided || res.DecisionReason != entityproposal.ReasonNotAnNFLTeam {
		t.Fatalf("Verify(Northern) = %+v, %v; want a not_an_nfl_team reject", res, decided)
	}
}

const rosterCSV = "full_name,team,position,status,gsis_id,week\nZeno Ashbury,WAV,LB,ACT,g1,5\n"

func writeRoster(t *testing.T, mtime time.Time, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "roster.csv")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadLeaguePlayers(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		cfg  func() config.RosterConfig
		want bool
	}{
		"fresh file loads": {func() config.RosterConfig {
			return config.RosterConfig{Enabled: true, Path: writeRoster(t, now.Add(-24*time.Hour), rosterCSV), MaxAgeDays: 7}
		}, true},
		"stale file disables the stage": {func() config.RosterConfig {
			return config.RosterConfig{Enabled: true, Path: writeRoster(t, now.Add(-10*24*time.Hour), rosterCSV), MaxAgeDays: 7}
		}, false},
		"corrupt file disables the stage": {func() config.RosterConfig {
			return config.RosterConfig{Enabled: true, Path: writeRoster(t, now, "not,a,roster\n"), MaxAgeDays: 7}
		}, false},
		"missing file disables the stage": {func() config.RosterConfig {
			return config.RosterConfig{Enabled: true, Path: filepath.Join(t.TempDir(), "nope.csv"), MaxAgeDays: 7}
		}, false},
		"disabled block": {func() config.RosterConfig { return config.RosterConfig{} }, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := len(loadLeaguePlayers(tc.cfg(), now, nopLogger{})) > 0
			if got != tc.want {
				t.Fatalf("loadLeaguePlayers() non-empty is %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseEngineGroups(t *testing.T) {
	got := parseEngineGroups([]string{"bing, yahoo", " ", ",", "yep"})
	if len(got) != 2 || len(got[0]) != 2 || got[0][1] != "yahoo" || got[1][0] != "yep" {
		t.Fatalf("parseEngineGroups() = %v, want [[bing yahoo] [yep]]", got)
	}
	if parseEngineGroups(nil) != nil {
		t.Fatal("no groups must stay nil so SearXNG keeps all its engines")
	}
}

type fakeArticleFinder struct{ byID map[string]article.Article }

func (f fakeArticleFinder) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f.byID[id]
	if !ok {
		return article.Article{}, errors.New("not found")
	}
	return a, nil
}

func TestArticleTexts_LoadsTitlesAndSummariesAndSkipsMissing(t *testing.T) {
	l := articleTexts{finder: fakeArticleFinder{byID: map[string]article.Article{"a1": {ID: "a1", Title: "T1", Summary: "S1"}}}}
	got, err := l.Texts(context.Background(), []string{"a1", "gone"})
	if err != nil || len(got) != 1 || got[0].ID != "a1" || got[0].Summary != "S1" {
		t.Fatalf("Texts() = %+v, %v; want only the existing article", got, err)
	}
}

func TestApplyProvider(t *testing.T) {
	if s, err := applyProvider("", Stages{}); err != nil || s != (Stages{}) {
		t.Fatalf("empty: %+v %v", s, err)
	}
	if s, err := applyProvider("wikidata", Stages{}); err != nil || !s.Wikidata || !s.NoFallback {
		t.Fatalf("wikidata: %+v %v", s, err)
	}
	for _, bad := range []Stages{{NoWikidata: true}, {Fallback: true}, {Offline: true}} {
		if _, err := applyProvider("wikidata", bad); err == nil {
			t.Errorf("want conflict error for %+v", bad)
		}
	}
	if _, err := applyProvider("llm", Stages{}); err == nil {
		t.Error("want error for unknown provider")
	}
}

func TestValidateEvalFilters(t *testing.T) {
	for _, tc := range []struct {
		status, kind, typ string
		ok                bool
	}{
		{"pending", "", "", true},
		{"all", "new_entity", "player", true},
		{"bogus", "", "", false},
		{"pending", "bogus", "", false},
		{"pending", "", "mascot", false},
	} {
		err := validateEvalFilters(tc.status, tc.kind, tc.typ)
		if (err == nil) != tc.ok {
			t.Errorf("%+v: err = %v", tc, err)
		}
	}
}

func TestLoadReviewed(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.csv")
	_ = os.WriteFile(good, []byte("proposal_id,verdict\nabc,correct\ndef,wrong\n"), 0o600)
	got, err := loadReviewed(good)
	if err != nil || !reflect.DeepEqual(got, map[string]string{"abc": "correct", "def": "wrong"}) {
		t.Fatalf("got=%v err=%v", got, err)
	}
	bad := filepath.Join(dir, "bad.csv")
	_ = os.WriteFile(bad, []byte("abc,maybe\n"), 0o600)
	if _, err := loadReviewed(bad); err == nil {
		t.Fatal("want error for unknown verdict")
	}
	if m, err := loadReviewed(""); err != nil || m != nil {
		t.Fatalf("empty path: %v %v", m, err)
	}
}
