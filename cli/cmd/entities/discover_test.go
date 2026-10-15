// cli/cmd/entities/discover_test.go
package entities

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// settingsFixtureTemplate is a full, valid settings.yml — config.Load
// validates every top-level section (scheduler, http, fetch, ...), not just
// entity_discovery, so this needs every required field even though
// `entities discover` itself only reads SQLite and EntityDiscovery. Kept
// local to this file rather than imported from cmd/testsupport: that
// package imports cmd (the root command), which imports this entities
// package, so importing testsupport here would be an import cycle.
const settingsFixtureTemplate = `
sqlite:
  path: %s
  busy_timeout_ms: 5000
scheduler:
  check_interval_minutes: 5
  outbox_interval_minutes: 1
  cleanup_interval_hours: 24
http:
  timeout_seconds: 10
  max_response_bytes: 2097152
scraping_service:
  enabled: false
fetch:
  global_concurrency: 6
  fan_in_buffer: 64
  sources_path: configs/sources.yml
source_policy:
  path: configs/source-policies.yml
classification:
  enabled: false
clustering:
  enabled: false
editorial:
  mode: disabled
  ambiguous_policy: reject
freshness:
  hard_news_hours: 72
  social_hours: 24
  analysis_hours: 168
  default_hours: 72
telegram:
  channel_id: "-1000000000000"
  timeout_seconds: 10
publication_policy:
  burst:
    max_per_window: 6
    window_minutes: 60
  same_topic:
    max_per_window: 2
    window_minutes: 180
  quiet_hours:
    enabled: false
    start_hour: 2
    end_hour: 6
publication:
  max_attempts: 6
  base_backoff_seconds: 30
  max_backoff_seconds: 1800
  claim_batch_size: 10
  stale_claim_after_minutes: 10
retention:
  rejected_articles_days: 14
  duplicates_days: 7
  audit_events_days: 30
  publication_attempts_days: 30
  clusters_days: 14
cluster_lifecycle:
  close_after_hours: 96
  batch_size: 200
  max_batches_per_run: 50
  time_budget_seconds: 30
  close_reason: "inactivity_ttl"
model:
  path: ""
  version: ""
dictionary:
  path: ""
  version: ""
editorial_cycle:
  sample_size: 50
  pool_limit: 500
  ambiguous_cooldown_days: 14
editorial_gates:
  min_precision: 0.7
  max_recall_regression: 0.05
  min_prauc: 0.5
  max_false_positive_rate: 0.15
  min_per_source_precision: 0.6
  min_source_examples: 5
  max_artifact_size_bytes: 1048576
  max_inference_nanos: 1000000
editorial_costs:
  false_publish_cost: 3
  missed_news_cost: 1
editorial_rollout:
  shadow_min_days: 14
  shadow_min_articles: 200
  canary_percent: 10
  canary_min_days: 7
  canary_min_articles: 100
  canary_max_false_positive_rate: 0.05
  canary_min_precision: 0.85
remote:
  sqlite_remote_db_hint: "~/phins-news-engine/data/operational.db"
  retention_days: 7
offline_training:
  llm:
    auto_label_threshold: 0.95
  review_scoring:
    weights:
      disagreement: 1.0
      nb_uncertainty: 0.7
      llm_uncertainty: 0.7
      novel_vocabulary: 0.5
      unknown_entities: 0.5
      new_source: 0.3
      recency: 0.2
    llm_confidence_auto_label_threshold: 0.9
    llm_confidence_uncertain_threshold: 0.5
    new_source_window_hours: 72
    recency_window_hours: 24
`

func writeSettingsWithEntityDiscovery(t *testing.T, dbPath, llmBaseURL string) string {
	t.Helper()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.yml")
	dictPath := filepath.Join(dir, "dictionary.yml")
	manualPath := filepath.Join(dir, "manual.yml")
	generatedPath := filepath.Join(dir, "generated.yml")

	if err := os.WriteFile(dictPath, []byte("meta:\n  version: \"1.0.0\"\ndecision:\n  positive_threshold_high: 0.8\n  positive_threshold_medium: 0.5\n  negative_threshold_high: 0.8\n  title_weight_multiplier: 1.5\n  max_count_per_term: 5\ncategories: {}\n"), 0o600); err != nil {
		t.Fatalf("write dictionary fixture: %v", err)
	}
	if err := os.WriteFile(manualPath, []byte("entities: []\n"), 0o600); err != nil {
		t.Fatalf("write manual fixture: %v", err)
	}
	if err := os.WriteFile(generatedPath, []byte("entities: []\n"), 0o600); err != nil {
		t.Fatalf("write generated fixture: %v", err)
	}

	settings := fmt.Sprintf(settingsFixtureTemplate, dbPath) +
		"entity_discovery:\n  enabled: true\n  extractor_version: entity-discovery-v1\n  batch_size: 20\n  limit: 500\n  llm:\n    base_url: " + llmBaseURL + "\n    model: test-model\n    temperature: 0\n    timeout_seconds: 5\n"
	if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}
	return settingsPath
}

