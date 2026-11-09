package entitywiring

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

// Default discover sizes when neither the run nor entity_discovery sets
// one (they match the YAML defaults).
const (
	defaultBatchSize = 20
	defaultLimit     = 500
	// allLimit is the effective article cap of DiscoverFlags.All.
	allLimit = 10_000_000
)

// DiscoverFlags are the inputs of one discover run.
type DiscoverFlags struct {
	// Since is the lower fetched-at bound (zero = none).
	Since time.Time
	// All considers every stored article, with no Limit cap.
	All bool
	// Force reprocesses articles already marked for this extractor version.
	Force bool
	// BatchSize and Limit: 0 = entity_discovery value, else 20 / 500.
	BatchSize, Limit int
	// ManualPath and GeneratedPath are the catalog files.
	ManualPath, GeneratedPath string
	// LLM overrides entity_discovery.llm for this run.
	LLM LLMOverrides
	// Verify, when non-nil, overrides catalog_maintenance.web_lookup.enabled.
	Verify *bool
	// VerifyMinConfidence is the threshold below which a candidate is verified.
	VerifyMinConfidence float64
	// Stages overrides the verification stages.
	Stages Stages
}

// DiscoverReport is everything a discover run reports.
type DiscoverReport struct {
	// Result is the run outcome; failed batches are counted, not returned
	// as an error.
	Result entitydiscovery.DiscoverResult
	// LeagueOn reports that the league gazetteer filtered mentions.
	LeagueOn bool
	// Extras are the cache-hit and search counters.
	Extras Extras
	// Warnings are operator notices about ineffective flags.
	Warnings []string
}

// verifyNoEffectWarning is emitted when Verify is forced on but the
// fallback stage stays off.
const verifyNoEffectWarning = "warning: --verify has no effect unless --fallback is given, because entity_discovery.verification.fallback is false"

// Discover runs one discovery pass. Failed LLM batches are not an error:
// they are counted in Result.BatchesFailed (with Result.BatchErrors) and
// the proposals of the other batches stay saved. On error the report
// still carries any Warnings already produced.
func Discover(ctx context.Context, db *sql.DB, cfg *config.Config, f DiscoverFlags, now time.Time, logs Loggers) (DiscoverReport, error) {
	var rep DiscoverReport
	discoveryCfg, maxTokens, disableThinking, err := DiscoveryConfig(cfg, f.LLM)
	if err != nil {
		return rep, err
	}
	catalogIdx, err := CatalogIndex(f.ManualPath, f.GeneratedPath)
	if err != nil {
		return rep, err
	}
	articlesRepo := sqliteadapter.NewArticleRepository(db)
	proposalsRepo := sqliteentityproposal.NewEntityProposalRepository(db)
	llm := NewDiscoveryLLM(discoveryCfg, maxTokens, disableThinking, logs("llm"))

	// A run value wins; otherwise the configured value, otherwise the
	// default — so entity_discovery.batch_size/limit are never silently
	// ignored (they matter for small local models).
	batchSize, limit := f.BatchSize, f.Limit
	if batchSize == 0 {
		batchSize = defaultBatchSize
		if discoveryCfg.BatchSize > 0 {
			batchSize = discoveryCfg.BatchSize
		}
	}
	if limit == 0 {
		limit = defaultLimit
		if discoveryCfg.Limit > 0 {
			limit = discoveryCfg.Limit
		}
	}

	verifyEnabled := cfg.CatalogMaintenance.WebLookup.Enabled
	if f.Verify != nil {
		verifyEnabled = *f.Verify
	}
	wikidataOn, fallbackOn := ResolveStages(cfg.EntityDiscovery.Verification, verifyEnabled, f.Stages)
	if f.Verify != nil && *f.Verify && !cfg.EntityDiscovery.Verification.Fallback && !f.Stages.Fallback && !f.Stages.Offline {
		rep.Warnings = append(rep.Warnings, verifyNoEffectWarning)
	}
	verifier, extras, err := ChainVerifier(ctx, db, cfg, llm, catalogIdx, wikidataOn, fallbackOn, f.Stages.chooserOn(), logs)
	if err != nil {
		return rep, err
	}

	opts := entitydiscovery.DiscoverOptions{
		Since: f.Since, BatchSize: batchSize, Limit: limit,
		ExtractorVersion: discoveryCfg.ExtractorVersion, Force: f.Force,
		VerifyMinConfidence: f.VerifyMinConfidence,
	}
	if f.All {
		opts.Limit = allLimit
	}
	leagueStage, err := LeagueStage(cfg, catalogIdx, now, logs("discover"))
	if err != nil {
		return rep, err
	}
	if leagueStage != nil {
		opts.League = leagueStage.Index
	}

	result, err := entitydiscovery.Discover(ctx, articlesRepo, llm, proposalsRepo, catalogIdx, opts, now.UTC(), verifier)
	if err != nil {
		return rep, err
	}
	rep.Result, rep.LeagueOn = result, opts.League != nil
	if extras != nil {
		rep.Extras = extras()
	}
	return rep, nil
}

