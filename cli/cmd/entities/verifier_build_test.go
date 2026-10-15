package entities

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func TestResolveVerificationStages(t *testing.T) {
	on := config.VerificationConfig{WikidataFirst: true, Fallback: true}
	tests := []struct {
		name    string
		cfg     config.VerificationConfig
		webLook bool
		f       verificationFlags
		wd, fb  bool
	}{
		{"config defaults, web_lookup on", config.VerificationConfig{Fallback: true}, true, verificationFlags{}, false, true},
		{"config defaults, web_lookup off", config.VerificationConfig{Fallback: true}, false, verificationFlags{}, false, false},
		{"wikidata_first from config", on, true, verificationFlags{}, true, true},
		{"--no-wikidata beats config", on, true, verificationFlags{noWikidata: true}, false, true},
		{"--wikidata enables", config.VerificationConfig{Fallback: true}, false, verificationFlags{wikidata: true}, true, false},
		{"--no-fallback", on, true, verificationFlags{noFallback: true}, true, false},
		{"--fallback forces web path on", config.VerificationConfig{}, false, verificationFlags{fallback: true}, false, true},
		{"--offline wins over everything", on, true, verificationFlags{offline: true, wikidata: true, fallback: true}, false, false},
	}
	for _, tc := range tests {
		wd, fb := resolveVerificationStages(tc.cfg, tc.webLook, tc.f)
		if wd != tc.wd || fb != tc.fb {
			t.Errorf("%s: got wd=%v fb=%v, want wd=%v fb=%v", tc.name, wd, fb, tc.wd, tc.fb)
		}
	}
}

func TestVerificationFlags_ContradictoryPairsAreMutuallyExclusive(t *testing.T) {
	for _, args := range [][]string{{"--wikidata", "--no-wikidata"}, {"--fallback", "--no-fallback"}} {
		var f verificationFlags
		cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
		addVerificationFlags(cmd, &f)
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "none of the others can be") {
			t.Errorf("%v: err = %v, want mutually exclusive error", args, err)
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

func TestBuildLeagueStage_DisabledIsNil(t *testing.T) {
	stage, err := buildLeagueStage(&config.Config{}, entitydiscovery.CatalogIndex{}, time.Now(), nopLogger{})
	if err != nil || stage != nil {
		t.Fatalf("buildLeagueStage() = %v, %v; want nil, nil when disabled", stage, err)
	}
}

func TestBuildLeagueStage_InvalidRulesFileFailsAtStartup(t *testing.T) {
	cfg := &config.Config{}
	cfg.EntityDiscovery.League = config.LeagueConfig{Enabled: true, RulesPath: "does/not/exist.yml"}
	_, err := buildLeagueStage(cfg, leagueTestCatalog(t), time.Now(), nopLogger{})
	if err == nil || !strings.Contains(err.Error(), "entity_discovery.league") {
		t.Fatalf("buildLeagueStage() error = %v, want a start-up error naming entity_discovery.league", err)
	}
}

func TestBuildLeagueStage_BuildsFromTheRepoRulesWithoutARoster(t *testing.T) {
	cfg := &config.Config{}
	cfg.EntityDiscovery.League = config.LeagueConfig{Enabled: true, RulesPath: repofs.ExampleConfig(t, "league_alias_rules.yml")}
	stage, err := buildLeagueStage(cfg, leagueTestCatalog(t), time.Now(), nopLogger{})
	if err != nil || stage == nil {
		t.Fatalf("buildLeagueStage() = %v, %v; teams and noise must work without a roster", stage, err)
	}
	res, decided := stage.Verify(context.Background(), entitydiscovery.VerifyCandidate{Name: "Northern", Type: entityproposal.EntityTypeTeam})
	if !decided || res.DecisionReason != entityproposal.ReasonNotAnNFLTeam {
		t.Fatalf("Verify(Northern) = %+v, %v; want a not_an_nfl_team reject", res, decided)
	}
}

func TestVerificationFlags_NoLLMFlagIsRegistered(t *testing.T) {
	var f verificationFlags
	cmd := &cobra.Command{Use: "x"}
	addVerificationFlags(cmd, &f)
	if err := cmd.ParseFlags([]string{"--no-llm"}); err != nil || !f.noLLM {
		t.Fatalf("--no-llm: err=%v noLLM=%v", err, f.noLLM)
	}
}
