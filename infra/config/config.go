// Package config loads and validates the application configuration from a
// YAML file plus environment-variable overrides for secrets. Validation is
// hand-written (no reflection-based validation library) per one method per
// section.
package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/storyguard"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

// Profile selects how strictly Config.ValidateForProfile and
// fetcherwiring.Build behave.
type Profile string

const (
	ProfileLocal      Profile = "local"
	ProfileTest       Profile = "test"
	ProfileProduction Profile = "production"
)

// DefaultProfileEnvVar is the environment variable ResolveProfile reads
// unless the host program renames it through ProfileEnvVar.
const DefaultProfileEnvVar = "NEWS_ENGINE_PROFILE"

// ProfileEnvVar is the environment variable ResolveProfile reads the
// deployment profile from. A host program may rename it once at startup.
var ProfileEnvVar = DefaultProfileEnvVar

// ParseProfile parses the --profile flag / ProfileEnvVar env value. An
// empty string defaults to ProfileLocal so existing dev workflows that
// pass neither keep working unchanged.
func ParseProfile(s string) (Profile, error) {
	switch strings.ToLower(s) {
	case "":
		return ProfileLocal, nil
	case "local":
		return ProfileLocal, nil
	case "test":
		return ProfileTest, nil
	case "production":
		return ProfileProduction, nil
	default:
		return "", fmt.Errorf("profile must be 'local', 'test' or 'production', got %q", s)
	}
}

// ResolveProfile applies the standard flag → ProfileEnvVar env var → default
// resolution both binaries use: flagValue wins if non-empty, otherwise the
// ProfileEnvVar environment variable, otherwise ParseProfile's own empty-
// string default (ProfileLocal).
func ResolveProfile(flagValue string) (Profile, error) {
	v := flagValue
	if v == "" {
		v = os.Getenv(ProfileEnvVar)
	}
	return ParseProfile(v)
}

// Config is the fully loaded and validated application configuration.
type Config struct {
	SQLite             SQLiteConfig             `yaml:"sqlite"`
	Scheduler          SchedulerConfig          `yaml:"scheduler"`
	HTTP               HTTPConfig               `yaml:"http"`
	ScrapingService    ScrapingServiceConfig    `yaml:"scraping_service"`
	Fetch              FetchConfig              `yaml:"fetch"`
	FetchSources       FetchSourcesConfig       `yaml:"fetch_sources"`
	SourcePolicy       SourcePolicyConfig       `yaml:"source_policy"`
	Normalization      NormalizationConfig      `yaml:"normalization"`
	Classification     ClassificationConfig     `yaml:"classification"`
	Deduplication      DeduplicationConfig      `yaml:"deduplication"`
	Clustering         ClusteringConfig         `yaml:"clustering"`
	Editorial          EditorialConfig          `yaml:"editorial"`
	Freshness          FreshnessConfig          `yaml:"freshness"`
	Telegram           TelegramConfig           `yaml:"telegram"`
	PublicationPolicy  PublicationPolicyConfig  `yaml:"publication_policy"`
	Publication        PublicationConfig        `yaml:"publication"`
	Retention          RetentionConfig          `yaml:"retention"`
	ClusterLifecycle   ClusterLifecycleConfig   `yaml:"cluster_lifecycle"`
	SeenItems          SeenItemsConfig          `yaml:"seen_items"`
	Model              ModelConfig              `yaml:"model"`
	Dictionary         DictionaryConfig         `yaml:"dictionary"`
	DictionaryMining   DictionaryMiningConfig   `yaml:"dictionary_mining"`
	EditorialCycle     EditorialCycleConfig     `yaml:"editorial_cycle"`
	EditorialGates     EditorialGatesConfig     `yaml:"editorial_gates"`
	EditorialCosts     EditorialCostsConfig     `yaml:"editorial_costs"`
	EditorialRollout   EditorialRolloutConfig   `yaml:"editorial_rollout"`
	Remote             RemoteConfig             `yaml:"remote"`
	DictionaryUpdate   DictionaryUpdateConfig   `yaml:"dictionary_update"`
	EntityDiscovery    EntityDiscoveryConfig    `yaml:"entity_discovery"`
	EntityExtraction   EntityExtractionConfig   `yaml:"entity_extraction"`
	CatalogMaintenance CatalogMaintenanceConfig `yaml:"catalog_maintenance"`
	OfflineTraining    OfflineTrainingConfig    `yaml:"offline_training"`
	EspnScoreboard     EspnScoreboardConfig     `yaml:"espn_scoreboard"`
}

// SQLiteConfig configures the operational SQLite database.
type SQLiteConfig struct {
	Path          string `yaml:"path"`
	BusyTimeoutMS int    `yaml:"busy_timeout_ms"`
}

// Validate checks SQLiteConfig has the minimum data required.
func (c SQLiteConfig) Validate() error {
	if c.Path == "" {
		return fmt.Errorf("path is required")
	}
	if c.BusyTimeoutMS <= 0 {
		return fmt.Errorf("busy_timeout_ms must be positive")
	}
	return nil
}

// BusyTimeout returns the configured busy timeout as a time.Duration.
func (c SQLiteConfig) BusyTimeout() time.Duration {
	return time.Duration(c.BusyTimeoutMS) * time.Millisecond
}

// SchedulerConfig configures the cycle, outbox-drain, and cleanup
// intervals.
type SchedulerConfig struct {
	CheckIntervalMinutes  int `yaml:"check_interval_minutes"`
	OutboxIntervalMinutes int `yaml:"outbox_interval_minutes"`
	CleanupIntervalHours  int `yaml:"cleanup_interval_hours"`
}

// Validate checks SchedulerConfig has the minimum data required.
func (c SchedulerConfig) Validate() error {
	if c.CheckIntervalMinutes <= 0 {
		return fmt.Errorf("check_interval_minutes must be positive")
	}
	if c.OutboxIntervalMinutes <= 0 {
		return fmt.Errorf("outbox_interval_minutes must be positive")
	}
	if c.CleanupIntervalHours <= 0 {
		return fmt.Errorf("cleanup_interval_hours must be positive")
	}
	return nil
}

// CheckInterval returns the cycle interval as a time.Duration.
func (c SchedulerConfig) CheckInterval() time.Duration {
	return time.Duration(c.CheckIntervalMinutes) * time.Minute
}

// OutboxInterval returns the outbox-drain interval as a time.Duration.
func (c SchedulerConfig) OutboxInterval() time.Duration {
	return time.Duration(c.OutboxIntervalMinutes) * time.Minute
}

// CleanupInterval returns the retention-cleanup interval as a time.Duration.
func (c SchedulerConfig) CleanupInterval() time.Duration {
	return time.Duration(c.CleanupIntervalHours) * time.Hour
}

// HTTPConfig bounds outbound HTTP calls made by six of the seven fetch
// categories: rss, reddit, bluesky, twitter, instagram, and youtube (via
// fetcherwiring's rssClientConfig for rss/reddit/instagram/youtube, and
// directly for bluesky/twitter). Only website is exempt — it uses
// scraping_service.timeout_seconds instead, since it goes through the
// external Scraping Service, not a direct HTTP call.
type HTTPConfig struct {
	TimeoutSeconds   int   `yaml:"timeout_seconds"`
	MaxResponseBytes int64 `yaml:"max_response_bytes"`
	// AllowPrivateHosts lets RSS and Reddit feed fetches reach loopback
	// and private-network addresses. Default false: catalog feed URLs that
	// resolve to loopback, RFC 1918, CGNAT or link-local addresses are
	// refused. The YouTube bridge base URL is operator infrastructure and
	// is always allowed on private networks (see docs/SECURITY.md).
	AllowPrivateHosts bool `yaml:"allow_private_hosts"`
}

// Validate checks HTTPConfig has the minimum data required.
func (c HTTPConfig) Validate() error {
	if c.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	if c.MaxResponseBytes <= 0 {
		return fmt.Errorf("max_response_bytes must be positive")
	}
	return nil
}

// Timeout returns the configured HTTP timeout as a time.Duration.
func (c HTTPConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// ScrapingServiceConfig points to the auxiliary Scraping Service used by
// fetchers that need rendered HTML (see ADR 0011). All fields besides
// Enabled/BaseURL are only validated when Enabled is true, matching the
// placeholder pattern earlier phases use for not-yet-active sections.
type ScrapingServiceConfig struct {
	Enabled                bool                                  `yaml:"enabled"`
	BaseURL                string                                `yaml:"base_url"`
	TimeoutSeconds         int                                   `yaml:"timeout_seconds"`
	MaxRetries             int                                   `yaml:"max_retries"`
	BackoffBaseMS          int                                   `yaml:"backoff_base_ms"`
	BackoffMaxMS           int                                   `yaml:"backoff_max_ms"`
	ResponseLimitBytes     int64                                 `yaml:"response_limit_bytes"`
	DefaultMode            string                                `yaml:"default_mode"`
	DefaultCacheTTLSeconds int                                   `yaml:"default_cache_ttl_seconds"`
	Concurrency            ScrapingConcurrencyConfig             `yaml:"concurrency"`
	CircuitBreaker         ScrapingCircuitBreakerConfig          `yaml:"circuit_breaker"`
	Domains                map[string]ScrapingDomainPolicyConfig `yaml:"domains"`
	APIKey                 string                                `yaml:"-"`
}

// ScrapingConcurrencyConfig bounds in-flight Scraping Service requests,
// globally and per target domain.
type ScrapingConcurrencyConfig struct {
	Global    int `yaml:"global"`
	PerDomain int `yaml:"per_domain"`
}

// ScrapingCircuitBreakerConfig tunes the host scraper adapter's
// in-memory, per-domain circuit breaker.
type ScrapingCircuitBreakerConfig struct {
	FailureThreshold  int `yaml:"failure_threshold"`
	CooldownSeconds   int `yaml:"cooldown_seconds"`
	HalfOpenMaxProbes int `yaml:"half_open_max_probes"`
}

// ScrapingDomainPolicyConfig overrides mode/cache_ttl/concurrency for one
// target domain. Zero-value fields mean "no override" (fall back to the
// section-level default).
type ScrapingDomainPolicyConfig struct {
	Mode            string `yaml:"mode"`
	CacheTTLSeconds int    `yaml:"cache_ttl_seconds"`
	Concurrency     int    `yaml:"concurrency"`
}

// Validate is a no-op when Enabled is false; when true, checks base_url is
// present and every tuning field is sane.
func (c ScrapingServiceConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required when enabled")
	}
	if c.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("max_retries must not be negative")
	}
	if c.BackoffBaseMS <= 0 {
		return fmt.Errorf("backoff_base_ms must be positive")
	}
	if c.BackoffMaxMS < c.BackoffBaseMS {
		return fmt.Errorf("backoff_max_ms must be >= backoff_base_ms")
	}
	if c.ResponseLimitBytes <= 0 {
		return fmt.Errorf("response_limit_bytes must be positive")
	}
	switch c.DefaultMode {
	case "http", "browser", "auto":
	default:
		return fmt.Errorf("default_mode must be 'http', 'browser' or 'auto', got %q", c.DefaultMode)
	}
	if c.DefaultCacheTTLSeconds <= 0 {
		return fmt.Errorf("default_cache_ttl_seconds must be positive")
	}
	if c.Concurrency.Global <= 0 {
		return fmt.Errorf("concurrency.global must be positive")
	}
	if c.Concurrency.PerDomain <= 0 {
		return fmt.Errorf("concurrency.per_domain must be positive")
	}
	if c.CircuitBreaker.FailureThreshold <= 0 {
		return fmt.Errorf("circuit_breaker.failure_threshold must be positive")
	}
	if c.CircuitBreaker.CooldownSeconds <= 0 {
		return fmt.Errorf("circuit_breaker.cooldown_seconds must be positive")
	}
	if c.CircuitBreaker.HalfOpenMaxProbes <= 0 {
		return fmt.Errorf("circuit_breaker.half_open_max_probes must be positive")
	}
	for domain, dp := range c.Domains {
		if domain == "" {
			return fmt.Errorf("domains: empty domain key")
		}
		switch dp.Mode {
		case "", "http", "browser", "auto":
		default:
			return fmt.Errorf("domains[%s]: mode must be 'http', 'browser' or 'auto', got %q", domain, dp.Mode)
		}
		if dp.CacheTTLSeconds < 0 {
			return fmt.Errorf("domains[%s]: cache_ttl_seconds must not be negative", domain)
		}
		if dp.Concurrency < 0 {
			return fmt.Errorf("domains[%s]: concurrency must not be negative", domain)
		}
	}
	return nil
}

// Timeout returns the configured Scraping Service HTTP timeout.
func (c ScrapingServiceConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// BackoffBase returns the configured retry backoff base delay.
func (c ScrapingServiceConfig) BackoffBase() time.Duration {
	return time.Duration(c.BackoffBaseMS) * time.Millisecond
}

// BackoffMax returns the configured retry backoff cap.
func (c ScrapingServiceConfig) BackoffMax() time.Duration {
	return time.Duration(c.BackoffMaxMS) * time.Millisecond
}

// DefaultCacheTTL returns the configured default per-request cache TTL.
func (c ScrapingServiceConfig) DefaultCacheTTL() time.Duration {
	return time.Duration(c.DefaultCacheTTLSeconds) * time.Second
}

// Cooldown returns the configured circuit breaker cooldown.
func (c ScrapingCircuitBreakerConfig) Cooldown() time.Duration {
	return time.Duration(c.CooldownSeconds) * time.Second
}

// EntityDiscoveryConfig configures the offline `entities discover`
// command: mining unknown entities/aliases for the
// deterministic entity catalog from stored article title/summary via an
// LLM. Disabled by default, same placeholder pattern as
// ScrapingServiceConfig — this section is never read by the ingestion
// pipeline (the service binary), only by the tools CLI.
type EntityDiscoveryConfig struct {
	Enabled          bool                     `yaml:"enabled"`
	ExtractorVersion string                   `yaml:"extractor_version"`
	BatchSize        int                      `yaml:"batch_size"`
	Limit            int                      `yaml:"limit"`
	LLM              EntityDiscoveryLLMConfig `yaml:"llm"`
	Wikidata         WikidataConfig           `yaml:"wikidata"`
	Verification     VerificationConfig       `yaml:"verification"`
	Roster           RosterConfig             `yaml:"roster"`
	League           LeagueConfig             `yaml:"league"`
}

// VerificationConfig toggles the Wikidata-first verification chain.
// Load defaults Fallback to true when the key is absent.
type VerificationConfig struct {
	// WikidataFirst resolves structured identities via Wikidata before
	// any LLM/SearXNG call.
	WikidataFirst bool `yaml:"wikidata_first"`
	// Fallback enables the LLM + SearXNG fallback for ambiguous,
	// not-found or error outcomes.
	Fallback bool `yaml:"fallback"`
}

// RosterConfig configures the offline nflverse roster stage of entity
// verification (read only by the tools CLI).
type RosterConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Path       string `yaml:"path"`         // local CSV written by `entities roster sync`
	MaxAgeDays int    `yaml:"max_age_days"` // older files disable the stage; 0 = never stale
	URL        string `yaml:"url"`          // empty = nflverse default for the current year
}

// Validate checks an enabled block has a path and a sane age.
func (c RosterConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Path) == "" {
		return fmt.Errorf("roster.path is required when enabled")
	}
	if c.MaxAgeDays < 0 {
		return fmt.Errorf("roster.max_age_days must not be negative")
	}
	return nil
}

// LeagueConfig configures the league gazetteer stage of entity verification
// (read only by the tools CLI).
type LeagueConfig struct {
	Enabled   bool                `yaml:"enabled"`
	RulesPath string              `yaml:"rules_path"`
	Chooser   LeagueChooserConfig `yaml:"chooser"`
}

// LeagueChooserConfig bounds the closed-set LLM chooser.
type LeagueChooserConfig struct {
	Enabled       bool `yaml:"enabled"`
	MaxCandidates int  `yaml:"max_candidates"` // 1..8
	MaxArticles   int  `yaml:"max_articles"`   // 1..5
}

// Validate checks an enabled block is usable.
func (c LeagueConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.RulesPath) == "" {
		return fmt.Errorf("league.rules_path is required when enabled")
	}
	if c.Chooser.Enabled {
		if c.Chooser.MaxCandidates < 1 || c.Chooser.MaxCandidates > 8 {
			return fmt.Errorf("league.chooser.max_candidates must be between 1 and 8")
		}
		if c.Chooser.MaxArticles < 1 || c.Chooser.MaxArticles > 5 {
			return fmt.Errorf("league.chooser.max_articles must be between 1 and 5")
		}
	}
	return nil
}

