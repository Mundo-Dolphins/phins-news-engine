package entities

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/lookback"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

// llmOverrides turns the llm_* params into wiring overrides: 0 max tokens
// and a false disable-thinking switch keep the configured values.
func llmOverrides(baseURL, model string, maxTokens int, disableThinking bool) entitywiring.LLMOverrides {
	o := entitywiring.LLMOverrides{BaseURL: baseURL, Model: model}
	if maxTokens != 0 {
		o.MaxTokens = &maxTokens
	}
	if disableThinking {
		o.DisableThinking = &disableThinking
	}
	return o
}

// stages maps the tri-state wikidata/fallback params (config, on, off) and
// the offline/no_llm switches onto the wiring stage overrides.
func stages(wikidata, fallback string, offline, noLLM bool) entitywiring.Stages {
	return entitywiring.Stages{
		Wikidata: wikidata == "on", NoWikidata: wikidata == "off",
		Fallback: fallback == "on", NoFallback: fallback == "off",
		Offline: offline, NoLLM: noLLM,
	}
}

type discoverParams struct {
	Since               string  `json:"since" help:"only consider articles fetched since this duration (24h, 7d, 30d) or RFC3339 date; empty = no lower bound" format:"duration|date"`
	All                 bool    `json:"all" help:"consider every stored article, with no limit cap (since is ignored)"`
	Force               bool    `json:"force" help:"reprocess articles already marked processed for this extractor version"`
	BatchSize           int     `json:"batch_size" help:"articles per LLM request (0 = entity_discovery.batch_size, else 20)" default:"0"`
	Limit               int     `json:"limit" help:"maximum number of articles to consider (0 = entity_discovery.limit, else 500; ignored with all)" default:"0"`
	Manual              string  `json:"manual" help:"manual entity catalog YAML file (default clustering.entity_catalog.manual_path)" format:"path"`
	Generated           string  `json:"generated" help:"generated entity catalog YAML file (default clustering.entity_catalog.generated_path)" format:"path"`
	Verify              string  `json:"verify" help:"bounded web-lookup verification of uncertain candidates: config = catalog_maintenance.web_lookup.enabled, on or off" enum:"config,on,off" default:"config"`
	VerifyMinConfidence float64 `json:"verify_min_confidence" help:"confidence threshold below which a candidate is verified" default:"0.85"`
	LLMBaseURL          string  `json:"llm_base_url" help:"override entity_discovery.llm.base_url for this run (empty = configured value)"`
	LLMModel            string  `json:"llm_model" help:"override entity_discovery.llm.model for this run (empty = configured value)"`
	LLMMaxTokens        int     `json:"llm_max_tokens" help:"maximum LLM response tokens for this run (0 = configured value, else the provider default)" default:"0"`
	LLMDisableThinking  bool    `json:"llm_disable_thinking" help:"ask compatible reasoning models (such as Ollama) not to generate thinking output"`
	Wikidata            string  `json:"wikidata" help:"Wikidata-first stage: config = entity_discovery.verification.wikidata_first, on or off" enum:"config,on,off" default:"config"`
	Fallback            string  `json:"fallback" help:"LLM + SearXNG fallback stage: config = entity_discovery.verification.fallback, on or off" enum:"config,on,off" default:"config"`
	Offline             bool    `json:"offline" help:"no network verification at all (disables both stages)"`
	NoLLM               bool    `json:"no_llm" help:"do not call the LLM chooser for ambiguous league mentions in this run"`
}

type verifyParams struct {
	Limit              int    `json:"limit" help:"maximum proposals to verify in this run (0 = no cap)" default:"50"`
	RetryAttempted     bool   `json:"retry_attempted" help:"re-verify proposals already attempted by an earlier run (e.g. uncertain ones), oldest attempt first"`
	Type               string `json:"type" help:"only verify proposals of this entity type" enum:"player,coach,executive,team"`
	DryRun             bool   `json:"dry_run" help:"report outcomes without changing any proposal (the verifier still runs and may write the web-search cache)"`
	Provider           string `json:"provider" help:"verify with a single provider: wikidata (Wikidata only, no LLM/SearXNG fallback)" enum:"wikidata"`
	Manual             string `json:"manual" help:"manual entity catalog YAML file (default clustering.entity_catalog.manual_path)" format:"path"`
	Generated          string `json:"generated" help:"generated entity catalog YAML file (default clustering.entity_catalog.generated_path)" format:"path"`
	LLMBaseURL         string `json:"llm_base_url" help:"override entity_discovery.llm.base_url for this run (empty = configured value)"`
	LLMModel           string `json:"llm_model" help:"override entity_discovery.llm.model for this run (empty = configured value)"`
	LLMMaxTokens       int    `json:"llm_max_tokens" help:"maximum LLM response tokens for this run (0 = configured value, else the provider default)" default:"0"`
	LLMDisableThinking bool   `json:"llm_disable_thinking" help:"ask compatible reasoning models (such as Ollama) not to generate thinking output"`
	Wikidata           string `json:"wikidata" help:"Wikidata-first stage: config = entity_discovery.verification.wikidata_first, on or off" enum:"config,on,off" default:"config"`
	Fallback           string `json:"fallback" help:"LLM + SearXNG fallback stage: config = entity_discovery.verification.fallback, on or off" enum:"config,on,off" default:"config"`
	Offline            bool   `json:"offline" help:"no network verification at all (disables both stages)"`
	NoLLM              bool   `json:"no_llm" help:"do not call the LLM chooser for ambiguous league mentions in this run"`
}

