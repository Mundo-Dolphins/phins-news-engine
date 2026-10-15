package sourcepolicy_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const sourcePolicyTestSettingsTemplate = `
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
  base_url: ""
fetch:
  global_concurrency: 6
  fan_in_buffer: 64
  sources_path: %s
source_policy:
  path: %s
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

const sourcePolicyTestValidSourcesYAML = `twitter:
  proxy_base_url: http://localhost:8080
  accounts:
    - handle: SprIsotopes
      name: Springfield Isotopes
      interval_minutes: 5
      max_items_per_cycle: 10
      max_age_hours: 24
`

const sourcePolicyTestValidPoliciesYAML = `version: 1
mode: shadow
policies:
  - source_id: twitter:sprisotopes
    authority: official_team
`

const sourcePolicyTestOrphanedSourcesYAML = `twitter:
  accounts: []
`

const sourcePolicyTestOrphanedPoliciesYAML = `version: 1
mode: shadow
policies:
  - source_id: twitter:nonexistent
    authority: official_team
`

// mustSourceID builds a source.ID for test fixtures, failing the test
// immediately on an invalid platform/identifier combination. TypeRSS is
// used for these platform-agnostic fixtures since RSS's identifier format
// (an arbitrary slug) comfortably accepts "sprisotopes" and the tests in
// this file never assert on a particular platform.
func mustSourceID(t *testing.T, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(source.TypeRSS, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", source.TypeRSS, raw, err)
	}
	return id
}

func writeSourcePolicyFixture(t *testing.T, sourcesYAML, policiesYAML string) (configPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "operational.db")
	sourcesPath := filepath.Join(dir, "sources.yml")
	policiesPath := filepath.Join(dir, "source-policies.yml")

	if err := os.WriteFile(sourcesPath, []byte(sourcesYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(sources) error = %v", err)
	}
	if err := os.WriteFile(policiesPath, []byte(policiesYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(policies) error = %v", err)
	}

	configPath = filepath.Join(dir, "settings.yml")
	contents := fmt.Sprintf(sourcePolicyTestSettingsTemplate, dbPath, sourcesPath, policiesPath)
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	return configPath
}

func TestSourcePolicyValidate_ValidConfig_PrintsOK(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)

	out, err := testsupport.Execute(t, "source-policy", "validate", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "source policies: OK") {
		t.Errorf("output = %q, want it to contain %q", out, "source policies: OK")
	}
}

func TestSourcePolicyValidate_OrphanedPolicy_ReturnsError(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestOrphanedSourcesYAML, sourcePolicyTestOrphanedPoliciesYAML)

	out, err := testsupport.Execute(t, "source-policy", "validate", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want an error for an orphaned policy")
	}
	if !strings.Contains(out, "ISSUE:") {
		t.Errorf("output = %q, want it to contain an ISSUE: line", out)
	}
}

func TestSourcePolicyInspect_ConfiguredSource_PrintsResolvedPolicy(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)

	out, err := testsupport.Execute(t, "source-policy", "inspect", "--source", "twitter:sprisotopes", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "source_id: twitter:sprisotopes") {
		t.Errorf("output = %q, want it to contain %q", out, "source_id: twitter:sprisotopes")
	}
	if !strings.Contains(out, "authority: official_team") {
		t.Errorf("output = %q, want it to contain %q", out, "authority: official_team")
	}
}

func TestSourcePolicyInspect_UnconfiguredSource_PrintsDefault(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)

	out, err := testsupport.Execute(t, "source-policy", "inspect", "--source", "unknown_source", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "source_id: unknown_source") {
		t.Errorf("output = %q, want it to contain %q", out, "source_id: unknown_source")
	}
	if !strings.Contains(out, "authority: unknown") {
		t.Errorf("output = %q, want it to contain %q", out, "authority: unknown")
	}
}

// seedSourcePolicyDB copies an empty migrated database and records a
// couple of source_policy_decisions rows via the real SourcePolicyRepository
// — source_policy_decisions.article_id
// has a foreign key to articles(id) (see label_test.go's
// testsupport.SeedArticleForLabeling), so the referenced articles are inserted first.
func seedSourcePolicyDB(t *testing.T, dbPath string) {
	t.Helper()
	testsupport.CopyMigratedTestDB(t, dbPath)
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	articles := sqlite.NewArticleRepository(db)
	for _, id := range []string{"article-1", "article-2"} {
		if err := articles.Save(context.Background(), article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source:    source.Source{ID: mustSourceID(t, "sprisotopes")},
			FetchedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Status: article.StatusFetched,
		}); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
	}

	repo := sqlite.NewSourcePolicyRepository(db)
	decidedAt := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	records := []out.SourcePolicyDecisionRecord{
		{
			ID: "1", ArticleID: "article-1", SourceID: "sprisotopes", Authority: "official_team",
			PolicyVersion: "1", DecisionCode: "official_confirmation_no_publish",
			WouldPublishStandard: true, WouldPublishSpecial: false, Mode: "shadow", DecidedAt: decidedAt,
			CooldownOutcome: "",
		},
		{
			ID: "2", ArticleID: "article-2", SourceID: "sprisotopes", Authority: "official_team",
			PolicyVersion: "1", DecisionCode: "publish_substantial_update",
			WouldPublishStandard: true, WouldPublishSpecial: true, Mode: "shadow", DecidedAt: decidedAt.Add(time.Hour),
			CooldownOutcome: "",
		},
	}
	for _, r := range records {
		if err := repo.Save(context.Background(), r); err != nil {
			t.Fatalf("seed record %s: %v", r.ID, err)
		}
	}
}

func TestSourcePolicyAudit_PrintsSummary(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedSourcePolicyDB(t, dbPath)

	out, err := testsupport.Execute(t, "source-policy", "audit", "--from", "2026-01-01", "--to", "2026-01-31", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "total decisions: 2") {
		t.Errorf("output = %q, want it to contain %q", out, "total decisions: 2")
	}
	if !strings.Contains(out, "divergences from standard policy: 1") {
		t.Errorf("output = %q, want it to contain %q", out, "divergences from standard policy: 1")
	}
	if !strings.Contains(out, "official_confirmation_no_publish: 1") {
		t.Errorf("output = %q, want it to contain %q", out, "official_confirmation_no_publish: 1")
	}
	if !strings.Contains(out, "by cooldown outcome:") {
		t.Errorf("output = %q, want it to contain %q", out, "by cooldown outcome:")
	}
	if !strings.Contains(out, "not_applicable: 2") {
		t.Errorf("output = %q, want it to contain %q", out, "not_applicable: 2")
	}
}

// seedSourcePolicyDBMixedCooldownOutcome mirrors seedSourcePolicyDB but
// records a mix of the two raw cooldown_outcome values that both mean "no
// cooldown decision applies": the empty string "" (reservation guard never
// ran) and the literal "not_applicable" (ResolveCooldownReservation ran but
// found the code not cooldown-relevant). It seeds two "" rows and three
// "not_applicable" rows so a correct text formatter must print exactly one
// merged "not_applicable: 5" line rather than two separate lines.
func seedSourcePolicyDBMixedCooldownOutcome(t *testing.T, dbPath string) {
	t.Helper()
	testsupport.CopyMigratedTestDB(t, dbPath)
	db, err := infrasqlite.Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	articles := sqlite.NewArticleRepository(db)
	articleIDs := []string{"article-1", "article-2", "article-3", "article-4", "article-5"}
	for _, id := range articleIDs {
		if err := articles.Save(context.Background(), article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Source:    source.Source{ID: mustSourceID(t, "sprisotopes")},
			FetchedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Status: article.StatusFetched,
		}); err != nil {
			t.Fatalf("seed article %s: %v", id, err)
		}
	}

	repo := sqlite.NewSourcePolicyRepository(db)
	decidedAt := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	cooldownOutcomes := []string{"", "", "not_applicable", "not_applicable", "not_applicable"}
	for i, id := range articleIDs {
		r := out.SourcePolicyDecisionRecord{
			ID: fmt.Sprintf("mixed-%d", i+1), ArticleID: id, SourceID: "sprisotopes", Authority: "official_team",
			PolicyVersion: "1", DecisionCode: "official_confirmation_no_publish",
			WouldPublishStandard: true, WouldPublishSpecial: false, Mode: "shadow",
			DecidedAt:       decidedAt.Add(time.Duration(i) * time.Hour),
			CooldownOutcome: cooldownOutcomes[i],
		}
		if err := repo.Save(context.Background(), r); err != nil {
			t.Fatalf("seed record %s: %v", r.ID, err)
		}
	}
}

func TestSourcePolicyAudit_MergesNotApplicableCooldownOutcomeVariants(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedSourcePolicyDBMixedCooldownOutcome(t, dbPath)

	out, err := testsupport.Execute(t, "source-policy", "audit", "--from", "2026-01-01", "--to", "2026-01-31", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v\noutput:\n%s", err, out)
	}

	// Both raw keys ("" and "not_applicable") are present in the seeded
	// data with different counts (2 and 3). The text formatter must merge
	// them into a single "not_applicable: 5" line, not print one line per
	// raw key.
	if n := strings.Count(out, "not_applicable:"); n != 1 {
		t.Fatalf("output contains %d \"not_applicable:\" lines, want exactly 1 (merged); output:\n%s", n, out)
	}
	if !strings.Contains(out, "  not_applicable: 5\n") {
		t.Errorf("output = %q, want it to contain a single merged %q line", out, "  not_applicable: 5")
	}
}

func TestSourcePolicyAudit_JSONFormat(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedSourcePolicyDB(t, dbPath)

	out, err := testsupport.Execute(t, "source-policy", "audit", "--from", "2026-01-01", "--to", "2026-01-31", "--format", "json", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, `"TotalDecisions": 2`) {
		t.Errorf("output = %q, want valid-looking JSON output containing TotalDecisions: 2", out)
	}
}

func TestSourcePolicyAudit_SourceFilter_ExcludesOtherSources(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedSourcePolicyDB(t, dbPath)

	out, err := testsupport.Execute(t, "source-policy", "audit", "--from", "2026-01-01", "--to", "2026-01-31", "--source", "nonexistent", "--format", "text", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "total decisions: 0") {
		t.Errorf("output = %q, want it to contain %q", out, "total decisions: 0")
	}
}

func TestSourcePolicySimulate_PrintsRecordedDecisions(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedSourcePolicyDB(t, dbPath)

	out, err := testsupport.Execute(t, "source-policy", "simulate", "--article-id", "article-1", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "article: article-1 (source: rss:sprisotopes)") {
		t.Errorf("output = %q, want it to contain %q", out, "article: article-1 (source: rss:sprisotopes)")
	}
	if !strings.Contains(out, "code=official_confirmation_no_publish") {
		t.Errorf("output = %q, want it to contain %q", out, "code=official_confirmation_no_publish")
	}
}

func TestSourcePolicySimulate_UnknownArticle_ReturnsError(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedSourcePolicyDB(t, dbPath)

	out, err := testsupport.Execute(t, "source-policy", "simulate", "--article-id", "nonexistent", "--config", configPath)
	if err == nil {
		t.Fatalf("Execute() error = nil, want an error for an unknown article ID\noutput:\n%s", out)
	}
}

func TestSourcePolicyAudit_InvalidFormat_ReturnsError(t *testing.T) {
	configPath := writeSourcePolicyFixture(t, sourcePolicyTestValidSourcesYAML, sourcePolicyTestValidPoliciesYAML)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	seedSourcePolicyDB(t, dbPath)

	out, err := testsupport.Execute(t, "source-policy", "audit", "--from", "2026-01-01", "--to", "2026-01-31", "--format", "bogus", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want an error for an unrecognized --format")
	}
	if !strings.Contains(err.Error(), `unknown --format "bogus"`) {
		t.Errorf("Execute() error = %v, want it to name the invalid --format value\noutput:\n%s", err, out)
	}
}