// VerifyFlags are the inputs of one proposals-verify run.
type VerifyFlags struct {
	// Limit bounds the proposals verified (0 = no cap).
	Limit int
	// RetryAttempted re-verifies proposals already attempted.
	RetryAttempted bool
	// DryRun reports outcomes without changing any proposal.
	DryRun bool
	// EntityType restricts the run (player, coach, executive, team; empty = all).
	EntityType string
	// Provider selects a single provider: "" or "wikidata".
	Provider string
	// ManualPath and GeneratedPath are the catalog files.
	ManualPath, GeneratedPath string
	// LLM overrides entity_discovery.llm for this run.
	LLM LLMOverrides
	// Stages overrides the verification stages.
	Stages Stages
}

// VerifyReport is everything a proposals-verify run reports.
type VerifyReport struct {
	// Result is the run outcome.
	Result entitydiscovery.ReverifyResult
	// Chain is the per-stage breakdown, nil when the verifier reports none.
	Chain *entitydiscovery.ChainStats
	// Extras are the cache-hit and search counters.
	Extras Extras
}

// ValidateVerifyFlags checks EntityType and Provider without touching the
// DB or the config; VerifyProposals runs it first too.
func ValidateVerifyFlags(f VerifyFlags) error {
	if err := validateEntityType(f.EntityType); err != nil {
		return err
	}
	_, err := applyProvider(f.Provider, f.Stages)
	return err
}

// VerifyProposals re-verifies the pending proposal backlog. It returns an
// error matching ErrNoStage when no verification stage is enabled.
func VerifyProposals(ctx context.Context, db *sql.DB, cfg *config.Config, f VerifyFlags, logs Loggers) (VerifyReport, error) {
	var rep VerifyReport
	if err := validateEntityType(f.EntityType); err != nil {
		return rep, err
	}
	stages, err := applyProvider(f.Provider, f.Stages)
	if err != nil {
		return rep, err
	}
	discoveryCfg, maxTokens, disableThinking, err := DiscoveryConfig(cfg, f.LLM)
	if err != nil {
		return rep, err
	}
	llm := NewDiscoveryLLM(discoveryCfg, maxTokens, disableThinking, logs("llm"))
	catalogIdx, err := CatalogIndex(f.ManualPath, f.GeneratedPath)
	if err != nil {
		return rep, err
	}
	// webLookupEnabled is true: this run always verifies, so the fallback
	// stage defaults on (subject to verification.fallback).
	wikidataOn, fallbackOn := ResolveStages(cfg.EntityDiscovery.Verification, true, stages)
	verifier, extras, err := ChainVerifier(ctx, db, cfg, llm, catalogIdx, wikidataOn, fallbackOn, stages.chooserOn(), logs)
	if err != nil {
		return rep, err
	}
	if verifier == nil {
		return rep, ErrNoStage
	}
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	result, err := entitydiscovery.ReverifyPending(ctx, repo, verifier, catalogIdx, entitydiscovery.ReverifyOptions{
		Limit: f.Limit, EntityType: f.EntityType, DryRun: f.DryRun, RetryAttempted: f.RetryAttempted, Wikidata: wikidataOn,
		Texts: articleTexts{finder: sqliteadapter.NewArticleRepository(db)},
	})
	if err != nil {
		return rep, err
	}
	rep.Result = result
	if sr, ok := verifier.(entitydiscovery.StatsReporter); ok {
		cs := sr.ChainStats()
		rep.Chain = &cs
	}
	rep.Extras = extras()
	return rep, nil
}

func validateEntityType(entityType string) error {
	switch entityproposal.EntityType(entityType) {
	case "", entityproposal.EntityTypePlayer, entityproposal.EntityTypeCoach, entityproposal.EntityTypeExecutive, entityproposal.EntityTypeTeam:
		return nil
	}
	return fmt.Errorf("--type must be one of player, coach, executive, team (got %q)", entityType)
}

// applyProvider maps a provider onto the stage overrides. wikidata means
// Wikidata only: no LLM/SearXNG fallback.
func applyProvider(provider string, s Stages) (Stages, error) {
	switch provider {
	case "":
		return s, nil
	case "wikidata":
		if s.NoWikidata || s.Fallback || s.Offline {
			return s, fmt.Errorf("--provider wikidata conflicts with --no-wikidata, --fallback and --offline")
		}
		s.Wikidata, s.NoFallback = true, true
		return s, nil
	}
	return s, fmt.Errorf("--provider must be wikidata (got %q)", provider)
}