// EntityDiscoveryLLMConfig points to any OpenAI-compatible chat-
// completions endpoint, local or cloud. APIKey is never read from YAML —
// see applyEnvOverrides's LLM_API_KEY handling.
type EntityDiscoveryLLMConfig struct {
	BaseURL        string  `yaml:"base_url"`
	Model          string  `yaml:"model"`
	Temperature    float64 `yaml:"temperature"`
	TimeoutSeconds int     `yaml:"timeout_seconds"`
	// MaxTokens bounds one discovery response. Zero lets the provider
	// choose its own limit.
	MaxTokens int `yaml:"max_tokens"`
	// DisableThinking requests reasoning_effort=none from compatible
	// OpenAI-style providers such as Ollama's /v1 endpoint.
	DisableThinking bool   `yaml:"disable_thinking"`
	APIKey          string `yaml:"-"`
}

// Timeout returns the configured LLM HTTP request timeout.
func (c EntityDiscoveryLLMConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// Validate is a no-op when Enabled is false; when true, checks the
// fields entities discover cannot safely default (base_url/model) and
// the numeric fields it needs to be positive.
func (c EntityDiscoveryConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.LLM.BaseURL == "" {
		return fmt.Errorf("llm.base_url is required when enabled")
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model is required when enabled")
	}
	if c.LLM.TimeoutSeconds <= 0 {
		return fmt.Errorf("llm.timeout_seconds must be positive")
	}
	if c.LLM.MaxTokens < 0 {
		return fmt.Errorf("llm.max_tokens must not be negative")
	}
	if c.LLM.Temperature < 0 {
		return fmt.Errorf("llm.temperature must not be negative")
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("batch_size must be positive")
	}
	if c.ExtractorVersion == "" {
		return fmt.Errorf("extractor_version must not be empty")
	}
	return nil
}

// WikidataConfig configures the read-only Wikidata knowledge source used
// by offline entity discovery/enrichment. It is validated
// independently of EntityDiscoveryConfig.Enabled and is validated at load by
// all binaries, but never used by the service binary. No API key exists or is needed.
type WikidataConfig struct {
	Enabled        bool   `yaml:"enabled"`
	BaseURL        string `yaml:"base_url"`
	Language       string `yaml:"language"`
	MaxCandidates  int    `yaml:"max_candidates"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	// MaxConcurrency caps in-flight requests; zero means 1.
	MaxConcurrency   int           `yaml:"max_concurrency"`
	PositiveCacheTTL time.Duration `yaml:"positive_cache_ttl"`
	// NegativeCacheTTL must not exceed PositiveCacheTTL.
	NegativeCacheTTL time.Duration `yaml:"negative_cache_ttl"`
	// CacheRetention is how long past expiry a cache row is kept (stale
	// fallback window) before Purge deletes it.
	CacheRetention    time.Duration `yaml:"cache_retention"`
	AllowStaleOnError bool          `yaml:"allow_stale_on_error"`
	// UserAgent must identify the project and a contact (URL or email),
	// per Wikimedia's API etiquette.
	UserAgent string `yaml:"user_agent"`
	// Properties overrides the built-in statement allow-list (property
	// IDs such as "P54"). Empty means the adapter's default.
	Properties []string `yaml:"properties"`
	// AllowPrivateHosts lets base_url point at loopback or a private
	// network (a local mirror). Default false; see wikidata.Config.
	AllowPrivateHosts bool `yaml:"allow_private_hosts"`
}

// Timeout returns the configured HTTP request timeout.
func (c WikidataConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// Concurrency returns the effective max in-flight request count (>= 1).
func (c WikidataConfig) Concurrency() int {
	if c.MaxConcurrency <= 0 {
		return 1
	}
	return c.MaxConcurrency
}

// Validate is a no-op when Enabled is false.
func (c WikidataConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required when enabled")
	}
	if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("base_url must be an absolute http(s) URL")
	}
	if c.Language == "" {
		return fmt.Errorf("language is required when enabled")
	}
	if !strings.Contains(c.UserAgent, "@") && !strings.Contains(c.UserAgent, "http") {
		return fmt.Errorf("user_agent must include project and contact information (a URL or email)")
	}
	if c.MaxCandidates <= 0 {
		return fmt.Errorf("max_candidates must be positive")
	}
	if c.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	if c.MaxConcurrency < 0 {
		return fmt.Errorf("max_concurrency must not be negative")
	}
	if c.PositiveCacheTTL <= 0 || c.NegativeCacheTTL <= 0 {
		return fmt.Errorf("positive_cache_ttl and negative_cache_ttl must be positive")
	}
	if c.NegativeCacheTTL > c.PositiveCacheTTL {
		return fmt.Errorf("negative_cache_ttl must not exceed positive_cache_ttl")
	}
	if c.CacheRetention <= 0 {
		return fmt.Errorf("cache_retention must be positive")
	}
	for _, p := range c.Properties {
		if !wikidataPropertyPattern.MatchString(p) {
			return fmt.Errorf("properties: %q is not a property ID like P54", p)
		}
	}
	return nil
}

var wikidataPropertyPattern = regexp.MustCompile(`^P[0-9]+$`)

// EntityExtractionConfig groups configuration for online, in-pipeline
// entity extraction behavior — currently just
// Fallback, kept as its own nested struct in case a sibling
// entity_extraction setting is added later without another top-level
// Config field.
type EntityExtractionConfig struct {
	Fallback EntityFallbackConfig `yaml:"fallback"`
}

// EntityFallbackConfig points to the OpenAI-compatible endpoint the
// online, per-candidate unknown-entity resolution fallback calls from
// inside the service binary. Deliberately separate
// from EntityDiscoveryLLMConfig: the two flows must be independently
// configurable (issue requirement) — e.g. offline pointed at a local
// small model, online at a free-tier cloud model, or vice versa. APIKey
// is never read from YAML — see applyEnvOverrides's LLM_FALLBACK_API_KEY
// handling.
type EntityFallbackConfig struct {
	Enabled bool `yaml:"enabled"`
	// Provider is a free-text label ("groq", "ollama", ...) used only
	// for log lines and the `entities fallback-test` CLI's output — it
	// never selects different request/response logic. Every provider
	// must still speak the same OpenAI-compatible chat-completions
	// shape transport.go sends. Optional; an empty value prints as
	// "unknown" wherever it's displayed.
	Provider                string  `yaml:"provider"`
	BaseURL                 string  `yaml:"base_url"`
	Model                   string  `yaml:"model"`
	Temperature             float64 `yaml:"temperature"`
	TimeoutSeconds          int     `yaml:"timeout_seconds"`
	MaxTokens               int     `yaml:"max_tokens"`
	MinConfidence           float64 `yaml:"min_confidence"`
	APIKey                  string  `yaml:"-"`
	MaxCandidatesPerArticle int     `yaml:"max_candidates_per_article"`
}

// Timeout returns the configured LLM HTTP request timeout.
func (c EntityFallbackConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// Validate is a no-op when Enabled is false; when true, checks the
// fields the fallback cannot safely default and that min_confidence is
// a valid probability.
func (c EntityFallbackConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required when enabled")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required when enabled")
	}
	if c.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	if c.Temperature < 0 {
		return fmt.Errorf("temperature must not be negative")
	}
	if c.MinConfidence < 0 || c.MinConfidence > 1 {
		return fmt.Errorf("min_confidence must be within [0,1]")
	}
	if c.MaxCandidatesPerArticle < 0 {
		return fmt.Errorf("max_candidates_per_article must not be negative")
	}
	return nil
}

// CatalogMaintenanceConfig groups configuration for the offline catalog
// maintenance tooling — currently just WebLookup, kept as
// its own nested struct in case a sibling catalog-maintenance setting is
// added later without another top-level Config field, same pattern as
// EntityExtractionConfig.
type CatalogMaintenanceConfig struct {
	WebLookup WebLookupConfig `yaml:"web_lookup"`
}

// WebLookupConfig configures the offline `entities discover --verify`
// bounded web-lookup tool loop. Disabled by default; never
// read by the ingestion pipeline (the service binary) or the online
// entity-resolution fallback — only by the tools CLI. APIKey
// is never read from YAML — see applyEnvOverrides's WEB_SEARCH_API_KEY
// handling.
type WebLookupConfig struct {
	Enabled bool `yaml:"enabled"`
	// Provider selects the concrete ports/out.WebSearcher implementation
	// via websearch.New(Provider, ...) — currently only "searxng" is
	// supported; any other value is a config error.
	Provider                string `yaml:"provider"`
	BaseURL                 string `yaml:"base_url"`
	APIKey                  string `yaml:"-"`
	TimeoutSeconds          int    `yaml:"timeout_seconds"`
	MaxSearchesPerCandidate int    `yaml:"max_searches_per_candidate"`
	MaxResultsPerSearch     int    `yaml:"max_results_per_search"`
	MaxSearchesPerRun       int    `yaml:"max_searches_per_run"`
	// MinSearchIntervalMS paces provider calls: at least this many
	// milliseconds between two consecutive searches, so a long run does not
	// trip the upstream engines' rate limits. 0 disables pacing.
	MinSearchIntervalMS int `yaml:"min_search_interval_ms"`
	// MaxConsecutiveProviderFailures opens a circuit after this many
	// back-to-back failed searches: the rest of the run stops calling the
	// provider (candidates are left to be retried later) instead of
	// hammering engines that are already suspended. 0 disables the circuit.
	MaxConsecutiveProviderFailures int `yaml:"max_consecutive_provider_failures"`
	// EngineGroups rotates consecutive searches across SearXNG engine sets
	// (each entry a comma-separated engine list, e.g. "bing,yahoo"). Empty
	// disables rotation and lets SearXNG query all its enabled engines.
	EngineGroups   []string      `yaml:"engine_groups"`
	CacheTTL       time.Duration `yaml:"cache_ttl"`
	TrustedDomains []string      `yaml:"trusted_domains"`
	// Fallback optionally names a second provider used only when the primary
	// reports an outage (e.g. SearXNG rate-limited -> Tavily). Its API key
	// comes only from TAVILY_API_KEY, never from this file.
	Fallback WebLookupFallbackConfig `yaml:"fallback"`
}

// WebLookupFallbackConfig configures the secondary search provider.
type WebLookupFallbackConfig struct {
	Provider          string `yaml:"provider"` // "" = none; "tavily"
	BaseURL           string `yaml:"base_url"`
	MaxSearchesPerRun int    `yaml:"max_searches_per_run"`
	APIKey            string `yaml:"-"`
}

// Timeout returns the configured web-search HTTP request timeout.
func (c WebLookupConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// OfflineTrainingConfig groups configuration for the offline
// LLM-teacher-assisted Naive Bayes training workflow: the tier threshold
// editoriallabelproposal.ComputeTier needs, the LLM
// endpoint/batching settings the `editorial classify` command needs, and the
// review-priority scoring bands `train review` needs. Never read by the ingestion pipeline
// (the service binary) — only by the tools CLI.
type OfflineTrainingConfig struct {
	LLM             OfflineTrainingLLMConfig `yaml:"llm"`
	ReviewScoring   ReviewScoringConfig      `yaml:"review_scoring"`
	Promotion       PromotionConfig          `yaml:"promotion"`
	RetrainTriggers RetrainTriggersConfig    `yaml:"retrain_triggers"`
	Mining          MiningConfig             `yaml:"mining"`
	ModelTraining   ModelTrainingConfig      `yaml:"model_training"`
}

// ModelTrainingConfig tunes how the editorial classifier is trained and
// how its thresholds are chosen. Zero values reproduce the pre-existing
// behavior (two-sided precision threshold sweep, plain time split, every
// labeled example, count-valued features) so omitting the block is safe.
type ModelTrainingConfig struct {
	// ThresholdMaxFalsePositiveRate, when > 0 and editorial.ambiguous_policy
	// is "reject", selects the accept threshold as the lowest score whose
	// validation FPR stays at or below this value. Keep it below
	// editorial_gates.max_false_positive_rate for margin.
	ThresholdMaxFalsePositiveRate float64 `yaml:"threshold_max_false_positive_rate"`
	// StratifySplitByLabel applies the time-ordered train/validate/test cut
	// per label, so negatives labeled in bursts still reach every split.
	StratifySplitByLabel bool `yaml:"stratify_split_by_label"`
	// RelevantOnly drops labeled examples the current relevance dictionary
	// would reject (unless from a trusted source): those never reach the
	// editorial classifier in production.
	RelevantOnly bool `yaml:"relevant_only"`
	// BinaryFeatures trains Naive Bayes on feature presence instead of
	// counts; the flag is stored in the model so inference matches.
	BinaryFeatures bool `yaml:"binary_features"`
	// SilverWeight scales Silver-tier (unreviewed LLM label) examples in
	// Naive Bayes counts; Gold rows always weigh 1.
	// 0 means unweighted (1.0).
	SilverWeight float64 `yaml:"silver_weight"`
}

// Validate rejects a threshold FPR cap outside [0, 1) and a silver weight
// outside [0, 1].
func (c ModelTrainingConfig) Validate() error {
	if c.SilverWeight < 0 || c.SilverWeight > 1 {
		return fmt.Errorf("silver_weight must be in [0, 1]")
	}
	if c.ThresholdMaxFalsePositiveRate < 0 || c.ThresholdMaxFalsePositiveRate >= 1 {
		return fmt.Errorf("threshold_max_false_positive_rate must be in [0, 1)")
	}
	return nil
}

// EspnScoreboardConfig configures the poller that stamps live match-state
// metadata (match_id/match_phase/match_home_team/match_away_team) onto
// accepted articles, feeding publication_policy.live's lane decision.
type EspnScoreboardConfig struct {
	Enabled               bool   `yaml:"enabled"`
	TeamAbbreviation      string `yaml:"team_abbreviation"`
	RequestTimeoutSeconds int    `yaml:"request_timeout_seconds"`
	// UserAgent is the HTTP User-Agent sent to the scoreboard endpoint.
	// Empty means the client's default ("phins-news-engine/1.0").
	UserAgent string `yaml:"user_agent"`
}

// Validate is a no-op when disabled, so an unconfigured deployment's
// behavior is unchanged.
func (c EspnScoreboardConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.TeamAbbreviation) == "" {
		return fmt.Errorf("team_abbreviation is required when enabled")
	}
	if c.RequestTimeoutSeconds <= 0 {
		return fmt.Errorf("request_timeout_seconds must be positive when enabled")
	}
	return nil
}

// Timeout converts RequestTimeoutSeconds into a time.Duration for the
// espnscoreboard.Client constructor.
func (c EspnScoreboardConfig) Timeout() time.Duration {
	return time.Duration(c.RequestTimeoutSeconds) * time.Second
}

// OfflineTrainingLLMConfig configures the offline LLM teacher's
// classification and auto-labeling
// behavior. APIKey is never read from YAML — see applyEnvOverrides's
// OFFLINE_TRAINING_LLM_API_KEY handling.
type OfflineTrainingLLMConfig struct {
	// AutoLabelThreshold is the confidence at or above which a proposal
	// is eligible to become a Silver training example
	// (editoriallabelproposal.ComputeTier) without human review.
	AutoLabelThreshold float64 `yaml:"auto_label_threshold"`
	BaseURL            string  `yaml:"base_url"`
	Model              string  `yaml:"model"`
	Temperature        float64 `yaml:"temperature"`
	TimeoutSeconds     int     `yaml:"timeout_seconds"`
	// BatchSize is how many articles are sent to the LLM per request.
	BatchSize int `yaml:"batch_size"`
	// Limit bounds how many articles a single `editorial classify` run
	// considers in total.
	Limit int `yaml:"limit"`
	// MaxTokens bounds one response. Zero lets the provider choose its
	// own limit.
	MaxTokens int `yaml:"max_tokens"`
	// DisableThinking asks reasoning models (Qwen3.5 on oMLX, Ollama) not
	// to generate thinking output before the JSON answer.
	DisableThinking bool   `yaml:"disable_thinking"`
	APIKey          string `yaml:"-"`
}

// PromotionConfig configures the promotion-specific quality bar (issue
// #253 Part 19) a candidate must additionally clear beyond
// EditorialGatesConfig before `train promote` will promote it without
// --force. See application/modelpromotion.PromotionCriteria — this type
// intentionally has no conversion method to it, for the same
// import-boundary reason EditorialGatesConfig's doc comment gives: the
// cmd layer converts these plain fields itself.
type PromotionConfig struct {
	// MinPrecision is an absolute floor, independent of
	// EditorialGatesConfig.MinPrecision — both are checked.
	MinPrecision float64 `yaml:"min_precision"`
	// MaxPrecisionDrop caps how much worse the candidate's precision may
	// be than the currently-promoted production model's. Skipped when
	// there is no promoted model yet.
	MaxPrecisionDrop float64 `yaml:"max_precision_drop"`
	// MinF1Improvement is the minimum F1 improvement over production
	// required to promote; 0 means no improvement is required (parity
	// is enough). Skipped when there is no promoted model yet.
	MinF1Improvement float64 `yaml:"min_f1_improvement"`
}

// Validate checks MinPrecision and MaxPrecisionDrop are within sensible
// ranges. An all-zero PromotionConfig is treated as "block omitted" and
// always passes, matching OfflineTrainingConfig's review_scoring
// omitted-block convention — see OfflineTrainingConfig.Validate.
func (c PromotionConfig) Validate() error {
	if c.MinPrecision == 0 && c.MaxPrecisionDrop == 0 && c.MinF1Improvement == 0 {
		return nil
	}
	if c.MinPrecision < 0 || c.MinPrecision > 1 {
		return fmt.Errorf("min_precision must be within [0,1]")
	}
	if c.MaxPrecisionDrop < 0 {
		return fmt.Errorf("max_precision_drop must not be negative")
	}
	return nil
}

// RetrainTriggersConfig configures when `train run --auto` is allowed to run unattended, and what conditions make it
// actually train. AutoEnabled is a coarse kill switch, independent of
// the six threshold fields below: it exists because the unattended
// path (and, once the hard-example-mining sub-project lands,
// its own LLM-backed mining step) calls the offline LLM teacher, and
// that must never happen without an operator's explicit opt-in — see
// the design spec's "AutoEnabled is a separate, coarser gate" section.
// Each threshold field is independently optional; its zero value means
// that specific trigger never fires. See
// application/retraintrigger.Config — this type intentionally has no
// conversion method to it, for the same import-boundary reason
// PromotionConfig's doc comment gives.
type RetrainTriggersConfig struct {
	// AutoEnabled gates train run --auto entirely: false (the default)
	// makes every --auto invocation a no-op that touches no DB state
	// and calls no LLM, regardless of the threshold fields below.
	AutoEnabled bool `yaml:"auto_enabled"`
	// MinNewGoldExamples/MinNewSilverExamples: retrain once at least
	// this many new Gold/Silver examples have accumulated since the
	// last SUCCEEDED training_runs row (trainrun.GatherAutoSignals uses
	// TrainingRunRepository.LatestSucceeded, not Latest, precisely so a
	// failed run's empty DatasetSummary never resets this baseline to
	// zero). 0 disables this trigger.
	MinNewGoldExamples   int `yaml:"min_new_gold_examples"`
	MinNewSilverExamples int `yaml:"min_new_silver_examples"`
	// MaxDisagreementRate: retrain when the current NB/LLM
	// disagreement share (review-queue disagreement count / total
	// dataset examples, post-CapPerCluster -- the same denominator the
	// training report prints) exceeds this. 0 disables this trigger.
	MaxDisagreementRate float64 `yaml:"max_disagreement_rate"`
	// MaxManualErrorRate: retrain when the currently-promoted model's
	// own ManualReviewRate exceeds this. 0 disables this trigger.
	//
	// WARNING: this is a latching, per-promoted-model snapshot, not a
	// trend. ManualReviewRate is computed once at training time for
	// whichever model is currently promoted and never changes while
	// that model stays promoted, so once it trips this threshold,
	// train run --auto will attempt to retrain every single night
	// until either a different model is promoted or an operator raises
	// this threshold / sets auto_enabled: false -- there is no
	// improve-in-place recovery.
	MaxManualErrorRate float64 `yaml:"max_manual_error_rate"`
	// NovelVocabularyThreshold: retrain when the review queue's count
	// of novel-vocabulary-dominant items reaches this. 0 disables this
	// trigger.
	NovelVocabularyThreshold int `yaml:"novel_vocabulary_threshold"`
	// MinHardExampleCount: retrain when unresolved hard-example
	// candidates reach this. 0 disables this trigger;
	// the signal that would feed it is always 0 until that sub-project
	// lands.
	MinHardExampleCount int `yaml:"min_hard_example_count"`
}

// Validate checks every numeric field is within a sensible range. An
// all-zero RetrainTriggersConfig (the default, including AutoEnabled
// staying false) always passes — it simply means no trigger can ever
// fire and --auto is always a no-op.
func (c RetrainTriggersConfig) Validate() error {
	if c.MinNewGoldExamples < 0 {
		return fmt.Errorf("min_new_gold_examples must be >= 0")
	}
	if c.MinNewSilverExamples < 0 {
		return fmt.Errorf("min_new_silver_examples must be >= 0")
	}
	if c.MaxDisagreementRate < 0 || c.MaxDisagreementRate > 1 {
		return fmt.Errorf("max_disagreement_rate must be within [0,1]")
	}
	if c.MaxManualErrorRate < 0 || c.MaxManualErrorRate > 1 {
		return fmt.Errorf("max_manual_error_rate must be within [0,1]")
	}
	if c.NovelVocabularyThreshold < 0 {
		return fmt.Errorf("novel_vocabulary_threshold must be >= 0")
	}
	if c.MinHardExampleCount < 0 {
		return fmt.Errorf("min_hard_example_count must be >= 0")
	}
	return nil
}

// MiningConfig configures `train mine`:
// scanning stored articles not yet in Gold for cases where the
// promoted NB model and the LLM teacher strongly disagree. BatchLimit
// bounds one scan; a scan this large is only ever run manually or from
// the nightly `train run --auto` timer, never from request-path code.
// MinNBConfidence is the NB-side confidence floor (mirrored against
// the LLM-side floor, offline_training.llm.auto_label_threshold, which
// this type does not duplicate). Reuses offline_training.llm's
// base_url/model/api_key as-is -- see the design spec's "no new LLM
// config" note.
type MiningConfig struct {
	// BatchLimit caps how many candidate articles one `train mine` scan
	// considers; 0 means mining finds nothing (an explicit BatchLimit is
	// required to mine at all, same "must opt in" posture as
	// RetrainTriggersConfig.AutoEnabled).
	BatchLimit int `yaml:"batch_limit"`
	// MinNBConfidence is the NB-side confidence floor a prediction must
	// clear (in either direction) before it can be paired with a
	// confident, disagreeing LLM label to form a hard example.
	MinNBConfidence float64 `yaml:"min_nb_confidence"`
}

// Validate checks BatchLimit and MinNBConfidence are within sensible
// ranges. An all-zero MiningConfig is valid — BatchLimit=0 simply means
// `train mine` finds nothing until an operator sets it.
func (c MiningConfig) Validate() error {
	if c.BatchLimit < 0 {
		return fmt.Errorf("batch_limit must be >= 0")
	}
	if c.MinNBConfidence < 0 || c.MinNBConfidence > 1 {
		return fmt.Errorf("min_nb_confidence must be within [0,1]")
	}
	return nil
}

// ReviewScoringWeights are review-priority scoring's per-component
// multipliers — see
// domain/reviewpriority.Score.
type ReviewScoringWeights struct {
	Disagreement    float64 `yaml:"disagreement"`
	NBUncertainty   float64 `yaml:"nb_uncertainty"`
	LLMUncertainty  float64 `yaml:"llm_uncertainty"`
	NovelVocabulary float64 `yaml:"novel_vocabulary"`
	UnknownEntities float64 `yaml:"unknown_entities"`
	NewSource       float64 `yaml:"new_source"`
	Recency         float64 `yaml:"recency"`
	HardMining      float64 `yaml:"hard_mining"`
}

// ReviewScoringConfig configures review-priority scoring's confidence
// bands and weights. LLMConfidenceAutoLabelThreshold /
// LLMConfidenceUncertainThreshold split LLM confidence into three bands
// (auto-label / review-if-useful / uncertain) — the same "obvious
// agreement" check `editorial classify`'s sync step and `train review`
// both rely on.
type ReviewScoringConfig struct {
	Weights ReviewScoringWeights `yaml:"weights"`
	// LLMConfidenceAutoLabelThreshold: an LLM proposal at or above this
	// confidence, agreeing with a confident NB zone, is an "obvious
	// agreement" and never enters the review queue.
	LLMConfidenceAutoLabelThreshold float64 `yaml:"llm_confidence_auto_label_threshold"`
	// LLMConfidenceUncertainThreshold: an LLM proposal at or below this
	// confidence is always queued, regardless of NB agreement.
	LLMConfidenceUncertainThreshold float64 `yaml:"llm_confidence_uncertain_threshold"`
	// NewSourceWindowHours is how long after a source's first_seen_at
	// the new_source signal stays above 0, decaying linearly to 0 at
	// this age.
	NewSourceWindowHours float64 `yaml:"new_source_window_hours"`
	// RecencyWindowHours is the equivalent decay window for an
	// article's own age (fetched_at), used by the recency signal.
	RecencyWindowHours float64 `yaml:"recency_window_hours"`
}

// Timeout returns the configured offline-teacher LLM HTTP request
// timeout.
func (c OfflineTrainingLLMConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// Validate checks AutoLabelThreshold and the review_scoring bands/
// weights. The LLM connection fields (BaseURL, Model, TimeoutSeconds,
// BatchSize, Limit) are intentionally NOT validated here: per the
// design spec, they are required only when the `editorial
// classify` command actually runs, not unconditionally whenever
// Config.Validate() runs (this section has no separate Enabled flag,
// and the shared test fixtures/most callers never set these fields).
// The `editorial classify` CLI command performs its own explicit
// checks on these fields before constructing the LLM client.
func (c OfflineTrainingConfig) Validate() error {
	if c.LLM.AutoLabelThreshold < 0 || c.LLM.AutoLabelThreshold > 1 {
		return fmt.Errorf("llm.auto_label_threshold must be between 0 and 1")
	}
	if err := c.Promotion.Validate(); err != nil {
		return fmt.Errorf("promotion: %w", err)
	}
	if err := c.RetrainTriggers.Validate(); err != nil {
		return fmt.Errorf("retrain_triggers: %w", err)
	}
	if err := c.Mining.Validate(); err != nil {
		return fmt.Errorf("mining: %w", err)
	}
	if err := c.ModelTraining.Validate(); err != nil {
		return fmt.Errorf("model_training: %w", err)
	}

	rs := c.ReviewScoring
	// A config that has never added the review_scoring block (the vast
	// majority of existing operator configs, plus the main ingestion
	// bot which doesn't need it at all) leaves both thresholds at Go's
	// zero value. A real, intentionally-configured block would never
	// set both thresholds to the same degenerate value of 0 (that
	// combination is otherwise rejected below as "uncertain must be <
	// auto-label"), so this exact zero-signature is treated as "block
	// omitted" and skipped entirely, rather than as a misconfiguration
	// that would break startup for every config missing this new block.
	if rs.LLMConfidenceAutoLabelThreshold == 0 && rs.LLMConfidenceUncertainThreshold == 0 {
		return nil
	}
	weights := map[string]float64{
		"disagreement": rs.Weights.Disagreement, "nb_uncertainty": rs.Weights.NBUncertainty,
		"llm_uncertainty": rs.Weights.LLMUncertainty, "novel_vocabulary": rs.Weights.NovelVocabulary,
		"unknown_entities": rs.Weights.UnknownEntities, "new_source": rs.Weights.NewSource,
		"recency": rs.Weights.Recency,
	}
	for _, name := range []string{"disagreement", "nb_uncertainty", "llm_uncertainty", "novel_vocabulary", "unknown_entities", "new_source", "recency"} {
		if weights[name] < 0 {
			return fmt.Errorf("review_scoring.weights.%s must be >= 0", name)
		}
	}
	if rs.LLMConfidenceUncertainThreshold < 0 {
		return fmt.Errorf("review_scoring.llm_confidence_uncertain_threshold must be >= 0")
	}
	if rs.LLMConfidenceAutoLabelThreshold > 1 {
		return fmt.Errorf("review_scoring.llm_confidence_auto_label_threshold must be <= 1")
	}
	if rs.LLMConfidenceUncertainThreshold >= rs.LLMConfidenceAutoLabelThreshold {
		return fmt.Errorf("review_scoring.llm_confidence_uncertain_threshold must be less than llm_confidence_auto_label_threshold")
	}
	if rs.NewSourceWindowHours <= 0 {
		return fmt.Errorf("review_scoring.new_source_window_hours must be positive")
	}
	if rs.RecencyWindowHours <= 0 {
		return fmt.Errorf("review_scoring.recency_window_hours must be positive")
	}
	return nil
}

// Validate is a no-op when Enabled is false; when true, checks the
// fields the tool loop cannot safely default, the numeric safeguards
// it needs to be strictly positive, and that a concrete provider can actually be constructed: base_url,
// a non-empty API key (sourced only from WEB_SEARCH_API_KEY —
// applyEnvOverrides has already run by the time Validate is called from
// Load), a positive cache_ttl, and at least one trusted domain.
func (c WebLookupConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Provider == "" {
		return fmt.Errorf("provider is required when enabled")
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required when enabled")
	}
	if c.APIKey == "" {
		return fmt.Errorf("WEB_SEARCH_API_KEY environment variable is required when enabled")
	}
	if c.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	if c.MaxSearchesPerCandidate <= 0 {
		return fmt.Errorf("max_searches_per_candidate must be positive")
	}
	if c.MaxResultsPerSearch <= 0 {
		return fmt.Errorf("max_results_per_search must be positive")
	}
	if c.MaxSearchesPerRun <= 0 {
		return fmt.Errorf("max_searches_per_run must be positive")
	}
	if c.MinSearchIntervalMS < 0 {
		return fmt.Errorf("min_search_interval_ms must not be negative")
	}
	if c.MaxConsecutiveProviderFailures < 0 {
		return fmt.Errorf("max_consecutive_provider_failures must not be negative")
	}
	if c.CacheTTL <= 0 {
		return fmt.Errorf("cache_ttl must be positive")
	}
	if c.Fallback.Provider != "" {
		// The key (TAVILY_API_KEY) is deliberately not required here: only the
		// code that builds the provider needs it, so loading the config for
		// any other tool, or in tests, must not depend on it.
		if c.Fallback.MaxSearchesPerRun <= 0 {
			return fmt.Errorf("web_lookup.fallback.max_searches_per_run must be positive")
		}
	}
	if len(c.TrustedDomains) == 0 {
		return fmt.Errorf("trusted_domains must contain at least one domain when enabled")
	}
	for _, d := range c.TrustedDomains {
		if strings.TrimSpace(d) == "" {
			return fmt.Errorf("trusted_domains: empty domain entry")
		}
	}
	return nil
}

// FetchConfig tunes the compositefetcher adapter: how many sources may run
// concurrently, how large its backpressure fan-in channel is, and where its
// source catalog (configs/sources.yml) lives.
type FetchConfig struct {
	GlobalConcurrency int    `yaml:"global_concurrency"`
	FanInBuffer       int    `yaml:"fan_in_buffer"`
	SourcesPath       string `yaml:"sources_path"`
}

// Validate checks FetchConfig has the minimum data required.
func (c FetchConfig) Validate() error {
	if c.GlobalConcurrency <= 0 {
		return fmt.Errorf("global_concurrency must be positive")
	}
	if c.FanInBuffer <= 0 {
		return fmt.Errorf("fan_in_buffer must be positive")
	}
	if c.SourcesPath == "" {
		return fmt.Errorf("sources_path is required")
	}
	return nil
}

// SourceCategoryConfig gates one of the 7 content-fetching categories
// (website, rss, reddit, bluesky, twitter, instagram, youtube). Enabled
// activates the category; OnMissingDependency overrides the profile
// default (see fetcherwiring.Build) for what happens when Enabled is true
// but the category's real dependency (scraper, or bridge base URL) is
// missing or invalid. Empty means "use the active profile's default."
type SourceCategoryConfig struct {
	Enabled             bool   `yaml:"enabled"`
	OnMissingDependency string `yaml:"on_missing_dependency"`
}

// Validate checks OnMissingDependency is one of the three accepted
// values.
func (c SourceCategoryConfig) Validate() error {
	switch c.OnMissingDependency {
	case "", "fatal", "degrade":
		return nil
	default:
		return fmt.Errorf("on_missing_dependency must be 'fatal', 'degrade' or empty, got %q", c.OnMissingDependency)
	}
}

// FetchSourcesConfig gates each of the 7 content-fetching categories
// independently.
// Omitting this section entirely from settings.yml leaves every category
// at its zero value (Enabled: false), which fetcherwiring.Build rejects
// as zero active sources — a clear startup failure rather than a silent
// no-op, by construction, with no extra validation needed here.
type FetchSourcesConfig struct {
	Website   SourceCategoryConfig `yaml:"website"`
	RSS       SourceCategoryConfig `yaml:"rss"`
	Reddit    SourceCategoryConfig `yaml:"reddit"`
	Bluesky   SourceCategoryConfig `yaml:"bluesky"`
	Twitter   SourceCategoryConfig `yaml:"twitter"`
	Instagram SourceCategoryConfig `yaml:"instagram"`
	YouTube   SourceCategoryConfig `yaml:"youtube"`
}

// Validate runs every category's Validate, wrapping errors with the
// category name.
func (c FetchSourcesConfig) Validate() error {
	sections := []struct {
		name string
		fn   func() error
	}{
		{"website", c.Website.Validate},
		{"rss", c.RSS.Validate},
		{"reddit", c.Reddit.Validate},
		{"bluesky", c.Bluesky.Validate},
		{"twitter", c.Twitter.Validate},
		{"instagram", c.Instagram.Validate},
		{"youtube", c.YouTube.Validate},
	}
	for _, s := range sections {
		if err := s.fn(); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// SourcePolicyConfig points at configs/source-policies.yml, the per-source
// deduplication/publication policy catalog. Path
// may point at a file that simply has an empty policies: [] list — the
// resolver built from it is then fully inert, matching "no config = no
// behavior change".
type SourcePolicyConfig struct {
	Path string `yaml:"path"`
}

// Validate checks SourcePolicyConfig has the minimum data required.
func (c SourcePolicyConfig) Validate() error {
	if c.Path == "" {
		return fmt.Errorf("source_policy: path is required")
	}
	return nil
}

// URLNormalizationConfig configures canonical-URL normalization.
type URLNormalizationConfig struct {
	TrackingParams      []string            `yaml:"tracking_params"`
	DomainIgnoredParams map[string][]string `yaml:"domain_ignored_params"`
	StripDefaultPorts   bool                `yaml:"strip_default_ports"`
	TrailingSlash       string              `yaml:"trailing_slash"`
	KnownLinkResolvers  []string            `yaml:"known_link_resolvers"`
}

// Validate checks TrailingSlash is a known policy (or empty, treated as
// "keep" by Policy()) and every configured resolver name is registered.
func (c URLNormalizationConfig) Validate() error {
	if c.TrailingSlash != "" && c.TrailingSlash != "strip" && c.TrailingSlash != "keep" {
		return fmt.Errorf("trailing_slash must be 'strip' or 'keep', got %q", c.TrailingSlash)
	}
	for _, name := range c.KnownLinkResolvers {
		if _, ok := normalization.Resolvers[name]; !ok {
			return fmt.Errorf("unknown known_link_resolvers entry %q", name)
		}
	}
	return nil
}

// Policy converts URLNormalizationConfig into the domain's URLConfig plus
// the resolved list of resolver functions.
func (c URLNormalizationConfig) Policy() (normalization.URLConfig, []normalization.KnownLinkResolver) {
	trailingSlash := c.TrailingSlash
	if trailingSlash == "" {
		trailingSlash = "keep"
	}
	resolvers := make([]normalization.KnownLinkResolver, 0, len(c.KnownLinkResolvers))
	for _, name := range c.KnownLinkResolvers {
		resolvers = append(resolvers, normalization.Resolvers[name])
	}
	return normalization.URLConfig{
		TrackingParams:      c.TrackingParams,
		DomainIgnoredParams: c.DomainIgnoredParams,
		StripDefaultPorts:   c.StripDefaultPorts,
		TrailingSlash:       trailingSlash,
	}, resolvers
}

// TextNormalizationConfig configures dedup/classification text normalization.
type TextNormalizationConfig struct {
	StripEmojis            bool     `yaml:"strip_emojis"`
	StripHashtags          bool     `yaml:"strip_hashtags"`
	StripMentions          bool     `yaml:"strip_mentions"`
	StripInlineURLs        bool     `yaml:"strip_inline_urls"`
	RepeatedPrefixPatterns []string `yaml:"repeated_prefix_patterns"`
	// BoilerplatePhrases are literal, case-insensitive CTA/footer strings
	// (e.g. "Subscribe to our channel.") stripped from article text
	// before feature extraction and dictionary mining — content that
	// HTML stripping alone doesn't catch since it's visible text, not
	// markup.
	BoilerplatePhrases []string `yaml:"boilerplate_phrases"`
	ShingleSize        int      `yaml:"shingle_size"`
	MinTokenLength     int      `yaml:"min_token_length"`
	StopWords          []string `yaml:"stop_words"`
}

// Validate checks ShingleSize/MinTokenLength are positive when set (zero
// means "text normalization not configured yet", tolerated for backward
// compatibility with pre-Phase-3 configs) and every prefix pattern compiles.
func (c TextNormalizationConfig) Validate() error {
	if c.ShingleSize < 0 {
		return fmt.Errorf("shingle_size must not be negative")
	}
	if c.MinTokenLength < 0 {
		return fmt.Errorf("min_token_length must not be negative")
	}
	for _, p := range c.RepeatedPrefixPatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("repeated_prefix_patterns: invalid pattern %q: %w", p, err)
		}
	}
	return nil
}

// Policy converts TextNormalizationConfig into the domain's TextConfig.
func (c TextNormalizationConfig) Policy() normalization.TextConfig {
	return normalization.TextConfig{
		StripEmojis:            c.StripEmojis,
		StripHashtags:          c.StripHashtags,
		StripMentions:          c.StripMentions,
		StripInlineURLs:        c.StripInlineURLs,
		RepeatedPrefixPatterns: c.RepeatedPrefixPatterns,
		BoilerplatePhrases:     c.BoilerplatePhrases,
		ShingleSize:            c.ShingleSize,
		MinTokenLength:         c.MinTokenLength,
		StopWords:              c.StopWords,
	}
}

// NormalizationConfig configures URL and text normalization shared by
// exact and near-duplicate detection.
type NormalizationConfig struct {
	URL  URLNormalizationConfig  `yaml:"url"`
	Text TextNormalizationConfig `yaml:"text"`
}

// Validate runs both nested sections' validation.
func (c NormalizationConfig) Validate() error {
	if err := c.URL.Validate(); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if err := c.Text.Validate(); err != nil {
		return fmt.Errorf("text: %w", err)
	}
	return nil
}

// ClassificationConfig configures team-relevance classification.
type ClassificationConfig struct {
	Enabled bool `yaml:"enabled"`
	// TrustedSourceIDs are source.Source.ID values that always skip
	// scoring and are treated as related (an audited bypass).
	TrustedSourceIDs []string `yaml:"trusted_source_ids"`
	// ContextSourceIDs disambiguate explicit Isotopes/Topes title evidence
	// without granting the unconditional trusted-source bypass.
	ContextSourceIDs []string `yaml:"context_source_ids"`
	// Anchor requires related articles from non-trusted sources to carry
	// evidence that names the team itself. See classification.AnchorPolicy.
	Anchor AnchorConfig `yaml:"anchor"`
}

// AnchorConfig configures classification.AnchorPolicy.
type AnchorConfig struct {
	Enabled bool `yaml:"enabled"`
	// Categories are dictionary category names whose evidence can anchor.
	Categories []string `yaml:"categories"`
	// MultiWordCategories is the subset of Categories whose single-token
	// entries (bare surnames) do not anchor.
	MultiWordCategories []string `yaml:"multi_word_categories"`
	// MinConfidence stops dictionary entries with a lower confidence (and
	// entries whose active_until has passed) from anchoring.
	MinConfidence float64 `yaml:"min_confidence"`
}

// Validate checks an enabled policy names at least one anchor category and
// that every multi-word category is also an anchor category.
func (c AnchorConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Categories) == 0 {
		return fmt.Errorf("categories must not be empty when enabled")
	}
	if c.MinConfidence < 0 || c.MinConfidence > 1 {
		return fmt.Errorf("min_confidence must be within [0,1]")
	}
	known := make(map[string]bool, len(c.Categories))
	for _, name := range c.Categories {
		if name == "" {
			return fmt.Errorf("categories: empty name")
		}
		known[name] = true
	}
	for _, name := range c.MultiWordCategories {
		if !known[name] {
			return fmt.Errorf("multi_word_categories: %q is not listed in categories", name)
		}
	}
	return nil
}

// Policy converts the section into the domain policy.
func (c AnchorConfig) Policy() classification.AnchorPolicy {
	p := classification.AnchorPolicy{
		Enabled:             c.Enabled,
		MinConfidence:       c.MinConfidence,
		Categories:          make(map[string]bool, len(c.Categories)),
		MultiWordCategories: make(map[string]bool, len(c.MultiWordCategories)),
	}
	for _, name := range c.Categories {
		p.Categories[name] = true
	}
	for _, name := range c.MultiWordCategories {
		p.MultiWordCategories[name] = true
	}
	return p
}

// Validate checks source lists have no empty, duplicate, or overlapping IDs.
func (c ClassificationConfig) Validate() error {
	seen := make(map[string]bool, len(c.TrustedSourceIDs))
	for _, id := range c.TrustedSourceIDs {
		if id == "" {
			return fmt.Errorf("trusted_source_ids: empty id")
		}
		if seen[id] {
			return fmt.Errorf("trusted_source_ids: duplicate id %q", id)
		}
		seen[id] = true
	}
	contextSeen := make(map[string]bool, len(c.ContextSourceIDs))
	for _, id := range c.ContextSourceIDs {
		if id == "" {
			return fmt.Errorf("context_source_ids: empty id")
		}
		if contextSeen[id] {
			return fmt.Errorf("context_source_ids: duplicate id %q", id)
		}
		if seen[id] {
			return fmt.Errorf("context_source_ids: id %q is already trusted", id)
		}
		contextSeen[id] = true
	}
	if err := c.Anchor.Validate(); err != nil {
		return fmt.Errorf("anchor: %w", err)
	}
	return nil
}

// NearDuplicateConfig configures near-duplicate and same-event scoring.
type NearDuplicateConfig struct {
	Enabled                     bool               `yaml:"enabled"`
	Method                      string             `yaml:"method"`
	DefaultThreshold            float64            `yaml:"default_threshold"`
	ThresholdBySourceType       map[string]float64 `yaml:"threshold_by_source_type"`
	ShortTextMaxTokens          int                `yaml:"short_text_max_tokens"`
	ShortTextThreshold          float64            `yaml:"short_text_threshold"`
	WindowHours                 int                `yaml:"window_hours"`
	MaxCandidates               int                `yaml:"max_candidates"`
	MinSharedTerms              int                `yaml:"min_shared_terms"`
	FieldsUsed                  []string           `yaml:"fields_used"`
	ContentTypeExceptions       []string           `yaml:"content_type_exceptions"`
	AttributeComboWindowMinutes int                `yaml:"attribute_combo_window_minutes"`
	RulesVersion                string             `yaml:"rules_version"`
}

// Validate is a no-op when Enabled is false (matching the placeholder
// pattern from earlier phases); when true, checks method/threshold/window
// values are sane and fields_used only names fields this phase's candidate
// index actually stores (title — see ADR 0009).
func (c NearDuplicateConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Method != "jaccard" && c.Method != "dice" {
		return fmt.Errorf("method must be 'jaccard' or 'dice', got %q", c.Method)
	}
	if c.DefaultThreshold <= 0 || c.DefaultThreshold > 1 {
		return fmt.Errorf("default_threshold must be within (0,1]")
	}
	if c.ShortTextThreshold <= 0 || c.ShortTextThreshold > 1 {
		return fmt.Errorf("short_text_threshold must be within (0,1]")
	}
	if c.WindowHours <= 0 {
		return fmt.Errorf("window_hours must be positive")
	}
	if c.MaxCandidates <= 0 {
		return fmt.Errorf("max_candidates must be positive")
	}
	if c.MinSharedTerms <= 0 {
		return fmt.Errorf("min_shared_terms must be positive")
	}
	if c.AttributeComboWindowMinutes <= 0 {
		return fmt.Errorf("attribute_combo_window_minutes must be positive")
	}
	for _, f := range c.FieldsUsed {
		if f != "title" {
			return fmt.Errorf("fields_used: unsupported field %q (only 'title' is indexed this phase)", f)
		}
	}
	return nil
}

// DeduplicationConfig configures duplicate detection.
type DeduplicationConfig struct {
	NearDuplicate NearDuplicateConfig `yaml:"near_duplicate"`
	StoryGuard    StoryGuardConfig    `yaml:"story_guard"`
}

// Validate runs the nested near-duplicate section's validation.
func (c DeduplicationConfig) Validate() error {
	if err := c.NearDuplicate.Validate(); err != nil {
		return fmt.Errorf("near_duplicate: %w", err)
	}
	if err := c.StoryGuard.Validate(); err != nil {
		return fmt.Errorf("story_guard: %w", err)
	}
	return nil
}

// LexicalJoinConfig configures the lexical cluster join. It reuses
// storyguard's matching, so its thresholds mean the same as story_guard's.
type LexicalJoinConfig struct {
	Enabled           bool    `yaml:"enabled"`
	WindowHours       int     `yaml:"window_hours"`
	MinSharedTokens   int     `yaml:"min_shared_tokens"`
	OverlapThreshold  float64 `yaml:"overlap_threshold"`
	GenericTokenShare float64 `yaml:"generic_token_share"`
}

// Validate checks an enabled lexical join has usable bounds.
func (c LexicalJoinConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.WindowHours <= 0 {
		return fmt.Errorf("window_hours must be positive")
	}
	if c.MinSharedTokens <= 0 {
		return fmt.Errorf("min_shared_tokens must be positive")
	}
	if c.OverlapThreshold <= 0 || c.OverlapThreshold > 1 {
		return fmt.Errorf("overlap_threshold must be within (0,1]")
	}
	if c.GenericTokenShare < 0 || c.GenericTokenShare >= 1 {
		return fmt.Errorf("generic_token_share must be within [0,1)")
	}
	return nil
}

// Policy converts the section into the domain policy.
func (c LexicalJoinConfig) Policy() storyguard.Policy {
	return storyguard.Policy{
		Enabled:           c.Enabled,
		Window:            time.Duration(c.WindowHours) * time.Hour,
		MinSharedTokens:   c.MinSharedTokens,
		OverlapThreshold:  c.OverlapThreshold,
		GenericTokenShare: c.GenericTokenShare,
	}
}

// StoryGuardConfig configures storyguard.Policy.
type StoryGuardConfig struct {
	Enabled          bool    `yaml:"enabled"`
	WindowHours      int     `yaml:"window_hours"`
	MaxPerStory      int     `yaml:"max_per_story"`
	MinSharedTokens  int     `yaml:"min_shared_tokens"`
	OverlapThreshold float64 `yaml:"overlap_threshold"`
	// GenericTokenShare ignores tokens present in more than this share of
	// recent titles; 0 disables the filter.
	GenericTokenShare float64 `yaml:"generic_token_share"`
	// ExemptTitlePatterns are case-insensitive regular expressions for
	// templated series (press conferences, stat cards) the guard must not cut.
	ExemptTitlePatterns []string `yaml:"exempt_title_patterns"`
}

// Validate checks an enabled guard has usable bounds.
func (c StoryGuardConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.WindowHours <= 0 {
		return fmt.Errorf("window_hours must be positive")
	}
	if c.MaxPerStory <= 0 {
		return fmt.Errorf("max_per_story must be positive")
	}
	if c.MinSharedTokens <= 0 {
		return fmt.Errorf("min_shared_tokens must be positive")
	}
	if c.OverlapThreshold <= 0 || c.OverlapThreshold > 1 {
		return fmt.Errorf("overlap_threshold must be within (0,1]")
	}
	if c.GenericTokenShare < 0 || c.GenericTokenShare >= 1 {
		return fmt.Errorf("generic_token_share must be within [0,1)")
	}
	for _, pat := range c.ExemptTitlePatterns {
		if _, err := regexp.Compile("(?i)" + pat); err != nil {
			return fmt.Errorf("exempt_title_patterns: %q: %w", pat, err)
		}
	}
	return nil
}

// Policy converts the section into the domain policy.
func (c StoryGuardConfig) Policy() storyguard.Policy {
	var exempt []*regexp.Regexp
	for _, pat := range c.ExemptTitlePatterns {
		exempt = append(exempt, regexp.MustCompile("(?i)"+pat))
	}
	return storyguard.Policy{
		ExemptTitles:      exempt,
		Enabled:           c.Enabled,
		Window:            time.Duration(c.WindowHours) * time.Hour,
		MaxPerStory:       c.MaxPerStory,
		MinSharedTokens:   c.MinSharedTokens,
		OverlapThreshold:  c.OverlapThreshold,
		GenericTokenShare: c.GenericTokenShare,
	}
}

// ClusteringConfig configures event clustering.
type ClusteringConfig struct {
	Enabled bool `yaml:"enabled"`
	// EventThreshold is the pre-Phase-6 shingle-similarity threshold.
	// Retained only for backward compatibility with the still-wired-up
	// in.ClusterSettings.EventThreshold decision path (process_cycle.go);
	// new decision code should prefer RulesPath's join/margin/ambiguous
	// thresholds (see clusteringrules.RulesFile.ToClusteringConfig) once a
	// later task wires clusteringrules.Load into the service and rewires
	// process_cycle.go off this field.
	EventThreshold float64 `yaml:"event_threshold"`
	// RulesPath locates the versioned event-clustering rules YAML file
	// (Task 3's configs/event_clustering_rules.yml), loaded via
	// clusteringrules.Load. Required when Enabled is true.
	RulesPath string `yaml:"rules_path"`
	// RulesVersion, if non-empty, pins the expected rules-file
	// meta.version. This package never reads the rules file and never
	// compares RulesVersion against anything — enforcing the pin (after
	// calling clusteringrules.Load) is main.go's job. Empty means "accept
	// whatever version is loaded."
	RulesVersion string `yaml:"rules_version"`
	// GeneratedRulesPath, when set, merges the operator-approved phrases of
	// configs/event_rules_generated.yml into the rules (spec 2026-10-09).
	// Empty (the default) leaves the hand-written rules untouched.
	GeneratedRulesPath string `yaml:"generated_rules_path"`
	// CandidateLimit bounds ClusterRepository.FindCandidatesByEventKeys per
	// lookup. Required when Enabled is true.
	CandidateLimit int `yaml:"candidate_limit"`
	// PublishAmbiguous publishes articles whose join-vs-create decision was
	// ambiguous (as a new cluster) instead of retaining them silently.
	PublishAmbiguous bool `yaml:"publish_ambiguous"`
	// LexicalJoin joins articles that event signals cannot place to the open
	// cluster of a recent article with overlapping distinctive wording.
	LexicalJoin LexicalJoinConfig `yaml:"lexical_join"`
	// EntityCatalog locates the two entity-catalog files merged into
	// RulesFile.Entities via clusteringrules.LoadWithEntityCatalog.
	// Required when Enabled is true.
	EntityCatalog EntityCatalogConfig `yaml:"entity_catalog"`
}

// EntityCatalogConfig locates the two files infra/
// entitycatalog.Load merges into eventsignals.Rules.Entities: a small,
// hand-authored file (teams, competitions) and a large, machine-generated
// file (Isotopes players/coaches/executives — see `phins-news-engine-tools
// entities generate`).
type EntityCatalogConfig struct {
	ManualPath    string `yaml:"manual_path"`
	GeneratedPath string `yaml:"generated_path"`
	// League controls the league-wide player entities of the production
	// extractor (phase 2 of the league gazetteer).
	League LeagueCatalogConfig `yaml:"league"`
}

// LeagueCatalogConfig controls the league-wide player entities of the
// production extractor (spec 2026-10-08, phase 2). Mode "off" (the default) is
// byte-for-byte the previous behaviour; "mentions_only" records league players
// in Entities/EntityMentions without letting them influence clustering. The
// "secondary" and "primary" stages are specified but not built, so they are
// rejected rather than silently downgraded.
type LeagueCatalogConfig struct {
	Mode       string `yaml:"mode"`
	RosterPath string `yaml:"roster_path"`
	RulesPath  string `yaml:"rules_path"`
	MaxAgeDays int    `yaml:"max_age_days"`
}

// Enabled reports whether league entities are active.
func (c LeagueCatalogConfig) Enabled() bool { return c.Mode == "mentions_only" }

// MaxAge is the roster age beyond which league entities are disabled (0 = never).
func (c LeagueCatalogConfig) MaxAge() time.Duration {
	return time.Duration(c.MaxAgeDays) * 24 * time.Hour
}

// Validate checks the block.
func (c LeagueCatalogConfig) Validate() error {
	switch c.Mode {
	case "", "off":
		return nil
	case "mentions_only":
	case "secondary", "primary":
		return fmt.Errorf("league.mode %q is specified but not implemented; use off or mentions_only", c.Mode)
	default:
		return fmt.Errorf("league.mode %q is not one of off, mentions_only", c.Mode)
	}
	if strings.TrimSpace(c.RosterPath) == "" || strings.TrimSpace(c.RulesPath) == "" {
		return fmt.Errorf("league.roster_path and league.rules_path are required when mode is mentions_only")
	}
	if c.MaxAgeDays < 0 {
		return fmt.Errorf("league.max_age_days must not be negative")
	}
	return nil
}

// Validate checks both paths are present and the league block is usable.
func (c EntityCatalogConfig) Validate() error {
	if c.ManualPath == "" {
		return fmt.Errorf("entity_catalog.manual_path is required")
	}
	if c.GeneratedPath == "" {
		return fmt.Errorf("entity_catalog.generated_path is required")
	}
	if err := c.League.Validate(); err != nil {
		return fmt.Errorf("entity_catalog: %w", err)
	}
	return nil
}

// Validate is a no-op when Enabled is false; when true, checks
// EventThreshold is within [0,1] and RulesPath is present. It never reads
// RulesPath or interprets RulesVersion — see their doc comments.
func (c ClusteringConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.EventThreshold < 0 || c.EventThreshold > 1 {
		return fmt.Errorf("event_threshold must be within [0,1]")
	}
	if c.RulesPath == "" {
		return fmt.Errorf("rules_path is required when enabled")
	}
	if c.CandidateLimit <= 0 {
		return fmt.Errorf("candidate_limit must be positive")
	}
	if err := c.LexicalJoin.Validate(); err != nil {
		return fmt.Errorf("lexical_join: %w", err)
	}
	if err := c.EntityCatalog.Validate(); err != nil {
		return fmt.Errorf("entity_catalog: %w", err)
	}
	return nil
}

// EditorialConfig configures the offline-trained editorial-quality
// classifier's runtime behavior.
type EditorialConfig struct {
	Mode                          string   `yaml:"mode"` // disabled | shadow | canary | enforce
	ModelBypassSourceIDs          []string `yaml:"model_bypass_source_ids"`
	UnconditionalPublishSourceIDs []string `yaml:"unconditional_publish_source_ids"`
	// AmbiguousPolicy controls what happens to an article whose editorial
	// score falls strictly between Thresholds.Reject and
	// Thresholds.Accept, in shadow/canary/enforce mode. "reject" (the
	// only safe default) makes ambiguous a terminal rejection, same
	// persistence discipline as a score-reject, reason code
	// "editorial:ambiguous_reject". "accept" preserves the pre-#168
	// fall-through-to-publish behavior explicitly, for an operator who
	// deliberately wants it — never the silent default. No default case:
	// an empty/typo'd value is a hard config error, matching Mode below.
	AmbiguousPolicy string `yaml:"ambiguous_policy"`
	// VetoPath is the path to the deterministic veto-rules YAML file
	// (infra/editorialveto.Load). Optional — empty
	// means no veto layer is loaded, main.go treats that the same as a
	// zero-value editorialveto.Config (never triggers).
	VetoPath string `yaml:"veto_path"`
}

// Validate checks Mode is one of the known values and AmbiguousPolicy is valid
// — no silent defaults, since "disabled" and an unset/typo'd value must not be
// conflated.
func (c EditorialConfig) Validate() error {
	for name, ids := range map[string][]string{"model_bypass_source_ids": c.ModelBypassSourceIDs, "unconditional_publish_source_ids": c.UnconditionalPublishSourceIDs} {
		seenSources := map[string]bool{}
		for _, id := range ids {
			if _, err := source.ParseID(id); err != nil {
				return fmt.Errorf("%s entry %q is invalid: %w", name, id, err)
			}
			if seenSources[id] {
				return fmt.Errorf("%s contains duplicate %q", name, id)
			}
			seenSources[id] = true
		}
	}
	switch c.Mode {
	case "disabled", "shadow", "canary", "enforce":
	default:
		return fmt.Errorf("mode must be one of disabled|shadow|canary|enforce, got %q", c.Mode)
	}
	switch c.AmbiguousPolicy {
	case "reject", "accept":
	default:
		return fmt.Errorf("ambiguous_policy must be one of reject|accept, got %q", c.AmbiguousPolicy)
	}
	return nil
}

// FreshnessConfig holds the maximum age, per content type, before an
// article is considered stale.
type FreshnessConfig struct {
	HardNewsHours int `yaml:"hard_news_hours"`
	SocialHours   int `yaml:"social_hours"`
	AnalysisHours int `yaml:"analysis_hours"`
	DefaultHours  int `yaml:"default_hours"`
	// LiveUpdates cuts the window of social posts that read as live game
	// updates to MaxAgeMinutes.
	LiveUpdates LiveUpdatesConfig `yaml:"live_updates"`
}

// LiveUpdatesConfig configures publishing.LiveUpdatePolicy. Disabled when
// MaxAgeMinutes is zero.
type LiveUpdatesConfig struct {
	MaxAgeMinutes int `yaml:"max_age_minutes"`
	// Patterns are case-insensitive regular expressions matched against the
	// post text.
	Patterns []string `yaml:"patterns"`
	// ExcludePatterns mark finished-game news (final score, recap) that must
	// keep the normal window even if a pattern matches.
	ExcludePatterns []string `yaml:"exclude_patterns"`
}

func (c LiveUpdatesConfig) validate() error {
	if c.MaxAgeMinutes < 0 {
		return fmt.Errorf("live_updates.max_age_minutes must not be negative")
	}
	if c.MaxAgeMinutes > 0 && len(c.Patterns) == 0 {
		return fmt.Errorf("live_updates.patterns must not be empty when max_age_minutes is set")
	}
	for _, pat := range append(append([]string{}, c.Patterns...), c.ExcludePatterns...) {
		if _, err := regexp.Compile("(?i)" + pat); err != nil {
			return fmt.Errorf("live_updates: pattern %q: %w", pat, err)
		}
	}
	return nil
}

func (c LiveUpdatesConfig) policy() publishing.LiveUpdatePolicy {
	p := publishing.LiveUpdatePolicy{MaxAge: time.Duration(c.MaxAgeMinutes) * time.Minute}
	for _, pat := range c.Patterns {
		p.Patterns = append(p.Patterns, regexp.MustCompile("(?i)"+pat))
	}
	for _, pat := range c.ExcludePatterns {
		p.Exclude = append(p.Exclude, regexp.MustCompile("(?i)"+pat))
	}
	return p
}

// Validate checks every threshold is a positive number of hours.
func (c FreshnessConfig) Validate() error {
	if c.HardNewsHours <= 0 || c.SocialHours <= 0 || c.AnalysisHours <= 0 || c.DefaultHours <= 0 {
		return fmt.Errorf("all thresholds must be positive")
	}
	return c.LiveUpdates.validate()
}

// Policy converts FreshnessConfig into the domain's FreshnessPolicy.
func (c FreshnessConfig) Policy() publishing.FreshnessPolicy {
	return publishing.FreshnessPolicy{
		HardNewsHours: c.HardNewsHours,
		SocialHours:   c.SocialHours,
		AnalysisHours: c.AnalysisHours,
		DefaultHours:  c.DefaultHours,
		LiveUpdates:   c.LiveUpdates.policy(),
	}
}

// TelegramConfig configures the publishing channel and (optionally) the
// MTProto user-client credentials `telegram import` uses to read a
// channel's own history — the Bot API cannot do this. BotToken/APIID/
// APIHash/Phone are never read from YAML: always from
// TELEGRAM_BOT_TOKEN/TELEGRAM_API_ID/TELEGRAM_API_HASH/TELEGRAM_PHONE.
// SessionPath may be overridden with TELEGRAM_SESSION_PATH for local tools.
// None of the MTProto fields are required by Validate — the service
// binary loads this same Config to publish via the Bot API and has no
// need for them; `telegram import` validates their presence itself.
type TelegramConfig struct {
	ChannelID      string `yaml:"channel_id"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	BotToken       string `yaml:"-"`
	APIID          int    `yaml:"-"`
	APIHash        string `yaml:"-"`
	Phone          string `yaml:"-"`
	SessionPath    string `yaml:"session_path"`
}

// Validate checks a channel id is present and timeout_seconds is positive.
func (c TelegramConfig) Validate() error {
	if c.ChannelID == "" {
		return fmt.Errorf("channel_id is required")
	}
	if c.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	return nil
}

// Timeout returns the configured Telegram Bot API client timeout.
func (c TelegramConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// PublicationPolicyConfig configures how many articles the outbox may send
// in a given window (burst limiting), how many about the same primary
// entity (same-topic throttling), and an optional quiet-hours window.
type PublicationPolicyConfig struct {
	Burst      BurstPolicyConfig           `yaml:"burst"`
	SameTopic  SameTopicPolicyConfig       `yaml:"same_topic"`
	QuietHours QuietHoursConfig            `yaml:"quiet_hours"`
	Live       LivePublicationPolicyConfig `yaml:"live"`
}

// LivePublicationPolicyConfig is deliberately opt-in. A source and a
// structured milestone must both be listed before an item can use live.
type LivePublicationPolicyConfig struct {
	Enabled         bool                  `yaml:"enabled"`
	TTLMinutes      int                   `yaml:"ttl_minutes"`
	SourceAllowlist []string              `yaml:"source_allowlist"`
	Milestones      []string              `yaml:"milestones"`
	Burst           BurstPolicyConfig     `yaml:"burst"`
	SameTopic       SameTopicPolicyConfig `yaml:"same_topic"`
	QuietHours      QuietHoursConfig      `yaml:"quiet_hours"`
}

// BurstPolicyConfig caps how many articles may publish within a rolling
// window.
type BurstPolicyConfig struct {
	MaxPerWindow  int `yaml:"max_per_window"`
	WindowMinutes int `yaml:"window_minutes"`
}

// Validate checks BurstPolicyConfig has the minimum data required. A
// MaxPerWindow of 0 disables the burst policy.
func (c BurstPolicyConfig) Validate() error {
	if c.MaxPerWindow < 0 {
		return fmt.Errorf("max_per_window must be non-negative")
	}
	if c.MaxPerWindow > 0 && c.WindowMinutes <= 0 {
		return fmt.Errorf("window_minutes must be positive")
	}
	return nil
}

// SameTopicPolicyConfig caps how many articles about the same primary
// entity may publish within a rolling window.
type SameTopicPolicyConfig struct {
	MaxPerWindow  int `yaml:"max_per_window"`
	WindowMinutes int `yaml:"window_minutes"`
	// ExemptEntities are primary entities (case-insensitive) the limit
	// never applies to — see publishing.PolicyConfig.SameTopicExemptEntities.
	ExemptEntities []string `yaml:"exempt_entities"`
}

// Validate checks SameTopicPolicyConfig has the minimum data required. A
// MaxPerWindow of 0 disables the same-topic policy.
func (c SameTopicPolicyConfig) Validate() error {
	if c.MaxPerWindow < 0 {
		return fmt.Errorf("max_per_window must be non-negative")
	}
	if c.MaxPerWindow > 0 && c.WindowMinutes <= 0 {
		return fmt.Errorf("window_minutes must be positive")
	}
	for _, entity := range c.ExemptEntities {
		if strings.TrimSpace(entity) == "" {
			return fmt.Errorf("exempt_entities must not contain blank entries")
		}
	}
	return nil
}

// QuietHoursConfig defines an hour-of-day (UTC) window during which
// publication is deferred. Off by default (Enabled: false), so an
// unconfigured deployment's publishing behavior is unchanged.
type QuietHoursConfig struct {
	Enabled   bool `yaml:"enabled"`
	StartHour int  `yaml:"start_hour"`
	EndHour   int  `yaml:"end_hour"`
}

// Validate is a no-op when Enabled is false; when true, checks both hours
// are within [0,23].
func (c QuietHoursConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.StartHour < 0 || c.StartHour > 23 {
		return fmt.Errorf("start_hour must be within [0,23]")
	}
	if c.EndHour < 0 || c.EndHour > 23 {
		return fmt.Errorf("end_hour must be within [0,23]")
	}
	return nil
}

// Validate runs every nested section's validation.
func (c PublicationPolicyConfig) Validate() error {
	if err := c.Burst.Validate(); err != nil {
		return fmt.Errorf("burst: %w", err)
	}
	if err := c.SameTopic.Validate(); err != nil {
		return fmt.Errorf("same_topic: %w", err)
	}
	if err := c.QuietHours.Validate(); err != nil {
		return fmt.Errorf("quiet_hours: %w", err)
	}
	if err := c.Live.Validate(); err != nil {
		return fmt.Errorf("live: %w", err)
	}
	return nil
}

func (c LivePublicationPolicyConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.TTLMinutes <= 0 {
		return fmt.Errorf("ttl_minutes must be positive when enabled")
	}
	if len(c.SourceAllowlist) == 0 {
		return fmt.Errorf("source_allowlist must not be empty when enabled")
	}
	for _, raw := range c.SourceAllowlist {
		if _, err := source.ParseID(raw); err != nil {
			return fmt.Errorf("source_allowlist entry %q is not a valid source id: %w", raw, err)
		}
	}
	if len(c.Milestones) == 0 {
		return fmt.Errorf("milestones must not be empty when enabled")
	}
	for _, milestone := range c.Milestones {
		m := strings.ToLower(strings.TrimSpace(milestone))
		switch m {
		case "kickoff", "q1", "q2", "halftime", "q3", "q4", "touchdown", "final":
		default:
			// "touchdown_<team>" names a team-specific touchdown tag set by
			// the source (e.g. touchdown_springfield).
			if !teamTouchdownMilestone.MatchString(m) {
				return fmt.Errorf("milestones contains unsupported value %q", milestone)
			}
		}
	}
	if err := c.Burst.Validate(); err != nil {
		return fmt.Errorf("burst: %w", err)
	}
	if err := c.SameTopic.Validate(); err != nil {
		return fmt.Errorf("same_topic: %w", err)
	}
	if err := c.QuietHours.Validate(); err != nil {
		return fmt.Errorf("quiet_hours: %w", err)
	}
	return nil
}

// Policy converts PublicationPolicyConfig into the domain's PolicyConfig.
func (c PublicationPolicyConfig) Policy() publishing.PolicyConfig {
	policy := publishing.PolicyConfig{
		BurstMaxPerWindow:       c.Burst.MaxPerWindow,
		BurstWindow:             time.Duration(c.Burst.WindowMinutes) * time.Minute,
		SameTopicMaxPerWindow:   c.SameTopic.MaxPerWindow,
		SameTopicWindow:         time.Duration(c.SameTopic.WindowMinutes) * time.Minute,
		SameTopicExemptEntities: c.SameTopic.ExemptEntities,
		QuietHours: publishing.QuietHoursConfig{
			Enabled:   c.QuietHours.Enabled,
			StartHour: c.QuietHours.StartHour,
			EndHour:   c.QuietHours.EndHour,
		},
	}
	if c.Live.Enabled {
		live := publishing.PolicyConfig{BurstMaxPerWindow: c.Live.Burst.MaxPerWindow, BurstWindow: time.Duration(c.Live.Burst.WindowMinutes) * time.Minute, SameTopicMaxPerWindow: c.Live.SameTopic.MaxPerWindow, SameTopicWindow: time.Duration(c.Live.SameTopic.WindowMinutes) * time.Minute, SameTopicExemptEntities: c.Live.SameTopic.ExemptEntities, QuietHours: publishing.QuietHoursConfig{Enabled: c.Live.QuietHours.Enabled, StartHour: c.Live.QuietHours.StartHour, EndHour: c.Live.QuietHours.EndHour}}
		policy.Live = &live
	}
	return policy
}

func (c PublicationPolicyConfig) LivePolicy() publishing.LivePolicy {
	p := publishing.LivePolicy{Enabled: c.Live.Enabled, SourceAllowlist: map[string]bool{}, Milestones: map[string]bool{}}
	for _, sourceID := range c.Live.SourceAllowlist {
		p.SourceAllowlist[strings.TrimSpace(sourceID)] = true
	}
	for _, milestone := range c.Live.Milestones {
		p.Milestones[strings.ToLower(strings.TrimSpace(milestone))] = true
	}
	return p
}

// PublicationConfig configures the outbox publisher: retry budget, backoff,
// batch size, and crash-recovery threshold.
type PublicationConfig struct {
	MaxAttempts            int                  `yaml:"max_attempts"`
	BaseBackoffSeconds     int                  `yaml:"base_backoff_seconds"`
	MaxBackoffSeconds      int                  `yaml:"max_backoff_seconds"`
	ClaimBatchSize         int                  `yaml:"claim_batch_size"`
	StaleClaimAfterMinutes int                  `yaml:"stale_claim_after_minutes"`
	Reconciliation         ReconciliationConfig `yaml:"reconciliation"`
	Backlog                BacklogConfig        `yaml:"backlog"`
}

// BacklogConfig holds the warning/critical thresholds used by
// `telegram outbox backlog`. Every limit is a strict "greater than"; a zero
// value selects the documented default (publishing.DefaultBacklogThresholds).
type BacklogConfig struct {
	PendingWarning        int `yaml:"pending_warning"`
	PendingCritical       int `yaml:"pending_critical"`
	AgeWarningMinutes     int `yaml:"age_warning_minutes"`
	AgeCriticalMinutes    int `yaml:"age_critical_minutes"`
	LagP95WarningMinutes  int `yaml:"lag_p95_warning_minutes"`
	LagP95CriticalMinutes int `yaml:"lag_p95_critical_minutes"`
}

// Validate rejects negative limits and a warning level above its critical level.
func (c BacklogConfig) Validate() error {
	for _, p := range []struct {
		name       string
		warn, crit int
	}{
		{"pending", c.PendingWarning, c.PendingCritical},
		{"age_minutes", c.AgeWarningMinutes, c.AgeCriticalMinutes},
		{"lag_p95_minutes", c.LagP95WarningMinutes, c.LagP95CriticalMinutes},
	} {
		if p.warn < 0 || p.crit < 0 {
			return fmt.Errorf("%s thresholds must not be negative", p.name)
		}
	}
	t := c.Thresholds()
	if t.PendingWarning > t.PendingCritical {
		return fmt.Errorf("pending_warning must be <= pending_critical")
	}
	if t.AgeWarning > t.AgeCritical {
		return fmt.Errorf("age_warning_minutes must be <= age_critical_minutes")
	}
	if t.LagP95Warning > t.LagP95Critical {
		return fmt.Errorf("lag_p95_warning_minutes must be <= lag_p95_critical_minutes")
	}
	return nil
}

// Thresholds converts BacklogConfig into the domain thresholds, filling unset
// (zero) values from the defaults.
func (c BacklogConfig) Thresholds() publishing.BacklogThresholds {
	t := publishing.DefaultBacklogThresholds()
	if c.PendingWarning > 0 {
		t.PendingWarning = c.PendingWarning
	}
	if c.PendingCritical > 0 {
		t.PendingCritical = c.PendingCritical
	}
	if c.AgeWarningMinutes > 0 {
		t.AgeWarning = time.Duration(c.AgeWarningMinutes) * time.Minute
	}
	if c.AgeCriticalMinutes > 0 {
		t.AgeCritical = time.Duration(c.AgeCriticalMinutes) * time.Minute
	}
	if c.LagP95WarningMinutes > 0 {
		t.LagP95Warning = time.Duration(c.LagP95WarningMinutes) * time.Minute
	}
	if c.LagP95CriticalMinutes > 0 {
		t.LagP95Critical = time.Duration(c.LagP95CriticalMinutes) * time.Minute
	}
	return t
}

// Validate checks PublicationConfig has the minimum data required.
func (c PublicationConfig) Validate() error {
	if c.MaxAttempts <= 0 {
		return fmt.Errorf("max_attempts must be positive")
	}
	if c.BaseBackoffSeconds <= 0 {
		return fmt.Errorf("base_backoff_seconds must be positive")
	}
	if c.MaxBackoffSeconds < c.BaseBackoffSeconds {
		return fmt.Errorf("max_backoff_seconds must be >= base_backoff_seconds")
	}
	if c.ClaimBatchSize <= 0 {
		return fmt.Errorf("claim_batch_size must be positive")
	}
	if c.StaleClaimAfterMinutes <= 0 {
		return fmt.Errorf("stale_claim_after_minutes must be positive")
	}
	if err := c.Reconciliation.Validate(); err != nil {
		return fmt.Errorf("reconciliation: %w", err)
	}
	if err := c.Backlog.Validate(); err != nil {
		return fmt.Errorf("backlog: %w", err)
	}
	return nil
}

// Backoff converts PublicationConfig into the domain's BackoffPolicy.
func (c PublicationConfig) Backoff() publishing.BackoffPolicy {
	return publishing.BackoffPolicy{
		BaseDelay:   time.Duration(c.BaseBackoffSeconds) * time.Second,
		MaxDelay:    time.Duration(c.MaxBackoffSeconds) * time.Second,
		MaxAttempts: c.MaxAttempts,
	}
}

// StaleClaimAfter returns how long an outbox item can stay Claimed before
// PublishOutbox reclaims it as crash-recovered.
func (c PublicationConfig) StaleClaimAfter() time.Duration {
	return time.Duration(c.StaleClaimAfterMinutes) * time.Minute
}

// ReconciliationConfig configures how PublishOutbox.Reconcile resolves
// sent_unconfirmed items whose outcome a locally-recorded message_id
// can't already answer — see ADR 0030. Disabled (Enabled: false) means
// every such item quarantines immediately instead of attempting a
// channel-history lookup; this is also what happens automatically,
// regardless of this flag, when no reusable Telegram MTProto session
// exists yet (see engine.Run's wiring) — Enabled exists
// as an explicit operator override to skip the attempt entirely (e.g. to
// avoid MTProto rate limits during a known bulk-crash-recovery event).
type ReconciliationConfig struct {
	Enabled             bool `yaml:"enabled"`
	ChannelHistoryLimit int  `yaml:"channel_history_limit"`
	// ClockSkewSeconds is how far a channel message's PostedAt may pre-date
	// the locally-recorded sent_at and still count as a match. It absorbs
	// clock drift between this host and Telegram's servers, so it is only
	// meaningful at a scale real drift occurs on: anything below
	// minReconciliationClockSkewSeconds is rejected rather than silently
	// accepted. A too-small tolerance makes MatchInHistory reject a genuine
	// match as pre-dating the send, which turns it from "match" into a
	// *confident non-match* — reconcile-negative, retry, duplicate channel
	// post. That is the exact failure this whole feature exists to prevent,
	// so it must not be reachable by an innocuous-looking config typo.
	ClockSkewSeconds int `yaml:"clock_skew_seconds"`
	BatchSize        int `yaml:"batch_size"`
}

// minReconciliationClockSkewSeconds is the floor Validate enforces on
// clock_skew_seconds: half the shipped 120s default, leaving room to tune
// down without opening the false-negative window described on the field.
const minReconciliationClockSkewSeconds = 60

// Validate is a no-op when Enabled is false.
func (c ReconciliationConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.ChannelHistoryLimit <= 0 {
		return fmt.Errorf("channel_history_limit must be positive")
	}
	if c.ClockSkewSeconds < minReconciliationClockSkewSeconds {
		return fmt.Errorf("clock_skew_seconds must be at least %d (a smaller tolerance cannot absorb real host/Telegram clock drift, and turns a genuine channel-history match into a confident non-match — which retries and duplicate-posts)", minReconciliationClockSkewSeconds)
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("batch_size must be positive")
	}
	return nil
}

// defaultReconciliationBatchSize, defaultReconciliationClockSkewSeconds,
// and defaultReconciliationHistoryLimit are used by Settings whenever the
// corresponding field is left at its zero value — including,
// deliberately, when Enabled is false (Validate only requires these to be
// positive when Enabled is true, so an operator who disabled
// reconciliation may legally leave all three unset). Enabled only gates
// whether a channel-history importer is built; it must never also leave
// Settings producing garbage tuning values, for two independent reasons:
//   - BatchSize: a 0 becomes LIMIT 0 in ListSentUnconfirmed — Reconcile
//     would list (and quarantine) nothing, ever, contradicting this
//     type's own "every such item quarantines immediately" doc comment.
//   - ClockSkewSeconds: a 0 makes MatchInHistory's cutoff exactly equal
//     to the locally-recorded sentAt — since a genuinely delivered
//     message's real PostedAt is always at or before sentAt (it's
//     stamped after the send), the cutoff would reject the true match,
//     flip a would-be quarantine into "not found," and get the item
//     re-published as a duplicate. (Caught in round-2 review: even
//     though buildReconcileOnlyUseCase now also refuses to build an
//     importer at all when Enabled is false — see its doc comment — this
//     clamp stays as defense in depth for any future Settings caller
//     that doesn't have that same gate.)
//   - ChannelHistoryLimit: a 0 would fetch zero messages per history
//     lookup, so the "did the search window even reach back far enough"
//     coverage check could never succeed either.
//
// Values match configs/settings.yml's documented defaults.
const (
	defaultReconciliationBatchSize           = 20
	defaultReconciliationClockSkewSeconds    = 120
	defaultReconciliationChannelHistoryLimit = 200
)

// Settings converts ReconciliationConfig into the PublishOutbox use
// case's ReconciliationConfig value bag. channelRef is threaded in
// separately (defaults to telegram.channel_id, same convention as the
// existing `telegram import` command); urlCfg/resolvers are the same
// normalization policy `telegram import` already uses
// (cfg.Normalization.URL.Policy()) — reconciliation must canonicalize a
// channel message's URL exactly the same way an article's own
// CanonicalURL was produced, or a genuine match would be missed.
func (c ReconciliationConfig) Settings(channelRef string, urlCfg normalization.URLConfig, resolvers []normalization.KnownLinkResolver) usecase.ReconciliationConfig {
	batchSize := c.BatchSize
	if batchSize <= 0 {
		batchSize = defaultReconciliationBatchSize
	}
	clockSkewSeconds := c.ClockSkewSeconds
	if clockSkewSeconds <= 0 {
		clockSkewSeconds = defaultReconciliationClockSkewSeconds
	}
	historyLimit := c.ChannelHistoryLimit
	if historyLimit <= 0 {
		historyLimit = defaultReconciliationChannelHistoryLimit
	}
	return usecase.ReconciliationConfig{
		BatchSize:    batchSize,
		ChannelRef:   channelRef,
		HistoryLimit: historyLimit,
		ClockSkew:    time.Duration(clockSkewSeconds) * time.Second,
		URLConfig:    urlCfg,
		URLResolvers: resolvers,
	}
}

// RetentionConfig configures, per data category, how long processed data is
// kept before RunRetention deletes it. Published articles, human labels,
// and promoted model/dictionary registry rows are never deleted regardless
// of these windows.
type RetentionConfig struct {
	RejectedArticlesDays    int `yaml:"rejected_articles_days"`
	DuplicatesDays          int `yaml:"duplicates_days"`
	AuditEventsDays         int `yaml:"audit_events_days"`
	PublicationAttemptsDays int `yaml:"publication_attempts_days"`
	// ClustersDays is how long a *closed* cluster is retained, measured
	// from its closed_at timestamp (not last_updated_at) — see
	// ClusterLifecycleConfig (timing invariant).
	ClustersDays int `yaml:"clusters_days"`
}

// Validate checks every retention window is a positive number of days.
func (c RetentionConfig) Validate() error {
	if c.RejectedArticlesDays <= 0 || c.DuplicatesDays <= 0 || c.AuditEventsDays <= 0 ||
		c.PublicationAttemptsDays <= 0 || c.ClustersDays <= 0 {
		return fmt.Errorf("all retention windows must be positive")
	}
	return nil
}

// Windows converts RetentionConfig into the RunRetention use case's window
// value bag.
func (c RetentionConfig) Windows() in.RetentionWindows {
	day := 24 * time.Hour
	return in.RetentionWindows{
		RejectedArticles:    time.Duration(c.RejectedArticlesDays) * day,
		Duplicates:          time.Duration(c.DuplicatesDays) * day,
		AuditEvents:         time.Duration(c.AuditEventsDays) * day,
		PublicationAttempts: time.Duration(c.PublicationAttemptsDays) * day,
		Clusters:            time.Duration(c.ClustersDays) * day,
	}
}

// ClusterLifecycleConfig configures the maintenance job that closes
// inactive event_clusters rows.
// CloseAfterHours + Retention.ClustersDays*24 must stay >= the widest
// entry in configs/event_clustering_rules.yml's temporal_windows (240h,
// draft, as of this writing) — see the design doc's "Timing invariant."
type ClusterLifecycleConfig struct {
	CloseAfterHours   int    `yaml:"close_after_hours"`
	BatchSize         int    `yaml:"batch_size"`
	MaxBatchesPerRun  int    `yaml:"max_batches_per_run"`
	TimeBudgetSeconds int    `yaml:"time_budget_seconds"`
	CloseReason       string `yaml:"close_reason"`
}

// Validate checks every numeric field is positive and CloseReason is set.
func (c ClusterLifecycleConfig) Validate() error {
	if c.CloseAfterHours <= 0 {
		return fmt.Errorf("close_after_hours must be positive")
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("batch_size must be positive")
	}
	if c.MaxBatchesPerRun <= 0 {
		return fmt.Errorf("max_batches_per_run must be positive")
	}
	if c.TimeBudgetSeconds <= 0 {
		return fmt.Errorf("time_budget_seconds must be positive")
	}
	if c.CloseReason == "" {
		return fmt.Errorf("close_reason must not be empty")
	}
	return nil
}

// Settings converts ClusterLifecycleConfig into the CloseInactiveClusters
// use case's config value bag.
func (c ClusterLifecycleConfig) Settings() in.CloseInactiveClustersConfig {
	return in.CloseInactiveClustersConfig{
		CloseAfter:       time.Duration(c.CloseAfterHours) * time.Hour,
		BatchSize:        c.BatchSize,
		MaxBatchesPerRun: c.MaxBatchesPerRun,
		TimeBudget:       time.Duration(c.TimeBudgetSeconds) * time.Second,
		CloseReason:      c.CloseReason,
	}
}

// SeenItemsConfig configures the stable-ingestion-identity claim registry.
type SeenItemsConfig struct {
	// ReclaimAfterMinutes bounds how long an unfinalized seen_items claim
	// is honored as in-flight before a later poll is allowed to reclaim it
	// (crash recovery). Defaults to 10 minutes when unset (<=0) — see
	// ReclaimAfter — so this section is optional and existing settings.yml
	// files without it keep working unchanged.
	ReclaimAfterMinutes int `yaml:"reclaim_after_minutes"`
}

// Validate is a no-op: ReclaimAfterMinutes has a safe default (see
// ReclaimAfter) and no structural constraint of its own.
func (c SeenItemsConfig) Validate() error { return nil }

// ReclaimAfter returns the configured reclaim window, defaulting to 10
// minutes when unset.
func (c SeenItemsConfig) ReclaimAfter() time.Duration {
	if c.ReclaimAfterMinutes <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(c.ReclaimAfterMinutes) * time.Minute
}

// ModelConfig locates the offline-trained, git-versioned editorial
// classifier artifact directory (models/editorial/current/ by
// convention — see infrastructure/editorialmodel). Path is required
// whenever editorial.mode is not "disabled" (checked in Config.Validate,
// not here — this method has no access to EditorialConfig).
type ModelConfig struct {
	Path    string `yaml:"path"`
	Version string `yaml:"version"`
}

// Validate is a no-op: ModelConfig has no structural constraint of its
// own. Whether Path is required depends on editorial.mode — see
// Config.Validate.
func (c ModelConfig) Validate() error { return nil }

// DictionaryConfig locates the team keyword dictionary file used by
// classification when enabled. If Version is set, it must match the
// loaded dictionary's meta.version — main.go checks this after loading
// (Validate only checks structure here; it never reads the file).
type DictionaryConfig struct {
	Path    string `yaml:"path"`
	Version string `yaml:"version"`
}

// Validate is a no-op: DictionaryConfig has no structural constraint of
// its own. Whether Path is required depends on classification.enabled,
// checked cross-section in Config.Validate.
func (c DictionaryConfig) Validate() error { return nil }

// DictionaryMiningConfig tunes the offline mining of candidate dictionary
// terms from completed editorial review sessions.
type DictionaryMiningConfig struct {
	Path string `yaml:"path"`
	// EnglishWordsPath is a newline-separated list of English words, such as
	// /usr/share/dict/words. With it, mining also proposes lower-case coined
	// words (nicknames, slang) while skipping every ordinary English word.
	// Optional: when empty or unreadable, only proper nouns are mined.
	EnglishWordsPath string `yaml:"english_words_path"`
}

// Validate is permissive for backward-compatible config loading. The
// dictionary mining command itself requires Path when it runs.
func (c DictionaryMiningConfig) Validate() error { return nil }

// EditorialCycleConfig configures `editorial cycle`'s active-learning
// candidate selection. See application/activelearning.
type EditorialCycleConfig struct {
	// SampleSize caps how many ranked candidates one review pass surfaces.
	SampleSize int `yaml:"sample_size"`
	// PoolLimit bounds how many raw candidates are pulled from storage
	// before scoring (keeps scoring bounded regardless of table size).
	PoolLimit int `yaml:"pool_limit"`
	// AmbiguousCooldownDays excludes an article from re-selection for
	// this many days after being marked "ambiguous".
	AmbiguousCooldownDays int `yaml:"ambiguous_cooldown_days"`
}

// Validate checks every value is positive.
func (c EditorialCycleConfig) Validate() error {
	if c.SampleSize <= 0 {
		return fmt.Errorf("sample_size must be positive")
	}
	if c.PoolLimit <= 0 {
		return fmt.Errorf("pool_limit must be positive")
	}
	if c.AmbiguousCooldownDays <= 0 {
		return fmt.Errorf("ambiguous_cooldown_days must be positive")
	}
	return nil
}

// AmbiguousCooldown converts AmbiguousCooldownDays into a time.Duration.
func (c EditorialCycleConfig) AmbiguousCooldown() time.Duration {
	return time.Duration(c.AmbiguousCooldownDays) * 24 * time.Hour
}

// EditorialGatesConfig configures the quality bar a trained candidate
// must clear before promote-model will promote it without --force. See
// application/editorialtrain.GateConfig — this type intentionally has no
// conversion method to it: infrastructure/config is imported by the
// service binary (the service binary), and application/editorialtrain
// must stay imported only by the tools CLI. The
// cmd layer converts these plain fields into editorialtrain.GateConfig
// itself.
type EditorialGatesConfig struct {
	MinPrecision                  float64  `yaml:"min_precision"`
	MaxRecallRegression           float64  `yaml:"max_recall_regression"`
	MinRegressionBaselineExamples int      `yaml:"min_regression_baseline_examples"`
	MinPRAUC                      float64  `yaml:"min_prauc"`
	MaxFalsePositiveRate          float64  `yaml:"max_false_positive_rate"`
	MinPerSourcePrecision         float64  `yaml:"min_per_source_precision"`
	MinSourceExamples             int      `yaml:"min_source_examples"`
	ExcludedSources               []string `yaml:"excluded_sources"`
	MaxArtifactSizeBytes          int64    `yaml:"max_artifact_size_bytes"`
	MaxInferenceNanos             int64    `yaml:"max_inference_nanos"`
}

// Validate checks every threshold is within a sensible range.
func (c EditorialGatesConfig) Validate() error {
	if c.MinPrecision < 0 || c.MinPrecision > 1 {
		return fmt.Errorf("min_precision must be within [0,1]")
	}
	if c.MinPRAUC < 0 || c.MinPRAUC > 1 {
		return fmt.Errorf("min_prauc must be within [0,1]")
	}
	if c.MinPerSourcePrecision < 0 || c.MinPerSourcePrecision > 1 {
		return fmt.Errorf("min_per_source_precision must be within [0,1]")
	}
	if c.MaxRecallRegression < 0 {
		return fmt.Errorf("max_recall_regression must not be negative")
	}
	if c.MinRegressionBaselineExamples < 0 {
		return fmt.Errorf("min_regression_baseline_examples must not be negative")
	}
	if c.MaxFalsePositiveRate < 0 || c.MaxFalsePositiveRate > 1 {
		return fmt.Errorf("max_false_positive_rate must be within [0,1]")
	}
	if c.MinSourceExamples < 0 {
		return fmt.Errorf("min_source_examples must not be negative")
	}
	if c.MaxArtifactSizeBytes <= 0 {
		return fmt.Errorf("max_artifact_size_bytes must be positive")
	}
	if c.MaxInferenceNanos <= 0 {
		return fmt.Errorf("max_inference_nanos must be positive")
	}
	return nil
}

// EditorialCostsConfig assigns explicit costs to the editorial
// classifier's two error types — see application/editorialtrain.CostConfig.
// This type intentionally has no conversion method to it, for the same
// import-boundary reason EditorialGatesConfig's doc comment gives: the
// cmd layer converts these plain fields itself.
type EditorialCostsConfig struct {
	FalsePublishCost float64 `yaml:"false_publish_cost"`
	MissedNewsCost   float64 `yaml:"missed_news_cost"`
}

// Validate checks both costs are positive — a zero or negative cost
// would make that error type free, defeating the point of an explicit
// cost model.
func (c EditorialCostsConfig) Validate() error {
	if c.FalsePublishCost <= 0 {
		return fmt.Errorf("false_publish_cost must be positive")
	}
	if c.MissedNewsCost <= 0 {
		return fmt.Errorf("missed_news_cost must be positive")
	}
	return nil
}

// EditorialRolloutConfig documents the shadow -> canary -> enforce
// rollout policy as finite, measurable numbers. Consumed as of issue
// #168: CanaryPercent drives the enforced/shadow split in
// usecase.CanaryEnforced (via DedupCascade), and
// CanaryMinDays/CanaryMinArticles are the gates `editorial canary
// status`/`promote` evaluate. CanaryMinPrecision and
// CanaryMaxFalsePositiveRate remain policy-only: computing them needs a
// labeled ground truth article_processing_events doesn't carry, so those
// two criteria are reported as not evaluated rather than as zero. See
// the design doc §6.
type EditorialRolloutConfig struct {
	ShadowMinDays              int     `yaml:"shadow_min_days"`
	ShadowMinArticles          int     `yaml:"shadow_min_articles"`
	CanaryPercent              int     `yaml:"canary_percent"`
	CanaryMinDays              int     `yaml:"canary_min_days"`
	CanaryMinArticles          int     `yaml:"canary_min_articles"`
	CanaryMaxFalsePositiveRate float64 `yaml:"canary_max_false_positive_rate"`
	CanaryMinPrecision         float64 `yaml:"canary_min_precision"`
}

// Validate checks every day/article minimum is positive, canary_percent
// is within [1,99], and canary_min_precision is at least
// editorial_gates.min_precision (the canary exit bar can never be looser
// than the promotion gate itself) — gateMinPrecision is passed in by
// Config.Validate rather than looked up here, since EditorialRolloutConfig
// has no access to its sibling EditorialGatesConfig on its own.
func (c EditorialRolloutConfig) Validate(gateMinPrecision float64) error {
	if c.ShadowMinDays <= 0 {
		return fmt.Errorf("shadow_min_days must be positive")
	}
	if c.ShadowMinArticles <= 0 {
		return fmt.Errorf("shadow_min_articles must be positive")
	}
	if c.CanaryPercent < 1 || c.CanaryPercent > 99 {
		return fmt.Errorf("canary_percent must be within [1,99]")
	}
	if c.CanaryMinDays <= 0 {
		return fmt.Errorf("canary_min_days must be positive")
	}
	if c.CanaryMinArticles <= 0 {
		return fmt.Errorf("canary_min_articles must be positive")
	}
	if c.CanaryMaxFalsePositiveRate < 0 || c.CanaryMaxFalsePositiveRate > 1 {
		return fmt.Errorf("canary_max_false_positive_rate must be within [0,1]")
	}
	if c.CanaryMinPrecision < gateMinPrecision {
		return fmt.Errorf("canary_min_precision (%.3f) must be >= editorial_gates.min_precision (%.3f)", c.CanaryMinPrecision, gateMinPrecision)
	}
	return nil
}

// RemoteConfig configures the non-secret defaults `editorial cycle`'s
// sync step and the standalone `deploy` command use. Host and SSH key
// path are deliberately NOT here — they're per-invocation/per-operator
// CLI flags (--remote, --key-path), matching how `telegram import`'s
// --channel flag already works rather than living in config.
type RemoteConfig struct {
	SQLiteRemoteDBHint string `yaml:"sqlite_remote_db_hint"`
	RetentionDays      int    `yaml:"retention_days"`
}

// Validate checks RetentionDays is positive. SQLiteRemoteDBHint may be
// empty — the sync script falls back to its own hardcoded standard paths
// when the hint doesn't exist (see the remote sync script).
func (c RemoteConfig) Validate() error {
	if c.RetentionDays <= 0 {
		return fmt.Errorf("retention_days must be positive")
	}
	return nil
}

// DictionarySourceConfig configures a single scraped page the dictionary
// auto-update fetches: where to fetch it from, which Scraping Service mode
// to request, and (optionally) which page sections/CSS selectors the
// parser should look at.
type DictionarySourceConfig struct {
	URL              string            `yaml:"url"`
	Mode             string            `yaml:"mode"`
	RelevantSections []string          `yaml:"relevant_sections,omitempty"`
	Selectors        map[string]string `yaml:"selectors,omitempty"`
}

// DeactivationPolicyConfig tunes when an entity that stops appearing in a
// dictionary source is deactivated: it must be absent for at least
// MinimumSuccessfulAbsences consecutive successful fetches, spanning at
// least MinimumElapsedDays.
type DeactivationPolicyConfig struct {
	MinimumSuccessfulAbsences int `yaml:"minimum_successful_absences"`
	MinimumElapsedDays        int `yaml:"minimum_elapsed_days"`
}

// PlausibilityConfig bounds how many entities a source's snapshot may
// contain, and how many of the previously known entities a snapshot may be
// missing, before it is rejected as implausible rather than applied.
type PlausibilityConfig struct {
	MinimumEntities     int     `yaml:"minimum_entities"`
	MaximumEntities     int     `yaml:"maximum_entities"`
	MaximumMissingRatio float64 `yaml:"maximum_missing_ratio,omitempty"`
}

// DictionaryUpdateConfig configures the automatic team-dictionary
// update feature: which pages to scrape, an allowlist of domains those
// pages' URLs must resolve to (defense in depth against a misconfigured or
// compromised source URL), the entity-deactivation policy, and per-source
// plausibility bounds. All fields besides Enabled are only validated when
// Enabled is true, matching the placeholder pattern earlier phases use for
// not-yet-active sections (see ScrapingServiceConfig).
type DictionaryUpdateConfig struct {
	Enabled bool `yaml:"enabled"`
	// Transport selects how dictionary fetch/update retrieve source pages:
	// "direct" (default, see Transport()) makes a plain HTTP GET with a
	// real browser User-Agent — no external dependency, but cannot render
	// JavaScript, so any source with mode: browser fails. "scraping_service"
	// routes through the Scraping Service like the rest of this bot's
	// rendered-HTML fetching (ADR 0011), requiring
	// scraping_service.enabled/base_url and SCRAPING_SERVICE_API_KEY. This
	// offline tooling is allowed a direct-HTTP path that the production
	// pipeline does not use.
	Transport          string                            `yaml:"transport,omitempty"`
	AllowedDomains     []string                          `yaml:"allowed_domains,omitempty"`
	Sources            map[string]DictionarySourceConfig `yaml:"sources,omitempty"`
	DeactivationPolicy DeactivationPolicyConfig          `yaml:"deactivation_policy"`
	Plausibility       map[string]PlausibilityConfig     `yaml:"plausibility,omitempty"`
}

// DictionaryUpdateTransportDirect and DictionaryUpdateTransportScrapingService
// are the two valid values for DictionaryUpdateConfig.Transport. An empty
// Transport (the zero value, e.g. configs that predate this field) means
// DictionaryUpdateTransportDirect — see EffectiveTransport.
const (
	DictionaryUpdateTransportDirect          = "direct"
	DictionaryUpdateTransportScrapingService = "scraping_service"
)

// EffectiveTransport returns c.Transport, defaulting to
// DictionaryUpdateTransportDirect when unset.
func (c DictionaryUpdateConfig) EffectiveTransport() string {
	if c.Transport == "" {
		return DictionaryUpdateTransportDirect
	}
	return c.Transport
}

// Validate is a no-op when Enabled is false; when true, checks Transport is
// a known value, allowed_domains is non-empty, every source URL's host is
// in allowed_domains, every source mode is one of the Scraping Service's
// known modes, the deactivation policy is sane, and every plausibility
// section's entity bounds are sane.
func (c DictionaryUpdateConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch c.Transport {
	case "", DictionaryUpdateTransportDirect, DictionaryUpdateTransportScrapingService:
	default:
		return fmt.Errorf("transport must be 'direct' or 'scraping_service', got %q", c.Transport)
	}
	if len(c.AllowedDomains) == 0 {
		return fmt.Errorf("allowed_domains must be non-empty when enabled")
	}
	allowed := make(map[string]bool, len(c.AllowedDomains))
	for _, d := range c.AllowedDomains {
		allowed[strings.ToLower(d)] = true
	}
	for name, src := range c.Sources {
		u, err := url.Parse(src.URL)
		if err != nil {
			return fmt.Errorf("sources[%s]: invalid url: %w", name, err)
		}
		if !allowed[strings.ToLower(u.Hostname())] {
			return fmt.Errorf("sources[%s]: url host %q is not in allowed_domains", name, u.Hostname())
		}
		switch src.Mode {
		case "http", "browser", "auto":
		default:
			return fmt.Errorf("sources[%s]: mode must be 'http', 'browser' or 'auto', got %q", name, src.Mode)
		}
	}
	if c.DeactivationPolicy.MinimumSuccessfulAbsences < 1 {
		return fmt.Errorf("deactivation_policy.minimum_successful_absences must be >= 1")
	}
	if c.DeactivationPolicy.MinimumElapsedDays < 0 {
		return fmt.Errorf("deactivation_policy.minimum_elapsed_days must not be negative")
	}
	for name, p := range c.Plausibility {
		if p.MinimumEntities < 0 {
			return fmt.Errorf("plausibility[%s]: minimum_entities must not be negative", name)
		}
		if p.MaximumEntities < p.MinimumEntities {
			return fmt.Errorf("plausibility[%s]: maximum_entities must be >= minimum_entities", name)
		}
		if p.MaximumMissingRatio < 0 || p.MaximumMissingRatio > 1 {
			return fmt.Errorf("plausibility[%s]: maximum_missing_ratio must be within [0,1]", name)
		}
	}
	return nil
}

// Validate runs every section's Validate method, wrapping errors with the
// section name so failures are easy to locate.
func (c Config) Validate() error {
	sections := []struct {
		name string
		fn   func() error
	}{
		{"sqlite", c.SQLite.Validate},
		{"scheduler", c.Scheduler.Validate},
		{"http", c.HTTP.Validate},
		{"scraping_service", c.ScrapingService.Validate},
		{"fetch", c.Fetch.Validate},
		{"fetch_sources", c.FetchSources.Validate},
		{"source_policy", c.SourcePolicy.Validate},
		{"normalization", c.Normalization.Validate},
		{"classification", c.Classification.Validate},
		{"deduplication", c.Deduplication.Validate},
		{"clustering", c.Clustering.Validate},
		{"editorial", c.Editorial.Validate},
		{"freshness", c.Freshness.Validate},
		{"telegram", c.Telegram.Validate},
		{"publication_policy", c.PublicationPolicy.Validate},
		{"publication", c.Publication.Validate},
		{"retention", c.Retention.Validate},
		{"cluster_lifecycle", c.ClusterLifecycle.Validate},
		{"seen_items", c.SeenItems.Validate},
		{"model", c.Model.Validate},
		{"dictionary", c.Dictionary.Validate},
		{"dictionary_mining", c.DictionaryMining.Validate},
		{"editorial_cycle", c.EditorialCycle.Validate},
		{"editorial_gates", c.EditorialGates.Validate},
		{"editorial_costs", c.EditorialCosts.Validate},
		{"remote", c.Remote.Validate},
		{"dictionary_update", c.DictionaryUpdate.Validate},
		{"entity_discovery", c.EntityDiscovery.Validate},
		{"entity_discovery_wikidata", c.EntityDiscovery.Wikidata.Validate},
		{"entity_discovery_roster", c.EntityDiscovery.Roster.Validate},
		{"entity_discovery_league", c.EntityDiscovery.League.Validate},
		{"entity_extraction_fallback", c.EntityExtraction.Fallback.Validate},
		{"catalog_maintenance_web_lookup", c.CatalogMaintenance.WebLookup.Validate},
		{"offline_training", c.OfflineTraining.Validate},
		{"espn_scoreboard", c.EspnScoreboard.Validate},
	}
	for _, s := range sections {
		if err := s.fn(); err != nil {
			return fmt.Errorf("config: %s: %w", s.name, err)
		}
	}
	if err := c.EditorialRollout.Validate(c.EditorialGates.MinPrecision); err != nil {
		return fmt.Errorf("config: editorial_rollout: %w", err)
	}
	if c.Classification.Enabled && c.Dictionary.Path == "" {
		return fmt.Errorf("config: dictionary.path is required when classification.enabled is true")
	}
	if c.Editorial.Mode != "disabled" && c.Model.Path == "" {
		return fmt.Errorf("config: model.path is required when editorial.mode is not \"disabled\"")
	}
	return nil
}

// Load reads path, decodes it strictly (unknown keys are rejected), applies
// environment-variable overrides for secrets, validates the result and
// returns it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided CLI/config flag, not user input
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	// Defaults that must survive an absent YAML key (decoding merges into
	// existing field values; an explicit `fallback: false` still wins).
	cfg.EntityDiscovery.Verification.Fallback = true
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	applyEnvOverrides(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyEnvOverrides overlays secrets and local-runtime overrides from the
// environment. Secrets are never read from YAML.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Telegram.BotToken = v
	}
	if v := os.Getenv("TELEGRAM_API_ID"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			cfg.Telegram.APIID = id
		}
	}
	if v := os.Getenv("TELEGRAM_API_HASH"); v != "" {
		cfg.Telegram.APIHash = v
	}
	if v := os.Getenv("TELEGRAM_PHONE"); v != "" {
		cfg.Telegram.Phone = v
	}
	if v := os.Getenv("TELEGRAM_SESSION_PATH"); v != "" {
		cfg.Telegram.SessionPath = v
	}
	if v := os.Getenv("TELEGRAM_TIMEOUT_SECONDS"); v != "" {
		if seconds, err := strconv.Atoi(v); err == nil {
			cfg.Telegram.TimeoutSeconds = seconds
		}
	}
	if v := os.Getenv("SCRAPING_SERVICE_API_KEY"); v != "" {
		cfg.ScrapingService.APIKey = v
	}
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		cfg.EntityDiscovery.LLM.APIKey = v
	}
	if v := os.Getenv("LLM_FALLBACK_API_KEY"); v != "" {
		cfg.EntityExtraction.Fallback.APIKey = v
	}
	if v := os.Getenv("OFFLINE_TRAINING_LLM_API_KEY"); v != "" {
		cfg.OfflineTraining.LLM.APIKey = v
	}
	if v := os.Getenv("WEB_SEARCH_API_KEY"); v != "" {
		cfg.CatalogMaintenance.WebLookup.APIKey = v
	}
	if v := os.Getenv("TAVILY_API_KEY"); v != "" {
		cfg.CatalogMaintenance.WebLookup.Fallback.APIKey = v
	}
	if v := os.Getenv("SQLITE_DB_PATH"); v != "" {
		cfg.SQLite.Path = v
	}
}

