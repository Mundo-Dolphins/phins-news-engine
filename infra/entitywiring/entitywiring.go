// Package entitywiring converts configuration into the dependencies of the
// offline entity workflows (entities discover, entities proposals verify,
// entities wikidata evaluate, entities league evaluate): the discovery LLM,
// the Wikidata-first verification chain, the league gazetteer stage and the
// catalog index. It is the single place that wiring lives so the CLI and
// the admin panel cannot drift. It must not import the cli tree: the admin
// binary consumes it without cobra globals.
//
// Errors carry no command prefix; callers add their own.
package entitywiring

import (
	"errors"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var (
	// ErrNoStage is returned (via errors.Is) by VerifyProposals when the
	// resolved flags and config leave no verification stage enabled.
	ErrNoStage = errors.New("no verification stage enabled (--offline, or --no-wikidata with --no-fallback; check entity_discovery.verification.wikidata_first and entity_discovery.verification.fallback)")
	// ErrLeagueDisabled is returned (via errors.Is) by EvaluateLeague when
	// entity_discovery.league is disabled.
	ErrLeagueDisabled = errors.New("entity_discovery.league is disabled")
)

// Loggers returns the logger for one component of a run ("llm", "verify",
// "discover", "league"). The CLI returns a stderr logger tagged with the
// component; the admin panel returns its job logger for every component.
type Loggers func(component string) out.Logger

// LLMOverrides are per-run overrides of entity_discovery.llm. Empty strings
// and nil pointers mean "not overridden": the configured value applies.
type LLMOverrides struct {
	BaseURL, Model  string
	MaxTokens       *int
	DisableThinking *bool
}

// DiscoveryConfig layers o over cfg.EntityDiscovery, enforces the
// enabled/override rule (a disabled block needs a BaseURL override) and
// validates. It returns the effective config plus the effective max tokens
// and disable-thinking switch.
func DiscoveryConfig(cfg *config.Config, o LLMOverrides) (config.EntityDiscoveryConfig, int, bool, error) {
	discoveryCfg := cfg.EntityDiscovery
	if o.BaseURL != "" {
		discoveryCfg.LLM.BaseURL = o.BaseURL
	}
	if o.Model != "" {
		discoveryCfg.LLM.Model = o.Model
	}
	maxTokens, disableThinking := discoveryCfg.LLM.MaxTokens, discoveryCfg.LLM.DisableThinking
	if o.MaxTokens != nil {
		maxTokens = *o.MaxTokens
	}
	if o.DisableThinking != nil {
		disableThinking = *o.DisableThinking
	}
	if !discoveryCfg.Enabled && o.BaseURL == "" {
		return discoveryCfg, 0, false, fmt.Errorf("entity_discovery.enabled is false in config — pass --llm-base-url to override for a one-off run")
	}
	discoveryCfg.Enabled = true
	if err := discoveryCfg.Validate(); err != nil {
		return discoveryCfg, 0, false, err
	}
	return discoveryCfg, maxTokens, disableThinking, nil
}

// NewDiscoveryLLM builds the LLM client from the effective discovery config.
// log receives bounded transport retries and timeouts.
func NewDiscoveryLLM(dc config.EntityDiscoveryConfig, maxTokens int, disableThinking bool, log out.Logger) *llmclient.Client {
	llmConfig := llmclient.Config{
		BaseURL: dc.LLM.BaseURL, APIKey: dc.LLM.APIKey,
		Model: dc.LLM.Model, Temperature: dc.LLM.Temperature,
		MaxTokens: maxTokens, Timeout: dc.LLM.Timeout(),
		// Surface bounded transport retries and timeouts to the operator.
		// These are offline, interactive workflows, so a silent logger
		// turns a stalled local model server into an opaque list of
		// identical batch errors.
		Logger: log,
	}
	if disableThinking {
		llmConfig = llmConfig.WithThinkingDisabled()
	}
	return llmclient.New(llmConfig)
}

// CatalogIndex loads the manual and generated catalogs into one index.
func CatalogIndex(manualPath, generatedPath string) (entitydiscovery.CatalogIndex, error) {
	manualRules, err := entitycatalog.LoadOne(manualPath)
	if err != nil {
		return entitydiscovery.CatalogIndex{}, err
	}
	generatedRules, err := entitycatalog.LoadOne(generatedPath)
	if err != nil {
		return entitydiscovery.CatalogIndex{}, err
	}
	all := make([]eventsignals.EntityRule, 0, len(manualRules)+len(generatedRules))
	all = append(all, manualRules...)
	all = append(all, generatedRules...)
	return entitydiscovery.BuildCatalogIndex(all), nil
}
