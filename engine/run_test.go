package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/logging"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const testRSSFeedXML = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Test Feed</title>
<item><title>Isotopes sign new player</title><link>https://example.com/binary-test</link>
<pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate></item>
</channel></rss>`

// TestRun_RSSWithoutScrapingService_FetchesAndPersistsArticles is the
// binary/composition-root test for booting the real
// engine.Run (the scheduler composition root) with scraping_service.enabled: false and one
// enabled RSS feed must produce a persisted article — proving the fix
// end-to-end, not just inside fetcherwiring.Build in isolation.
func TestRun_RSSWithoutScrapingService_FetchesAndPersistsArticles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(testRSSFeedXML))
	}))
	defer server.Close()

	dir := t.TempDir()
	sourcesPath := filepath.Join(dir, "sources.yml")
	dbPath := filepath.Join(dir, "operational.db")
	sourcePoliciesPath := filepath.Join(dir, "source-policies.yml")

	if err := os.WriteFile(sourcePoliciesPath, []byte("policies: []\n"), 0o600); err != nil {
		t.Fatalf("write source-policies fixture: %v", err)
	}
	if err := os.WriteFile(sourcesPath, []byte(`
websites: []
rss_feeds:
  - name: "Test Feed"
    url: "`+server.URL+`/feed.xml"
    source_type: hard_news
    interval_minutes: 1
    max_items_per_cycle: 20
    max_age_hours: 999999
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
	settingsYAML := minimalSettingsYAML(dbPath, sourcesPath, sourcePoliciesPath)
	if err := os.WriteFile(settingsPath, []byte(settingsYAML), 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}

	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := logging.New("error", false)

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, settingsPath, config.ProfileLocal, logger, Options{})
	}()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open test db handle: %v", err)
	}
	defer db.Close()

	deadline := time.Now().Add(10 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		row := db.QueryRow(`SELECT COUNT(*) FROM articles`)
		if err := row.Scan(&count); err == nil && count > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s of cancellation")
	}

	if count == 0 {
		t.Fatal("articles table has 0 rows, want >= 1 — RSS should run without the Scraping Service")
	}
}

func minimalSettingsYAML(dbPath, sourcesPath, sourcePoliciesPath string) string {
	return `
sqlite:
  path: ` + dbPath + `
  busy_timeout_ms: 5000
scheduler:
  check_interval_minutes: 1
  outbox_interval_minutes: 1
  cleanup_interval_hours: 24
http:
  timeout_seconds: 10
  max_response_bytes: 2097152
  allow_private_hosts: true
scraping_service:
  enabled: false
fetch:
  global_concurrency: 4
  fan_in_buffer: 16
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

// writeTestConfig writes a minimal-but-valid settings.yml (plus the
// sources.yml/source-policies.yml fixtures it references, reusing the exact
// same minimal fixture content TestRun_RSSWithoutScrapingService_... already
// writes) into dir, with editorial.mode and model.path overridden — for
// TestRun_* tests exercising engine.Run's startup-fatal editorial
// model checks. Returns the settings.yml path.
func writeTestConfig(t *testing.T, dir, editorialMode, modelPath string) string {
	t.Helper()
	sourcesPath := filepath.Join(dir, "sources.yml")
	dbPath := filepath.Join(dir, "operational.db")
	sourcePoliciesPath := filepath.Join(dir, "source-policies.yml")

	if err := os.WriteFile(sourcePoliciesPath, []byte("policies: []\n"), 0o600); err != nil {
		t.Fatalf("write source-policies fixture: %v", err)
	}
	if err := os.WriteFile(sourcesPath, []byte(`
websites: []
rss_feeds:
  - name: "Test Feed"
    url: "http://127.0.0.1:1/feed.xml"
    source_type: hard_news
    interval_minutes: 1
    max_items_per_cycle: 20
    max_age_hours: 999999
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
	settingsYAML := minimalSettingsYAML(dbPath, sourcesPath, sourcePoliciesPath)
	settingsYAML = strings.Replace(settingsYAML,
		"editorial:\n  mode: disabled\n  ambiguous_policy: reject",
		"editorial:\n  mode: "+editorialMode+"\n  ambiguous_policy: reject", 1)
	settingsYAML = strings.Replace(settingsYAML,
		"model:\n  path: \"\"\n  version: \"\"",
		"model:\n  path: \""+modelPath+"\"\n  version: \"\"", 1)
	if !strings.Contains(settingsYAML, "mode: "+editorialMode) || !strings.Contains(settingsYAML, "path: \""+modelPath+"\"") {
		t.Fatalf("writeTestConfig: string-replace of editorial.mode/model.path into minimalSettingsYAML's template failed (template shape changed?)")
	}
	if err := os.WriteFile(settingsPath, []byte(settingsYAML), 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}
	return settingsPath
}