// WikidataEvalFlags are the inputs of one Wikidata evaluation.
type WikidataEvalFlags struct {
	// Limit caps the proposals evaluated (0 = no cap; ignored with Sample).
	Limit int
	// Sample evaluates a deterministic random subset of this size.
	Sample int
	// Seed seeds Sample.
	Seed int64
	// Status is pending, approved, rejected or all.
	Status string
	// Kind restricts the proposal kind (empty = all).
	Kind string
	// EntityType restricts the entity type (empty = all).
	EntityType string
	// ReviewedPath is an optional proposal_id,verdict CSV.
	ReviewedPath string
	// ManualPath and GeneratedPath are the catalog files.
	ManualPath, GeneratedPath string
}

// ValidateWikidataEvalFlags checks Status, Kind and EntityType and reads
// the reviewed CSV, without touching the DB or the config;
// EvaluateWikidata runs it first too.
func ValidateWikidataEvalFlags(f WikidataEvalFlags) error {
	if err := validateEvalFilters(f.Status, f.Kind, f.EntityType); err != nil {
		return err
	}
	_, err := loadReviewed(f.ReviewedPath)
	return err
}

// EvaluateWikidata benchmarks the Wikidata stage on stored proposals. It
// never changes proposals and never calls the LLM fallback.
func EvaluateWikidata(ctx context.Context, db *sql.DB, cfg *config.Config, f WikidataEvalFlags, logs Loggers) (entitydiscovery.EvalReport, error) {
	if err := validateEvalFilters(f.Status, f.Kind, f.EntityType); err != nil {
		return entitydiscovery.EvalReport{}, err
	}
	reviewed, err := loadReviewed(f.ReviewedPath)
	if err != nil {
		return entitydiscovery.EvalReport{}, err
	}
	catalogIdx, err := CatalogIndex(f.ManualPath, f.GeneratedPath)
	if err != nil {
		return entitydiscovery.EvalReport{}, err
	}
	verifier, extras, err := ChainVerifier(ctx, db, cfg, nil, catalogIdx, true, false, false, logs)
	if err != nil {
		return entitydiscovery.EvalReport{}, err
	}
	assessor, ok := verifier.(entitydiscovery.Assessor)
	if !ok {
		return entitydiscovery.EvalReport{}, errors.New("verifier does not support assessment")
	}
	st := f.Status
	if st == "all" {
		st = ""
	}
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	rep, err := entitydiscovery.Evaluate(ctx, repo, assessor, catalogIdx, entitydiscovery.EvalOptions{
		Limit: f.Limit, Status: st, Kind: f.Kind, EntityType: f.EntityType,
		Sample: f.Sample, Seed: f.Seed, Reviewed: reviewed,
	})
	if err != nil {
		return entitydiscovery.EvalReport{}, err
	}
	rep.CacheHits = extras().CacheHits
	return rep, nil
}

func validateEvalFilters(status, kind, entityType string) error {
	switch status {
	case "pending", "approved", "rejected", "all":
	default:
		return fmt.Errorf("--status must be one of pending, approved, rejected, all (got %q)", status)
	}
	switch entityproposal.Kind(kind) {
	case "", entityproposal.KindNewEntity, entityproposal.KindNewAlias, entityproposal.KindPossibleDuplicate:
	default:
		return fmt.Errorf("--kind must be one of new_entity, new_alias, possible_duplicate (got %q)", kind)
	}
	return validateEntityType(entityType)
}

// loadReviewed reads a CSV of proposal_id,verdict (correct|wrong). An empty
// path means no reviewed sample.
func loadReviewed(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) // #nosec G304 -- path is an explicit operator-provided input path.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = 2
	out := map[string]string{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reviewed file %s: %w", path, err)
		}
		id, verdict := strings.TrimSpace(rec[0]), strings.ToLower(strings.TrimSpace(rec[1]))
		if id == "proposal_id" {
			continue
		}
		if verdict != "correct" && verdict != "wrong" {
			return nil, fmt.Errorf("reviewed file %s: verdict for %q must be correct or wrong (got %q)", path, id, rec[1])
		}
		out[id] = verdict
	}
}

// EvaluateLeague replays the league gazetteer on the pending backlog (read
// only). It returns an error matching ErrLeagueDisabled when
// entity_discovery.league is disabled.
func EvaluateLeague(ctx context.Context, db *sql.DB, cfg *config.Config, manualPath, generatedPath string, now time.Time, logs Loggers) (entitydiscovery.LeagueEvalReport, error) {
	catalogIdx, err := CatalogIndex(manualPath, generatedPath)
	if err != nil {
		return entitydiscovery.LeagueEvalReport{}, err
	}
	stage, err := LeagueStage(cfg, catalogIdx, now, logs("league"))
	if err != nil {
		return entitydiscovery.LeagueEvalReport{}, err
	}
	if stage == nil {
		return entitydiscovery.LeagueEvalReport{}, ErrLeagueDisabled
	}
	stage.Texts = articleTexts{finder: sqliteadapter.NewArticleRepository(db)}
	return entitydiscovery.EvaluateLeague(ctx, sqliteentityproposal.NewEntityProposalRepository(db), stage)
}