func runDiscover(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*discoverParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	since, err := lookback.Parse(p.Since, env.Now().UTC())
	if err != nil {
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: "since: " + err.Error(), Err: err}
	}
	if p.All {
		since = time.Time{}
	}
	cat := env.Config.Clustering.EntityCatalog
	f := entitywiring.DiscoverFlags{
		Since: since, All: p.All, Force: p.Force, BatchSize: p.BatchSize, Limit: p.Limit,
		ManualPath: or(p.Manual, cat.ManualPath), GeneratedPath: or(p.Generated, cat.GeneratedPath),
		LLM:                 llmOverrides(p.LLMBaseURL, p.LLMModel, p.LLMMaxTokens, p.LLMDisableThinking),
		VerifyMinConfidence: p.VerifyMinConfidence, Stages: stages(p.Wikidata, p.Fallback, p.Offline, p.NoLLM),
	}
	switch p.Verify {
	case "on":
		v := true
		f.Verify = &v
	case "off":
		v := false
		f.Verify = &v
	}
	if _, _, _, err := entitywiring.DiscoveryConfig(env.Config, f.LLM); err != nil {
		return action.Result{}, action.Conflict(err)
	}
	rep, err := entitywiring.Discover(ctx, env.DB, env.Config, f, env.Now(), loggers(env))
	for _, w := range rep.Warnings {
		env.Log.Warn(w)
	}
	if err != nil {
		return action.Result{}, err
	}
	r := rep.Result
	summary := fmt.Sprintf("%d proposals, %d auto-rejected, %d uncertain", r.ProposalsUpserted, r.AutoRejected, r.Uncertain)
	if r.BatchesFailed > 0 {
		for _, e := range r.BatchErrors {
			env.Log.Warn("discover batch failed", "error", e)
		}
		summary = fmt.Sprintf("WARNING: %d of %d batches failed; %s", r.BatchesFailed, r.BatchesTotal, summary)
	}
	return action.Result{Render: "object", Object: rep, Summary: summary}, nil
}

func runVerify(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*verifyParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	cat := env.Config.Clustering.EntityCatalog
	f := entitywiring.VerifyFlags{
		Limit: p.Limit, RetryAttempted: p.RetryAttempted, DryRun: p.DryRun, EntityType: p.Type, Provider: p.Provider,
		ManualPath: or(p.Manual, cat.ManualPath), GeneratedPath: or(p.Generated, cat.GeneratedPath),
		LLM:    llmOverrides(p.LLMBaseURL, p.LLMModel, p.LLMMaxTokens, p.LLMDisableThinking),
		Stages: stages(p.Wikidata, p.Fallback, p.Offline, p.NoLLM),
	}
	if err := entitywiring.ValidateVerifyFlags(f); err != nil {
		return action.Result{}, invalid(err)
	}
	if _, _, _, err := entitywiring.DiscoveryConfig(env.Config, f.LLM); err != nil {
		return action.Result{}, action.Conflict(err)
	}
	rep, err := entitywiring.VerifyProposals(ctx, env.DB, env.Config, f, loggers(env))
	if errors.Is(err, entitywiring.ErrNoStage) {
		return action.Result{}, action.Conflict(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	r := rep.Result
	summary := fmt.Sprintf("processed=%d kept=%d auto_rejected=%d uncertain=%d", r.Processed, r.Kept, r.AutoRejected, r.Uncertain)
	if p.DryRun {
		summary = "dry run: " + summary
	}
	return action.Result{Render: "object", Object: rep, Summary: summary}, nil
}
