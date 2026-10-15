package testsupport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// secretEnvVars are every config knob config.go reads directly from the
// process environment (os.Getenv) rather than from the YAML fixture. A
// developer's shell commonly has these exported for running the bot
// manually (bot tokens, LLM/API keys, a real Telegram phone number) —
// left set, they leak into `go test` and silently satisfy "credentials
// missing" preconditions that tests rely on, or worse, make live calls to
// external services. init() below clears them once per test binary so
// every test that drives the CLI through Execute/ExecuteWithStdin/etc.
// starts from a hermetic environment regardless of the operator's shell;
// a test that needs one of these set still does so explicitly via
// t.Setenv, which scopes it to that test and restores it afterwards.
var secretEnvVars = []string{
	"TELEGRAM_BOT_TOKEN",
	"TELEGRAM_API_ID",
	"TELEGRAM_API_HASH",
	"TELEGRAM_PHONE",
	"TELEGRAM_SESSION_PATH",
	"TELEGRAM_TIMEOUT_SECONDS",
	"SCRAPING_SERVICE_API_KEY",
	"LLM_API_KEY",
	"LLM_FALLBACK_API_KEY",
	"OFFLINE_TRAINING_LLM_API_KEY",
	"WEB_SEARCH_API_KEY",
	"SQLITE_DB_PATH",
}

func init() {
	for _, name := range append(secretEnvVars, config.ProfileEnvVar) {
		_ = os.Unsetenv(name)
	}
}

const FixtureTemplate = `
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
dictionary_mining:
  path: ""
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

const testDictionaryYAML = `
meta:
  version: "1.0.0"
  name: test_dictionary
  language: en
  generated_at: "2026-01-01"
decision:
  positive_threshold_high: 10
  positive_threshold_medium: 7
  negative_threshold_high: 0
  title_weight_multiplier: 2
  max_count_per_term: 3
categories:
  official_names:
    weight: 10
    entries:
      - {canonical_name: "Isotopes", type: official_name, source: manual, confidence: 0.9}
  coaching_staff:
    weight: 5
    entries: []
  nfl_people_context:
    weight: 1
    entries: []
`

const DedupTestSettingsYAML = `
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
  allow_private_hosts: true
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

func WriteFixture(t *testing.T) string {
	t.Helper()
	return WriteFixtureWithDatabase(t, true)
}

func WriteFreshFixture(t *testing.T) string {
	t.Helper()
	return WriteFixtureWithDatabase(t, false)
}

func WriteFixtureWithDatabase(t *testing.T, migrated bool) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "operational.db")
	miningPolicyPath := writeDictionaryMiningPolicy(t, dir)
	if migrated {
		CopyMigratedTestDB(t, dbPath)
	}
	configPath := filepath.Join(dir, "settings.yml")
	contents := withDictionaryMiningPolicy(fmt.Sprintf(FixtureTemplate, dbPath), miningPolicyPath)
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return configPath
}

func writeDictionaryMiningPolicy(t testing.TB, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "dictionary_mining.yml")
	if err := os.WriteFile(path, []byte("min_support: 1\nignored_terms: []\nignored_tokens: []\nignored_substrings: []\ncategory_guesses: {}\n"), 0o600); err != nil {
		t.Fatalf("write dictionary mining policy: %v", err)
	}
	return path
}

func withDictionaryMiningPolicy(contents, path string) string {
	return strings.Replace(contents, "dictionary_mining:\n  path: \"\"", fmt.Sprintf("dictionary_mining:\n  path: %q", path), 1)
}

func CopyMigratedTestDB(t testing.TB, path string) {
	t.Helper()
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("open SQLite test database: %v", err)
	}
	defer db.Close()
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate SQLite test database: %v", err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint SQLite test database: %v", err)
	}
}

// resetFlags restores every flag of the command tree to its default. The CLI
// tree is a package-level singleton re-executed by many tests, and pflag never
// resets a flag a later invocation omits, so without this a --json, --force or
// --format from one test leaks into the next (and into a second -count run).
func resetFlags(c *cobra.Command) {
	reset := func(fs *pflag.FlagSet) {
		fs.VisitAll(func(f *pflag.Flag) {
			if !f.Changed {
				return
			}
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		})
	}
	reset(c.Flags())
	reset(c.PersistentFlags())
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

func Execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	defer resetFlags(cli.Root())
	var out bytes.Buffer
	cli.Root().SetOut(&out)
	cli.Root().SetErr(&out)
	cli.Root().SetArgs(args)
	err := cli.Root().Execute()
	return out.String(), err
}

