package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

const validYAML = `
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
  global_concurrency: 4
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
  path: ""
  version: ""
dictionary:
  path: ""
  version: ""
dictionary_mining:
  path: configs/dictionary_mining.yml
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
    base_url: http://localhost:11434/v1
    model: qwen3:4b
    temperature: 0
    timeout_seconds: 60
    batch_size: 20
    limit: 500
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

func TestParseProfile(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    config.Profile
		wantErr bool
	}{
		{"empty defaults to local", "", config.ProfileLocal, false},
		{"local", "local", config.ProfileLocal, false},
		{"test", "test", config.ProfileTest, false},
		{"production", "production", config.ProfileProduction, false},
		{"case insensitive", "Production", config.ProfileProduction, false},
		{"unknown", "staging", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.ParseProfile(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseProfile(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("ParseProfile(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveProfile(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		env     string
		want    config.Profile
		wantErr bool
	}{
		{"flag wins over env", "production", "test", config.ProfileProduction, false},
		{"env used when flag empty", "", "test", config.ProfileTest, false},
		{"default when both empty", "", "", config.ProfileLocal, false},
		{"error on invalid flag value", "staging", "", "", true},
		{"error on invalid env value", "", "staging", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(config.ProfileEnvVar, tt.env)
			got, err := config.ResolveProfile(tt.flag)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveProfile(%q) error = %v, wantErr %v", tt.flag, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("ResolveProfile(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
	}
}

func TestProfileEnvVar_DefaultIsNeutralAndRenamable(t *testing.T) {
	if config.ProfileEnvVar != "NEWS_ENGINE_PROFILE" {
		t.Fatalf("ProfileEnvVar = %q, want NEWS_ENGINE_PROFILE", config.ProfileEnvVar)
	}
	t.Cleanup(func() { config.ProfileEnvVar = config.DefaultProfileEnvVar })
	config.ProfileEnvVar = "HOST_PROFILE"
	t.Setenv("HOST_PROFILE", "production")
	t.Setenv(config.DefaultProfileEnvVar, "test")
	got, err := config.ResolveProfile("")
	if err != nil || got != config.ProfileProduction {
		t.Fatalf("ResolveProfile() = %q, %v; want production from the renamed variable", got, err)
	}
}

func TestSourceCategoryConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.SourceCategoryConfig
		wantErr bool
	}{
		{"disabled, no override", config.SourceCategoryConfig{}, false},
		{"enabled, no override", config.SourceCategoryConfig{Enabled: true}, false},
		{"fatal override", config.SourceCategoryConfig{Enabled: true, OnMissingDependency: "fatal"}, false},
		{"degrade override", config.SourceCategoryConfig{Enabled: true, OnMissingDependency: "degrade"}, false},
		{"bogus override", config.SourceCategoryConfig{Enabled: true, OnMissingDependency: "sometimes"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFetchSourcesConfig_Validate(t *testing.T) {
	valid := config.FetchSourcesConfig{
		RSS:     config.SourceCategoryConfig{Enabled: true},
		Reddit:  config.SourceCategoryConfig{Enabled: true},
		Bluesky: config.SourceCategoryConfig{Enabled: true},
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
	invalid := valid
	invalid.Twitter = config.SourceCategoryConfig{Enabled: true, OnMissingDependency: "bogus"}
	if err := invalid.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for invalid twitter.on_missing_dependency")
	}
}

func writeFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoad_ValidConfig(t *testing.T) {
	path := writeFixture(t, validYAML)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SQLite.Path != "data/operational.db" {
		t.Errorf("SQLite.Path = %q", cfg.SQLite.Path)
	}
	wantRecon := config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 120, BatchSize: 20}
	if cfg.Publication.Reconciliation != wantRecon {
		t.Errorf("Publication.Reconciliation = %+v, want %+v", cfg.Publication.Reconciliation, wantRecon)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatal("Load() error = nil, want error for missing file")
	}
}

func TestLoad_RejectsUnknownFields(t *testing.T) {
	path := writeFixture(t, validYAML+"\nunknown_field: true\n")

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error for unknown field")
	}
}

func TestLoad_RejectsInvalidSection(t *testing.T) {
	invalid := `
sqlite:
  path: ""
  busy_timeout_ms: 5000
scheduler:
  check_interval_minutes: 5
  cleanup_interval_hours: 24
http:
  timeout_seconds: 10
  max_response_bytes: 2097152
freshness:
  hard_news_hours: 72
  social_hours: 24
  analysis_hours: 168
  default_hours: 72
telegram:
  channel_id: "-1000000000000"
retention:
  days: 14
`
	path := writeFixture(t, invalid)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error for empty sqlite.path")
	}
}

func TestLoad_EnvOverridesSecretsAndPaths(t *testing.T) {
	path := writeFixture(t, validYAML)
	t.Setenv("TELEGRAM_BOT_TOKEN", "secret-token")
	t.Setenv("SQLITE_DB_PATH", "/tmp/override.db")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Telegram.BotToken != "secret-token" {
		t.Errorf("Telegram.BotToken = %q, want %q", cfg.Telegram.BotToken, "secret-token")
	}
	if cfg.SQLite.Path != "/tmp/override.db" {
		t.Errorf("SQLite.Path = %q, want override", cfg.SQLite.Path)
	}
}

func TestLoad_EnvOverridesScrapingServiceAPIKey(t *testing.T) {
	t.Setenv("SCRAPING_SERVICE_API_KEY", "test-key")
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ScrapingService.APIKey != "test-key" {
		t.Errorf("cfg.ScrapingService.APIKey = %q, want %q", cfg.ScrapingService.APIKey, "test-key")
	}
}

func TestEntityFallbackConfig_Validate(t *testing.T) {
	if err := (config.EntityFallbackConfig{Enabled: false}).Validate(); err != nil {
		t.Errorf("disabled config should validate: %v", err)
	}
	valid := config.EntityFallbackConfig{
		Enabled: true, BaseURL: "http://localhost:11434/v1", Model: "qwen3:4b",
		TimeoutSeconds: 10, MaxTokens: 200, MinConfidence: 0.7,
	}
	if err := valid.Validate(); err != nil {
		t.Errorf("valid enabled config should validate: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*config.EntityFallbackConfig)
	}{
		{"empty base_url", func(c *config.EntityFallbackConfig) { c.BaseURL = "" }},
		{"empty model", func(c *config.EntityFallbackConfig) { c.Model = "" }},
		{"zero timeout_seconds", func(c *config.EntityFallbackConfig) { c.TimeoutSeconds = 0 }},
		{"negative temperature", func(c *config.EntityFallbackConfig) { c.Temperature = -1 }},
		{"min_confidence below 0", func(c *config.EntityFallbackConfig) { c.MinConfidence = -0.1 }},
		{"min_confidence above 1", func(c *config.EntityFallbackConfig) { c.MinConfidence = 1.1 }},
		{"negative max_candidates_per_article", func(c *config.EntityFallbackConfig) { c.MaxCandidatesPerArticle = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid
			tt.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
	withNoCap := valid
	withNoCap.MaxCandidatesPerArticle = 0
	if err := withNoCap.Validate(); err != nil {
		t.Errorf("max_candidates_per_article = 0 (no cap) should validate: %v", err)
	}
}

func TestEntityFallbackConfig_ProviderIsOptionalAndNotValidated(t *testing.T) {
	cfg := config.EntityFallbackConfig{
		Enabled: true, BaseURL: "https://api.groq.com/openai/v1", Model: "llama-3.1-8b-instant",
		TimeoutSeconds: 10, MinConfidence: 0.7,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with empty Provider = %v, want nil", err)
	}
	cfg.Provider = "groq"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with Provider set = %v, want nil", err)
	}
}

func TestEntityFallbackConfig_ProviderRoundTripsThroughYAML(t *testing.T) {
	yamlContent := `