// writeSettingsWithWikidata is writeSettingsWithEntityDiscovery plus an
// entity_discovery.wikidata section pointing at wikidataBaseURL.
func writeSettingsWithWikidata(t *testing.T, dbPath, llmBaseURL, wikidataBaseURL string) string {
	t.Helper()
	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, llmBaseURL)
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	extra := "  wikidata:\n    enabled: true\n    base_url: " + wikidataBaseURL + "\n    language: en\n    max_candidates: 5\n    timeout_seconds: 5\n    max_concurrency: 1\n    positive_cache_ttl: 720h\n    negative_cache_ttl: 24h\n    cache_retention: 720h\n    user_agent: \"test/1.0 (test@example.com)\"\n    allow_private_hosts: true\n  verification:\n    wikidata_first: true\n    fallback: true\n"
	if err := os.WriteFile(settingsPath, append(b, []byte(extra)...), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return settingsPath
}

func newDiscoverTestDB(t *testing.T) (dbPath string, db *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "operational.db")
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return dbPath, db
}

func TestDiscoverCommand_EndToEnd_NewPlayerBecomesPendingProposal(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)

	articlesRepo := sqliteadapter.NewArticleRepository(db)
	now := time.Now().UTC()
	sourceID, err := source.NewID(source.TypeWeb, "example-com")
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if err := articlesRepo.Save(context.Background(), article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Source:    source.Source{ID: sourceID, Name: "Example", Type: source.TypeWeb},
		Title:     "Isotopes sign former Cypress CB Jai'Rene' Mossbank",
		Summary:   "Springfield has signed cornerback Jai'Rene' Mossbank, who previously played at TCU and Cypress.",
		FetchedAt: now, Status: article.StatusFetched,
	}); err != nil {
		t.Fatalf("save article: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"entities":[{"name":"Jai'Rene' Mossbank","type":"player","aliases":["Mossbank"],"metadata":{"position":"CB"},"confidence":0.9}]}`,
		}}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer server.Close()

	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, server.URL)
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newDiscoverCommand()
	cmd.SetArgs([]string{
		"--all",
		"--dictionary", filepath.Join(filepath.Dir(settingsPath), "dictionary.yml"),
		"--manual", filepath.Join(filepath.Dir(settingsPath), "manual.yml"),
		"--generated", filepath.Join(filepath.Dir(settingsPath), "generated.yml"),
	})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("discover command Execute() error = %v\noutput:\n%s", err, stdout.String())
	}

	proposalsRepo := sqliteentityproposal.NewEntityProposalRepository(db)
	proposals, err := proposalsRepo.List(context.Background(), "", "pending")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(proposals) != 1 || proposals[0].CanonicalName != "Jai'Rene' Mossbank" {
		t.Fatalf("List() = %+v, want one pending proposal for Jai'Rene' Mossbank", proposals)
	}
}

