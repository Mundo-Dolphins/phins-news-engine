// Package trainwiring converts configuration into the dependencies and
// options of the offline training workflow (train run / promote /
// rollback). It is the single place that wiring lives so the CLI and the
// admin panel cannot drift. It must not import the cli tree: the admin
// binary consumes it without cobra globals.
package trainwiring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/app/retraintrigger"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainrun"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var (
	// ErrNotNaiveBayes is returned (via errors.Is) when a candidate's
	// algorithm is not naive_bayes; production must remain naive_bayes.
	ErrNotNaiveBayes = errors.New("candidate algorithm is not eligible")
	// ErrGatesFailed is returned (via errors.Is) when a candidate fails
	// quality gates and force was not requested.
	ErrGatesFailed = errors.New("candidate fails quality gates")
)

// wiringError carries a precise operator-facing message while matching a
// sentinel via errors.Is.
type wiringError struct {
	msg  string
	kind error
}

func (e *wiringError) Error() string { return e.msg }
func (e *wiringError) Unwrap() error { return e.kind }

// GateConfig converts cfg.EditorialGates into editorialtrain.GateConfig.
func GateConfig(cfg *config.Config) editorialtrain.GateConfig {
	g := cfg.EditorialGates
	return editorialtrain.GateConfig{
		MinPrecision: g.MinPrecision, MaxRecallRegression: g.MaxRecallRegression,
		MinRegressionBaselineExamples: g.MinRegressionBaselineExamples,
		MinPRAUC:                      g.MinPRAUC, MaxFalsePositiveRate: g.MaxFalsePositiveRate,
		MinPerSourcePrecision: g.MinPerSourcePrecision, MinSourceExamples: g.MinSourceExamples,
		ExcludedSources:      g.ExcludedSources,
		MaxArtifactSizeBytes: g.MaxArtifactSizeBytes, MaxInferenceNanos: g.MaxInferenceNanos,
	}
}

// PromotionCriteria converts config.PromotionConfig into
// modelpromotion.PromotionCriteria (application packages never import
// infra/config).
func PromotionCriteria(c config.PromotionConfig) modelpromotion.PromotionCriteria {
	return modelpromotion.PromotionCriteria{
		MinPrecision: c.MinPrecision, MaxPrecisionDrop: c.MaxPrecisionDrop, MinF1Improvement: c.MinF1Improvement,
	}
}

// RetrainTriggerConfig converts config.RetrainTriggersConfig into
// retraintrigger.Config. AutoEnabled is excluded deliberately: callers
// check it against the config before evaluating.
func RetrainTriggerConfig(c config.RetrainTriggersConfig) retraintrigger.Config {
	return retraintrigger.Config{
		MinNewGoldExamples: c.MinNewGoldExamples, MinNewSilverExamples: c.MinNewSilverExamples,
		MaxDisagreementRate: c.MaxDisagreementRate, MaxManualErrorRate: c.MaxManualErrorRate,
		NovelVocabularyThreshold: c.NovelVocabularyThreshold, MinHardExampleCount: c.MinHardExampleCount,
	}
}

// LLMClientConfig builds the LLM teacher client config shared by
// `editorial classify`, `train mine` and `train run` from
// offline_training.llm.
func LLMClientConfig(c config.OfflineTrainingLLMConfig) llmclient.Config {
	out := llmclient.Config{
		BaseURL: c.BaseURL, APIKey: c.APIKey,
		Model: c.Model, Temperature: c.Temperature,
		MaxTokens: c.MaxTokens, Timeout: c.Timeout(),
	}
	if c.DisableThinking {
		out = out.WithThinkingDisabled()
	}
	return out
}

