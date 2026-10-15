// Package replayfixtures builds the minimal-but-valid settings.yml,
// dictionary.yml, editorial-model directory, and source.ID fixtures shared
// by pipelinereplay's and backtestcandidate's tests — all
// three exercise the same pipelinereplay.Wire wiring against a scratch lab
// DB, so they need identical fixture shapes.
package replayfixtures

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

// MustSourceID wraps source.NewID and fails the test on error.
func MustSourceID(t *testing.T, platform source.Type, rawIdentifier string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, rawIdentifier)
	if err != nil {
		t.Fatalf("source.NewID(%q, %q) error = %v", platform, rawIdentifier, err)
	}
	return id
}

// TestDictionaryYAML writes a minimal-but-valid dictionary YAML file to a
// temp dir and returns its path. It reuses the category/entry shape from
// domain/classification/classification_test.go's buildMatcher: a
// strong_override official_names category containing "Springfield Isotopes".
func TestDictionaryYAML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dictionary.yml")
	contents := `
meta:
  version: "1.0.0"
  name: test_dictionary
  language: en

decision:
  positive_threshold_high: 10
  positive_threshold_medium: 6
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3

categories:
  official_names:
    weight: 12
    strong_override: true
    entries:
      - canonical_name: "Springfield Isotopes"
        type: official_name
        source: manual
        confidence: 1.0
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write dictionary fixture: %v", err)
	}
	return path
}

// TestEditorialModelDir writes a real, genuinely loadable editorial-model
// directory (model.json/manifest.json/metrics.json/schema.json/
// CHECKSUMS.sha256) via editorialmodel.Save — the same package
// editorialmodel.Load reads — and returns its path.
func TestEditorialModelDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "current")
	m := editorial.Model{
		Version: "editorial-test-v1", Algorithm: "linear_svm", FeatureSchemaVersion: "fs-1",
		Bias:    0.0,
		Weights: map[string]float64{"uni:isotopes": 1.0},
		Thresholds: editorial.Thresholds{
			// Reject/Accept span outside editorial.Evaluate's [0,100]
			// output range so the article lands in ZoneAmbiguous — under
			// editorial.mode: shadow this only appends an audit event, it
			// never gates accept/reject, so tests asserting a real FK
			// round-trip aren't coupled to a specific score.
			Reject: -1000, Accept: 1000,
		},
	}
	manifest := editorialmodel.Manifest{
		ID: "editorial-test-v1", Version: "editorial-test-v1", Algorithm: "linear_svm", FeatureSchemaVersion: "fs-1",
		TrainedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DatasetHash: "test-dataset-hash",
		MetricsSummary: `{}`, Thresholds: m.Thresholds, Config: `{}`, Status: "promoted",
	}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes"}}
	if err := editorialmodel.Save(dir, m, manifest, `{}`, schema); err != nil {
		t.Fatalf("editorialmodel.Save() error = %v", err)
	}
	return dir
}

// WriteTestConfig writes a minimal-but-valid settings.yml pointed at
// dictionaryPath, editorialMode ("disabled"|"shadow"|"enforce"), an
// editorial model directory at modelPath (ignored when editorialMode is
// "disabled"), and a minimal source-policies.yml fixture, then returns the
// settings.yml path. classification is always enabled; clustering stays
// disabled — pipelinereplay.Wire's own tests only exercise ProcessCycle
// wiring, not clustering behavior.
func WriteTestConfig(t *testing.T, dictionaryPath, editorialMode, modelPath string) string {
	t.Helper()
	dir := t.TempDir()

	sourcePoliciesPath := filepath.Join(dir, "source-policies.yml")
	if err := os.WriteFile(sourcePoliciesPath, []byte("version: 1\npolicies: []\n"), 0o600); err != nil {
		t.Fatalf("write source-policies fixture: %v", err)
	}

	// sqlite.path is required by config.Validate but never opened by
	// Wire — the lab DB is passed in directly.
	throwawaySQLitePath := filepath.Join(dir, "unused.db")

	settingsYAML := fmt.Sprintf(`
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
  allow_private_hosts: true
scraping_service:
  enabled: false
  base_url: ""
fetch:
  global_concurrency: 4
  fan_in_buffer: 64
  sources_path: configs/sources.yml
source_policy:
  path: %s
classification:
  enabled: true
deduplication:
  near_duplicate:
    enabled: false
    fields_used: ["title"]
clustering:
  enabled: false
editorial:
  mode: %s
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
  reconciliation:
    enabled: true
    channel_history_limit: 200
    clock_skew_seconds: 120
    batch_size: 20
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
  path: %s
  version: ""
dictionary:
  path: %s
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
`, throwawaySQLitePath, sourcePoliciesPath, editorialMode, modelPath, dictionaryPath)

	path := filepath.Join(dir, "settings.yml")
	if err := os.WriteFile(path, []byte(settingsYAML), 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}
	return path
}
