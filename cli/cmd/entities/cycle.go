// cli/cmd/entities/cycle.go
package entities

import (
	"bufio"
	"database/sql"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Verification modes of the cycle's entity step.
const (
	CycleVerificationLocal = "local"
	CycleVerificationFull  = "full"
	CycleVerificationNone  = "none"
)

// CycleOptions configures RunCycleStep.
type CycleOptions struct{ Verification string }

// ValidateCycleVerification checks an --entities-verification value.
func ValidateCycleVerification(v string) error {
	switch v {
	case CycleVerificationLocal, CycleVerificationFull, CycleVerificationNone:
		return nil
	}
	return fmt.Errorf("--entities-verification must be local, full or none (got %q)", v)
}

// NewCycleLLM builds the discovery LLM; a variable so tests can substitute it.
var NewCycleLLM = func(cmd *cobra.Command, cfg config.EntityDiscoveryConfig, maxTokens int, disableThinking bool) appout.EntityDiscoveryLLM {
	return newDiscoveryLLM(cmd, cfg, maxTokens, disableThinking)
}

// CycleStepDisabledReason returns why the entity step cannot run with cfg, or ""
// when it can.
func CycleStepDisabledReason(cfg *config.Config) string {
	switch {
	case !cfg.EntityDiscovery.Enabled:
		return "entity_discovery.enabled is false"
	case cfg.Clustering.EntityCatalog.ManualPath == "" || cfg.Clustering.EntityCatalog.GeneratedPath == "":
		return "clustering.entity_catalog paths are not configured"
	case cfg.Dictionary.Path == "":
		return "dictionary.path is empty"
	}
	return ""
}

// cycleStages maps a verification mode to the chain stages. local never uses
// web search; full behaves like `entities discover --verify --fallback`.
func cycleStages(cfg *config.Config, mode string) (wikidataOn, fallbackOn, chooserOn, verify bool) {
	switch mode {
	case CycleVerificationFull:
		w, f := resolveVerificationStages(cfg.EntityDiscovery.Verification, cfg.CatalogMaintenance.WebLookup.Enabled, verificationFlags{fallback: true})
		return w, f, true, true
	case CycleVerificationNone:
		return false, false, false, false
	default: // local
		return cfg.EntityDiscovery.Wikidata.Enabled, false, true, true
	}
}

// cycleVerifyMinConfidence is the confidence below which a discovered alias is
// sent through verification; it matches `entities discover`'s
// --verify-min-confidence default.
const cycleVerifyMinConfidence = 0.85

// sessionArticleIDs is the sample of the entity step: the articles the operator
// published or rejected in the session, once each. Skipped, ambiguous,
// duplicate and update decisions are not part of it.
func sessionArticleIDs(decisions []appout.ReviewDecision) []string {
	seen := map[string]bool{}
	var ids []string
	for _, d := range decisions {
		if (d.Label != "publish" && d.Label != "reject") || seen[d.ArticleID] {
			continue
		}
		seen[d.ArticleID] = true
		ids = append(ids, d.ArticleID)
	}
	return ids
}

// RunCycleStep runs the entity step of `editorial cycle` for one session.
func RunCycleStep(cmd *cobra.Command, cfg *config.Config, db *sql.DB, sessionID string, scanner *bufio.Scanner, opts CycleOptions) (entitycycle.Result, error) {
	var zero entitycycle.Result
	if err := ValidateCycleVerification(opts.Verification); err != nil {
		return zero, err
	}
	decisions, err := sqliteadapter.NewReviewDecisionRepository(db).ListBySession(cmd.Context(), sessionID)
	if err != nil {
		return zero, fmt.Errorf("entities: list session decisions: %w", err)
	}
	ids := sessionArticleIDs(decisions)
	if len(ids) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "entities: the session has no reviewed articles")
		return zero, nil
	}

	discoveryCfg, maxTokens, disableThinking, err := resolveDiscoveryConfig(cmd, cfg, llmFlagValues{})
	if err != nil {
		return zero, fmt.Errorf("entities: %w", err)
	}
	catalog, err := loadCatalogIndex(cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath)
	if err != nil {
		return zero, fmt.Errorf("entities: %w", err)
	}
	llm := NewCycleLLM(cmd, discoveryCfg, maxTokens, disableThinking)

	wikidataOn, fallbackOn, chooserOn, verify := cycleStages(cfg, opts.Verification)
	leagueStage, err := buildLeagueStage(cfg, catalog, time.Now(), discoveryLogger{out: cmd.OutOrStderr(), component: "entities"})
	if err != nil {
		return zero, fmt.Errorf("entities: %w", err)
	}
	var verifier entitydiscovery.Verifier
	if verify {
		verifier, _, err = buildChainVerifier(cmd, db, cfg, llm, catalog, wikidataOn, fallbackOn, chooserOn)
		if err != nil {
			return zero, fmt.Errorf("entities: %w", err)
		}
	}

	batch := discoveryCfg.BatchSize
	if batch <= 0 {
		batch = 20
	}
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	deps := entitycycle.Deps{
		Lister:    entitycycle.SessionLister{IDs: ids, Articles: sqliteadapter.NewArticleRepository(db), Processed: repo},
		LLM:       llm,
		Proposals: repo,
		Catalog:   catalog,
		Verifier:  verifier,
		Apply: func(p entityproposal.Proposal, now time.Time) error {
			return entitydiscovery.ApplyApproval(p, entitydiscovery.CatalogPaths{
				DictionaryPath: cfg.Dictionary.Path, ManualCatalogPath: cfg.Clustering.EntityCatalog.ManualPath,
				GeneratedCatalogPath: cfg.Clustering.EntityCatalog.GeneratedPath,
			}, now)
		},
		Regenerate: func() error {
			_, err := GenerateCatalog(cfg.Dictionary.Path, cfg.Clustering.EntityCatalog.GeneratedPath)
			return err
		},
		Now: time.Now,
	}
	if leagueStage != nil {
		deps.League = leagueStage.Index
	}
	return entitycycle.Run(cmd.Context(), deps, entitycycle.Input{
		SessionArticleIDs: ids, ExtractorVersion: discoveryCfg.ExtractorVersion,
		BatchSize: batch, Limit: len(ids), VerifyMinConfidence: cycleVerifyMinConfidence,
		Scanner: scanner, Out: cmd.OutOrStdout(),
	})
}
