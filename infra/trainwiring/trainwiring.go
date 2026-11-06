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
	"math"
	"strconv"
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
	"github.com/Mundo-Dolphins/phins-news-engine/app/shadowcompare"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainmine"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainrun"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
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

// ExcludedSources is the set of first-party official sources kept out of
// Naive Bayes training and datasets: model_bypass_source_ids united with
// unconditional_publish_source_ids.
func ExcludedSources(cfg *config.Config) map[string]bool {
	excluded := make(map[string]bool, len(cfg.Editorial.ModelBypassSourceIDs)+len(cfg.Editorial.UnconditionalPublishSourceIDs))
	for _, id := range cfg.Editorial.ModelBypassSourceIDs {
		excluded[id] = true
	}
	for _, id := range cfg.Editorial.UnconditionalPublishSourceIDs {
		excluded[id] = true
	}
	return excluded
}

// BuildFlags are the operator-supplied knobs of one `train build`.
type BuildFlags struct {
	OutPath       string
	MaxPerCluster int
	IncludeDB     []string
}

// BuildReport describes the dataset BuildDataset wrote.
type BuildReport struct {
	OutPath             string
	Examples            int
	Gold, Silver        int
	Dropped             int // examples removed by the per-cluster cap
	UnresolvedConflicts int // articles excluded because of an unresolved legacy label conflict
}

// BuildDataset assembles the Gold+Silver training dataset from db (plus the
// read-only snapshots in f.IncludeDB), writes it and its manifest to
// f.OutPath and reports what it wrote. It does not migrate db.
func BuildDataset(ctx context.Context, db *sql.DB, cfg *config.Config, f BuildFlags, now time.Time) (BuildReport, error) {
	if cfg.OfflineTraining.LLM.AutoLabelThreshold <= 0 {
		return BuildReport{}, fmt.Errorf("editorialtrain: offline_training.llm.auto_label_threshold must be configured above 0 before Silver examples can be included in a training dataset")
	}
	labelRepo := sqliteadapter.NewHumanLabelRepository(db)
	articleRepo := sqliteadapter.NewArticleRepository(db)
	proposalRepo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db)

	trustedSourceIDs := make(map[string]bool, len(cfg.Classification.TrustedSourceIDs))
	for _, id := range cfg.Classification.TrustedSourceIDs {
		trustedSourceIDs[id] = true
	}
	excludedSources := ExcludedSources(cfg)

	gold, err := editorialtrain.ExportDataset(ctx, labelRepo, trustedSourceIDs, excludedSources)
	if err != nil {
		return BuildReport{}, err
	}
	goldGroups := [][]editorialtrain.TrainingExample{gold}
	for _, path := range f.IncludeDB {
		archive, openErr := infrasqlite.OpenReadOnly(path, cfg.SQLite.BusyTimeout())
		if openErr != nil {
			return BuildReport{}, openErr
		}
		archiveLabels := sqliteadapter.NewHumanLabelRepository(archive)
		archived, exportErr := editorialtrain.ExportDataset(ctx, archiveLabels, trustedSourceIDs, excludedSources)
		closeErr := archive.Close()
		if exportErr != nil {
			return BuildReport{}, exportErr
		}
		if closeErr != nil {
			return BuildReport{}, closeErr
		}
		goldGroups = append(goldGroups, archived)
	}
	gold, err = editorialtrain.MergeTrainingExamples(goldGroups...)
	if err != nil {
		return BuildReport{}, err
	}
	silver, skippedMissingArticle, err := editorialtrain.FetchSilverExamples(
		ctx, proposalRepo, labelRepo, articleRepo, trustedSourceIDs, cfg.OfflineTraining.LLM.AutoLabelThreshold,
	)
	if err != nil {
		return BuildReport{}, err
	}
	silver = (editorialtrain.Dataset{Examples: silver}).ExcludeSources(excludedSources).Examples

	examples := editorialtrain.CapPerCluster(append(gold, silver...), f.MaxPerCluster)
	if len(examples) == 0 {
		return BuildReport{}, fmt.Errorf("editorialtrain: no Gold or Silver examples found — use `label` to add Gold examples, or `editorial classify` to generate Silver proposals, first")
	}
	if err := editorialtrain.SaveDataset(f.OutPath, examples); err != nil {
		return BuildReport{}, err
	}
	unresolved, err := labelRepo.CountUnresolvedConflicts(ctx)
	if err != nil {
		return BuildReport{}, err
	}
	reloaded, err := editorialtrain.LoadDataset(f.OutPath)
	if err != nil {
		return BuildReport{}, err
	}
	if err := editorialtrain.WriteManifest(f.OutPath, examples, reloaded.Hash(), now, unresolved, skippedMissingArticle); err != nil {
		return BuildReport{}, err
	}
	return BuildReport{
		OutPath: f.OutPath, Examples: len(examples), Gold: len(gold), Silver: len(silver),
		Dropped: len(gold) + len(silver) - len(examples), UnresolvedConflicts: unresolved,
	}, nil
}

