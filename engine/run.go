// Package engine is the scheduler service's composition root: it loads the
// configuration, opens and migrates SQLite, wires the use cases and runs the
// scheduler until the context is cancelled. Host programs inject optional,
// non-generic capabilities through Options.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/inbound/scheduler"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/espnscoreboard"
	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/telegram"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entityresolution"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	infraeditorialveto "github.com/Mundo-Dolphins/phins-news-engine/infra/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/fetcherwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguecatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/logging"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Options carries everything a host program injects into the service.
type Options struct {
	// ServiceName identifies this service instance: it is the owner
	// recorded on outbox claims and the name in the start/stop log lines.
	// Empty means DefaultServiceName.
	ServiceName string
	Fetchers    fetcherwiring.Extensions
	// ChannelImporter builds the optional Telegram history client; nil result = reconciliation by history unavailable.
	ChannelImporter func(cfg *config.Config, logger out.Logger) out.TelegramChannelImporter
}

// DefaultServiceName is the service name used when Options.ServiceName is
// empty.
const DefaultServiceName = "phins-news-engine"

// serviceName returns ServiceName, or DefaultServiceName when it is blank.
func (o Options) serviceName() string {
	if name := strings.TrimSpace(o.ServiceName); name != "" {
		return name
	}
	return DefaultServiceName
}