// TrainConfig is the one editorialtrain.TrainConfig every editorial
// training entrypoint starts from, so split, pruning, threshold rule,
// ambiguous policy and the inference-population filter cannot drift.
// Callers only override what is genuinely theirs.
func TrainConfig(cfg *config.Config, schemaVersion string) (editorialtrain.TrainConfig, error) {
	mt := cfg.OfflineTraining.ModelTraining
	tc := editorialtrain.TrainConfig{
		Split: editorialtrain.SplitConfig{
			TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15, NearDupThreshold: 0.8,
			StratifyByLabel: mt.StratifySplitByLabel,
		},
		AmbiguousPolicy: cfg.Editorial.AmbiguousPolicy,
		Prune:           editorialtrain.PruneConfig{MinDocFreq: 0.01, MaxDocFreq: 0.9, MaxFeatures: 2000},
		Thresholds: editorialtrain.ThresholdConfig{
			AcceptTargetPrecision: 0.9, RejectTargetPrecision: 0.9,
			MaxFalsePositiveRate: mt.ThresholdMaxFalsePositiveRate,
		},
		Seed:           42,
		SchemaVersion:  schemaVersion,
		BinaryFeatures: mt.BinaryFeatures,
		SilverWeight:   mt.SilverWeight,
	}
	if mt.RelevantOnly {
		relevant, err := RelevanceFilter(cfg)
		if err != nil {
			return editorialtrain.TrainConfig{}, err
		}
		tc.Relevant = relevant
	}
	return tc, nil
}

// RelevanceFilter reports whether a labeled example would pass
// ProcessCycle's relevance stage with the current dictionary and source
// lists, i.e. whether it would ever reach the editorial classifier in
// production. It mirrors that step's three branches: trusted-source
// bypass, source-context decision, plain decision.
func RelevanceFilter(cfg *config.Config) (func(editorialtrain.TrainingExample) bool, error) {
	d, err := dictionaryfile.Load(cfg.Dictionary.Path)
	if err != nil {
		return nil, fmt.Errorf("editorial relevance filter: load dictionary %s: %w", cfg.Dictionary.Path, err)
	}
	matcher, err := dictionary.Prepare(d)
	if err != nil {
		return nil, fmt.Errorf("editorial relevance filter: prepare dictionary: %w", err)
	}
	cascade := cfg.DedupCascade()
	if !cascade.Classification.Enabled {
		// Relevance gating is off in production, so everything reaches the
		// editorial classifier.
		return func(editorialtrain.TrainingExample) bool { return true }, nil
	}
	textCfg := cascade.Normalization.Text
	trusted, context := cascade.Classification.TrustedSourceIDs, cascade.Classification.ContextSourceIDs

	return func(ex editorialtrain.TrainingExample) bool {
		if trusted[ex.SourceID] {
			return true
		}
		title := normalization.Normalize(ex.Title, textCfg).ForClassification
		body := normalization.Normalize(ex.Body, textCfg).ForClassification
		if context[ex.SourceID] {
			return classification.DecideWithSourceContext(title, body, matcher).Related
		}
		return classification.Decide(title, body, matcher).Related
	}, nil
}

// KnownTerms builds the lowercase word set trainmine.UnknownTerms treats
// as "already known": every canonical name/alias in the classification
// dictionary plus every canonical name/term in the entity catalog, each
// split into words the same way UnknownTerms tokenizes titles. A load
// failure on either source logs a warning and contributes nothing from
// that source; this is a best-effort report nudge, never a reason to
// abort a promotion run.
func KnownTerms(cfg *config.Config) map[string]bool {
	known := map[string]bool{}
	addWords := func(phrase string) {
		for _, w := range strings.FieldsFunc(phrase, func(r rune) bool { return !unicode.IsLetter(r) }) {
			known[strings.ToLower(w)] = true
		}
	}

	if cfg.Dictionary.Path != "" {
		dict, err := dictionaryfile.Load(cfg.Dictionary.Path)
		if err != nil {
			log.Printf("train run: load dictionary %s for known-terms nudge: %v (continuing without it)", cfg.Dictionary.Path, err)
		} else {
			for _, cat := range dict.Categories {
				for _, e := range cat.Entries {
					addWords(e.CanonicalName)
					for _, alias := range e.Aliases {
						addWords(alias)
					}
				}
			}
		}
	}

	if cfg.Clustering.EntityCatalog.ManualPath != "" && cfg.Clustering.EntityCatalog.GeneratedPath != "" {
		entities, err := entitycatalog.Load(cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath)
		if err != nil {
			log.Printf("train run: load entity catalog for known-terms nudge: %v (continuing without it)", err)
		} else {
			for _, e := range entities {
				addWords(e.CanonicalName)
				for _, term := range e.Terms {
					addWords(term)
				}
			}
		}
	}

	return known
}