// DedupCascade converts the normalization, deduplication, and clustering
// sections into the single value object ProcessCycle needs.
func (c Config) DedupCascade() in.DedupCascadeConfig {
	urlCfg, resolvers := c.Normalization.URL.Policy()
	contentTypeExceptions := make(map[article.ContentType]bool, len(c.Deduplication.NearDuplicate.ContentTypeExceptions))
	for _, ct := range c.Deduplication.NearDuplicate.ContentTypeExceptions {
		contentTypeExceptions[article.ContentType(ct)] = true
	}
	trustedSourceIDs := make(map[string]bool, len(c.Classification.TrustedSourceIDs))
	for _, id := range c.Classification.TrustedSourceIDs {
		trustedSourceIDs[id] = true
	}
	contextSourceIDs := make(map[string]bool, len(c.Classification.ContextSourceIDs))
	for _, id := range c.Classification.ContextSourceIDs {
		contextSourceIDs[id] = true
	}
	modelBypassSourceIDs := make(map[string]bool, len(c.Editorial.ModelBypassSourceIDs))
	for _, id := range c.Editorial.ModelBypassSourceIDs {
		modelBypassSourceIDs[id] = true
	}
	unconditionalPublishSourceIDs := make(map[string]bool, len(c.Editorial.UnconditionalPublishSourceIDs))
	for _, id := range c.Editorial.UnconditionalPublishSourceIDs {
		unconditionalPublishSourceIDs[id] = true
	}
	return in.DedupCascadeConfig{
		Classification: in.ClassificationSettings{
			Enabled: c.Classification.Enabled, TrustedSourceIDs: trustedSourceIDs, ContextSourceIDs: contextSourceIDs,
			Anchor: c.Classification.Anchor.Policy(),
		},
		Editorial: in.EditorialSettings{Mode: c.Editorial.Mode, AmbiguousPolicy: c.Editorial.AmbiguousPolicy, CanaryPercent: c.EditorialRollout.CanaryPercent, ModelBypassSourceIDs: modelBypassSourceIDs, UnconditionalPublishSourceIDs: unconditionalPublishSourceIDs},
		Normalization: in.NormalizationSettings{
			URL:       urlCfg,
			Resolvers: resolvers,
			Text:      c.Normalization.Text.Policy(),
		},
		Dedup: in.DedupSettings{
			Enabled:               c.Deduplication.NearDuplicate.Enabled,
			Method:                c.Deduplication.NearDuplicate.Method,
			DefaultThreshold:      c.Deduplication.NearDuplicate.DefaultThreshold,
			ThresholdBySourceType: c.Deduplication.NearDuplicate.ThresholdBySourceType,
			ShortTextMaxTokens:    c.Deduplication.NearDuplicate.ShortTextMaxTokens,
			ShortTextThreshold:    c.Deduplication.NearDuplicate.ShortTextThreshold,
			WindowHours:           c.Deduplication.NearDuplicate.WindowHours,
			MaxCandidates:         c.Deduplication.NearDuplicate.MaxCandidates,
			MinSharedTerms:        c.Deduplication.NearDuplicate.MinSharedTerms,
			FieldsUsed:            c.Deduplication.NearDuplicate.FieldsUsed,
			ContentTypeExceptions: contentTypeExceptions,
			AttributeComboWindow:  time.Duration(c.Deduplication.NearDuplicate.AttributeComboWindowMinutes) * time.Minute,
			RulesVersion:          c.Deduplication.NearDuplicate.RulesVersion,
			EventThreshold:        c.Clustering.EventThreshold,
		},
		// Cluster is only Enabled here; Rules/Decide/Representative/
		// RulesVersion require clusteringrules.Load(c.Clustering.RulesPath),
		// which can fail and therefore does not belong in this
		// error-free config method. main.go loads and overlays the full
		// ClusterSettings onto this base value when c.Clustering.Enabled.
		Cluster:    in.ClusterSettings{Enabled: c.Clustering.Enabled},
		SeenItems:  in.SeenItemsSettings{ReclaimAfter: c.SeenItems.ReclaimAfter()},
		StoryGuard: c.Deduplication.StoryGuard.Policy(),

		PublishAmbiguousClusters: c.Clustering.PublishAmbiguous,
		LexicalJoin:              c.Clustering.LexicalJoin.Policy(),
	}
}