provider: groq
enabled: true
base_url: https://api.groq.com/openai/v1
model: llama-3.1-8b-instant
timeout_seconds: 10
min_confidence: 0.7
`
	var cfg config.EntityFallbackConfig
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if cfg.Provider != "groq" {
		t.Fatalf("Provider = %q, want %q", cfg.Provider, "groq")
	}
}

func TestLoad_EnvOverridesFallbackAPIKey(t *testing.T) {
	t.Setenv("LLM_FALLBACK_API_KEY", "fallback-test-key")
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.EntityExtraction.Fallback.APIKey != "fallback-test-key" {
		t.Errorf("cfg.EntityExtraction.Fallback.APIKey = %q, want %q", cfg.EntityExtraction.Fallback.APIKey, "fallback-test-key")
	}
}

func TestWebLookupConfig_Validate_NoopWhenDisabled(t *testing.T) {
	c := config.WebLookupConfig{Enabled: false}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil when disabled", err)
	}
}

func TestWebLookupConfig_Validate_RequiresFieldsWhenEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.WebLookupConfig
	}{
		{"missing provider", config.WebLookupConfig{Enabled: true, TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50}},
		{"non-positive timeout", config.WebLookupConfig{Enabled: true, Provider: "serper", TimeoutSeconds: 0, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50}},
		{"non-positive max searches per candidate", config.WebLookupConfig{Enabled: true, Provider: "serper", TimeoutSeconds: 10, MaxSearchesPerCandidate: 0, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50}},
		{"non-positive max results per search", config.WebLookupConfig{Enabled: true, Provider: "serper", TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 0, MaxSearchesPerRun: 50}},
		{"non-positive max searches per run", config.WebLookupConfig{Enabled: true, Provider: "serper", TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 0}},
		{"missing base_url", config.WebLookupConfig{Enabled: true, Provider: "searxng", APIKey: "key", TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50, CacheTTL: time.Hour, TrustedDomains: []string{"league.example.com"}}},
		{"missing api key", config.WebLookupConfig{Enabled: true, Provider: "searxng", BaseURL: "http://localhost:8081/search", TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50, CacheTTL: time.Hour, TrustedDomains: []string{"league.example.com"}}},
		{"non-positive cache_ttl", config.WebLookupConfig{Enabled: true, Provider: "searxng", BaseURL: "http://localhost:8081/search", APIKey: "key", TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50, CacheTTL: 0, TrustedDomains: []string{"league.example.com"}}},
		{"empty trusted_domains", config.WebLookupConfig{Enabled: true, Provider: "searxng", BaseURL: "http://localhost:8081/search", APIKey: "key", TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50, CacheTTL: time.Hour, TrustedDomains: nil}},
		{"blank trusted_domains entry", config.WebLookupConfig{Enabled: true, Provider: "searxng", BaseURL: "http://localhost:8081/search", APIKey: "key", TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50, CacheTTL: time.Hour, TrustedDomains: []string{"  "}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err == nil {
				t.Fatalf("Validate() error = nil for %+v, want an error", tt.cfg)
			}
		})
	}
}

func TestWebLookupConfig_Validate_AcceptsFullyPopulatedConfig(t *testing.T) {
	c := config.WebLookupConfig{
		Enabled: true, Provider: "searxng", BaseURL: "http://localhost:8081/search", APIKey: "test-key",
		TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50,
		CacheTTL: 168 * time.Hour, TrustedDomains: []string{"isotopes.example.com", "league.example.com"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestApplyEnvOverrides_WebSearchAPIKey(t *testing.T) {
	t.Setenv("WEB_SEARCH_API_KEY", "test-key")
	path := writeFixture(t, validYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CatalogMaintenance.WebLookup.APIKey != "test-key" {
		t.Fatalf("CatalogMaintenance.WebLookup.APIKey = %q, want test-key", cfg.CatalogMaintenance.WebLookup.APIKey)
	}
}

func TestSchedulerConfig_Durations(t *testing.T) {
	c := config.SchedulerConfig{CheckIntervalMinutes: 5, OutboxIntervalMinutes: 1, CleanupIntervalHours: 24}
	if c.CheckInterval() != 5*time.Minute {
		t.Errorf("CheckInterval() = %v", c.CheckInterval())
	}
	if c.OutboxInterval() != time.Minute {
		t.Errorf("OutboxInterval() = %v", c.OutboxInterval())
	}
	if c.CleanupInterval() != 24*time.Hour {
		t.Errorf("CleanupInterval() = %v", c.CleanupInterval())
	}
}

func TestHTTPConfig_Timeout(t *testing.T) {
	c := config.HTTPConfig{TimeoutSeconds: 10, MaxResponseBytes: 1}
	if c.Timeout() != 10*time.Second {
		t.Errorf("Timeout() = %v", c.Timeout())
	}
}

func TestSQLiteConfig_BusyTimeout(t *testing.T) {
	c := config.SQLiteConfig{Path: "x", BusyTimeoutMS: 5000}
	if c.BusyTimeout() != 5*time.Second {
		t.Errorf("BusyTimeout() = %v", c.BusyTimeout())
	}
}

func TestFreshnessConfig_Policy(t *testing.T) {
	c := config.FreshnessConfig{HardNewsHours: 72, SocialHours: 24, AnalysisHours: 168, DefaultHours: 72}
	p := c.Policy()
	if p.HardNewsHours != 72 || p.SocialHours != 24 || p.AnalysisHours != 168 || p.DefaultHours != 72 {
		t.Errorf("Policy() = %+v", p)
	}
}

func TestTextNormalizationConfig_PolicyIncludesBoilerplatePhrases(t *testing.T) {
	c := config.TextNormalizationConfig{
		BoilerplatePhrases: []string{"Subscribe to our channel.", "Follow us on Facebook."},
	}
	p := c.Policy()
	want := []string{"Subscribe to our channel.", "Follow us on Facebook."}
	if len(p.BoilerplatePhrases) != len(want) || p.BoilerplatePhrases[0] != want[0] || p.BoilerplatePhrases[1] != want[1] {
		t.Errorf("Policy().BoilerplatePhrases = %v, want %v", p.BoilerplatePhrases, want)
	}
}

func TestDictionaryMiningConfig_Validate(t *testing.T) {
	if err := (config.DictionaryMiningConfig{Path: "configs/dictionary_mining.yml"}).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for path", err)
	}
	if err := (config.DictionaryMiningConfig{}).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for empty path", err)
	}
}

func TestScrapingServiceConfig_Validate(t *testing.T) {
	if err := (config.ScrapingServiceConfig{Enabled: true, BaseURL: ""}).Validate(); err == nil {
		t.Error("Validate() error = nil, want error when enabled without base_url")
	}
	if err := (config.ScrapingServiceConfig{Enabled: false}).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil when disabled", err)
	}
}

func TestScrapingServiceConfig_Validate_FullSection(t *testing.T) {
	valid := config.ScrapingServiceConfig{
		Enabled: true, BaseURL: "https://scraper.internal",
		TimeoutSeconds: 30, MaxRetries: 2, BackoffBaseMS: 500, BackoffMaxMS: 5000,
		ResponseLimitBytes: 10485760, DefaultMode: "auto", DefaultCacheTTLSeconds: 900,
		Concurrency:    config.ScrapingConcurrencyConfig{Global: 4, PerDomain: 1},
		CircuitBreaker: config.ScrapingCircuitBreakerConfig{FailureThreshold: 5, CooldownSeconds: 60, HalfOpenMaxProbes: 1},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	tests := []struct {
		name   string
		mutate func(*config.ScrapingServiceConfig)
	}{
		{"zero timeout_seconds", func(c *config.ScrapingServiceConfig) { c.TimeoutSeconds = 0 }},
		{"negative max_retries", func(c *config.ScrapingServiceConfig) { c.MaxRetries = -1 }},
		{"backoff_max below backoff_base", func(c *config.ScrapingServiceConfig) { c.BackoffMaxMS = 100; c.BackoffBaseMS = 500 }},
		{"zero response_limit_bytes", func(c *config.ScrapingServiceConfig) { c.ResponseLimitBytes = 0 }},
		{"invalid default_mode", func(c *config.ScrapingServiceConfig) { c.DefaultMode = "wat" }},
		{"zero default_cache_ttl_seconds", func(c *config.ScrapingServiceConfig) { c.DefaultCacheTTLSeconds = 0 }},
		{"zero concurrency.global", func(c *config.ScrapingServiceConfig) { c.Concurrency.Global = 0 }},
		{"zero concurrency.per_domain", func(c *config.ScrapingServiceConfig) { c.Concurrency.PerDomain = 0 }},
		{"zero failure_threshold", func(c *config.ScrapingServiceConfig) { c.CircuitBreaker.FailureThreshold = 0 }},
		{"zero cooldown_seconds", func(c *config.ScrapingServiceConfig) { c.CircuitBreaker.CooldownSeconds = 0 }},
		{"zero half_open_max_probes", func(c *config.ScrapingServiceConfig) { c.CircuitBreaker.HalfOpenMaxProbes = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid
			tt.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Error("Validate() error = nil, want error")
			}
		})
	}
}

func TestScrapingServiceConfig_Validate_ZeroMaxRetriesIsValid(t *testing.T) {
	c := config.ScrapingServiceConfig{
		Enabled: true, BaseURL: "https://scraper.internal",
		TimeoutSeconds: 30, MaxRetries: 0, BackoffBaseMS: 500, BackoffMaxMS: 5000,
		ResponseLimitBytes: 10485760, DefaultMode: "auto", DefaultCacheTTLSeconds: 900,
		Concurrency:    config.ScrapingConcurrencyConfig{Global: 4, PerDomain: 1},
		CircuitBreaker: config.ScrapingCircuitBreakerConfig{FailureThreshold: 5, CooldownSeconds: 60, HalfOpenMaxProbes: 1},
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil — max_retries: 0 means one attempt, no retries, and is valid", err)
	}
}

func TestScrapingServiceConfig_Validate_DisabledSkipsFieldChecks(t *testing.T) {
	c := config.ScrapingServiceConfig{Enabled: false}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil when disabled", err)
	}
}

func TestScrapingServiceConfig_Validate_DomainOverrides(t *testing.T) {
	base := config.ScrapingServiceConfig{
		Enabled: true, BaseURL: "https://scraper.internal",
		TimeoutSeconds: 30, MaxRetries: 2, BackoffBaseMS: 500, BackoffMaxMS: 5000,
		ResponseLimitBytes: 10485760, DefaultMode: "auto", DefaultCacheTTLSeconds: 900,
		Concurrency:    config.ScrapingConcurrencyConfig{Global: 4, PerDomain: 1},
		CircuitBreaker: config.ScrapingCircuitBreakerConfig{FailureThreshold: 5, CooldownSeconds: 60, HalfOpenMaxProbes: 1},
	}

	valid := base
	valid.Domains = map[string]config.ScrapingDomainPolicyConfig{"scraped-site.example.com": {Mode: "browser", CacheTTLSeconds: 1800, Concurrency: 1}}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	invalid := base
	invalid.Domains = map[string]config.ScrapingDomainPolicyConfig{"x.com": {Mode: "warp-speed"}}
	if err := invalid.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for invalid domain mode")
	}
}

func TestScrapingServiceConfig_DurationHelpers(t *testing.T) {
	c := config.ScrapingServiceConfig{
		TimeoutSeconds: 30, BackoffBaseMS: 500, BackoffMaxMS: 5000, DefaultCacheTTLSeconds: 900,
	}
	if c.Timeout() != 30*time.Second {
		t.Errorf("Timeout() = %v", c.Timeout())
	}
	if c.BackoffBase() != 500*time.Millisecond {
		t.Errorf("BackoffBase() = %v", c.BackoffBase())
	}
	if c.BackoffMax() != 5*time.Second {
		t.Errorf("BackoffMax() = %v", c.BackoffMax())
	}
	if c.DefaultCacheTTL() != 15*time.Minute {
		t.Errorf("DefaultCacheTTL() = %v", c.DefaultCacheTTL())
	}
	if (config.ScrapingCircuitBreakerConfig{CooldownSeconds: 60}).Cooldown() != time.Minute {
		t.Error("Cooldown() mismatch")
	}
}

func TestLoad_ParsesScrapingServiceEnabledSection(t *testing.T) {
	const yamlContent = `
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
  enabled: true
  base_url: "https://scraper.internal"
  timeout_seconds: 30
  max_retries: 2
  backoff_base_ms: 500
  backoff_max_ms: 5000
  response_limit_bytes: 10485760
  default_mode: auto
  default_cache_ttl_seconds: 900
  concurrency:
    global: 4
    per_domain: 1
  circuit_breaker:
    failure_threshold: 5
    cooldown_seconds: 60
    half_open_max_probes: 1
  domains:
    scraped-site.example.com:
      mode: browser
      cache_ttl_seconds: 1800
      concurrency: 1
fetch:
  global_concurrency: 4
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
    base_url: http://localhost:11434/v1
    model: qwen3:4b
    temperature: 0
    timeout_seconds: 60
    batch_size: 20
    limit: 500
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
	path := writeFixture(t, yamlContent)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.ScrapingService.Enabled || cfg.ScrapingService.BaseURL != "https://scraper.internal" {
		t.Errorf("ScrapingService = %+v", cfg.ScrapingService)
	}
	if cfg.ScrapingService.Domains["scraped-site.example.com"].Mode != "browser" {
		t.Errorf("Domains[scraped-site.example.com] = %+v", cfg.ScrapingService.Domains["scraped-site.example.com"])
	}
}

func TestRetentionConfig_Validate(t *testing.T) {
	valid := config.RetentionConfig{RejectedArticlesDays: 14, DuplicatesDays: 7, AuditEventsDays: 30, PublicationAttemptsDays: 30, ClustersDays: 14}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	invalid := valid
	invalid.DuplicatesDays = 0
	if err := invalid.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for zero duplicates_days")
	}
}

func TestRetentionConfig_Windows(t *testing.T) {
	c := config.RetentionConfig{RejectedArticlesDays: 14, DuplicatesDays: 7, AuditEventsDays: 30, PublicationAttemptsDays: 30, ClustersDays: 14}
	w := c.Windows()
	if w.RejectedArticles != 14*24*time.Hour {
		t.Errorf("RejectedArticles = %v, want %v", w.RejectedArticles, 14*24*time.Hour)
	}
	if w.Duplicates != 7*24*time.Hour {
		t.Errorf("Duplicates = %v, want %v", w.Duplicates, 7*24*time.Hour)
	}
	if w.AuditEvents != 30*24*time.Hour {
		t.Errorf("AuditEvents = %v, want %v", w.AuditEvents, 30*24*time.Hour)
	}
	if w.PublicationAttempts != 30*24*time.Hour {
		t.Errorf("PublicationAttempts = %v, want %v", w.PublicationAttempts, 30*24*time.Hour)
	}
	if w.Clusters != 14*24*time.Hour {
		t.Errorf("Clusters = %v, want %v", w.Clusters, 14*24*time.Hour)
	}
}

func TestClusterLifecycleConfig_Validate(t *testing.T) {
	valid := config.ClusterLifecycleConfig{CloseAfterHours: 96, BatchSize: 200, MaxBatchesPerRun: 50, TimeBudgetSeconds: 30, CloseReason: "inactivity_ttl"}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() on valid config error = %v, want nil", err)
	}

	cases := []struct {
		name string
		cfg  config.ClusterLifecycleConfig
	}{
		{"zero close_after_hours", config.ClusterLifecycleConfig{CloseAfterHours: 0, BatchSize: 200, MaxBatchesPerRun: 50, TimeBudgetSeconds: 30, CloseReason: "x"}},
		{"zero batch_size", config.ClusterLifecycleConfig{CloseAfterHours: 96, BatchSize: 0, MaxBatchesPerRun: 50, TimeBudgetSeconds: 30, CloseReason: "x"}},
		{"zero max_batches_per_run", config.ClusterLifecycleConfig{CloseAfterHours: 96, BatchSize: 200, MaxBatchesPerRun: 0, TimeBudgetSeconds: 30, CloseReason: "x"}},
		{"zero time_budget_seconds", config.ClusterLifecycleConfig{CloseAfterHours: 96, BatchSize: 200, MaxBatchesPerRun: 50, TimeBudgetSeconds: 0, CloseReason: "x"}},
		{"empty close_reason", config.ClusterLifecycleConfig{CloseAfterHours: 96, BatchSize: 200, MaxBatchesPerRun: 50, TimeBudgetSeconds: 30, CloseReason: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Error("Validate() error = nil, want an error")
			}
		})
	}
}

func TestClusterLifecycleConfig_Settings(t *testing.T) {
	cfg := config.ClusterLifecycleConfig{CloseAfterHours: 96, BatchSize: 200, MaxBatchesPerRun: 50, TimeBudgetSeconds: 30, CloseReason: "inactivity_ttl"}
	got := cfg.Settings()
	if got.CloseAfter != 96*time.Hour {
		t.Errorf("CloseAfter = %v, want 96h", got.CloseAfter)
	}
	if got.TimeBudget != 30*time.Second {
		t.Errorf("TimeBudget = %v, want 30s", got.TimeBudget)
	}
	if got.BatchSize != 200 || got.MaxBatchesPerRun != 50 || got.CloseReason != "inactivity_ttl" {
		t.Errorf("Settings() = %+v, want BatchSize=200 MaxBatchesPerRun=50 CloseReason=inactivity_ttl", got)
	}
}