func ExecuteWithStdin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	defer resetFlags(cli.Root())
	var out bytes.Buffer
	cli.Root().SetOut(&out)
	cli.Root().SetErr(&out)
	cli.Root().SetIn(strings.NewReader(stdin))
	cli.Root().SetArgs(args)
	err := cli.Root().Execute()
	return out.String(), err
}

func ExecuteWithContext(t *testing.T, ctx context.Context, stdin string, args ...string) (string, error) {
	t.Helper()
	defer resetFlags(cli.Root())
	var out bytes.Buffer
	cli.Root().SetOut(&out)
	cli.Root().SetErr(&out)
	cli.Root().SetIn(strings.NewReader(stdin))
	cli.Root().SetArgs(args)

	if target, _, findErr := cli.Root().Find(args); findErr == nil {
		cli.Root().SetContext(ctx)
		target.SetContext(ctx)
		defer func() {
			cli.Root().SetContext(context.Background())
			target.SetContext(context.Background())
		}()
	}

	err := cli.Root().ExecuteContext(ctx)
	return out.String(), err
}

func ExecuteWithReader(t *testing.T, r io.Reader, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cli.Root().SetOut(&out)
	cli.Root().SetErr(&out)
	cli.Root().SetIn(r)
	cli.Root().SetArgs(args)
	err := cli.Root().Execute()
	return out.String(), err
}

func SeedArticleForLabeling(t *testing.T) (configPath, articleID string) {
	t.Helper()
	configPath = WriteFixture(t)
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")

	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	articles := sqliteadapter.NewArticleRepository(db)
	articleID = "a1"
	if err := articles.Save(context.Background(), article.Article{
		ID: articleID, OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		FetchedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Status: article.StatusFetched,
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	return configPath, articleID
}

func MustID(t testing.TB, platform source.Type, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", platform, raw, err)
	}
	return id
}

func WriteFixtureWithDictionary(t *testing.T) (configPath, dictPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "operational.db")
	miningPolicyPath := writeDictionaryMiningPolicy(t, dir)
	CopyMigratedTestDB(t, dbPath)
	dictPath = filepath.Join(dir, "dictionary.yml")
	if err := os.WriteFile(dictPath, []byte(testDictionaryYAML), 0o600); err != nil {
		t.Fatalf("write dictionary: %v", err)
	}
	configPath = filepath.Join(dir, "settings.yml")
	contents := strings.Replace(withDictionaryMiningPolicy(fmt.Sprintf(FixtureTemplate, dbPath), miningPolicyPath), "classification:\n  enabled: false", "classification:\n  enabled: true", 1)
	contents = strings.Replace(contents, "dictionary:\n  path: \"\"\n  version: \"\"", fmt.Sprintf("dictionary:\n  path: %q\n  version: \"\"", dictPath), 1)
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return configPath, dictPath
}

func RewriteFixtureModelPath(t *testing.T, srcConfigPath, dstConfigPath, modelPath string) {
	t.Helper()
	rootDir := filepath.Dir(srcConfigPath)
	srcName, err := filepath.Rel(rootDir, srcConfigPath)
	if err != nil {
		t.Fatalf("resolve fixture source path: %v", err)
	}
	dstName, err := filepath.Rel(rootDir, dstConfigPath)
	if err != nil {
		t.Fatalf("resolve fixture destination path: %v", err)
	}
	if strings.HasPrefix(srcName, ".."+string(filepath.Separator)) || strings.HasPrefix(dstName, ".."+string(filepath.Separator)) || filepath.IsAbs(srcName) || filepath.IsAbs(dstName) {
		t.Fatalf("fixture rewrite paths must stay under %s", rootDir)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	defer root.Close()

	data, err := root.ReadFile(srcName)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rewritten := strings.Replace(string(data), "model:\n  path: \"\"\n  version: \"\"", fmt.Sprintf("model:\n  path: %q\n  version: \"\"", modelPath), 1)
	if rewritten == string(data) {
		t.Fatal("RewriteFixtureModelPath: model block not found in FixtureTemplate")
	}
	if err := root.WriteFile(dstName, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("write rewritten fixture: %v", err)
	}
}

func PassingGateReportJSON() string {
	report := editorialtrain.Report{
		Overall: editorialtrain.BinaryMetrics{TP: 90, FP: 5, FN: 5, TN: 900},
		PRAUC:   0.9, InferenceNanos: 100,
	}
	data, _ := json.Marshal(struct {
		Validate editorialtrain.Report
		Test     editorialtrain.Report
	}{report, report})
	return string(data)
}

func WriteGatePassingCandidateModel(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Bias: 0, Weights: map[string]float64{"uni:x": 1}, Thresholds: editorial.Thresholds{Reject: 20, Accept: 80}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(dir, m, manifest, PassingGateReportJSON(), schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return dir
}
