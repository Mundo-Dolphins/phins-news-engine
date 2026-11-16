package entities_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

func TestEntitiesDiscover_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "entities", "discover")
}

func TestEntitiesProposalsVerify_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "entities", "proposals", "verify")
}

// entityFixture is the fixture config with entity discovery pointed at a
// dead address, so only --llm-server can lead a command to the fake, plus
// empty manual and generated catalogs.
func entityFixture(t *testing.T, fake *testsupport.FakeLLMServer) (configPath string, catalogArgs []string) {
	t.Helper()
	configPath = testsupport.WriteFixture(t)
	testsupport.SeedUnreviewedArticle(t, configPath, "a1")
	fake.AppendServerA(t, configPath)
	testsupport.AppendToConfig(t, configPath, "entity_discovery:\n  enabled: true\n  extractor_version: entity-discovery-v1\n  batch_size: 20\n  limit: 500\n  llm:\n    base_url: http://127.0.0.1:1/v1\n    model: test-model\n    temperature: 0\n    timeout_seconds: 5\n")
	dir := filepath.Dir(configPath)
	for _, name := range []string{"manual.yml", "generated.yml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("entities: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return configPath, []string{"--manual", filepath.Join(dir, "manual.yml"), "--generated", filepath.Join(dir, "generated.yml")}
}

func TestEntitiesDiscover_LLMServerReachesTheDiscoveryLLM(t *testing.T) {
	fake := testsupport.NewFakeLLMServer(t, "m1")
	configPath, catalog := entityFixture(t, fake)

	args := append([]string{"--config", configPath, "entities", "discover", "--all"}, catalog...)
	out, err := testsupport.Execute(t, append(args, "--llm-server", "server-a", "--llm-model", "m1")...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	fake.RequireModel(t, "m1")
}

func TestEntitiesProposalsVerify_LLMServerReachesTheVerifierLLM(t *testing.T) {
	fake := testsupport.NewFakeLLMServer(t, "m1")
	configPath, catalog := entityFixture(t, fake)
	testsupport.AppendToConfig(t, configPath, "catalog_maintenance:\n  web_lookup:\n    enabled: true\n    provider: searxng\n    base_url: "+fake.BaseURL+
		"\n    timeout_seconds: 2\n    max_searches_per_candidate: 2\n    max_results_per_search: 3\n    max_searches_per_run: 10\n    cache_ttl: 1h\n    trusted_domains: [\"league.example.com\"]\n")
	t.Setenv("WEB_SEARCH_API_KEY", "test-key")

	db, err := infrasqlite.Open(filepath.Join(filepath.Dir(configPath), "operational.db"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := sqliteentityproposal.NewEntityProposalRepository(db).Upsert(context.Background(), entityproposal.Proposal{
		CanonicalName: "Springfield", NormalizedName: "springfield",
		EntityType: entityproposal.EntityTypeTeam, Kind: entityproposal.KindNewEntity,
		Confidence: 0.6, Occurrences: 1, ExtractorVersion: "test", CreatedAt: time.Now().UTC(),
		Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "Springfield wins"}},
	}); err != nil {
		t.Fatal(err)
	}

	args := append([]string{"--config", configPath, "entities", "proposals", "verify", "--dry-run", "--limit", "5"}, catalog...)
	out, err := testsupport.Execute(t, append(args, "--llm-server", "server-a", "--llm-model", "m1")...)
	if err != nil {
		t.Fatalf("Execute() error = %v\n%s", err, out)
	}
	fake.RequireModel(t, "m1")
}