func TestPublicationConfig_Validate(t *testing.T) {
	valid := config.PublicationConfig{MaxAttempts: 6, BaseBackoffSeconds: 30, MaxBackoffSeconds: 1800, ClaimBatchSize: 10, StaleClaimAfterMinutes: 10}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	tooLowMax := valid
	tooLowMax.MaxBackoffSeconds = 10 // less than BaseBackoffSeconds
	if err := tooLowMax.Validate(); err == nil {
		t.Error("Validate() error = nil, want error when max_backoff_seconds < base_backoff_seconds")
	}

	zeroBatch := valid
	zeroBatch.ClaimBatchSize = 0
	if err := zeroBatch.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for zero claim_batch_size")
	}
}

func TestPublicationConfig_Backoff(t *testing.T) {
	c := config.PublicationConfig{MaxAttempts: 6, BaseBackoffSeconds: 30, MaxBackoffSeconds: 1800, ClaimBatchSize: 10, StaleClaimAfterMinutes: 10}
	b := c.Backoff()
	if b.BaseDelay != 30*time.Second || b.MaxDelay != 1800*time.Second || b.MaxAttempts != 6 {
		t.Errorf("Backoff() = %+v", b)
	}
	if c.StaleClaimAfter() != 10*time.Minute {
		t.Errorf("StaleClaimAfter() = %v, want 10m", c.StaleClaimAfter())
	}
}

func TestReconciliationConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.ReconciliationConfig
		wantErr bool
	}{
		{"valid", config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 120, BatchSize: 20}, false},
		{"disabled skips numeric checks", config.ReconciliationConfig{Enabled: false}, false},
		{"enabled requires positive history limit", config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 0, ClockSkewSeconds: 120, BatchSize: 20}, true},
		{"enabled requires positive clock skew", config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 0, BatchSize: 20}, true},
		// A positive-but-tiny skew is the dangerous case: it passes a
		// naive "must be positive" check, cannot absorb any real
		// host/Telegram clock drift, and so turns a genuine channel-history
		// match into a confident non-match — retry, duplicate post.
		{"enabled rejects a positive-but-too-small clock skew", config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 1, BatchSize: 20}, true},
		{"enabled rejects just below the clock-skew floor", config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 59, BatchSize: 20}, true},
		{"enabled accepts exactly the clock-skew floor", config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 60, BatchSize: 20}, false},
		{"enabled requires positive batch size", config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 120, BatchSize: 0}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestReconciliationConfig_Settings(t *testing.T) {
	c := config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 120, BatchSize: 20}
	urlCfg := normalization.URLConfig{}
	resolvers := []normalization.KnownLinkResolver{normalization.YouTubeResolver}
	got := c.Settings("-1000000000000", urlCfg, resolvers)
	if got.BatchSize != 20 || got.ChannelRef != "-1000000000000" || got.HistoryLimit != 200 || got.ClockSkew != 120*time.Second {
		t.Errorf("Settings() = %+v", got)
	}
	if len(got.URLResolvers) != 1 {
		t.Errorf("Settings().URLResolvers = %+v, want 1 resolver", got.URLResolvers)
	}
}

// TestReconciliationConfig_Settings_ClampsZeroBatchSize pins the fix for
// the bug where Enabled: false (or any config that leaves batch_size
// unset) would silently produce BatchSize: 0, which becomes LIMIT 0 in
// ListSentUnconfirmed — meaning Reconcile lists (and quarantines)
// nothing, forever. Enabled must only gate whether a channel-history
// importer is built, never whether Reconcile runs its listing/quarantine
// pass at all.
func TestReconciliationConfig_Settings_ClampsZeroBatchSize(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ReconciliationConfig
	}{
		{"disabled with zero batch size", config.ReconciliationConfig{Enabled: false}},
		{"enabled with zero batch size (would fail Validate, but Settings must still clamp)", config.ReconciliationConfig{Enabled: true, ChannelHistoryLimit: 200, ClockSkewSeconds: 120}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.Settings("-1000000000000", normalization.URLConfig{}, nil)
			if got.BatchSize <= 0 {
				t.Errorf("Settings().BatchSize = %d, want a positive default (got zero — Reconcile would list nothing)", got.BatchSize)
			}
		})
	}
}

func TestTelegramConfig_Validate(t *testing.T) {
	if err := (config.TelegramConfig{}).Validate(); err == nil {
		t.Error("Validate() error = nil, want error for missing channel_id")
	}
}

func TestTelegramConfig_Validate_DoesNotRequireMTProtoCredentials(t *testing.T) {
	c := config.TelegramConfig{ChannelID: "-1000000000000", TimeoutSeconds: 10} // APIID/APIHash/Phone left zero-value
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil — MTProto credentials are not required for the service binary to load config", err)
	}
}

func TestTelegramConfig_Validate_RequiresPositiveTimeoutSeconds(t *testing.T) {
	c := config.TelegramConfig{ChannelID: "-1000000000000", TimeoutSeconds: 0}
	if err := c.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for timeout_seconds <= 0")
	}
}

func TestTelegramConfig_Timeout(t *testing.T) {
	c := config.TelegramConfig{TimeoutSeconds: 10}
	if c.Timeout() != 10*time.Second {
		t.Errorf("Timeout() = %v, want 10s", c.Timeout())
	}
}

