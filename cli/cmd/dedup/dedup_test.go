package dedup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

const dedupTestSettingsYAML = `
sqlite:
  path: data/operational.db
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
  sources_path: configs/sources.yml
source_policy:
  path: configs/source-policies.yml
classification:
  enabled: false
normalization:
  url:
    tracking_params: []
    strip_default_ports: true
    trailing_slash: strip
  text:
    shingle_size: 3
    min_token_length: 3
deduplication:
  near_duplicate:
    enabled: true
    method: jaccard
    default_threshold: 0.82
    short_text_max_tokens: 8
    short_text_threshold: 0.90
    window_hours: 72
    max_candidates: 50
    min_shared_terms: 2
    fields_used: [title]
    attribute_combo_window_minutes: 2
    rules_version: dedup-rules-v1
clustering:
  enabled: true
  event_threshold: 0.55
  rules_path: configs/event_clustering_rules.yml
  entity_catalog:
    manual_path: configs/entity_catalog_manual.yml
    generated_path: configs/entity_catalog_generated.yml
  candidate_limit: 50
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

const dedupTestDatasetYAML = `
pairs:
  - a: {title: "Isotopes Beat Sharks In Overtime Thriller", source_type: rss, content_type: article, published_at: "2026-07-01T10:00:00Z"}
    b: {title: "Isotopes Beat Sharks In An Overtime Thriller", source_type: rss, content_type: article, published_at: "2026-07-01T10:05:00Z"}
    label: same_content
  - a: {title: "Isotopes Sign Free Agent Cornerback", source_type: rss, content_type: article, published_at: "2026-07-01T08:00:00Z"}
    b: {title: "Weather Forecast Calls For Rain This Weekend", source_type: rss, content_type: article, published_at: "2026-07-01T08:00:00Z"}
    label: different
`

func writeDedupFixture(t *testing.T) (configPath, datasetPath string) {
	t.Helper()
	dir := t.TempDir()
	configPath = filepath.Join(dir, "settings.yml")
	if err := os.WriteFile(configPath, []byte(dedupTestSettingsYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	datasetPath = filepath.Join(dir, "dedup_pairs.yml")
	if err := os.WriteFile(datasetPath, []byte(dedupTestDatasetYAML), 0o600); err != nil {
		t.Fatalf("WriteFile(dataset) error = %v", err)
	}
	return configPath, datasetPath
}

func TestDedupEvaluateCmd_PrintsMetrics(t *testing.T) {
	configPath, datasetPath := writeDedupFixture(t)

	out, err := testsupport.Execute(t, "dedup", "evaluate", "--config", configPath, "--dataset", datasetPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !strings.Contains(out, "precision=") {
		t.Errorf("output = %q, want a precision= line", out)
	}
	if !strings.Contains(out, "confusion matrix") {
		t.Errorf("output = %q, want a confusion matrix section", out)
	}
}

func TestDedupEvaluateCmd_MissingDatasetFileReturnsError(t *testing.T) {
	configPath, _ := writeDedupFixture(t)

	_, err := testsupport.Execute(t, "dedup", "evaluate", "--config", configPath, "--dataset", filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatal("Execute() error = nil, want an error for a missing dataset file")
	}
}