// TestDiscoverCommand_EndToEnd_LLMFailure_ExitsNonZero verifies that when
// every LLM batch fails (the LLM server is unreachable/erroring), the
// command still completes the run but Execute() returns a non-nil error so
// the process exits non-zero, per the design spec's requirement that an
// unreachable LLM / invalid JSON must fail the batch, log it, let the rest
// of the run continue, and end the command with a non-zero exit code.
func TestDiscoverCommand_EndToEnd_LLMFailure_ExitsNonZero(t *testing.T) {
	dbPath, db := newDiscoverTestDB(t)

	articlesRepo := sqliteadapter.NewArticleRepository(db)
	now := time.Now().UTC()
	sourceID, err := source.NewID(source.TypeWeb, "example-com")
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if err := articlesRepo.Save(context.Background(), article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Source:    source.Source{ID: sourceID, Name: "Example", Type: source.TypeWeb},
		Title:     "Isotopes sign former Cypress CB Jai'Rene' Mossbank",
		Summary:   "Springfield has signed cornerback Jai'Rene' Mossbank, who previously played at TCU and Cypress.",
		FetchedAt: now, Status: article.StatusFetched,
	}); err != nil {
		t.Fatalf("save article: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, server.URL)
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newDiscoverCommand()
	cmd.SetArgs([]string{
		"--all",
		"--dictionary", filepath.Join(filepath.Dir(settingsPath), "dictionary.yml"),
		"--manual", filepath.Join(filepath.Dir(settingsPath), "manual.yml"),
		"--generated", filepath.Join(filepath.Dir(settingsPath), "generated.yml"),
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("discover command Execute() error = nil, want non-nil when every LLM batch fails\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

// TestDiscoverCommand_VerifyFlagValidatesEffectiveConfig guards against a
// regression of the final review's Important #1 finding: when --verify
// forces verification on for a run while catalog_maintenance.web_lookup.
// enabled is still false in YAML (the exact scenario the flag's help text
// documents — "overrides catalog_maintenance.web_lookup.enabled for this
// run"), the command must still validate the *effective* (enabled=true)
// web_lookup config before constructing a searcher. Before the fix,
// WebLookupConfig.Validate() no-ops whenever Enabled is false, so an
// incomplete web_lookup section (missing base_url here) would silently
// pass validation and a live searxng client would be constructed with a
// blank base_url instead of failing fast with a clear config error.
func TestDiscoverCommand_VerifyFlagValidatesEffectiveConfig(t *testing.T) {
	dbPath, _ := newDiscoverTestDB(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	settingsPath := writeSettingsWithEntityDiscovery(t, dbPath, server.URL)

	// Append a web_lookup section that is disabled in YAML but otherwise
	// incomplete (no base_url, no trusted_domains) — exactly the shape an
	// operator relying on --verify to turn it on for a one-off run would
	// have, since they never filled in the rest of the section because
	// enabled stayed false.
	f, err := os.OpenFile(settingsPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open settings fixture for append: %v", err)
	}
	if _, err := f.WriteString("catalog_maintenance:\n  web_lookup:\n    enabled: false\n    provider: searxng\n"); err != nil {
		t.Fatalf("append web_lookup section: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close settings fixture: %v", err)
	}

	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newDiscoverCommand()
	cmd.SetArgs([]string{
		"--all", "--verify",
		"--dictionary", filepath.Join(filepath.Dir(settingsPath), "dictionary.yml"),
		"--manual", filepath.Join(filepath.Dir(settingsPath), "manual.yml"),
		"--generated", filepath.Join(filepath.Dir(settingsPath), "generated.yml"),
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err = cmd.Execute()
	if err == nil {
		t.Fatalf("discover command Execute() error = nil, want a catalog_maintenance.web_lookup validation error\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if !bytes.Contains([]byte(err.Error()), []byte("base_url is required when enabled")) {
		t.Fatalf("Execute() error = %q, want it to surface the effective config's base_url validation failure", err.Error())
	}
}

// TestDiscoverCommand_OfflineMakesNoWikidataRequests proves --offline wins
// over a config that enables Wikidata-first: zero Wikidata requests and a
// zero fallback line.
func TestDiscoverCommand_OfflineMakesNoWikidataRequests(t *testing.T) {
	if n := runDiscoverWithWikidataConfig(t, true); n != 0 {
		t.Fatalf("wikidata requests = %d, want 0", n)
	}
}

// Positive control for the test above: the same setup without --offline
// does reach the Wikidata server, so the zero count is meaningful.
func TestDiscoverCommand_WithoutOfflineMakesWikidataRequests(t *testing.T) {
	if n := runDiscoverWithWikidataConfig(t, false); n == 0 {
		t.Fatalf("wikidata requests = 0, want > 0 with wikidata_first: true and no --offline")
	}
}

func runDiscoverWithWikidataConfig(t *testing.T, offline bool) int64 {
	t.Helper()
	dbPath, db := newDiscoverTestDB(t)
	articlesRepo := sqliteadapter.NewArticleRepository(db)
	sourceID, err := source.NewID(source.TypeWeb, "example-com")
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if err := articlesRepo.Save(context.Background(), article.Article{
		ID: "a1", OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Source:    source.Source{ID: sourceID, Name: "Example", Type: source.TypeWeb},
		Title:     "Isotopes sign former Cypress CB Jai'Rene' Mossbank",
		Summary:   "Springfield has signed cornerback Jai'Rene' Mossbank.",
		FetchedAt: time.Now().UTC(), Status: article.StatusFetched,
	}); err != nil {
		t.Fatalf("save article: %v", err)
	}

	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{
			"content": `{"entities":[{"name":"Jai'Rene' Mossbank","type":"player","aliases":["Mossbank"],"metadata":{"position":"CB"},"confidence":0.9}]}`,
		}}}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer llmServer.Close()
	var wikidataRequests atomic.Int64
	wdServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wikidataRequests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer wdServer.Close()

	settingsPath := writeSettingsWithWikidata(t, dbPath, llmServer.URL, wdServer.URL)
	shared.ConfigPath = settingsPath
	defer func() { shared.ConfigPath = "configs/settings.yml" }()

	cmd := newDiscoverCommand()
	args := []string{
		"--all",
		"--dictionary", filepath.Join(filepath.Dir(settingsPath), "dictionary.yml"),
		"--manual", filepath.Join(filepath.Dir(settingsPath), "manual.yml"),
		"--generated", filepath.Join(filepath.Dir(settingsPath), "generated.yml"),
	}
	if offline {
		args = append(args, "--offline")
	}
	cmd.SetArgs(args)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\noutput:\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "fallback: LLM calls 0, SearXNG searches 0") {
		t.Fatalf("stdout missing fallback line:\n%s", stdout.String())
	}
	return wikidataRequests.Load()
}
