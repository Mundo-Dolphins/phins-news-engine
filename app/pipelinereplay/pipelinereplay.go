// Package pipelinereplay wires a real usecase.ProcessCycle against a
// scratch "lab" SQLite database, loading v2's dictionary/editorial-model/
// clustering/source-policy configuration exactly the way
// engine/run.go does — with optional dictionary/editorial-model
// path overrides, so a candidate artifact can be evaluated through the same
// real pipeline as production without touching any operational database.
// Shared by host-provided historical backtests (replaying v1's historical traffic) and
// backtestcandidate (replaying v2's own historical traffic against a
// candidate dictionary/model).
package pipelinereplay

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	infraeditorialveto "github.com/Mundo-Dolphins/phins-news-engine/infra/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguecatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

// loadClusterRules loads the clustering rules exactly like
// engine/run.go: the hand-written rules with the entity catalog,
// then the generated rules file merged in (clustering.generated_rules_path;
// empty means none, which leaves the hand-written set unchanged).
func loadClusterRules(cfg *config.Config) (clusteringrules.RulesFile, error) {
	rulesFile, err := clusteringrules.LoadWithEntityCatalog(
		cfg.Clustering.RulesPath, cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath,
	)
	if err != nil {
		return clusteringrules.RulesFile{}, fmt.Errorf("pipelinereplay: load clustering rules: %w", err)
	}
	rulesFile, _, err = clusteringrules.LoadWithGenerated(rulesFile, cfg.Clustering.GeneratedRulesPath)
	if err != nil {
		return clusteringrules.RulesFile{}, fmt.Errorf("pipelinereplay: load generated clustering rules: %w", err)
	}
	return rulesFile, nil
}

// Fetcher is an out.Fetcher whose single batch is set by the caller before
// each ProcessCycle.Run call, letting a replay drive one historical batch
// at a time.
type Fetcher struct {
	mu       sync.Mutex
	articles []article.Article
}

// Set replaces the batch Fetch will return on its next call.
func (f *Fetcher) Set(articles []article.Article) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.articles = articles
}

// Fetch implements out.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context) ([]article.Article, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.articles, nil
}

// Clock is an out.Clock whose Now() is pinned by the caller to each
// replayed batch's historical timestamp before that batch's
// ProcessCycle.Run call, so dedup/clustering time windows behave as they
// would have at that historical moment.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Set pins Now() to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Now implements out.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// noopLogger discards everything — replay callers report results through
// their own summary, not through ProcessCycle's structured logging.
type noopLogger struct{}

func (noopLogger) Debug(string, ...any)        {}
func (noopLogger) Info(string, ...any)         {}
func (noopLogger) Warn(string, ...any)         {}
func (noopLogger) Error(string, error, ...any) {}

// Overrides replaces cfg's own dictionary/editorial-model paths with a
// candidate artifact's path, for a "what would this candidate do" replay.
// A zero-value Overrides makes Wire behave identically to loading cfg's
// current production dictionary/model, which is what a historical backtest wants.
type Overrides struct {
	DictionaryPath string // empty = use cfg.Dictionary.Path
	ModelPath      string // empty = use cfg.Model.Path
}

