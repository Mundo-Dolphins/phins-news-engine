package configcmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli"
)

func TestConfigValidate_PrintsCapabilityReport(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "operational.db")
	sourcesPath := filepath.Join(dir, "sources.yml")
	sourcePoliciesPath := filepath.Join(dir, "source-policies.yml")
	if err := os.WriteFile(sourcePoliciesPath, []byte("policies: []\n"), 0o600); err != nil {
		t.Fatalf("write source-policies fixture: %v", err)
	}
	if err := os.WriteFile(sourcesPath, []byte(`
websites: []
rss_feeds:
  - name: "Feed"
    url: "https://example.com/feed.xml"
    interval_minutes: 5
    max_items_per_cycle: 20
    max_age_hours: 24
    enabled: true
reddit: {feeds: []}
bluesky: {accounts: [], feeds: []}
twitter: {proxy_base_url: "", accounts: [], searches: []}
instagram: {rss_base_url: "", accounts: []}
youtube: {rss_base_url: "", accounts: []}
`), 0o600); err != nil {
		t.Fatalf("write sources fixture: %v", err)
	}
	settingsPath := filepath.Join(dir, "settings.yml")
	if err := os.WriteFile(settingsPath, []byte(configValidateFixtureYAML(dbPath, sourcesPath, sourcePoliciesPath)), 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}

	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	var out bytes.Buffer
	cli.Root().SetOut(&out)
	cli.Root().SetArgs([]string{"config", "validate", "--config", settingsPath, "--profile", "local"})
	if err := cli.Root().Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "rss") {
		t.Errorf("output = %q, want it to mention the rss category", got)
	}
}

// configValidateFixtureYAML is self-contained (does not reuse cmd_test.go's
// shared testsupport.FixtureTemplate) so this file has zero blast radius on the other
// ~12 test files that parameterize testsupport.FixtureTemplate via fmt.Sprintf with a
// single %s verb. Field-for-field identical in shape to Task 11's
// minimalSettingsYAML in the host service tests.
func configValidateFixtureYAML(dbPath, sourcesPath, sourcePoliciesPath string) string {
	return `
sqlite:
  path: ` + dbPath + `
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
  sources_path: ` + sourcesPath + `
fetch_sources:
  website: {enabled: false}
  rss: {enabled: true}
  reddit: {enabled: false}
  bluesky: {enabled: false}
  twitter: {enabled: false}
  instagram: {enabled: false}
  youtube: {enabled: false}
source_policy:
  path: ` + sourcePoliciesPath + `
classification:
  enabled: false
normalization:
  url: {}
  text: {}
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
  channel_id: "-1001234567890"
  timeout_seconds: 10
publication_policy:
  burst: {max_per_window: 6, window_minutes: 60}
  same_topic: {max_per_window: 2, window_minutes: 180}
  quiet_hours: {enabled: false, start_hour: 2, end_hour: 6}
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
  sqlite_remote_db_hint: ""
  retention_days: 7
dictionary_update:
  enabled: false
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
}