// testEditorialModelArtifactHash recomputes the real artifact_hash formula
// modelpromotion.Promote uses when it stamps manifest.json (sha256 of
// json.Marshal({Bias, Weights}) with that exact anonymous struct
// shape/field order — see infra/editorialmodel's own
// model_test.go, which this mirrors since that helper is unexported and
// lives in a different package), so the fatal-startup tests below exercise
// editorialmodel.Validate's genuine checksum comparison instead of a
// hardcoded placeholder that would never actually be checked.
func testEditorialModelArtifactHash(m editorial.Model) string {
	data, err := json.Marshal(struct {
		Bias    float64
		Weights map[string]float64
	}{m.Bias, m.Weights})
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func testEditorialModel() editorial.Model {
	return editorial.Model{
		Version: "test-model-v1", Algorithm: "linear_svm", FeatureSchemaVersion: "fs-1",
		Bias: 0.5, Weights: map[string]float64{"uni:isotopes": 1.2, "uni:sponsored": -0.8},
		Thresholds: editorial.Thresholds{Reject: 20, Accept: 80},
	}
}

func testEditorialManifest(m editorial.Model) editorialmodel.Manifest {
	return editorialmodel.Manifest{
		ID: "m1", Version: m.Version, Algorithm: m.Algorithm, FeatureSchemaVersion: m.FeatureSchemaVersion,
		TrainedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DatasetHash: "dh1",
		MetricsSummary: `{"f1":0.85}`, Thresholds: m.Thresholds,
		Config: `{"seed":42}`, Status: "promoted",
		ArtifactHash: testEditorialModelArtifactHash(m),
	}
}

func testEditorialFeatureSchema() editorialmodel.FeatureSchema {
	return editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}
}

// validEditorialModelDir saves a genuinely well-formed, internally
// consistent model artifact (correct artifact_hash, correctly-ordered
// thresholds, matching CHECKSUMS.sha256) to dir/model, via the same
// editorialmodel.Save this package's production code (run.go) reads with
// Inspect/Load/Validate.
func validEditorialModelDir(t *testing.T, dir string) string {
	t.Helper()
	modelDir := filepath.Join(dir, "model")
	m := testEditorialModel()
	if err := editorialmodel.Save(modelDir, m, testEditorialManifest(m), `{"f1":0.85}`, testEditorialFeatureSchema()); err != nil {
		t.Fatalf("editorialmodel.Save() error = %v", err)
	}
	return modelDir
}

// tamperedChecksumModelDir saves a model artifact whose manifest.json
// artifact_hash field is deliberately wrong (does not match model.json's
// actual Bias/Weights) — editorialmodel.Load/Inspect's own per-file
// CHECKSUMS.sha256 checks pass (Save computes CHECKSUMS.sha256 from
// whatever manifest content is actually written), but
// editorialmodel.Validate's cross-file artifact_hash consistency check
// must fail.
func tamperedChecksumModelDir(t *testing.T, dir string) string {
	t.Helper()
	modelDir := filepath.Join(dir, "model")
	m := testEditorialModel()
	manifest := testEditorialManifest(m)
	manifest.ArtifactHash = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := editorialmodel.Save(modelDir, m, manifest, `{"f1":0.85}`, testEditorialFeatureSchema()); err != nil {
		t.Fatalf("editorialmodel.Save() error = %v", err)
	}
	return modelDir
}

// modelDirWithThresholds saves a model artifact with the given
// (deliberately invertible) Reject/Accept thresholds — checksum-valid
// (Validate's checksum check runs first and must pass), so only the
// threshold-ordering check at the end of editorialmodel.Validate is
// exercised.
func modelDirWithThresholds(t *testing.T, dir string, reject, accept float64) string {
	t.Helper()
	modelDir := filepath.Join(dir, "model")
	m := testEditorialModel()
	m.Thresholds = editorial.Thresholds{Reject: reject, Accept: accept}
	manifest := testEditorialManifest(m)
	manifest.Thresholds = m.Thresholds
	if err := editorialmodel.Save(modelDir, m, manifest, `{"f1":0.85}`, testEditorialFeatureSchema()); err != nil {
		t.Fatalf("editorialmodel.Save() error = %v", err)
	}
	return modelDir
}

// TestRun_CorruptEditorialModel_FailsStartupBeforeFirstCycle proves issue
// #168's fail-fast contract (Task 10): a genuinely corrupt model.json (its
// bytes no longer match the hash CHECKSUMS.sha256 recorded for it at Save
// time) must abort Run() before the scheduler ever starts, in enforce mode.
func TestRun_CorruptEditorialModel_FailsStartupBeforeFirstCycle(t *testing.T) {
	dir := t.TempDir()
	modelDir := validEditorialModelDir(t, dir)
	if err := os.WriteFile(filepath.Join(modelDir, "model.json"), []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("corrupt model.json: %v", err)
	}
	cfgPath := writeTestConfig(t, dir, "enforce", modelDir)
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	err := Run(context.Background(), cfgPath, config.ProfileTest, logging.New("error", false), Options{})
	if err == nil {
		t.Fatal("Run() error = nil, want a fatal error for a corrupt editorial model in enforce mode")
	}
}