// Run is the scheduler service: it loads configPath, wires every adapter and
// use case, and blocks running the scheduler until ctx is cancelled (a
// cancellation is a clean shutdown and returns nil).
func Run(ctx context.Context, configPath string, profile config.Profile, logger *logging.Logger, opts Options) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	articles := sqliteadapter.NewArticleRepository(db)
	dedup := sqliteadapter.NewDeduplicationRepository(db)
	aliases := sqliteadapter.NewAliasRepository(db)
	seenItems := sqliteadapter.NewSeenItemsRepository(db)
	similarityIndex := sqliteadapter.NewSimilarityIndexRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	checkpointer := sqliteadapter.NewCheckpointer(db)

	realClock := clock.Real{}

	sourcesCatalog, err := sourcesfile.Load(cfg.Fetch.SourcesPath)
	if err != nil {
		return fmt.Errorf("load sources: %w", err)
	}

	fetcher, capabilityReport, err := fetcherwiring.Build(cfg, sourcesCatalog, profile, realClock, logger, opts.Fetchers)
	if err != nil {
		return err
	}
	logger.Info("capabilities",
		"active", capabilityReport.Active(),
		"inactive", capabilityReport.Inactive(),
	)
	publisher := telegram.New(telegram.Config{
		ChannelID: cfg.Telegram.ChannelID,
		BotToken:  cfg.Telegram.BotToken,
		Timeout:   cfg.Telegram.Timeout(),
	})

	// channelImporter is only built when reconciliation is enabled, and only
	// by the host-supplied Options.ChannelImporter (which owns the session
	// file checks, the non-interactive client construction and their warning
	// logs). Reconciliation disabled, no builder, or a nil result all leave
	// channelImporter nil, which PublishOutbox treats as "reconciliation-by-
	// history unavailable" rather than an error: ambiguous sent_unconfirmed
	// items with no message_id simply quarantine immediately instead of
	// being cross-checked against channel history.
	var channelImporter out.TelegramChannelImporter
	if cfg.Publication.Reconciliation.Enabled && opts.ChannelImporter != nil {
		channelImporter = opts.ChannelImporter(cfg, logger)
	}

	var matcher dictionary.Matcher
	if cfg.Classification.Enabled {
		dict, err := dictionaryfile.Load(cfg.Dictionary.Path)
		if err != nil {
			return fmt.Errorf("load dictionary: %w", err)
		}
		for _, issue := range dictionary.Validate(dict) {
			if issue.Severity == dictionary.SeverityError {
				return fmt.Errorf("dictionary %s failed validation: %s", cfg.Dictionary.Path, issue)
			}
		}
		if cfg.Dictionary.Version != "" && cfg.Dictionary.Version != dict.Version {
			return fmt.Errorf("dictionary %s is version %s, want pinned version %s", cfg.Dictionary.Path, dict.Version, cfg.Dictionary.Version)
		}

		matcher, err = dictionary.Prepare(dict)
		if err != nil {
			return fmt.Errorf("prepare dictionary: %w", err)
		}

		entryCount := 0
		for _, cat := range dict.Categories {
			entryCount += len(cat.Entries)
		}
		dictionaryRegistry := sqliteadapter.NewDictionaryRegistryRepository(db)
		if err := dictionaryRegistry.Register(ctx, dictionary.Registration{
			Version: dict.Version, Path: cfg.Dictionary.Path, EntryCount: entryCount, PromotedAt: realClock.Now(),
		}); err != nil {
			return fmt.Errorf("register dictionary: %w", err)
		}
		logger.Info("dictionary loaded", "version", dict.Version, "categories", len(dict.Categories), "entries", entryCount)
	}

	var editorialModel *editorial.Model
	if cfg.Editorial.Mode != "disabled" {
		// Fail-fast: unlike some other optional-artifact blocks in this
		// function, a missing/corrupt/inconsistent promoted model must
		// abort startup rather than silently degrade, since editorial.mode
		// being non-"disabled" means the operator has explicitly opted
		// into editorial gating as a production control.
		manifest, featureSchema, _, err := editorialmodel.Inspect(cfg.Model.Path)
		if err != nil {
			return fmt.Errorf("load editorial model (required for editorial.mode=%s): %w", cfg.Editorial.Mode, err)
		}
		m, err := editorialmodel.Load(cfg.Model.Path)
		if err != nil {
			return fmt.Errorf("load editorial model (required for editorial.mode=%s): %w", cfg.Editorial.Mode, err)
		}
		if err := editorialmodel.Validate(m, manifest, featureSchema); err != nil {
			return fmt.Errorf("editorial model at %s failed validation (required for editorial.mode=%s): %w", cfg.Model.Path, cfg.Editorial.Mode, err)
		}
		if cfg.Model.Version != "" && cfg.Model.Version != m.Version {
			return fmt.Errorf("editorial model %s is version %s, want pinned version %s", cfg.Model.Path, m.Version, cfg.Model.Version)
		}
		m.Checksum = manifest.ArtifactHash
		editorialModel = &m

		modelRegistry := sqliteadapter.NewModelRegistryRepository(db)
		if err := modelRegistry.Register(ctx, training.ModelMetadata{
			Version: m.Version, Kind: "editorial-classifier", Path: cfg.Model.Path,
			Algorithm: m.Algorithm, FeatureSchemaVersion: m.FeatureSchemaVersion,
			Status: "promoted", PromotedAt: realClock.Now(),
		}); err != nil {
			return fmt.Errorf("register editorial model: %w", err)
		}
		logger.Info("editorial model loaded and validated", "version", m.Version, "algorithm", m.Algorithm, "mode", cfg.Editorial.Mode, "checksum", m.Checksum)
	}

	var editorialVetoCfg *editorialveto.Config
	if cfg.Editorial.VetoPath != "" {
		v, err := infraeditorialveto.Load(cfg.Editorial.VetoPath)
		if err != nil {
			return fmt.Errorf("load deterministic editorial veto rules: %w", err)
		} else {
			editorialVetoCfg = &v
			logger.Info("editorial veto rules loaded", "version", v.Version, "rules", len(v.Rules))
		}
	}

	cascade := cfg.DedupCascade()
	if cfg.Clustering.Enabled {
		rulesFile, err := clusteringrules.LoadWithEntityCatalog(
			cfg.Clustering.RulesPath, cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath,
		)
		if err != nil {
			return fmt.Errorf("load clustering rules: %w", err)
		}
		rulesFile, genWarnings, err := clusteringrules.LoadWithGenerated(rulesFile, cfg.Clustering.GeneratedRulesPath)
		if err != nil {
			return fmt.Errorf("load generated clustering rules: %w", err)
		}
		for _, w := range genWarnings {
			logger.Warn("clustering: " + w)
		}
		if path := cfg.Clustering.GeneratedRulesPath; path != "" {
			missing, merged, err := generatedRulesNotice(path, genWarnings)
			if err != nil {
				return fmt.Errorf("load generated clustering rules: %w", err)
			}
			if missing {
				logger.Warn("clustering: generated rules file not found; no generated rules merged (check clustering.generated_rules_path)", "path", path)
			} else {
				logger.Info("generated clustering rules merged", "path", path, "phrases", merged)
			}
		}
		if cfg.Clustering.RulesVersion != "" && cfg.Clustering.RulesVersion != rulesFile.Version {
			return fmt.Errorf("clustering rules %s is version %s, want pinned version %s", cfg.Clustering.RulesPath, rulesFile.Version, cfg.Clustering.RulesVersion)
		}
		cascade.Cluster = buildClusterSettings(rulesFile, cfg.Clustering.CandidateLimit)
		if lp := leaguecatalog.FromConfig(cfg.Clustering.EntityCatalog.League, cascade.Cluster.Rules.Entities, logger); lp != nil {
			cascade.Cluster.LeagueEntities = lp
			logger.Info("league entities enabled", "mode", cfg.Clustering.EntityCatalog.League.Mode)
		}
		logger.Info("clustering rules loaded", "version", rulesFile.Version, "entities", len(rulesFile.Entities))
	}

	// sourcePolicies is wired to the real SQLite adapter, and
	// sourcePolicyResolver is built from configs/source-policies.yml. This
	// load happens unconditionally because source-policy resolution applies
	// to every article regardless of which fetcher produced it.
	sourcePolicies := sqliteadapter.NewSourcePolicyRepository(db)
	sourcePolicyFile, err := sourcepolicyfile.Load(cfg.SourcePolicy.Path)
	if err != nil {
		return fmt.Errorf("load source policies: %w", err)
	}

	if result := usecase.ValidateSourcePolicies(sourcesCatalog, sourcePolicyFile); len(result.Issues) > 0 {
		for _, issue := range result.Issues {
			logger.Error("source policy validation issue", nil, "issue", issue)
		}
		return fmt.Errorf("source policy validation failed: %d issue(s) found", len(result.Issues))
	}

	var sourcePolicyResolver sourcepolicy.Resolver = sourcepolicyfile.NewResolver(sourcePolicyFile)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	unknownEntities := sqliteadapter.NewUnknownEntityRepository(db)
	var entityFallback *entityresolution.Deps
	if cfg.EntityExtraction.Fallback.Enabled {
		resolver := llmclient.NewResolver(llmclient.Config{
			BaseURL:     cfg.EntityExtraction.Fallback.BaseURL,
			APIKey:      cfg.EntityExtraction.Fallback.APIKey,
			Model:       cfg.EntityExtraction.Fallback.Model,
			Temperature: cfg.EntityExtraction.Fallback.Temperature,
			MaxTokens:   cfg.EntityExtraction.Fallback.MaxTokens,
			Timeout:     cfg.EntityExtraction.Fallback.Timeout(),
			Logger:      logger,
		})
		entityFallback = &entityresolution.Deps{
			LLM:                     resolver,
			Cache:                   sqliteentityproposal.NewEntityProposalRepository(db),
			MinConfidence:           cfg.EntityExtraction.Fallback.MinConfidence,
			Logger:                  logger,
			Timeout:                 cfg.EntityExtraction.Fallback.Timeout(),
			MaxCandidatesPerArticle: cfg.EntityExtraction.Fallback.MaxCandidatesPerArticle,
		}
		if w := leagueFallbackWarning(cfg); w != "" {
			logger.Warn(w)
		}
		logEntityFallback(logger, cfg)
	}
	processCycle := usecase.NewProcessCycle(
		fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow,
		cfg.Freshness.Policy(), matcher, editorialModel, editorialVetoCfg, cascade, realClock, logger,
		sourcePolicies, sourcePolicyResolver, clusterPublications, seenItems, sourcePolicyCooldowns, unknownEntities,
		entityFallback,
	)
	processCycle.SetLivePolicy(cfg.PublicationPolicy.LivePolicy())
	processCycle.SetRecentStories(similarityIndex)
	if cfg.EspnScoreboard.Enabled {
		processCycle.SetMatchStateProvider(espnscoreboard.NewClient(cfg.EspnScoreboard.TeamAbbreviation, cfg.EspnScoreboard.UserAgent, cfg.EspnScoreboard.Timeout()))
		logger.Info("espn scoreboard match-state provider enabled", "team", cfg.EspnScoreboard.TeamAbbreviation)
	}
	outbox.SetLiveTTL(time.Duration(cfg.PublicationPolicy.Live.TTLMinutes) * time.Minute)
	urlCfg, urlResolvers := cfg.Normalization.URL.Policy()
	publishOutbox := usecase.NewPublishOutbox(
		articles, outbox, decisions, uow, publisher,
		cfg.Publication.Backoff(), cfg.PublicationPolicy.Policy(), opts.serviceName(), cfg.Publication.ClaimBatchSize, cfg.Publication.StaleClaimAfter(),
		realClock, logger, clusterPublications, sourcePolicyCooldowns,
		cfg.Publication.Reconciliation.Settings(cfg.Telegram.ChannelID, urlCfg, urlResolvers), channelImporter,
	)
	runRetention := usecase.NewRunRetention(articles, decisions, outbox, clusters, labels, checkpointer, cfg.Retention.Windows(), realClock, logger)
	closeInactiveClusters := usecase.NewCloseInactiveClusters(clusters, realClock, logger, cfg.ClusterLifecycle.Settings())

	sched := scheduler.New(
		processCycle, publishOutbox, closeInactiveClusters, runRetention,
		cfg.Scheduler.CheckInterval(), cfg.Scheduler.OutboxInterval(), cfg.Scheduler.CleanupInterval(),
		logger,
	)

	logger.Info(
		opts.serviceName()+" starting",
		"sqlite_path", cfg.SQLite.Path,
		"check_interval", cfg.Scheduler.CheckInterval().String(),
		"outbox_interval", cfg.Scheduler.OutboxInterval().String(),
		"cleanup_interval", cfg.Scheduler.CleanupInterval().String(),
	)

	if err := sched.Run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Info(opts.serviceName() + " shutting down")
			return nil
		}
		return fmt.Errorf("scheduler: %w", err)
	}
	return nil
}