// EvaluateFlags are the operator-supplied knobs of one `train evaluate`.
type EvaluateFlags struct {
	DatasetPath, OutDir                          string
	Algorithms                                   []string
	Seed                                         int64
	AcceptTargetPrecision, RejectTargetPrecision float64
	NearDupThreshold                             float64
	CostGrid                                     string // comma-separated target precisions; empty disables the sweep
}

// CostGridWinner is the cost-grid sweep outcome for one algorithm: the
// accept/reject pair minimizing expected cost among those clearing the
// promotion gates, or Found=false when none does.
type CostGridWinner struct {
	Algorithm      string
	Found          bool
	Accept, Reject float64
	ExpectedCost   float64
}

// EvaluateReport is what Evaluate fitted: the dataset counts, the current
// promoted model's report (nil when none) and one Result per candidate.
type EvaluateReport struct {
	ExcludedOfficial     int // examples dropped as first-party official sources
	UnparseableSourceIDs int // dataset rows whose source_id does not parse
	DatasetSize          int // examples in the dataset before exclusion
	Promoted             *editorialtrain.Report
	Results              []modeltraining.Result
	CostGrid             []CostGridWinner
	OutDir               string
}

// Evaluate fits and evaluates every requested candidate algorithm from the
// built dataset, saving and registering each under f.OutDir. It does not
// migrate db.
func Evaluate(ctx context.Context, db *sql.DB, cfg *config.Config, f EvaluateFlags, now time.Time) (EvaluateReport, error) {
	var report EvaluateReport
	dataset, err := editorialtrain.LoadDataset(f.DatasetPath)
	if err != nil {
		return report, err
	}
	report.UnparseableSourceIDs = dataset.UnparseableSourceIDCount
	report.DatasetSize = len(dataset.Examples)
	report.OutDir = f.OutDir

	dataset = dataset.ExcludeSources(ExcludedSources(cfg))
	report.ExcludedOfficial = report.DatasetSize - len(dataset.Examples)

	trainCfg, err := TrainConfig(cfg, fmt.Sprintf("fs-%d", now.UTC().UnixNano()))
	if err != nil {
		return report, err
	}
	trainCfg.Split.NearDupThreshold = f.NearDupThreshold
	trainCfg.Thresholds.AcceptTargetPrecision, trainCfg.Thresholds.RejectTargetPrecision = f.AcceptTargetPrecision, f.RejectTargetPrecision
	trainCfg.Seed = f.Seed
	trainCfg.Algorithms = f.Algorithms
	gateCfg := GateConfig(cfg)
	promoted, err := modeltraining.EvaluatePromotedModel(cfg.Model.Path, dataset, trainCfg)
	if err != nil {
		return report, err
	}
	report.Promoted = promoted

	registry := sqliteadapter.NewModelRegistryRepository(db)
	results, err := modeltraining.RunAndRegister(ctx, modeltraining.Deps{Registry: registry}, dataset, trainCfg, f.OutDir, gateCfg, promoted)
	if err != nil {
		return report, err
	}
	report.Results = results

	if f.CostGrid == "" {
		return report, nil
	}
	if cfg.EditorialCosts.FalsePublishCost <= 0 && cfg.EditorialCosts.MissedNewsCost <= 0 {
		return report, fmt.Errorf("train evaluate: --cost-grid requires editorial_costs to be configured in settings (false_publish_cost and missed_news_cost are both <= 0)")
	}
	values, err := parseCostGrid(f.CostGrid)
	if err != nil {
		return report, err
	}
	_, validate, _ := editorialtrain.SplitForTraining(dataset, trainCfg)
	costs := editorialtrain.CostConfig{FalsePublishCost: cfg.EditorialCosts.FalsePublishCost, MissedNewsCost: cfg.EditorialCosts.MissedNewsCost}
	for _, r := range results {
		if r.Candidate.Model.Algorithm == "trivial_majority" {
			continue
		}
		artifactSize, err := modeltraining.DirSize(r.Dir)
		if err != nil {
			return report, err
		}
		var best editorial.Thresholds
		bestCost := math.Inf(1)
		found := false
		for _, accept := range values {
			for _, reject := range values {
				th := editorialtrain.SelectThresholds(r.Candidate.Model, validate.Examples, editorialtrain.ThresholdConfig{AcceptTargetPrecision: accept, RejectTargetPrecision: reject})
				m := r.Candidate.Model
				m.Thresholds = th
				rep := editorialtrain.Evaluate(m, validate.Examples, nil, cfg.Editorial.AmbiguousPolicy)
				gates := editorialtrain.EvaluateGates(rep, promoted, artifactSize, gateCfg)
				if !gates.Passed {
					continue
				}
				if cost := rep.ExpectedCost(costs); cost < bestCost {
					bestCost, best, found = cost, th, true
				}
			}
		}
		w := CostGridWinner{Algorithm: r.Candidate.Model.Algorithm, Found: found}
		if found {
			w.Accept, w.Reject, w.ExpectedCost = best.Accept, best.Reject, bestCost
		}
		report.CostGrid = append(report.CostGrid, w)
	}
	return report, nil
}