// placeholderTelegramChannelID is the fake channel id committed to
// configs/settings.yml before a real channel is configured. Rejected by
// ValidateForProfile only under ProfileProduction.
const placeholderTelegramChannelID = "-1000000000000"

// IsLoopbackHost reports whether rawURL's hostname is a loopback or
// unspecified address (localhost, 127.0.0.1, 0.0.0.0, ::1), case-
// insensitive. Returns false for an empty or unparseable URL — callers
// must check those cases separately, since "not loopback" and "not a
// valid URL at all" usually need different error messages. No network
// call: only url.Parse and a string comparison. This is the single
// definition of "unauthorized localhost" shared by
// Config.ValidateForProfile (for fields that live in Config) and
// fetcherwiring.invalidBridgeHost (for bridge URLs that live in
// sources.yml, outside Config's reach).
func IsLoopbackHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return true
	default:
		return false
	}
}

// ValidateForProfile is the config-owned half of the profile contract:
// every profile requires TELEGRAM_BOT_TOKEN and (when scraping_service is
// enabled) SCRAPING_SERVICE_API_KEY. Under ProfileProduction only, it
// additionally rejects the known placeholder telegram.channel_id and
// unauthorized localhost on enabled dictionary_update source URLs via
// IsLoopbackHost. It deliberately does not reject loopback for
// scraping_service.base_url: that client is an internal dependency and a
// same-host deployment (for example localhost:9090 on the same host) is a
// valid production topology. It never forces editorial.mode to a specific
// value — that stays required-and-validated (EditorialConfig.Validate) in
// every profile identically; activating enforce in production is issue
// #168's job, not this method's.
//
// The other half of the profile contract lives in
// fetcherwiring.invalidBridgeHost and invalidYouTubeChannelID: bridge
// URLs and YouTube channel ids come from sources.yml, a file Config has
// no access to, so those checks cannot move here without breaking the
// config/fetcherwiring package boundary. Bridge validation accepts
// loopback hosts for same-host deployments and still rejects missing or
// malformed endpoints; YouTube validation rejects placeholder channel IDs.
// Called by fetcherwiring.Build before evaluating any fetch-source
// category. Never makes a network call — every check is string/struct
// inspection over already-loaded values.
func (c Config) ValidateForProfile(p Profile) error {
	if c.Telegram.BotToken == "" {
		return fmt.Errorf("config: TELEGRAM_BOT_TOKEN is required")
	}
	if c.ScrapingService.Enabled && c.ScrapingService.APIKey == "" {
		return fmt.Errorf("config: SCRAPING_SERVICE_API_KEY is required when scraping_service.enabled is true")
	}
	if p != ProfileProduction {
		return nil
	}
	if c.Telegram.ChannelID == placeholderTelegramChannelID {
		return fmt.Errorf("config: telegram.channel_id is a known placeholder — set a real channel id for production")
	}
	if c.DictionaryUpdate.Enabled {
		for name, src := range c.DictionaryUpdate.Sources {
			if IsLoopbackHost(src.URL) {
				return fmt.Errorf("config: dictionary_update.sources[%s].url rejected: localhost not allowed in production profile", name)
			}
		}
	}
	return nil
}