// buildClusterSettings converts a loaded clusteringrules.RulesFile into the
// in.ClusterSettings ProcessCycle needs. The Representative field's 6-of-9
// filler weights and its ClickbaitPenaltyWeight sign fix live in
// clusteringrules.RulesFile.ToRepresentativeConfig — the one place both
// this binary and the tools CLI's offline "clustering evaluate"
// command (Task 9) build a clustering.RepresentativeConfig from a RulesFile,
// so the two can never drift apart. See ToRepresentativeConfig's doc
// comment for the full known-gap explanation.
func buildClusterSettings(rf clusteringrules.RulesFile, candidateLimit int) in.ClusterSettings {
	return in.ClusterSettings{
		Enabled:        true,
		Rules:          rf.ToEventSignalsRules(),
		Decide:         rf.ToClusteringConfig(),
		RulesVersion:   rf.Version,
		Representative: rf.ToRepresentativeConfig(),
		CandidateLimit: candidateLimit,
	}
}

// infoWarnLogger is the part of *logging.Logger that logEntityFallback uses.
type infoWarnLogger interface {
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
}

// logEntityFallback reports the enabled LLM entity fallback. The base URL
// goes through netguard.RedactURL: it is operator configuration and may
// carry userinfo or a key in its query string.
func logEntityFallback(logger infoWarnLogger, cfg *config.Config) {
	baseURL := netguard.RedactURL(cfg.EntityExtraction.Fallback.BaseURL)
	logger.Info("online entity resolution fallback enabled", "base_url", baseURL, "model", cfg.EntityExtraction.Fallback.Model)
	if cfg.EntityExtraction.Fallback.APIKey == "" {
		logger.Warn("online entity resolution fallback enabled with no API key configured — requests will likely fail authentication", "base_url", baseURL)
	}
}

// leagueFallbackWarning explains a known interaction: league players are
// "known" names, so with both features on the online fallback is no longer asked
// about them and may resolve fewer candidates than it would without the league
// entities. That is the intended call-saving effect, but it means the "league
// entities change nothing" guarantee does not hold while the fallback is on.
func leagueFallbackWarning(cfg *config.Config) string {
	if cfg.EntityExtraction.Fallback.Enabled && cfg.Clustering.EntityCatalog.League.Enabled() {
		return "league entities and the online entity fallback are both enabled: league players are no longer sent to the fallback, so representations can differ from a run without league entities"
	}
	return ""
}