// Wire loads v2's dictionary, editorial model, clustering rules, and
// source policies from cfg — exactly the way engine/run.go
// loads them for production, except that overrides.DictionaryPath/ModelPath
// (when non-empty) replace cfg's own paths — and builds a real
// usecase.ProcessCycle against labDB's real SQLite repositories. It skips
// loading cfg's own sources.yml catalog (a replay deliberately doesn't
// require historical sources to be declared in v2's current source
// catalog) and skips the full production dictionary/model registry
// bookkeeping main.go performs (path/entry-count precision, structured log
// lines): it writes only the minimal dictionary_registry/model_registry
// row each loaded version needs, since articles.dictionary_version and
// articles.classifier_version are foreign keys into those tables
// (migration 0017/0005) and every saved article would otherwise fail that
// constraint.
func Wire(cfg *config.Config, labDB *sql.DB, overrides Overrides) (*usecase.ProcessCycle, *sqliteadapter.ArticleRepository, *Fetcher, *Clock, error) {
	var matcher dictionary.Matcher
	if cfg.Classification.Enabled {
		dictPath := cfg.Dictionary.Path
		if overrides.DictionaryPath != "" {
			dictPath = overrides.DictionaryPath
		}
		dict, err := dictionaryfile.Load(dictPath)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("pipelinereplay: load dictionary: %w", err)
		}
		for _, issue := range dictionary.Validate(dict) {
			if issue.Severity == dictionary.SeverityError {
				return nil, nil, nil, nil, fmt.Errorf("pipelinereplay: dictionary %s failed validation: %s", dictPath, issue)
			}
		}
		matcher, err = dictionary.Prepare(dict)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("pipelinereplay: prepare dictionary: %w", err)
		}

		// articles.dictionary_version FKs into dictionary_registry(version)
		// (migration 0017) — every accepted article is saved with the
		// loaded dictionary's version (see ProcessCycle Step 3), so a row
		// for that version must exist in the lab DB or the save fails the
		// foreign key check. This is a minimal registration purely to
		// satisfy that constraint, not the full production bookkeeping
		// main.go performs (which also feeds retention/audit tooling that
		// is irrelevant for a throwaway lab database). PromotedAt uses
		// wall-clock time.Now() rather than the Clock constructed below
		// deliberately: that clock isn't set to a historical moment until
		// the caller starts driving batches (after Wire returns), and this
		// registry row is never read by ProcessCycle anyway — it exists
		// solely to satisfy the foreign key above.
		entryCount := 0
		for _, cat := range dict.Categories {
			entryCount += len(cat.Entries)
		}
		dictRegistry := sqliteadapter.NewDictionaryRegistryRepository(labDB)
		if err := dictRegistry.Register(context.Background(), dictionary.Registration{
			Version: dict.Version, Path: dictPath, EntryCount: entryCount, PromotedAt: time.Now(),
		}); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("pipelinereplay: register dictionary %s in lab db: %w", dict.Version, err)
		}
	}

	var editorialModel *editorial.Model
	if cfg.Editorial.Mode != "disabled" {
		modelPath := cfg.Model.Path
		if overrides.ModelPath != "" {
			modelPath = overrides.ModelPath
		}
		m, err := editorialmodel.Load(modelPath)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("pipelinereplay: load editorial model: %w", err)
		}
		// Best-effort model identity for the replay's persisted events.
		// Unlike the production composition root (engine/
		// main.go), this lab tool deliberately does NOT hard-fail on a
		// missing/mismatched manifest: a replay is routinely pointed at
		// a bare candidate model.json with no promotion manifest beside
		// it, and refusing to replay it would defeat the tool's purpose.
		// So the artifact hash is attached when a manifest happens to be
		// present, and left empty otherwise; editorialmodel.Validate is
		// likewise not enforced here. Replay-produced events may
		// therefore carry an empty model_checksum — never true of a
		// production event.
		if manifest, _, _, err := editorialmodel.Inspect(modelPath); err == nil {
			m.Checksum = manifest.ArtifactHash
		}
		editorialModel = &m

		// articles.classifier_version FKs into model_registry(version)
		// (migration 0017) — same reasoning as the dictionary registration
		// above, minimal registration to satisfy the constraint (and same
		// deliberate use of wall-clock time.Now() for PromotedAt).
		modelRegistry := sqliteadapter.NewModelRegistryRepository(labDB)
		if err := modelRegistry.Register(context.Background(), training.ModelMetadata{
			Version: m.Version, Kind: "editorial-classifier", Path: modelPath,
			Algorithm: m.Algorithm, FeatureSchemaVersion: m.FeatureSchemaVersion,
			Status: "promoted", PromotedAt: time.Now(),
		}); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("pipelinereplay: register editorial model %s in lab db: %w", m.Version, err)
		}
	}

	var editorialVetoCfg *editorialveto.Config
	if cfg.Editorial.VetoPath != "" {
		v, err := infraeditorialveto.Load(cfg.Editorial.VetoPath)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("pipelinereplay: load editorial veto rules: %w", err)
		}
		editorialVetoCfg = &v
	}

	cascade := cfg.DedupCascade()
	if cfg.Clustering.Enabled {
		rulesFile, err := loadClusterRules(cfg)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		cascade.Cluster = in.ClusterSettings{
			Enabled:        true,
			Rules:          rulesFile.ToEventSignalsRules(),
			Decide:         rulesFile.ToClusteringConfig(),
			RulesVersion:   rulesFile.Version,
			Representative: rulesFile.ToRepresentativeConfig(),
			CandidateLimit: cfg.Clustering.CandidateLimit,
		}
		if lp := leaguecatalog.FromConfig(cfg.Clustering.EntityCatalog.League, cascade.Cluster.Rules.Entities, noopLogger{}); lp != nil {
			cascade.Cluster.LeagueEntities = lp
		}
	}

	sourcePolicyFile, err := sourcepolicyfile.Load(cfg.SourcePolicy.Path)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("pipelinereplay: load source policies: %w", err)
	}
	var sourcePolicyResolver sourcepolicy.Resolver = sourcepolicyfile.NewResolver(sourcePolicyFile)

	articles := sqliteadapter.NewArticleRepository(labDB)
	dedup := sqliteadapter.NewDeduplicationRepository(labDB)
	aliases := sqliteadapter.NewAliasRepository(labDB)
	similarityIndex := sqliteadapter.NewSimilarityIndexRepository(labDB)
	clusters := sqliteadapter.NewClusterRepository(labDB)
	decisions := sqliteadapter.NewDecisionRepository(labDB)
	outbox := sqliteadapter.NewOutboxRepository(labDB)
	uow := sqliteadapter.NewUnitOfWork(labDB)
	sourcePolicies := sqliteadapter.NewSourcePolicyRepository(labDB)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(labDB)
	seenItems := sqliteadapter.NewSeenItemsRepository(labDB)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(labDB)
	unknownEntities := sqliteadapter.NewUnknownEntityRepository(labDB)

	fetcher := &Fetcher{}
	clock := &Clock{}

	pc := usecase.NewProcessCycle(
		fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow,
		cfg.Freshness.Policy(), matcher, editorialModel, editorialVetoCfg, cascade, clock, noopLogger{},
		sourcePolicies, sourcePolicyResolver, clusterPublications, seenItems, sourcePolicyCooldowns, unknownEntities, nil,
	)
	pc.SetRecentStories(similarityIndex)
	return pc, articles, fetcher, clock, nil
}