func TestApplyEnvOverrides_TelegramMTProtoCredentials(t *testing.T) {
	t.Setenv("TELEGRAM_API_ID", "12345")
	t.Setenv("TELEGRAM_API_HASH", "abc123")
	t.Setenv("TELEGRAM_PHONE", "+15551234567")
	t.Setenv("TELEGRAM_SESSION_PATH", "../telegram_session.json")
	t.Setenv("TELEGRAM_TIMEOUT_SECONDS", "180")

	path := writeFixture(t, validYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Telegram.APIID != 12345 {
		t.Errorf("Telegram.APIID = %d, want 12345", cfg.Telegram.APIID)
	}
	if cfg.Telegram.APIHash != "abc123" {
		t.Errorf("Telegram.APIHash = %q, want %q", cfg.Telegram.APIHash, "abc123")
	}
	if cfg.Telegram.Phone != "+15551234567" {
		t.Errorf("Telegram.Phone = %q, want %q", cfg.Telegram.Phone, "+15551234567")
	}
	if cfg.Telegram.SessionPath != "../telegram_session.json" {
		t.Errorf("Telegram.SessionPath = %q, want %q", cfg.Telegram.SessionPath, "../telegram_session.json")
	}
	if cfg.Telegram.TimeoutSeconds != 180 {
		t.Errorf("Telegram.TimeoutSeconds = %d, want %d", cfg.Telegram.TimeoutSeconds, 180)
	}
}

func TestPublicationPolicyConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.PublicationPolicyConfig
		wantErr bool
	}{
		{"valid, quiet hours disabled", config.PublicationPolicyConfig{
			Burst:     config.BurstPolicyConfig{MaxPerWindow: 6, WindowMinutes: 60},
			SameTopic: config.SameTopicPolicyConfig{MaxPerWindow: 2, WindowMinutes: 180},
		}, false},
		{"valid, quiet hours enabled", config.PublicationPolicyConfig{
			Burst:      config.BurstPolicyConfig{MaxPerWindow: 6, WindowMinutes: 60},
			SameTopic:  config.SameTopicPolicyConfig{MaxPerWindow: 2, WindowMinutes: 180},
			QuietHours: config.QuietHoursConfig{Enabled: true, StartHour: 2, EndHour: 6},
		}, false},
		{"zero burst max disables burst policy", config.PublicationPolicyConfig{
			Burst:     config.BurstPolicyConfig{MaxPerWindow: 0, WindowMinutes: 60},
			SameTopic: config.SameTopicPolicyConfig{MaxPerWindow: 2, WindowMinutes: 180},
		}, false},
		{"negative burst max", config.PublicationPolicyConfig{
			Burst:     config.BurstPolicyConfig{MaxPerWindow: -1, WindowMinutes: 60},
			SameTopic: config.SameTopicPolicyConfig{MaxPerWindow: 2, WindowMinutes: 180},
		}, true},
		{"zero same-topic max disables same-topic policy", config.PublicationPolicyConfig{
			Burst:     config.BurstPolicyConfig{MaxPerWindow: 6, WindowMinutes: 60},
			SameTopic: config.SameTopicPolicyConfig{MaxPerWindow: 0, WindowMinutes: 180},
		}, false},
		{"quiet hours start_hour out of range", config.PublicationPolicyConfig{
			Burst:      config.BurstPolicyConfig{MaxPerWindow: 6, WindowMinutes: 60},
			SameTopic:  config.SameTopicPolicyConfig{MaxPerWindow: 2, WindowMinutes: 180},
			QuietHours: config.QuietHoursConfig{Enabled: true, StartHour: 24, EndHour: 6},
		}, true},
		{"blank same-topic exempt entity", config.PublicationPolicyConfig{
			Burst:     config.BurstPolicyConfig{MaxPerWindow: 6, WindowMinutes: 60},
			SameTopic: config.SameTopicPolicyConfig{MaxPerWindow: 2, WindowMinutes: 180, ExemptEntities: []string{"  "}},
		}, true},
		{"quiet hours disabled ignores out-of-range hours", config.PublicationPolicyConfig{
			Burst:      config.BurstPolicyConfig{MaxPerWindow: 6, WindowMinutes: 60},
			SameTopic:  config.SameTopicPolicyConfig{MaxPerWindow: 2, WindowMinutes: 180},
			QuietHours: config.QuietHoursConfig{Enabled: false, StartHour: 99, EndHour: 99},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPublicationPolicyConfig_Policy(t *testing.T) {
	cfg := config.PublicationPolicyConfig{
		Burst:      config.BurstPolicyConfig{MaxPerWindow: 6, WindowMinutes: 60},
		SameTopic:  config.SameTopicPolicyConfig{MaxPerWindow: 2, WindowMinutes: 180, ExemptEntities: []string{"Springfield Isotopes"}},
		QuietHours: config.QuietHoursConfig{Enabled: true, StartHour: 2, EndHour: 6},
	}
	p := cfg.Policy()
	if len(p.SameTopicExemptEntities) != 1 || p.SameTopicExemptEntities[0] != "Springfield Isotopes" {
		t.Errorf("same topic exempt entities = %v", p.SameTopicExemptEntities)
	}
	if p.BurstMaxPerWindow != 6 || p.BurstWindow != time.Hour {
		t.Errorf("burst = %+v", p)
	}
	if p.SameTopicMaxPerWindow != 2 || p.SameTopicWindow != 3*time.Hour {
		t.Errorf("same topic = %+v", p)
	}
	if !p.QuietHours.Enabled || p.QuietHours.StartHour != 2 || p.QuietHours.EndHour != 6 {
		t.Errorf("quiet hours = %+v", p.QuietHours)
	}
}

func TestPublicationPolicyConfig_LivePolicyIsIndependentAndExplicit(t *testing.T) {
	cfg := config.PublicationPolicyConfig{Live: config.LivePublicationPolicyConfig{
		Enabled: true, TTLMinutes: 30, SourceAllowlist: []string{"rss:score"},
		Milestones: []string{"kickoff", "halftime", "touchdown_springfield", "final"},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	policy := cfg.LivePolicy()
	if !policy.Enabled || !policy.SourceAllowlist["rss:score"] || !policy.Milestones["touchdown_springfield"] {
		t.Fatalf("unexpected live policy: %+v", policy)
	}
	if cfg.Policy().Live == nil {
		t.Fatal("live policy not propagated")
	}
}

func TestLivePublicationPolicyConfig_TeamTouchdownMilestones(t *testing.T) {
	for _, m := range []string{"touchdown", "touchdown_springfield", "Touchdown_Capital_City"} {
		cfg := config.LivePublicationPolicyConfig{Enabled: true, TTLMinutes: 30, SourceAllowlist: []string{"rss:score"}, Milestones: []string{m}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("milestone %q rejected: %v", m, err)
		}
	}
	for _, m := range []string{"touchdown_", "touchdown-springfield", "touchdown_spring field", "overtime"} {
		cfg := config.LivePublicationPolicyConfig{Enabled: true, TTLMinutes: 30, SourceAllowlist: []string{"rss:score"}, Milestones: []string{m}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("milestone %q accepted, want an error", m)
		}
	}
}

func TestLivePublicationPolicyConfig_ValidateRejectsMalformedSourceAllowlistEntry(t *testing.T) {
	cfg := config.LivePublicationPolicyConfig{
		Enabled:         true,
		TTLMinutes:      30,
		SourceAllowlist: []string{"rss:news-aggregator", ""},
		Milestones:      []string{"kickoff"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error for the empty/malformed source_allowlist entry")
	}
}

func TestLivePublicationPolicyConfig_ValidateAcceptsRealSettingsYAMLAllowlist(t *testing.T) {
	data, err := os.ReadFile(repofs.ExampleConfig(t, "settings.yml"))
	if err != nil {
		t.Fatalf("read settings.yml: %v", err)
	}
	var raw struct {
		PublicationPolicy struct {
			Live config.LivePublicationPolicyConfig `yaml:"live"`
		} `yaml:"publication_policy"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse settings.yml: %v", err)
	}
	if len(raw.PublicationPolicy.Live.SourceAllowlist) == 0 {
		t.Fatal("settings.yml's publication_policy.live.source_allowlist parsed empty — fixture drifted")
	}
	if err := raw.PublicationPolicy.Live.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want the example allowlist in settings.yml to validate cleanly", err)
	}
}

// TestLoad_LivePolicyEndToEndFromRealSettingsYAML proves the actual shipped
// configs/example/settings.yml, converted through config.PublicationPolicyConfig's
// real LivePolicy(), produces the intended lane decisions — this is the
// seam finding 1 (ESPN's stale "final" status) lived in, and the seam no
// prior test (hand-built LivePolicy, or config.Validate() in isolation)
// covered end-to-end.
func TestLoad_LivePolicyEndToEndFromRealSettingsYAML(t *testing.T) {
	data, err := os.ReadFile(repofs.ExampleConfig(t, "settings.yml"))
	if err != nil {
		t.Fatalf("read settings.yml: %v", err)
	}
	var raw struct {
		PublicationPolicy config.PublicationPolicyConfig `yaml:"publication_policy"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse settings.yml: %v", err)
	}
	if err := raw.PublicationPolicy.Validate(); err != nil {
		t.Fatalf("publication_policy.Validate() error = %v", err)
	}
	livePolicy := raw.PublicationPolicy.LivePolicy()

	inGame := map[string]string{
		"match_state_kind": "live", "match_id": "401671000",
		"match_home_team": "Springfield Isotopes", "match_away_team": "Shelbyville Sharks",
		"match_phase": "q3",
	}
	if lane := livePolicy.LaneFor("rss:example-sports-wire", inGame); lane != publishing.LaneLive {
		t.Errorf("LaneFor(allow-listed source, q3) = %q, want %q", lane, publishing.LaneLive)
	}
	if lane := livePolicy.LaneFor("bluesky:isotopes.example.com", inGame); lane != publishing.LaneNormal {
		t.Errorf("LaneFor(non-allow-listed source, q3) = %q, want %q", lane, publishing.LaneNormal)
	}

	final := map[string]string{
		"match_state_kind": "live", "match_id": "401671000",
		"match_home_team": "Springfield Isotopes", "match_away_team": "Shelbyville Sharks",
		"match_phase": "final",
	}
	if lane := livePolicy.LaneFor("rss:example-sports-wire", final); lane != publishing.LaneNormal {
		t.Errorf("LaneFor(allow-listed source, final) = %q, want %q (final must not admit to the live lane, per finding 1)", lane, publishing.LaneNormal)
	}
}

const validYAMLWithDedupCascade = validYAML + `normalization:
  url:
    tracking_params: [utm_source, utm_medium, fbclid]
    domain_ignored_params: {}
    strip_default_ports: true
    trailing_slash: strip
    known_link_resolvers: [youtube]
  text:
    strip_emojis: true
    strip_hashtags: true
    strip_mentions: true
    strip_inline_urls: true
    repeated_prefix_patterns: ['^\s*\[[^\]]*\]\s*']
    shingle_size: 3
    min_token_length: 3
    stop_words: [the, and, with]
`

func TestLoad_ParsesNormalizationAndDedupCascadeSections(t *testing.T) {
	yaml := `
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
  global_concurrency: 4
  fan_in_buffer: 64
  sources_path: configs/sources.yml
source_policy:
  path: configs/source-policies.yml
classification:
  enabled: false
normalization:
  url:
    tracking_params: [utm_source]
    domain_ignored_params: {}
    strip_default_ports: true
    trailing_slash: strip
    known_link_resolvers: [youtube]
  text:
    strip_emojis: true
    strip_hashtags: true
    strip_mentions: true
    strip_inline_urls: true
    repeated_prefix_patterns: ['^\s*\[[^\]]*\]\s*']
    shingle_size: 3
    min_token_length: 3
    stop_words: [the, and]
deduplication:
  near_duplicate:
    enabled: true
    method: jaccard
    default_threshold: 0.82
    threshold_by_source_type: {twitter: 0.75, rss: 0.85}
    short_text_max_tokens: 8
    short_text_threshold: 0.90
    window_hours: 72
    max_candidates: 50
    min_shared_terms: 2
    fields_used: [title]
    content_type_exceptions: [video]
    attribute_combo_window_minutes: 2
    rules_version: dedup-rules-v1
clustering:
  enabled: true
  event_threshold: 0.55
  rules_path: configs/event_clustering_rules.yml
  rules_version: ""
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
    base_url: http://localhost:11434/v1
    model: qwen3:4b
    temperature: 0
    timeout_seconds: 60
    batch_size: 20
    limit: 500
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
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Normalization.URL.TrailingSlash != "strip" {
		t.Errorf("Normalization.URL.TrailingSlash = %q, want strip", cfg.Normalization.URL.TrailingSlash)
	}
	if cfg.Normalization.Text.ShingleSize != 3 {
		t.Errorf("Normalization.Text.ShingleSize = %d, want 3", cfg.Normalization.Text.ShingleSize)
	}
	if cfg.Deduplication.NearDuplicate.DefaultThreshold != 0.82 {
		t.Errorf("Deduplication.NearDuplicate.DefaultThreshold = %v, want 0.82", cfg.Deduplication.NearDuplicate.DefaultThreshold)
	}
	if cfg.Deduplication.NearDuplicate.ThresholdBySourceType["twitter"] != 0.75 {
		t.Errorf("threshold_by_source_type[twitter] = %v, want 0.75", cfg.Deduplication.NearDuplicate.ThresholdBySourceType["twitter"])
	}
	if cfg.Clustering.EventThreshold != 0.55 {
		t.Errorf("Clustering.EventThreshold = %v, want 0.55", cfg.Clustering.EventThreshold)
	}

	cascade := cfg.DedupCascade()
	if cascade.Dedup.EventThreshold != 0.55 {
		t.Errorf("DedupCascade().Dedup.EventThreshold = %v, want 0.55", cascade.Dedup.EventThreshold)
	}
	if cascade.Dedup.RulesVersion != "dedup-rules-v1" {
		t.Errorf("DedupCascade().Dedup.RulesVersion = %q, want dedup-rules-v1", cascade.Dedup.RulesVersion)
	}
	if !cascade.Dedup.ContentTypeExceptions["video"] {
		t.Error("DedupCascade().Dedup.ContentTypeExceptions[video] = false, want true")
	}
	if len(cascade.Normalization.Resolvers) != 1 {
		t.Errorf("DedupCascade().Normalization.Resolvers has %d entries, want 1 (youtube)", len(cascade.Normalization.Resolvers))
	}
}

func TestNormalizationConfig_Validate_RejectsInvalidTrailingSlashPolicy(t *testing.T) {
	yaml := validYAMLWithDedupCascade
	// Corrupt trailing_slash to an invalid value via string replace, since
	// the fixture above is otherwise valid.
	invalid := replaceOnce(yaml, "trailing_slash: strip", "trailing_slash: bogus")
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want validation error for invalid trailing_slash")
	}
}

func replaceOnce(s, old, new string) string {
	i := indexOf(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestLoad_OriginalMinimalFixtureStillValidatesWithZeroValueNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("Load() error = %v, want the pre-Phase-3 minimal fixture to still validate", err)
	}
}

func TestClassificationConfig_Validate(t *testing.T) {
	if err := (config.ClassificationConfig{}).Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for empty config", err)
	}
	valid := config.ClassificationConfig{Enabled: true, TrustedSourceIDs: []string{"a", "b"}, ContextSourceIDs: []string{"c"}}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
	dup := config.ClassificationConfig{TrustedSourceIDs: []string{"a", "a"}}
	if err := dup.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for duplicate trusted_source_ids")
	}
	empty := config.ClassificationConfig{TrustedSourceIDs: []string{""}}
	if err := empty.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for an empty trusted_source_ids entry")
	}
	contextDup := config.ClassificationConfig{ContextSourceIDs: []string{"a", "a"}}
	if err := contextDup.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for duplicate context_source_ids")
	}
	overlap := config.ClassificationConfig{TrustedSourceIDs: []string{"a"}, ContextSourceIDs: []string{"a"}}
	if err := overlap.Validate(); err == nil {
		t.Error("Validate() error = nil, want error when an id is both trusted and contextual")
	}
}

func TestConfigValidate_RequiresDictionaryPathWhenClassificationEnabled(t *testing.T) {
	yaml := validYAML +
		"\nnormalization:\n  url: {}\n  text: {}\n"
	yaml = replaceOnce(yaml, "classification:\n  enabled: false", "classification:\n  enabled: true\n  trusted_source_ids: []")
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error: classification.enabled=true with dictionary.path empty")
	}
}

func TestConfigValidate_ClassificationEnabledWithDictionaryPathSucceeds(t *testing.T) {
	yaml := validYAML +
		"\nnormalization:\n  url: {}\n  text: {}\n"
	yaml = replaceOnce(yaml, "classification:\n  enabled: false", "classification:\n  enabled: true\n  trusted_source_ids: []")
	yaml = replaceOnce(yaml, "dictionary:\n  path: \"\"\n  version: \"\"", "dictionary:\n  path: \"configs/dictionary.yml\"\n  version: \"\"")
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Classification.Enabled || cfg.Dictionary.Path != "configs/dictionary.yml" {
		t.Errorf("cfg.Classification/Dictionary = %+v / %+v", cfg.Classification, cfg.Dictionary)
	}
	cascade := cfg.DedupCascade()
	if !cascade.Classification.Enabled {
		t.Error("DedupCascade().Classification.Enabled = false, want true")
	}
}

func TestFetchConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.FetchConfig
		wantErr bool
	}{
		{"valid", config.FetchConfig{GlobalConcurrency: 6, FanInBuffer: 64, SourcesPath: "configs/sources.yml"}, false},
		{"zero global_concurrency", config.FetchConfig{GlobalConcurrency: 0, FanInBuffer: 64, SourcesPath: "x"}, true},
		{"zero fan_in_buffer", config.FetchConfig{GlobalConcurrency: 6, FanInBuffer: 0, SourcesPath: "x"}, true},
		{"empty sources_path", config.FetchConfig{GlobalConcurrency: 6, FanInBuffer: 64, SourcesPath: ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ParsesFetchSection(t *testing.T) {
	yamlContent := `
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
    base_url: http://localhost:11434/v1
    model: qwen3:4b
    temperature: 0
    timeout_seconds: 60
    batch_size: 20
    limit: 500
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
	path := writeFixture(t, yamlContent)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Fetch.GlobalConcurrency != 6 || cfg.Fetch.FanInBuffer != 64 || cfg.Fetch.SourcesPath != "configs/sources.yml" {
		t.Errorf("Fetch = %+v", cfg.Fetch)
	}
}

// validEntityCatalogConfig is a valid config.EntityCatalogConfig used by
// TestClusteringConfig_Validate's Enabled: true cases that don't
// specifically exercise entity_catalog validation themselves.
var validEntityCatalogConfig = config.EntityCatalogConfig{
	ManualPath:    "configs/entity_catalog_manual.yml",
	GeneratedPath: "configs/entity_catalog_generated.yml",
}

func TestClusteringConfig_Validate_RequiresEntityCatalogPathsWhenEnabled(t *testing.T) {
	c := config.ClusteringConfig{
		Enabled: true, EventThreshold: 0.5, RulesPath: "x.yml", CandidateLimit: 10,
		EntityCatalog: config.EntityCatalogConfig{ManualPath: "", GeneratedPath: "g.yml"},
	}
	if err := c.Validate(); err == nil {
		t.Error("Validate: expected error for missing entity_catalog.manual_path, got nil")
	}
}

func TestClusteringConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.ClusteringConfig
		wantErr bool
	}{
		{"disabled skips all checks", config.ClusteringConfig{Enabled: false}, false},
		{
			"disabled skips checks even with an out-of-range threshold and no rules_path",
			config.ClusteringConfig{Enabled: false, EventThreshold: 5},
			false,
		},
		{
			"enabled requires rules_path",
			config.ClusteringConfig{Enabled: true, EventThreshold: 0.55, RulesPath: ""},
			true,
		},
		{
			"enabled with rules_path is valid",
			config.ClusteringConfig{Enabled: true, EventThreshold: 0.55, RulesPath: "configs/event_clustering_rules.yml", CandidateLimit: 50, EntityCatalog: validEntityCatalogConfig},
			false,
		},
		{
			"enabled with out-of-range event_threshold",
			config.ClusteringConfig{Enabled: true, EventThreshold: 1.5, RulesPath: "configs/event_clustering_rules.yml", CandidateLimit: 50, EntityCatalog: validEntityCatalogConfig},
			true,
		},
		{
			"enabled with empty rules_version is valid (no enforcement at this layer)",
			config.ClusteringConfig{Enabled: true, EventThreshold: 0.55, RulesPath: "configs/event_clustering_rules.yml", RulesVersion: "", CandidateLimit: 50, EntityCatalog: validEntityCatalogConfig},
			false,
		},
		{
			"enabled with a non-empty rules_version is valid (plain field here, never compared against anything)",
			config.ClusteringConfig{Enabled: true, EventThreshold: 0.55, RulesPath: "configs/event_clustering_rules.yml", RulesVersion: "1.0.0", CandidateLimit: 50, EntityCatalog: validEntityCatalogConfig},
			false,
		},
		{
			"enabled requires candidate_limit",
			config.ClusteringConfig{Enabled: true, EventThreshold: 0.55, RulesPath: "configs/event_clustering_rules.yml", CandidateLimit: 0, EntityCatalog: validEntityCatalogConfig},
			true,
		},
		{
			"enabled with candidate_limit is valid",
			config.ClusteringConfig{Enabled: true, EventThreshold: 0.55, RulesPath: "configs/event_clustering_rules.yml", CandidateLimit: 50, EntityCatalog: validEntityCatalogConfig},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfigValidate_RequiresClusteringRulesPathWhenEnabled(t *testing.T) {
	yaml := replaceOnce(validYAML, "clustering:\n  enabled: false", "clustering:\n  enabled: true\n  event_threshold: 0.55")
	path := writeFixture(t, yaml)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error: clustering.enabled=true with rules_path empty")
	}
}

func TestConfigValidate_ClusteringEnabledWithRulesPathSucceeds(t *testing.T) {
	yaml := replaceOnce(
		validYAML,
		"clustering:\n  enabled: false",
		"clustering:\n  enabled: true\n  event_threshold: 0.55\n  rules_path: configs/event_clustering_rules.yml\n  rules_version: \"\"\n  entity_catalog:\n    manual_path: configs/entity_catalog_manual.yml\n    generated_path: configs/entity_catalog_generated.yml\n  candidate_limit: 50",
	)
	path := writeFixture(t, yaml)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Clustering.RulesPath != "configs/event_clustering_rules.yml" {
		t.Errorf("Clustering.RulesPath = %q, want configs/event_clustering_rules.yml", cfg.Clustering.RulesPath)
	}
	if cfg.Clustering.RulesVersion != "" {
		t.Errorf("Clustering.RulesVersion = %q, want empty (no pin)", cfg.Clustering.RulesVersion)
	}
}

func TestConfigValidate_ClusteringEnabledWithPinnedRulesVersionSucceeds(t *testing.T) {
	// Config.Validate never reads the rules file or compares RulesVersion
	// against anything — that enforcement is main.go's job, after it calls
	// clusteringrules.Load (out of scope for this package). At this layer,
	// a non-empty rules_version is just a plain string that round-trips
	// through Load unchanged, whether or not it happens to match the rules
	// file's real meta.version (configs/event_clustering_rules.yml's is
	// "1.0.0" as of Task 3).
	yaml := replaceOnce(
		validYAML,
		"clustering:\n  enabled: false",
		"clustering:\n  enabled: true\n  event_threshold: 0.55\n  rules_path: configs/event_clustering_rules.yml\n  rules_version: does-not-exist-and-is-never-checked-here\n  entity_catalog:\n    manual_path: configs/entity_catalog_manual.yml\n    generated_path: configs/entity_catalog_generated.yml\n  candidate_limit: 50",
	)
	path := writeFixture(t, yaml)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil — config.Validate must not enforce rules_version against the rules file", err)
	}
	if cfg.Clustering.RulesVersion != "does-not-exist-and-is-never-checked-here" {
		t.Errorf("Clustering.RulesVersion = %q, want the pinned string preserved verbatim", cfg.Clustering.RulesVersion)
	}
}

func TestEditorialConfig_Validate(t *testing.T) {
	for _, mode := range []string{"disabled", "shadow", "enforce"} {
		if err := (config.EditorialConfig{Mode: mode, AmbiguousPolicy: "reject"}).Validate(); err != nil {
			t.Errorf("Validate() for mode %q error = %v, want nil", mode, err)
		}
	}
	if err := (config.EditorialConfig{Mode: "bogus", AmbiguousPolicy: "reject"}).Validate(); err == nil {
		t.Error("Validate() for mode \"bogus\" error = nil, want an error")
	}
	if err := (config.EditorialConfig{}).Validate(); err == nil {
		t.Error("Validate() for zero-value (empty Mode) error = nil, want an error — no silent default")
	}
}

func TestEditorialConfig_Validate_AmbiguousPolicyRejectAndAcceptAreValid(t *testing.T) {
	for _, policy := range []string{"reject", "accept"} {
		cfg := config.EditorialConfig{Mode: "enforce", AmbiguousPolicy: policy}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() error = %v for ambiguous_policy=%q, want nil", err, policy)
		}
	}
}

func TestEditorialConfig_Validate_AmbiguousPolicyInvalidFails(t *testing.T) {
	cfg := config.EditorialConfig{Mode: "enforce", AmbiguousPolicy: "hold"}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for unknown ambiguous_policy")
	}
}

func TestEditorialConfig_Validate_AmbiguousPolicyEmptyFails(t *testing.T) {
	cfg := config.EditorialConfig{Mode: "enforce", AmbiguousPolicy: ""}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error: ambiguous_policy must not silently default")
	}
}

func TestLoad_RejectsEnforceModeWithoutModelPath(t *testing.T) {
	// model.path is still "" in validYAML — enforce with no model path must fail.
	badYAML := replaceOnce(validYAML, "editorial:\n  mode: disabled", "editorial:\n  mode: enforce")
	path := writeFixture(t, badYAML)
	if _, err := config.Load(path); err == nil {
		t.Error("Load() error = nil, want an error when editorial.mode is enforce but model.path is empty")
	}
}

func TestEditorialCycleConfig_Validate_RequiresPositiveValues(t *testing.T) {
	valid := config.EditorialCycleConfig{SampleSize: 50, PoolLimit: 500, AmbiguousCooldownDays: 14}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	invalid := config.EditorialCycleConfig{SampleSize: 0, PoolLimit: 500, AmbiguousCooldownDays: 14}
	if err := invalid.Validate(); err == nil {
		t.Error("Validate() error = nil, want an error for sample_size = 0")
	}
}

func TestEditorialCycleConfig_AmbiguousCooldown_ConvertsDaysToDuration(t *testing.T) {
	c := config.EditorialCycleConfig{SampleSize: 50, PoolLimit: 500, AmbiguousCooldownDays: 14}
	if got, want := c.AmbiguousCooldown(), 14*24*time.Hour; got != want {
		t.Errorf("AmbiguousCooldown() = %v, want %v", got, want)
	}
}

// validEditorialGatesConfig returns an EditorialGatesConfig that passes
// Validate(), for tests to mutate a single field off of.
func validEditorialGatesConfig() config.EditorialGatesConfig {
	return config.EditorialGatesConfig{
		MinPrecision: 0.7, MaxRecallRegression: 0.05, MinRegressionBaselineExamples: 500,
		MinPRAUC: 0.5, MaxFalsePositiveRate: 0.15, MinPerSourcePrecision: 0.6, MinSourceExamples: 5,
		MaxArtifactSizeBytes: 1_048_576, MaxInferenceNanos: 1_000_000,
	}
}

func TestEditorialGatesConfig_Validate_RequiresSensibleValues(t *testing.T) {
	valid := validEditorialGatesConfig()
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	tests := []struct {
		name string
		cfg  config.EditorialGatesConfig
	}{
		{"MinPrecision above 1", config.EditorialGatesConfig{MinPrecision: 1.5, MaxRecallRegression: 0.05, MinPRAUC: 0.5, MaxFalsePositiveRate: 0.5, MinPerSourcePrecision: 0.6, MinSourceExamples: 5, MaxArtifactSizeBytes: 1, MaxInferenceNanos: 1}},
		{"MinPrecision negative", config.EditorialGatesConfig{MinPrecision: -0.1, MaxRecallRegression: 0.05, MinPRAUC: 0.5, MaxFalsePositiveRate: 0.5, MinPerSourcePrecision: 0.6, MinSourceExamples: 5, MaxArtifactSizeBytes: 1, MaxInferenceNanos: 1}},
		{"negative MaxRecallRegression", config.EditorialGatesConfig{MinPrecision: 0.7, MaxRecallRegression: -0.01, MinPRAUC: 0.5, MaxFalsePositiveRate: 0.5, MinPerSourcePrecision: 0.6, MinSourceExamples: 5, MaxArtifactSizeBytes: 1, MaxInferenceNanos: 1}},
		{"zero MaxArtifactSizeBytes", config.EditorialGatesConfig{MinPrecision: 0.7, MaxRecallRegression: 0.05, MinPRAUC: 0.5, MaxFalsePositiveRate: 0.5, MinPerSourcePrecision: 0.6, MinSourceExamples: 5, MaxArtifactSizeBytes: 0, MaxInferenceNanos: 1}},
		{"zero MaxInferenceNanos", config.EditorialGatesConfig{MinPrecision: 0.7, MaxRecallRegression: 0.05, MinPRAUC: 0.5, MaxFalsePositiveRate: 0.5, MinPerSourcePrecision: 0.6, MinSourceExamples: 5, MaxArtifactSizeBytes: 1, MaxInferenceNanos: 0}},
		{"negative MinSourceExamples", config.EditorialGatesConfig{MinPrecision: 0.7, MaxRecallRegression: 0.05, MinPRAUC: 0.5, MaxFalsePositiveRate: 0.5, MinPerSourcePrecision: 0.6, MinSourceExamples: -1, MaxArtifactSizeBytes: 1, MaxInferenceNanos: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want an error for %s", tt.name)
			}
		})
	}
}

func TestEditorialGatesConfig_Validate_MaxFalsePositiveRateOutOfRangeFails(t *testing.T) {
	cfg := validEditorialGatesConfig() // existing test helper building a passing config
	cfg.MaxFalsePositiveRate = 1.5
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for max_false_positive_rate > 1")
	}
}