// Redacted returns a copy of c safe to log or print: every secret field
// (Telegram.BotToken, Telegram.APIID, Telegram.APIHash, Telegram.Phone,
// ScrapingService.APIKey — everything tagged yaml:"-") is masked. A
// non-empty string secret becomes "[REDACTED]"; an empty one stays
// empty, so "unset" remains distinguishable from "set but hidden."
// Telegram.APIID (an int secret) is zeroed when non-zero. c is a value
// receiver, so mutating the returned copy's nested struct fields never
// affects the caller's original Config.
func (c Config) Redacted() Config {
	if c.Telegram.BotToken != "" {
		c.Telegram.BotToken = "[REDACTED]"
	}
	if c.Telegram.APIID != 0 {
		c.Telegram.APIID = 0
	}
	if c.Telegram.APIHash != "" {
		c.Telegram.APIHash = "[REDACTED]"
	}
	if c.Telegram.Phone != "" {
		c.Telegram.Phone = "[REDACTED]"
	}
	if c.ScrapingService.APIKey != "" {
		c.ScrapingService.APIKey = "[REDACTED]"
	}
	if c.EntityDiscovery.LLM.APIKey != "" {
		c.EntityDiscovery.LLM.APIKey = "[REDACTED]"
	}
	if c.EntityExtraction.Fallback.APIKey != "" {
		c.EntityExtraction.Fallback.APIKey = "[REDACTED]"
	}
	if c.OfflineTraining.LLM.APIKey != "" {
		c.OfflineTraining.LLM.APIKey = "[REDACTED]"
	}
	if c.CatalogMaintenance.WebLookup.APIKey != "" {
		c.CatalogMaintenance.WebLookup.APIKey = "[REDACTED]"
	}
	if c.CatalogMaintenance.WebLookup.Fallback.APIKey != "" {
		c.CatalogMaintenance.WebLookup.Fallback.APIKey = "[REDACTED]"
	}
	return c
}

// teamTouchdownMilestone matches a team-specific touchdown milestone tag,
// "touchdown_" followed by a lowercase team slug.
var teamTouchdownMilestone = regexp.MustCompile(`^touchdown_[a-z0-9]+(?:_[a-z0-9]+)*$`)
