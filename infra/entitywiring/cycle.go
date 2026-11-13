package entitywiring

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Verification modes of the editorial cycle's entity step.
const (
	CycleVerificationLocal = "local"
	CycleVerificationFull  = "full"
	CycleVerificationNone  = "none"
)

// cycleVerifyMinConfidence is the confidence below which a discovered alias is
// sent through verification; it matches `entities discover`'s
// --verify-min-confidence default.
const cycleVerifyMinConfidence = 0.85

// ValidateCycleVerification checks an entity step verification mode.
func ValidateCycleVerification(v string) error {
	switch v {
	case CycleVerificationLocal, CycleVerificationFull, CycleVerificationNone:
		return nil
	}
	return fmt.Errorf("--entities-verification must be local, full or none (got %q)", v)
}

// CycleStepDisabledReason returns why the editorial cycle's entity step
// cannot run with cfg, or "" when it can.
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

// DiscoveryLLMFactory builds the discovery LLM from the effective discovery
// config; log receives the client's own log lines.
type DiscoveryLLMFactory func(dc config.EntityDiscoveryConfig, maxTokens int, disableThinking bool, log out.Logger) out.EntityDiscoveryLLM

// CycleDeps wires the entity step of one editorial cycle session: the
// discovery LLM, the verification chain for the verification mode, the
// league stage, the catalog approval and regeneration. Wiring log lines go to
// logOut, tagged by component ("[llm] ...").
//
// The returned Input carries the session's sample (the articles published or
// rejected in it) and the discovery options; the caller adds Scanner and Out.
// A session with no such article returns an Input with no SessionArticleIDs
// and builds nothing else. newLLM nil means NewDiscoveryLLM.
func CycleDeps(ctx context.Context, cfg *config.Config, db *sql.DB, sessionID, verification string, logOut io.Writer, newLLM DiscoveryLLMFactory) (entitycycle.Deps, entitycycle.Input, error) {
	var deps entitycycle.Deps
	var in entitycycle.Input
	if err := ValidateCycleVerification(verification); err != nil {
		return deps, in, err
	}
	decisions, err := sqliteadapter.NewReviewDecisionRepository(db).ListBySession(ctx, sessionID)
	if err != nil {
		return deps, in, fmt.Errorf("entities: list session decisions: %w", err)
	}
	ids := sessionArticleIDs(decisions)
	if len(ids) == 0 {
		return deps, in, nil
	}
	if newLLM == nil {
		newLLM = func(dc config.EntityDiscoveryConfig, maxTokens int, disableThinking bool, log out.Logger) out.EntityDiscoveryLLM {
			return NewDiscoveryLLM(dc, maxTokens, disableThinking, log)
		}
	}
	logs := Loggers(func(c string) out.Logger { return writerLogger{out: logOut, component: c} })

	discoveryCfg, maxTokens, disableThinking, err := DiscoveryConfig(cfg, LLMOverrides{})
	if err != nil {
		return deps, in, fmt.Errorf("entities: %w", err)
	}
	catalog, err := CatalogIndex(cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath)
	if err != nil {
		return deps, in, fmt.Errorf("entities: %w", err)
	}
	llm := newLLM(discoveryCfg, maxTokens, disableThinking, logs("llm"))

	wikidataOn, fallbackOn, chooserOn, verify := cycleStages(cfg, verification)
	leagueStage, err := LeagueStage(cfg, catalog, time.Now(), logs("entities"))
	if err != nil {
		return deps, in, fmt.Errorf("entities: %w", err)
	}
	var verifier entitydiscovery.Verifier
	if verify {
		verifier, _, err = ChainVerifier(ctx, db, cfg, llm, catalog, wikidataOn, fallbackOn, chooserOn, logs)
		if err != nil {
			return deps, in, fmt.Errorf("entities: %w", err)
		}
	}

	batch := discoveryCfg.BatchSize
	if batch <= 0 {
		batch = 20
	}
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	deps = entitycycle.Deps{
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
	in = entitycycle.Input{
		SessionArticleIDs: ids, ExtractorVersion: discoveryCfg.ExtractorVersion,
		BatchSize: batch, Limit: len(ids), VerifyMinConfidence: cycleVerifyMinConfidence,
	}
	return deps, in, nil
}

// cycleStages maps a verification mode to the chain stages. local never uses
// web search; full behaves like `entities discover --verify --fallback`.
func cycleStages(cfg *config.Config, mode string) (wikidataOn, fallbackOn, chooserOn, verify bool) {
	switch mode {
	case CycleVerificationFull:
		w, f := ResolveStages(cfg.EntityDiscovery.Verification, cfg.CatalogMaintenance.WebLookup.Enabled, Stages{Fallback: true})
		return w, f, true, true
	case CycleVerificationNone:
		return false, false, false, false
	default: // local
		return cfg.EntityDiscovery.Wikidata.Enabled, false, true, true
	}
}

// sessionArticleIDs is the sample of the entity step: the articles the operator
// published or rejected in the session, once each. Skipped, ambiguous,
// duplicate and update decisions are not part of it.
func sessionArticleIDs(decisions []out.ReviewDecision) []string {
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

// writerLogger is an out.Logger writing one tagged line per message to a
// writer, in the entity commands' "[component] ..." format.
type writerLogger struct {
	out       io.Writer
	component string
}

func (l writerLogger) Debug(string, ...any) {}

func (l writerLogger) Info(msg string, kv ...any) {
	fmt.Fprintf(l.out, "[%s] %s %v\n", l.component, msg, kv)
}

func (l writerLogger) Warn(msg string, kv ...any) {
	fmt.Fprintf(l.out, "[%s] WARN %s %v\n", l.component, msg, kv)
}

func (l writerLogger) Error(msg string, err error, kv ...any) {
	fmt.Fprintf(l.out, "[%s] ERROR %s: %v %v\n", l.component, msg, err, kv)
}