func TestEditorialGatesConfig_Validate_MinRegressionBaselineExamplesNegativeFails(t *testing.T) {
	cfg := validEditorialGatesConfig()
	cfg.MinRegressionBaselineExamples = -1
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for negative min_regression_baseline_examples")
	}
}

func TestRemoteConfig_Validate_RequiresPositiveRetentionDays(t *testing.T) {
	c := config.RemoteConfig{SQLiteRemoteDBHint: "~/phins-news-engine/data/operational.db", RetentionDays: 0}
	if err := c.Validate(); err == nil {
		t.Error("Validate() error = nil, want an error when retention_days is not positive")
	}
}

func TestRemoteConfig_Validate_AcceptsValidConfig(t *testing.T) {
	c := config.RemoteConfig{SQLiteRemoteDBHint: "~/phins-news-engine/data/operational.db", RetentionDays: 7}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestDictionaryUpdateConfig_Validate_RejectsEmptyAllowedDomainsWhenEnabled(t *testing.T) {
	cfg := config.DictionaryUpdateConfig{
		Enabled:        true,
		AllowedDomains: nil,
		Sources: map[string]config.DictionarySourceConfig{
			"roster": {URL: "https://www.isotopes.example.com/team/players-roster/", Mode: "auto"},
		},
		DeactivationPolicy: config.DeactivationPolicyConfig{MinimumSuccessfulAbsences: 2, MinimumElapsedDays: 3},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error when enabled with no allowed_domains")
	}
}

func TestDictionaryUpdateConfig_Validate_RejectsSourceURLOutsideAllowedDomains(t *testing.T) {
	cfg := config.DictionaryUpdateConfig{
		Enabled:        true,
		AllowedDomains: []string{"isotopes.example.com"},
		Sources: map[string]config.DictionarySourceConfig{
			"roster": {URL: "https://example.com/team/players-roster/", Mode: "auto"},
		},
		DeactivationPolicy: config.DeactivationPolicyConfig{MinimumSuccessfulAbsences: 2, MinimumElapsedDays: 3},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error when a source URL's host is not in allowed_domains")
	}
}

func TestDictionaryUpdateConfig_Validate_AcceptsWellFormedConfig(t *testing.T) {
	cfg := config.DictionaryUpdateConfig{
		Enabled:        true,
		AllowedDomains: []string{"isotopes.example.com", "www.isotopes.example.com"},
		Sources: map[string]config.DictionarySourceConfig{
			"roster": {URL: "https://www.isotopes.example.com/team/players-roster/", Mode: "auto"},
		},
		DeactivationPolicy: config.DeactivationPolicyConfig{MinimumSuccessfulAbsences: 2, MinimumElapsedDays: 3},
		Plausibility: map[string]config.PlausibilityConfig{
			"roster": {MinimumEntities: 40, MaximumEntities: 130, MaximumMissingRatio: 0.30},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
}

func TestDictionaryUpdateConfig_Validate_RejectsUnknownTransport(t *testing.T) {
	cfg := config.DictionaryUpdateConfig{
		Enabled:        true,
		Transport:      "carrier-pigeon",
		AllowedDomains: []string{"isotopes.example.com"},
		Sources: map[string]config.DictionarySourceConfig{
			"roster": {URL: "https://isotopes.example.com/team/players-roster/", Mode: "auto"},
		},
		DeactivationPolicy: config.DeactivationPolicyConfig{MinimumSuccessfulAbsences: 2, MinimumElapsedDays: 3},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error for an unknown transport value")
	}
}

func TestDictionaryUpdateConfig_EffectiveTransport_DefaultsToDirect(t *testing.T) {
	cfg := config.DictionaryUpdateConfig{}
	if got := cfg.EffectiveTransport(); got != config.DictionaryUpdateTransportDirect {
		t.Errorf("EffectiveTransport() = %q, want %q", got, config.DictionaryUpdateTransportDirect)
	}
	cfg.Transport = config.DictionaryUpdateTransportScrapingService
	if got := cfg.EffectiveTransport(); got != config.DictionaryUpdateTransportScrapingService {
		t.Errorf("EffectiveTransport() = %q, want %q", got, config.DictionaryUpdateTransportScrapingService)
	}
}

func TestDictionaryUpdateConfig_Validate_SkippedWhenDisabled(t *testing.T) {
	cfg := config.DictionaryUpdateConfig{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: disabled config should never error, got: %v", err)
	}
}

func TestSourcePolicyConfig_Validate_RequiresPath(t *testing.T) {
	c := config.SourcePolicyConfig{}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error for missing path")
	}
	c.Path = "configs/source-policies.yml"
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func validConfigForProfile(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func TestConfigValidateForProfile_RequiresTelegramBotToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	path := filepath.Join(t.TempDir(), "settings.yml")
	if err := os.WriteFile(path, []byte(validYAML), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := config.Load(path) // no TELEGRAM_BOT_TOKEN set
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.ValidateForProfile(config.ProfileLocal); err == nil {
		t.Error("ValidateForProfile() error = nil, want error for missing TELEGRAM_BOT_TOKEN")
	}
}

func TestConfigValidateForProfile_RequiresScrapingServiceAPIKeyWhenEnabled(t *testing.T) {
	cfg := validConfigForProfile(t)
	cfg.ScrapingService.Enabled = true
	cfg.ScrapingService.APIKey = ""
	if err := cfg.ValidateForProfile(config.ProfileLocal); err == nil {
		t.Error("ValidateForProfile() error = nil, want error for scraping_service.enabled with empty APIKey")
	}
}

func TestConfigValidateForProfile_ProductionRejectsPlaceholderTelegramChannelID(t *testing.T) {
	cfg := validConfigForProfile(t)
	cfg.Telegram.ChannelID = "-1000000000000"
	if err := cfg.ValidateForProfile(config.ProfileProduction); err == nil {
		t.Error("ValidateForProfile(production) error = nil, want error for placeholder telegram.channel_id")
	}
	if err := cfg.ValidateForProfile(config.ProfileLocal); err != nil {
		t.Errorf("ValidateForProfile(local) error = %v, want nil — placeholder only rejected in production", err)
	}
}

func TestConfigValidateForProfile_ValidConfigPasses(t *testing.T) {
	cfg := validConfigForProfile(t)
	cfg.Telegram.ChannelID = "-1001234567890"
	for _, p := range []config.Profile{config.ProfileLocal, config.ProfileTest, config.ProfileProduction} {
		if err := cfg.ValidateForProfile(p); err != nil {
			t.Errorf("ValidateForProfile(%v) error = %v, want nil", p, err)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   bool
	}{
		{"empty is not loopback (callers check empty separately)", "", false},
		{"malformed url is not loopback", "http://[::not-valid", false},
		{"real public host", "https://bridge.internal.example.com", false},
		{"127.0.0.1", "http://127.0.0.1:8090", true},
		{"localhost", "http://localhost:7000", true},
		{"0.0.0.0", "http://0.0.0.0:8090", true},
		{"::1", "http://[::1]:8090", true},
		{"mixed case LOCALHOST", "http://LOCALHOST:7000", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := config.IsLoopbackHost(tt.rawURL); got != tt.want {
				t.Errorf("IsLoopbackHost(%q) = %v, want %v", tt.rawURL, got, tt.want)
			}
		})
	}
}

func TestConfigValidateForProfile_ProductionAllowsLocalhostScrapingServiceBaseURL(t *testing.T) {
	cfg := validConfigForProfile(t)
	cfg.Telegram.ChannelID = "-1001234567890"
	cfg.ScrapingService.Enabled = true
	cfg.ScrapingService.APIKey = "test-key"
	cfg.ScrapingService.BaseURL = "http://127.0.0.1:9000"
	for _, p := range []config.Profile{config.ProfileLocal, config.ProfileProduction} {
		if err := cfg.ValidateForProfile(p); err != nil {
			t.Errorf("ValidateForProfile(%s) error = %v, want nil", p, err)
		}
	}
}

func TestConfigValidateForProfile_ProductionRejectsLocalhostDictionaryUpdateSourceURL(t *testing.T) {
	cfg := validConfigForProfile(t)
	cfg.DictionaryUpdate.Enabled = true
	cfg.DictionaryUpdate.Sources = map[string]config.DictionarySourceConfig{
		"roster": {URL: "http://localhost:8080/roster", Mode: "auto"},
	}
	if err := cfg.ValidateForProfile(config.ProfileProduction); err == nil {
		t.Error("ValidateForProfile(production) error = nil, want error for localhost dictionary_update source url")
	}
	if err := cfg.ValidateForProfile(config.ProfileLocal); err != nil {
		t.Errorf("ValidateForProfile(local) error = %v, want nil — localhost only rejected in production", err)
	}
}

func TestConfigValidateForProfile_ProductionAcceptsShadowAndEnforceEditorialMode(t *testing.T) {
	for _, mode := range []string{"shadow", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			cfg := validConfigForProfile(t)
			cfg.Telegram.ChannelID = "-1001234567890"
			cfg.Editorial.Mode = mode
			if err := cfg.ValidateForProfile(config.ProfileProduction); err != nil {
				t.Errorf("ValidateForProfile(production) error = %v, want nil — mode=%s must not be forced or blocked by this task", err, mode)
			}
		})
	}
}

func TestEditorialRolloutConfig_Validate(t *testing.T) {
	valid := config.EditorialRolloutConfig{
		ShadowMinDays: 14, ShadowMinArticles: 200,
		CanaryPercent: 10, CanaryMinDays: 7, CanaryMinArticles: 100,
		CanaryMaxFalsePositiveRate: 0.05, CanaryMinPrecision: 0.85,
	}
	if err := valid.Validate(0.7); err != nil {
		t.Errorf("Validate() error = %v, want nil for a valid config (gate floor 0.7)", err)
	}

	tooLowCanaryPercent := valid
	tooLowCanaryPercent.CanaryPercent = 0
	if err := tooLowCanaryPercent.Validate(0.7); err == nil {
		t.Error("Validate() error = nil, want error for canary_percent = 0")
	}

	tooHighCanaryPercent := valid
	tooHighCanaryPercent.CanaryPercent = 100
	if err := tooHighCanaryPercent.Validate(0.7); err == nil {
		t.Error("Validate() error = nil, want error for canary_percent = 100")
	}

	belowGateFloor := valid
	belowGateFloor.CanaryMinPrecision = 0.5
	if err := belowGateFloor.Validate(0.7); err == nil {
		t.Error("Validate() error = nil, want error when canary_min_precision < editorial_gates.min_precision")
	}

	zeroShadowDays := valid
	zeroShadowDays.ShadowMinDays = 0
	if err := zeroShadowDays.Validate(0.7); err == nil {
		t.Error("Validate() error = nil, want error for shadow_min_days = 0")
	}
}

func TestConfig_Redacted_CoversEverySecretField(t *testing.T) {
	var cfg config.Config
	paths := collectYAMLDashFieldPaths(t, reflect.TypeOf(cfg), "")
	if len(paths) == 0 {
		t.Fatal("collectYAMLDashFieldPaths found no yaml:\"-\" fields — walker or fixture is broken")
	}

	v := reflect.ValueOf(&cfg).Elem()
	for _, path := range paths {
		setNonZero(t, fieldByPath(t, v, path))
	}

	redacted := cfg.Redacted()
	origV := reflect.ValueOf(cfg)
	redV := reflect.ValueOf(redacted)
	for _, path := range paths {
		ov := fieldByPath(t, origV, path)
		rv := fieldByPath(t, redV, path)
		if reflect.DeepEqual(ov.Interface(), rv.Interface()) {
			t.Errorf("Redacted() did not change field %s from its original value %v", path, ov.Interface())
		}
	}
}

// collectYAMLDashFieldPaths recursively walks typ's fields (including
// nested structs) and returns the dotted path of every field tagged
// yaml:"-" — the secret fields Redacted() must mask.
func collectYAMLDashFieldPaths(t *testing.T, typ reflect.Type, prefix string) []string {
	t.Helper()
	var paths []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := f.Name
		if prefix != "" {
			name = prefix + "." + name
		}
		if f.Tag.Get("yaml") == "-" {
			paths = append(paths, name)
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			paths = append(paths, collectYAMLDashFieldPaths(t, f.Type, name)...)
		}
	}
	return paths
}

func fieldByPath(t *testing.T, v reflect.Value, path string) reflect.Value {
	t.Helper()
	for _, part := range strings.Split(path, ".") {
		v = v.FieldByName(part)
	}
	return v
}

func setNonZero(t *testing.T, fv reflect.Value) {
	t.Helper()
	switch fv.Kind() {
	case reflect.String:
		fv.SetString("sentinel-secret-value")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fv.SetInt(999)
	default:
		t.Fatalf("setNonZero: unsupported kind %v for field type %v — extend this helper when a new secret field type is added", fv.Kind(), fv.Type())
	}
}

func TestEntityDiscoveryConfig_ValidateRequiresBaseURLAndModelWhenEnabled(t *testing.T) {
	cfg := config.EntityDiscoveryConfig{Enabled: true, ExtractorVersion: "entity-discovery-v1", BatchSize: 20, Limit: 500}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error for missing llm.base_url/model when enabled")
	}
	cfg.LLM = config.EntityDiscoveryLLMConfig{BaseURL: "http://localhost:11434/v1", Model: "qwen3:4b", TimeoutSeconds: 60}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil once llm.base_url/model are set", err)
	}
	cfg.LLM.MaxTokens = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error for negative llm.max_tokens")
	}
}