// RunDeps wires the repositories and LLM teacher `train run` needs.
// archived are read-only human-label repositories merged into Gold.
func RunDeps(db *sql.DB, cfg *config.Config, archived []out.HumanLabelRepository) trainrun.Deps {
	return trainrun.Deps{
		Registry:       sqliteadapter.NewModelRegistryRepository(db),
		RunRepo:        sqliteadapter.NewTrainingRunRepository(db),
		UOW:            sqliteadapter.NewUnitOfWork(db),
		Labels:         sqliteadapter.NewHumanLabelRepository(db),
		Articles:       sqliteadapter.NewArticleRepository(db),
		Proposals:      sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db),
		Queue:          sqlitereviewqueue.NewReviewQueueRepository(db),
		ArchivedLabels: archived,
		LLM:            llmclient.New(LLMClientConfig(cfg.OfflineTraining.LLM)),
		HardExamples:   sqliteadapter.NewHardExampleRepository(db),
	}
}

// RunFlags are the operator-supplied knobs of one `train run`.
type RunFlags struct {
	DatasetPath, CandidatesDir string
	MaxPerCluster              int
	Force, Auto                bool
}

// RunOptions builds trainrun.Options from cfg and flags. The teacher is
// deliberately offline-only: its output may improve a candidate dataset,
// but the production model remains Naive Bayes, so Algorithms is pinned
// to naive_bayes. The feature schema version is fs-<now.UnixNano()>.
func RunOptions(cfg *config.Config, f RunFlags, now time.Time) (trainrun.Options, error) {
	trustedSourceIDs := make(map[string]bool, len(cfg.Classification.TrustedSourceIDs))
	for _, id := range cfg.Classification.TrustedSourceIDs {
		trustedSourceIDs[id] = true
	}
	trainCfg, err := TrainConfig(cfg, fmt.Sprintf("fs-%d", now.UnixNano()))
	if err != nil {
		return trainrun.Options{}, err
	}
	trainCfg.Algorithms = []string{"naive_bayes"}
	return trainrun.Options{
		DatasetPath: f.DatasetPath, MaxPerCluster: f.MaxPerCluster, CandidatesDir: f.CandidatesDir,
		ModelPath: cfg.Model.Path, TrustedSourceIDs: trustedSourceIDs,
		AutoLabelThreshold: cfg.OfflineTraining.LLM.AutoLabelThreshold,
		TrainCfg:           trainCfg,
		GateCfg:            GateConfig(cfg),
		PromotionCfg:       PromotionCriteria(cfg.OfflineTraining.Promotion),
		Force:              f.Force, Auto: f.Auto,
		MiningBatchLimit: cfg.OfflineTraining.Mining.BatchLimit, MiningMinNBConfidence: cfg.OfflineTraining.Mining.MinNBConfidence,
		KnownTerms: KnownTerms(cfg),
	}, nil
}