// TestRun_ChecksumMismatch_FailsStartup proves Task 10's fail-fast contract
// for the cross-file (not per-file) checksum case: manifest.json's
// artifact_hash does not match model.json's actual weights.
func TestRun_ChecksumMismatch_FailsStartup(t *testing.T) {
	dir := t.TempDir()
	modelDir := tamperedChecksumModelDir(t, dir)
	cfgPath := writeTestConfig(t, dir, "enforce", modelDir)
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	err := Run(context.Background(), cfgPath, config.ProfileTest, logging.New("error", false), Options{})
	if err == nil {
		t.Fatal("Run() error = nil, want a fatal error for checksum mismatch in enforce mode")
	}
}

// TestRun_InvertedThresholds_FailsStartup proves Task 10's fail-fast
// contract for a promoted-but-nonsensical model: Reject >= Accept.
func TestRun_InvertedThresholds_FailsStartup(t *testing.T) {
	dir := t.TempDir()
	modelDir := modelDirWithThresholds(t, dir, 90, 10)
	cfgPath := writeTestConfig(t, dir, "enforce", modelDir)
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	err := Run(context.Background(), cfgPath, config.ProfileTest, logging.New("error", false), Options{})
	if err == nil {
		t.Fatal("Run() error = nil, want a fatal error for inverted reject/accept thresholds")
	}
}

// TestRun_ShadowMode_SameCorruptModel_AlsoFailsStartup confirms Task 10's
// fatal behavior applies to shadow (and, by the same code path, canary),
// not just enforce — the design spec is explicit that only "disabled"
// skips model loading entirely.
func TestRun_ShadowMode_SameCorruptModel_AlsoFailsStartup(t *testing.T) {
	dir := t.TempDir()
	modelDir := validEditorialModelDir(t, dir)
	if err := os.WriteFile(filepath.Join(modelDir, "model.json"), []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("corrupt model.json: %v", err)
	}
	cfgPath := writeTestConfig(t, dir, "shadow", modelDir)
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	err := Run(context.Background(), cfgPath, config.ProfileTest, logging.New("error", false), Options{})
	if err == nil {
		t.Fatal(`Run() error = nil, want a fatal error in shadow mode too (only "disabled" may skip model loading)`)
	}
}

func TestBuildClusterSettings_UsesConfiguredCandidateLimit(t *testing.T) {
	got := buildClusterSettings(clusteringrules.RulesFile{}, 123)
	if got.CandidateLimit != 123 {
		t.Errorf("CandidateLimit = %d, want 123", got.CandidateLimit)
	}
}

func TestRun_MissingConfig_ReturnsLoadConfigError(t *testing.T) {
	err := Run(context.Background(), filepath.Join(t.TempDir(), "missing.yml"), config.ProfileLocal, logging.New("error", false), Options{})
	if err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("Run() error = %v, want an error containing %q", err, "load config")
	}
}

// TestRun_ReconciliationDisabled_NeverCallsChannelImporter boots Run all the
// way into the scheduler (an article gets persisted) with
// publication.reconciliation disabled, and proves the host-supplied
// ChannelImporter — which logs warnings when no session exists — is never
// invoked.
func TestRun_ReconciliationDisabled_NeverCallsChannelImporter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(testRSSFeedXML))
	}))
	defer server.Close()

	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "disabled", "")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	sourcesPath := filepath.Join(dir, "sources.yml")
	sources, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcesPath, []byte(strings.Replace(string(sources), "http://127.0.0.1:1", server.URL, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "reconciliation") {
		t.Fatal("fixture unexpectedly configures reconciliation")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var called atomic.Bool
	opts := Options{ChannelImporter: func(*config.Config, out.Logger) out.TelegramChannelImporter {
		called.Store(true)
		return nil
	}}
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfgPath, config.ProfileTest, logging.New("error", false), opts)
	}()

	db, err := sql.Open("sqlite", filepath.Join(dir, "operational.db"))
	if err != nil {
		t.Fatalf("open test db handle: %v", err)
	}
	defer db.Close()
	deadline := time.Now().Add(10 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM articles`).Scan(&count); err == nil && count > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s of cancellation")
	}
	if count == 0 {
		t.Fatal("no article persisted; Run never reached the scheduler, so the assertion below would be vacuous")
	}
	if called.Load() {
		t.Fatal("ChannelImporter was called with publication.reconciliation disabled")
	}
}

func TestRun_ReconciliationEnabled_CallsChannelImporterOnce(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, "disabled", "")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "publication:\n  max_attempts: 6\n"
	if !strings.Contains(string(raw), anchor) {
		t.Fatal("fixture template changed: publication block anchor not found")
	}
	enabled := strings.Replace(string(raw), anchor,
		anchor+"  reconciliation: {enabled: true, channel_history_limit: 100, clock_skew_seconds: 120, batch_size: 10}\n", 1)
	if err := os.WriteFile(cfgPath, []byte(enabled), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	opts := Options{ChannelImporter: func(cfg *config.Config, _ out.Logger) out.TelegramChannelImporter {
		calls++
		cancel() // stop the scheduler as soon as it starts
		if !cfg.Publication.Reconciliation.Enabled {
			t.Error("ChannelImporter got a config with reconciliation disabled")
		}
		return nil
	}}
	if err := Run(ctx, cfgPath, config.ProfileTest, logging.New("error", false), opts); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("ChannelImporter calls = %d, want 1", calls)
	}
}