func TestEntityDiscoveryConfig_ValidateIsNoOpWhenDisabled(t *testing.T) {
	cfg := config.EntityDiscoveryConfig{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil when disabled", err)
	}
}

func TestOfflineTrainingConfig_ValidateRejectsOutOfRangeThreshold(t *testing.T) {
	for _, threshold := range []float64{-0.01, 1.01} {
		cfg := config.OfflineTrainingConfig{
			LLM: config.OfflineTrainingLLMConfig{AutoLabelThreshold: threshold},
			ReviewScoring: config.ReviewScoringConfig{
				Weights:                         config.ReviewScoringWeights{Disagreement: 1, NBUncertainty: 0.7, LLMUncertainty: 0.7, NovelVocabulary: 0.5, UnknownEntities: 0.5, NewSource: 0.3, Recency: 0.2},
				LLMConfidenceAutoLabelThreshold: 0.9,
				LLMConfidenceUncertainThreshold: 0.5,
				NewSourceWindowHours:            72,
				RecencyWindowHours:              24,
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate() with threshold %v error = nil, want an error", threshold)
		}
	}
}

func TestOfflineTrainingConfig_ValidateAcceptsInRangeThreshold(t *testing.T) {
	for _, threshold := range []float64{0, 0.95, 1} {
		cfg := config.OfflineTrainingConfig{
			LLM: config.OfflineTrainingLLMConfig{AutoLabelThreshold: threshold},
			ReviewScoring: config.ReviewScoringConfig{
				Weights:                         config.ReviewScoringWeights{Disagreement: 1, NBUncertainty: 0.7, LLMUncertainty: 0.7, NovelVocabulary: 0.5, UnknownEntities: 0.5, NewSource: 0.3, Recency: 0.2},
				LLMConfidenceAutoLabelThreshold: 0.9,
				LLMConfidenceUncertainThreshold: 0.5,
				NewSourceWindowHours:            72,
				RecencyWindowHours:              24,
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with threshold %v error = %v, want nil", threshold, err)
		}
	}
}

func TestEntityDiscoveryLLMConfig_Timeout(t *testing.T) {
	cfg := config.EntityDiscoveryLLMConfig{TimeoutSeconds: 60}
	if got, want := cfg.Timeout(), 60*time.Second; got != want {
		t.Errorf("Timeout() = %v, want %v", got, want)
	}
}

func TestApplyEnvOverrides_SetsLLMAPIKeyFromEnv(t *testing.T) {
	t.Setenv("LLM_API_KEY", "test-secret")
	path := writeFixture(t, validYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.EntityDiscovery.LLM.APIKey != "test-secret" {
		t.Errorf("EntityDiscovery.LLM.APIKey = %q, want %q", cfg.EntityDiscovery.LLM.APIKey, "test-secret")
	}
}

func TestConfig_Redacted_MasksLLMAPIKey(t *testing.T) {
	cfg := config.Config{EntityDiscovery: config.EntityDiscoveryConfig{LLM: config.EntityDiscoveryLLMConfig{APIKey: "secret"}}}
	if got := cfg.Redacted().EntityDiscovery.LLM.APIKey; got != "[REDACTED]" {
		t.Errorf("Redacted().EntityDiscovery.LLM.APIKey = %q, want [REDACTED]", got)
	}
}

func TestOfflineTrainingLLMConfig_Timeout(t *testing.T) {
	cfg := config.OfflineTrainingLLMConfig{TimeoutSeconds: 60}
	if got, want := cfg.Timeout(), 60*time.Second; got != want {
		t.Errorf("Timeout() = %v, want %v", got, want)
	}
}

func TestApplyEnvOverrides_SetsOfflineTrainingLLMAPIKeyFromEnv(t *testing.T) {
	t.Setenv("OFFLINE_TRAINING_LLM_API_KEY", "test-secret")
	path := writeFixture(t, validYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OfflineTraining.LLM.APIKey != "test-secret" {
		t.Errorf("OfflineTraining.LLM.APIKey = %q, want %q", cfg.OfflineTraining.LLM.APIKey, "test-secret")
	}
}

func TestConfig_Redacted_MasksOfflineTrainingLLMAPIKey(t *testing.T) {
	cfg := config.Config{OfflineTraining: config.OfflineTrainingConfig{LLM: config.OfflineTrainingLLMConfig{APIKey: "secret"}}}
	if got := cfg.Redacted().OfflineTraining.LLM.APIKey; got != "[REDACTED]" {
		t.Errorf("Redacted().OfflineTraining.LLM.APIKey = %q, want [REDACTED]", got)
	}
}

// TestOfflineTrainingConfig_Validate_ReviewScoringOmittedIsNotAnError
// covers Finding 5 (final review): a config that has never added the
// offline_training.review_scoring block leaves both LLM confidence
// thresholds at their Go zero value. That must be treated as "block
// omitted" and skipped, not as a misconfiguration -- otherwise every
// existing operator config (including the main ingestion bot's, which
// doesn't need review_scoring at all) fails to load.
func TestOfflineTrainingConfig_Validate_ReviewScoringOmittedIsNotAnError(t *testing.T) {
	cfg := config.OfflineTrainingConfig{
		LLM: config.OfflineTrainingLLMConfig{AutoLabelThreshold: 0.95},
		// ReviewScoring left entirely at its zero value -- as if the
		// block was never added to the YAML config.
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with omitted review_scoring block error = %v, want nil", err)
	}
}

func TestOfflineTrainingConfig_Validate_ReviewScoring(t *testing.T) {
	base := func() config.OfflineTrainingConfig {
		return config.OfflineTrainingConfig{
			LLM: config.OfflineTrainingLLMConfig{AutoLabelThreshold: 0.95},
			ReviewScoring: config.ReviewScoringConfig{
				Weights:                         config.ReviewScoringWeights{Disagreement: 1, NBUncertainty: 0.7, LLMUncertainty: 0.7, NovelVocabulary: 0.5, UnknownEntities: 0.5, NewSource: 0.3, Recency: 0.2},
				LLMConfidenceAutoLabelThreshold: 0.9,
				LLMConfidenceUncertainThreshold: 0.5,
				NewSourceWindowHours:            72,
				RecencyWindowHours:              24,
			},
		}
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("Validate() on valid config error = %v, want nil", err)
	}

	tests := []struct {
		name   string
		mutate func(*config.OfflineTrainingConfig)
	}{
		{"negative weight", func(c *config.OfflineTrainingConfig) { c.ReviewScoring.Weights.Disagreement = -1 }},
		{"uncertain threshold negative", func(c *config.OfflineTrainingConfig) { c.ReviewScoring.LLMConfidenceUncertainThreshold = -0.1 }},
		{"auto label threshold above 1", func(c *config.OfflineTrainingConfig) { c.ReviewScoring.LLMConfidenceAutoLabelThreshold = 1.1 }},
		{"uncertain threshold not below auto label threshold", func(c *config.OfflineTrainingConfig) {
			c.ReviewScoring.LLMConfidenceUncertainThreshold = 0.9
			c.ReviewScoring.LLMConfidenceAutoLabelThreshold = 0.9
		}},
		{"new source window not positive", func(c *config.OfflineTrainingConfig) { c.ReviewScoring.NewSourceWindowHours = 0 }},
		{"recency window not positive", func(c *config.OfflineTrainingConfig) { c.ReviewScoring.RecencyWindowHours = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want an error")
			}
		})
	}
}

func TestPromotionConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.PromotionConfig
		wantErr bool
	}{
		{"all zero (omitted block) is valid", config.PromotionConfig{}, false},
		{"valid configured block", config.PromotionConfig{MinPrecision: 0.9, MaxPrecisionDrop: 0.02, MinF1Improvement: 0}, false},
		{"min_precision below 0", config.PromotionConfig{MinPrecision: -0.1}, true},
		{"min_precision above 1", config.PromotionConfig{MinPrecision: 1.1}, true},
		{"max_precision_drop negative", config.PromotionConfig{MaxPrecisionDrop: -0.01}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRetrainTriggersConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.RetrainTriggersConfig
		wantErr bool
	}{
		{"all zero is valid (every trigger disabled)", config.RetrainTriggersConfig{}, false},
		{"auto_enabled alone with no thresholds is valid", config.RetrainTriggersConfig{AutoEnabled: true}, false},
		{"valid configured block", config.RetrainTriggersConfig{
			AutoEnabled: true, MinNewGoldExamples: 100, MinNewSilverExamples: 50,
			MaxDisagreementRate: 0.05, MaxManualErrorRate: 0.2, NovelVocabularyThreshold: 10, MinHardExampleCount: 5,
		}, false},
		{"negative min_new_gold_examples", config.RetrainTriggersConfig{MinNewGoldExamples: -1}, true},
		{"negative min_new_silver_examples", config.RetrainTriggersConfig{MinNewSilverExamples: -1}, true},
		{"max_disagreement_rate above 1", config.RetrainTriggersConfig{MaxDisagreementRate: 1.1}, true},
		{"max_disagreement_rate negative", config.RetrainTriggersConfig{MaxDisagreementRate: -0.1}, true},
		{"max_manual_error_rate above 1", config.RetrainTriggersConfig{MaxManualErrorRate: 1.1}, true},
		{"negative novel_vocabulary_threshold", config.RetrainTriggersConfig{NovelVocabularyThreshold: -1}, true},
		{"negative min_hard_example_count", config.RetrainTriggersConfig{MinHardExampleCount: -1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMiningConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.MiningConfig
		wantErr bool
	}{
		{"all zero is valid (mining effectively disabled at batch_limit=0)", config.MiningConfig{}, false},
		{"valid configured block", config.MiningConfig{BatchLimit: 200, MinNBConfidence: 0.8}, false},
		{"negative batch_limit", config.MiningConfig{BatchLimit: -1}, true},
		{"min_nb_confidence below 0", config.MiningConfig{MinNBConfidence: -0.1}, true},
		{"min_nb_confidence above 1", config.MiningConfig{MinNBConfidence: 1.1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLivePublicationPolicyConfig_ValidateAcceptsQuarterMilestones(t *testing.T) {
	c := config.LivePublicationPolicyConfig{
		Enabled:         true,
		TTLMinutes:      30,
		SourceAllowlist: []string{"rss:score"},
		Milestones:      []string{"kickoff", "q1", "q2", "halftime", "q3", "q4", "final"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for q1-q4 milestones", err)
	}
}

func TestEspnScoreboardConfig_ValidateRequiresTeamAndTimeoutWhenEnabled(t *testing.T) {
	disabled := config.EspnScoreboardConfig{Enabled: false}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil when disabled", err)
	}

	missingTeam := config.EspnScoreboardConfig{Enabled: true, RequestTimeoutSeconds: 5}
	if err := missingTeam.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error for empty team_abbreviation when enabled")
	}

	missingTimeout := config.EspnScoreboardConfig{Enabled: true, TeamAbbreviation: "spr"}
	if err := missingTimeout.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want an error for a non-positive request_timeout_seconds when enabled")
	}

	valid := config.EspnScoreboardConfig{Enabled: true, TeamAbbreviation: "spr", RequestTimeoutSeconds: 5}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a complete config", err)
	}
	if got := valid.Timeout(); got != 5*time.Second {
		t.Fatalf("Timeout() = %v, want 5s", got)
	}
}

func TestConfig_ValidateRunsEspnScoreboardSection(t *testing.T) {
	cfg := validConfigForProfile(t)
	cfg.EspnScoreboard = config.EspnScoreboardConfig{Enabled: true} // missing team_abbreviation
	if err := cfg.Validate(); err == nil {
		t.Fatal("Config.Validate() error = nil, want the espn_scoreboard section's error to surface")
	}
}

func TestModelTrainingConfig_ValidateRejectsOutOfRangeFPR(t *testing.T) {
	for _, fpr := range []float64{-0.01, 1, 1.5} {
		cfg := config.OfflineTrainingConfig{ModelTraining: config.ModelTrainingConfig{ThresholdMaxFalsePositiveRate: fpr}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate() with threshold_max_false_positive_rate %v error = nil, want an error", fpr)
		}
	}
}

func TestModelTrainingConfig_ValidateAcceptsDisabledOrInRangeFPR(t *testing.T) {
	for _, fpr := range []float64{0, 0.12, 0.99} {
		cfg := config.OfflineTrainingConfig{ModelTraining: config.ModelTrainingConfig{ThresholdMaxFalsePositiveRate: fpr}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with threshold_max_false_positive_rate %v error = %v, want nil", fpr, err)
		}
	}
}

func TestModelTrainingConfig_ValidatesSilverWeight(t *testing.T) {
	for _, w := range []float64{-0.1, 1.1} {
		cfg := config.OfflineTrainingConfig{ModelTraining: config.ModelTrainingConfig{SilverWeight: w}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate() with silver_weight %v error = nil, want an error", w)
		}
	}
	for _, w := range []float64{0, 0.6, 1} {
		cfg := config.OfflineTrainingConfig{ModelTraining: config.ModelTrainingConfig{SilverWeight: w}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() with silver_weight %v error = %v, want nil", w, err)
		}
	}
}

func TestLoad_RepoSettingsEnableEnforceReadyModelTraining(t *testing.T) {
	// settings.yml enables web lookup, whose key is a secret supplied only
	// via the environment; Load rejects the config without it. The Tavily
	// fallback key is deliberately not required at load time.
	t.Setenv("WEB_SEARCH_API_KEY", "test-key")
	cfg, err := config.Load(repofs.ExampleConfig(t, "settings.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	mt := cfg.OfflineTraining.ModelTraining
	if mt.ThresholdMaxFalsePositiveRate <= 0 || mt.ThresholdMaxFalsePositiveRate >= cfg.EditorialGates.MaxFalsePositiveRate {
		t.Errorf("threshold_max_false_positive_rate = %v, want in (0, editorial_gates.max_false_positive_rate=%v) so validation keeps a margin under the gate",
			mt.ThresholdMaxFalsePositiveRate, cfg.EditorialGates.MaxFalsePositiveRate)
	}
	if mt.SilverWeight <= 0 || mt.SilverWeight >= 1 {
		t.Errorf("silver_weight = %v, want in (0, 1) so Silver rows weigh less than Gold", mt.SilverWeight)
	}
	if !mt.StratifySplitByLabel || !mt.RelevantOnly || !mt.BinaryFeatures {
		t.Errorf("model_training = %+v, want stratify_split_by_label, relevant_only and binary_features enabled", mt)
	}
}

func validWikidataConfig() config.WikidataConfig {
	return config.WikidataConfig{
		Enabled:          true,
		BaseURL:          "https://www.wikidata.org/w/api.php",
		Language:         "en",
		UserAgent:        "phins-news-engine/1.0 (https://example.com/contact; ops@example.com)",
		MaxCandidates:    5,
		TimeoutSeconds:   10,
		PositiveCacheTTL: 720 * time.Hour,
		NegativeCacheTTL: 24 * time.Hour,
		CacheRetention:   720 * time.Hour,
	}
}

func TestWikidataConfig_ValidateIsNoOpWhenDisabled(t *testing.T) {
	if err := (config.WikidataConfig{}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil when disabled", err)
	}
}

func TestWikidataConfig_ValidateAcceptsValidConfig(t *testing.T) {
	if err := validWikidataConfig().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestWikidataConfig_ValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.WikidataConfig)
	}{
		{"empty base_url", func(c *config.WikidataConfig) { c.BaseURL = "" }},
		{"non-http base_url", func(c *config.WikidataConfig) { c.BaseURL = "ftp://x" }},
		{"empty language", func(c *config.WikidataConfig) { c.Language = "" }},
		{"user_agent without contact", func(c *config.WikidataConfig) { c.UserAgent = "phins-news-engine" }},
		{"zero max_candidates", func(c *config.WikidataConfig) { c.MaxCandidates = 0 }},
		{"zero timeout", func(c *config.WikidataConfig) { c.TimeoutSeconds = 0 }},
		{"negative concurrency", func(c *config.WikidataConfig) { c.MaxConcurrency = -1 }},
		{"zero positive ttl", func(c *config.WikidataConfig) { c.PositiveCacheTTL = 0 }},
		{"zero negative ttl", func(c *config.WikidataConfig) { c.NegativeCacheTTL = 0 }},
		{"negative ttl longer than positive", func(c *config.WikidataConfig) { c.NegativeCacheTTL = 800 * time.Hour }},
		{"zero retention", func(c *config.WikidataConfig) { c.CacheRetention = 0 }},
		{"bad property id", func(c *config.WikidataConfig) { c.Properties = []string{"54"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validWikidataConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
		})
	}
}

func TestWikidataConfig_Helpers(t *testing.T) {
	cfg := config.WikidataConfig{TimeoutSeconds: 7}
	if cfg.Timeout() != 7*time.Second {
		t.Errorf("Timeout() = %v, want 7s", cfg.Timeout())
	}
	if cfg.Concurrency() != 1 {
		t.Errorf("Concurrency() = %d, want default 1", cfg.Concurrency())
	}
	cfg.MaxConcurrency = 3
	if cfg.Concurrency() != 3 {
		t.Errorf("Concurrency() = %d, want 3", cfg.Concurrency())
	}
}

func TestEntityDiscoveryVerificationDefaults(t *testing.T) {
	cfg, err := config.Load(writeFixture(t, validYAML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	v := cfg.EntityDiscovery.Verification
	if v.WikidataFirst || !v.Fallback {
		t.Fatalf("defaults = %+v, want wikidata_first=false fallback=true", v)
	}
}

func TestEntityDiscoveryVerificationExplicitValues(t *testing.T) {
	y := validYAML + "\nentity_discovery:\n  verification:\n    wikidata_first: true\n    fallback: false\n"
	cfg, err := config.Load(writeFixture(t, y))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	v := cfg.EntityDiscovery.Verification
	if !v.WikidataFirst || v.Fallback {
		t.Fatalf("values = %+v, want wikidata_first=true fallback=false", v)
	}
}

func TestBacklogConfig_ThresholdsDefaultsAndOverrides(t *testing.T) {
	if got := (config.BacklogConfig{}).Thresholds(); got != publishing.DefaultBacklogThresholds() {
		t.Errorf("zero BacklogConfig.Thresholds() = %+v, want defaults %+v", got, publishing.DefaultBacklogThresholds())
	}
	got := config.BacklogConfig{PendingWarning: 5, PendingCritical: 10, AgeWarningMinutes: 15, AgeCriticalMinutes: 30, LagP95WarningMinutes: 60, LagP95CriticalMinutes: 120}.Thresholds()
	want := publishing.BacklogThresholds{PendingWarning: 5, PendingCritical: 10, AgeWarning: 15 * time.Minute, AgeCritical: 30 * time.Minute, LagP95Warning: time.Hour, LagP95Critical: 2 * time.Hour}
	if got != want {
		t.Errorf("Thresholds() = %+v, want %+v", got, want)
	}
}

func TestBacklogConfig_ValidateRejectsWarningAboveCritical(t *testing.T) {
	if err := (config.BacklogConfig{PendingWarning: 10, PendingCritical: 5}).Validate(); err == nil {
		t.Error("Validate() = nil, want error when pending_warning > pending_critical")
	}
	if err := (config.BacklogConfig{AgeWarningMinutes: 90, AgeCriticalMinutes: 30}).Validate(); err == nil {
		t.Error("Validate() = nil, want error when age_warning_minutes > age_critical_minutes")
	}
	if err := (config.BacklogConfig{PendingWarning: -1}).Validate(); err == nil {
		t.Error("Validate() = nil, want error for negative threshold")
	}
	if err := (config.BacklogConfig{}).Validate(); err != nil {
		t.Errorf("Validate() zero value = %v, want nil (defaults apply)", err)
	}
}

func TestRosterConfig_Validate(t *testing.T) {
	ok := config.RosterConfig{Enabled: true, Path: "data/nflverse_roster.csv", MaxAgeDays: 7}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	for name, bad := range map[string]config.RosterConfig{
		"no path":      {Enabled: true, MaxAgeDays: 7},
		"negative age": {Enabled: true, Path: "x", MaxAgeDays: -1},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
	if err := (config.RosterConfig{}).Validate(); err != nil {
		t.Fatalf("a disabled block must always validate, got %v", err)
	}
}

func TestWebLookupConfig_Validate_Fallback(t *testing.T) {
	base := func() config.WebLookupConfig {
		return config.WebLookupConfig{
			Enabled: true, Provider: "searxng", BaseURL: "http://localhost:8081/search", APIKey: "test-key",
			TimeoutSeconds: 10, MaxSearchesPerCandidate: 2, MaxResultsPerSearch: 5, MaxSearchesPerRun: 50,
			CacheTTL: 168 * time.Hour, TrustedDomains: []string{"league.example.com"},
		}
	}
	none := base()
	none.Fallback = config.WebLookupFallbackConfig{MaxSearchesPerRun: -5}
	if err := none.Validate(); err != nil {
		t.Fatalf("an empty fallback provider must ignore every other fallback field, got %v", err)
	}
	// The key is checked where the provider is built (buildVerifier), not at
	// load time, so tools and tests that never search can load the repo config.
	noKey := base()
	noKey.Fallback = config.WebLookupFallbackConfig{Provider: "tavily", MaxSearchesPerRun: 100}
	if err := noKey.Validate(); err != nil {
		t.Fatalf("a missing TAVILY_API_KEY must not fail config validation, got %v", err)
	}
	noBudget := base()
	noBudget.Fallback = config.WebLookupFallbackConfig{Provider: "tavily", APIKey: "tvly-x"}
	if err := noBudget.Validate(); err == nil {
		t.Fatal("a fallback provider without a positive max_searches_per_run must fail validation")
	}
	ok := base()
	ok.Fallback = config.WebLookupFallbackConfig{Provider: "tavily", APIKey: "tvly-x", MaxSearchesPerRun: 100}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestApplyEnvOverrides_TavilyAPIKey(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "tvly-env")
	cfg, err := config.Load(writeFixture(t, validYAML))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CatalogMaintenance.WebLookup.Fallback.APIKey != "tvly-env" {
		t.Fatalf("Fallback.APIKey = %q, want tvly-env", cfg.CatalogMaintenance.WebLookup.Fallback.APIKey)
	}
}

func TestLeagueConfig_Validate(t *testing.T) {
	good := config.LeagueConfig{Enabled: true, RulesPath: "configs/league_alias_rules.yml", Chooser: config.LeagueChooserConfig{Enabled: true, MaxCandidates: 8, MaxArticles: 3}}
	if err := good.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	for name, bad := range map[string]config.LeagueConfig{
		"no rules path":       {Enabled: true},
		"chooser no budget":   {Enabled: true, RulesPath: "x", Chooser: config.LeagueChooserConfig{Enabled: true}},
		"too many candidates": {Enabled: true, RulesPath: "x", Chooser: config.LeagueChooserConfig{Enabled: true, MaxCandidates: 9, MaxArticles: 3}},
		"too many articles":   {Enabled: true, RulesPath: "x", Chooser: config.LeagueChooserConfig{Enabled: true, MaxCandidates: 8, MaxArticles: 6}},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
	if err := (config.LeagueConfig{}).Validate(); err != nil {
		t.Fatalf("a disabled block must always validate, got %v", err)
	}
	if err := (config.LeagueConfig{Chooser: config.LeagueChooserConfig{Enabled: true}}).Validate(); err != nil {
		t.Fatalf("a disabled league block must ignore the chooser settings, got %v", err)
	}
}

func TestLeagueCatalogConfig_Validate(t *testing.T) {
	for name, c := range map[string]config.LeagueCatalogConfig{
		"empty is off":  {},
		"explicit off":  {Mode: "off"},
		"mentions only": {Mode: "mentions_only", RosterPath: "data/nflverse_roster.csv", RulesPath: "configs/league_alias_rules.yml", MaxAgeDays: 14},
	} {
		if err := c.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}
	for name, c := range map[string]config.LeagueCatalogConfig{
		"secondary is not built":  {Mode: "secondary", RosterPath: "x", RulesPath: "y"},
		"primary is not built":    {Mode: "primary", RosterPath: "x", RulesPath: "y"},
		"unknown mode":            {Mode: "shadow"},
		"mentions without roster": {Mode: "mentions_only", RulesPath: "y"},
		"mentions without rules":  {Mode: "mentions_only", RosterPath: "x"},
		"negative age":            {Mode: "mentions_only", RosterPath: "x", RulesPath: "y", MaxAgeDays: -1},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
	if !(config.LeagueCatalogConfig{Mode: "mentions_only"}).Enabled() || (config.LeagueCatalogConfig{Mode: "off"}).Enabled() || (config.LeagueCatalogConfig{}).Enabled() {
		t.Error("Enabled() must be true only for mentions_only")
	}
	if got := (config.LeagueCatalogConfig{MaxAgeDays: 14}).MaxAge(); got != 14*24*time.Hour {
		t.Errorf("MaxAge() = %v", got)
	}
}

func TestRepoSettings_LeagueCatalogShipsOff(t *testing.T) {
	t.Setenv("WEB_SEARCH_API_KEY", "test-key")
	cfg, err := config.Load(repofs.ExampleConfig(t, "settings.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clustering.EntityCatalog.League.Enabled() {
		t.Fatal("configs/example/settings.yml must ship with the league catalog off; enabling it is an explicit operator decision")
	}
}

func TestAnchorConfig_Validate(t *testing.T) {
	ok := config.AnchorConfig{Enabled: true, Categories: []string{"a", "b"}, MultiWordCategories: []string{"b"}, MinConfidence: 0.7}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := (config.AnchorConfig{}).Validate(); err != nil {
		t.Fatalf("disabled must always validate: %v", err)
	}
	for name, c := range map[string]config.AnchorConfig{
		"no categories":       {Enabled: true},
		"unlisted multi-word": {Enabled: true, Categories: []string{"a"}, MultiWordCategories: []string{"b"}},
		"confidence above 1":  {Enabled: true, Categories: []string{"a"}, MinConfidence: 1.5},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestStoryGuardConfig_Validate(t *testing.T) {
	ok := config.StoryGuardConfig{Enabled: true, WindowHours: 48, MaxPerStory: 2, MinSharedTokens: 4, OverlapThreshold: 0.65, GenericTokenShare: 0.05}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := (config.StoryGuardConfig{}).Validate(); err != nil {
		t.Fatalf("disabled must always validate: %v", err)
	}
	for name, mutate := range map[string]func(*config.StoryGuardConfig){
		"window":                 func(c *config.StoryGuardConfig) { c.WindowHours = 0 },
		"max":                    func(c *config.StoryGuardConfig) { c.MaxPerStory = 0 },
		"shared":                 func(c *config.StoryGuardConfig) { c.MinSharedTokens = 0 },
		"overlap":                func(c *config.StoryGuardConfig) { c.OverlapThreshold = 1.2 },
		"generic":                func(c *config.StoryGuardConfig) { c.GenericTokenShare = 1 },
		"verbatim above one":     func(c *config.StoryGuardConfig) { c.VerbatimOverlapThreshold = 1.2 },
		"verbatim below overlap": func(c *config.StoryGuardConfig) { c.VerbatimOverlapThreshold = 0.5 },
	} {
		c := ok
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