// EvaluateAuto gathers the auto-retrain signals and evaluates the
// configured triggers. When triggered, reasons lists the fired criteria.
func EvaluateAuto(ctx context.Context, deps trainrun.Deps, opts trainrun.Options, cfg *config.Config) (triggered bool, reasons []string, err error) {
	signals, err := trainrun.GatherAutoSignals(ctx, deps, opts)
	if err != nil {
		return false, nil, err
	}
	result := retraintrigger.Evaluate(RetrainTriggerConfig(cfg.OfflineTraining.RetrainTriggers), signals)
	return result.Triggered, result.Reasons, nil
}

// PromoteReport describes what PromoteCandidate found and did, so callers
// can render it. It is returned even alongside an error, filled as far as
// the operation got.
type PromoteReport struct {
	Version           string
	GateFailures      []string
	Forced            bool
	VocabularyOverlap *float64 // Jaccard in [0,1]; nil when no promoted model exists
	Added, Removed    int
}

// PromoteCandidate gate-checks candidateDir and promotes it to
// cfg.Model.Path. version defaults to the candidate's manifest version.
// It does not migrate db. Gate failures abort with ErrGatesFailed unless
// force is set; a non-naive_bayes candidate fails with ErrNotNaiveBayes.
func PromoteCandidate(ctx context.Context, db *sql.DB, cfg *config.Config, candidateDir, version string, force bool) (PromoteReport, error) {
	var report PromoteReport
	if cfg.Model.Path == "" {
		return report, fmt.Errorf("train promote: model.path is not configured")
	}
	manifest, schema, _, err := editorialmodel.Inspect(candidateDir)
	if err != nil {
		return report, err
	}
	if manifest.Algorithm != "naive_bayes" {
		return report, &wiringError{
			msg:  fmt.Sprintf("train promote: candidate algorithm %q is not eligible; production classifier must remain naive_bayes", manifest.Algorithm),
			kind: ErrNotNaiveBayes,
		}
	}

	promoted := modeltraining.LoadPromotedReport(cfg.Model.Path)
	gateResult, err := modelpromotion.CheckGates(candidateDir, GateConfig(cfg), PromotionCriteria(cfg.OfflineTraining.Promotion), promoted)
	if err != nil {
		return report, err
	}
	if !gateResult.Passed {
		report.GateFailures = gateResult.Failures
		if !force {
			return report, &wiringError{
				msg:  fmt.Sprintf("train promote: candidate fails %d gate(s); re-run with --force to override", len(gateResult.Failures)),
				kind: ErrGatesFailed,
			}
		}
		report.Forced = true
	}

	report.Version = version
	if report.Version == "" {
		report.Version = manifest.Version
	}

	if _, promotedSchema, _, err := editorialmodel.Inspect(cfg.Model.Path); err == nil {
		jaccard, added, removed := editorialtrain.SchemaOverlap(
			editorialtrain.FeatureSchema{Version: schema.Version, Features: schema.Features},
			editorialtrain.FeatureSchema{Version: promotedSchema.Version, Features: promotedSchema.Features},
		)
		report.VocabularyOverlap, report.Added, report.Removed = &jaccard, added, removed
	}

	deps := modelpromotion.Deps{Registry: sqliteadapter.NewModelRegistryRepository(db), UOW: sqliteadapter.NewUnitOfWork(db)}
	if err := modelpromotion.Promote(ctx, deps, candidateDir, report.Version, cfg.Model.Path); err != nil {
		return report, fmt.Errorf("train promote: %w", err)
	}
	return report, nil
}

// Rollback restores the previous promoted model slot. It does not migrate db.
func Rollback(ctx context.Context, db *sql.DB, cfg *config.Config) error {
	if cfg.Model.Path == "" {
		return fmt.Errorf("train rollback: model.path is not configured")
	}
	deps := modelpromotion.Deps{Registry: sqliteadapter.NewModelRegistryRepository(db), UOW: sqliteadapter.NewUnitOfWork(db)}
	if err := modelpromotion.Rollback(ctx, deps, cfg.Model.Path); err != nil {
		return fmt.Errorf("train rollback: %w", err)
	}
	return nil
}