func parseCostGrid(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	values := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, fmt.Errorf("train evaluate: invalid --cost-grid value %q: %w", p, err)
		}
		values = append(values, v)
	}
	return values, nil
}

// Mine scans stored articles for strong NB/LLM disagreement. A positive
// limitOverride replaces offline_training.mining.batch_limit. It does not
// migrate db.
func Mine(ctx context.Context, db *sql.DB, cfg *config.Config, limitOverride int, now time.Time) (trainmine.Result, error) {
	batchLimit := cfg.OfflineTraining.Mining.BatchLimit
	if limitOverride > 0 {
		batchLimit = limitOverride
	}
	llm := llmclient.New(LLMClientConfig(cfg.OfflineTraining.LLM))
	return trainmine.Mine(ctx, cfg.Model.Path,
		sqliteadapter.NewArticleRepository(db), llm, sqliteadapter.NewHardExampleRepository(db),
		trainmine.Options{
			BatchLimit: batchLimit, MinNBConfidence: cfg.OfflineTraining.Mining.MinNBConfidence,
			LLMAutoLabelThreshold: cfg.OfflineTraining.LLM.AutoLabelThreshold,
		}, now)
}

// ShadowCompare scores the last days of stored articles (at most limit) with
// the production model and the candidate in candidateDir, storing every
// disagreement. It does not migrate db.
func ShadowCompare(ctx context.Context, db *sql.DB, cfg *config.Config, candidateDir string, days, limit int, now time.Time) (shadowcompare.Result, error) {
	if days <= 0 {
		return shadowcompare.Result{}, fmt.Errorf("train shadow-compare: --days must be positive")
	}
	if cfg.Model.Path == "" {
		return shadowcompare.Result{}, fmt.Errorf("train shadow-compare: model.path is not configured — there is no production model to compare against")
	}
	production, err := editorialmodel.Load(cfg.Model.Path)
	if err != nil {
		return shadowcompare.Result{}, fmt.Errorf("train shadow-compare: load production model from %s: %w", cfg.Model.Path, err)
	}
	candidate, err := editorialmodel.Load(candidateDir)
	if err != nil {
		return shadowcompare.Result{}, fmt.Errorf("train shadow-compare: load candidate model from %s: %w", candidateDir, err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	shadowRepo := sqliteadapter.NewShadowComparisonRepository(db)
	since := now.AddDate(0, 0, -days)
	return shadowcompare.Run(ctx, articles, shadowRepo, production, candidate, shadowcompare.Options{Since: since, Limit: limit}, now)
}
