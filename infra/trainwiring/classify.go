package trainwiring

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialreview"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialteacher"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewpriority"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
)

var (
	// ErrReviewQueueSync is matched (via errors.Is) by a Classify error
	// raised by the review-queue sync, after the LLM classification
	// itself succeeded: the returned report's Result is then valid.
	ErrReviewQueueSync = errors.New("review queue sync failed")
	// ErrWriteMisses is matched (via errors.Is) by a ClassifyEval error
	// raised while writing the misses CSV, after the evaluation itself
	// succeeded: the returned report is then complete except for
	// MissesWritten.
	ErrWriteMisses = errors.New("write eval misses failed")
)

// stageError keeps the underlying error's message and chain while also
// matching a stage sentinel via errors.Is.
type stageError struct {
	stage error
	err   error
}

func (e *stageError) Error() string   { return e.err.Error() }
func (e *stageError) Unwrap() []error { return []error{e.stage, e.err} }

// Default classify sizes when neither the run nor offline_training.llm
// sets one (they match the YAML defaults).
const (
	defaultClassifyBatchSize = 20
	defaultClassifyLimit     = 500
	// classifyAllLimit is the effective article cap of ClassifyFlags.All.
	classifyAllLimit = 10_000_000
)

// ClassifyFlags are the inputs of one `editorial classify` run.
type ClassifyFlags struct {
	// Since is the lower fetched-at bound (zero = none).
	Since time.Time
	// All considers every stored article, with no Limit cap.
	All bool
	// Force reprocesses articles already marked processed for the
	// current prompt version.
	Force bool
	// BatchSize and Limit: 0 = offline_training.llm value, else 20 / 500.
	BatchSize, Limit int
	// BaseURL and Model override offline_training.llm when non-empty.
	BaseURL, Model string
	// MaxTokens, when non-nil, overrides offline_training.llm.max_tokens.
	MaxTokens *int
	// DisableThinking, when non-nil, overrides
	// offline_training.llm.disable_thinking.
	DisableThinking *bool
}

// ClassifyReport is everything a classify run reports.
type ClassifyReport struct {
	// Result is the LLM classification outcome; failed batches are
	// counted (with their errors), not returned as an error.
	Result editorialteacher.ClassifyResult
	// SyncSkipped is the reason the review-queue sync was skipped, or ""
	// when it ran.
	SyncSkipped string
	// Sync is the review-queue sync outcome, nil when it was skipped.
	Sync *editorialreview.SyncResult
}

// ClassifyLLMConfig applies the run's LLM overrides to
// offline_training.llm and checks the connection fields a classify run
// needs. It touches neither the DB nor the network; Classify runs it
// first.
func ClassifyLLMConfig(cfg *config.Config, f ClassifyFlags) (config.OfflineTrainingLLMConfig, error) {
	llmCfg := cfg.OfflineTraining.LLM
	if f.BaseURL != "" {
		llmCfg.BaseURL = f.BaseURL
	}
	if f.Model != "" {
		llmCfg.Model = f.Model
	}
	if f.MaxTokens != nil {
		llmCfg.MaxTokens = *f.MaxTokens
	}
	if f.DisableThinking != nil {
		llmCfg.DisableThinking = *f.DisableThinking
	}

	// OfflineTrainingConfig.Validate() intentionally checks only
	// AutoLabelThreshold (see config.go) — the LLM connection
	// fields below are required only when this command actually
	// runs, so this run performs its own explicit checks
	// rather than relying on a global Validate() to cover them.
	if llmCfg.BaseURL == "" {
		return llmCfg, errors.New("offline_training.llm.base_url is required (or pass --llm-base-url)")
	}
	if llmCfg.Model == "" {
		return llmCfg, errors.New("offline_training.llm.model is required (or pass --llm-model)")
	}
	if llmCfg.TimeoutSeconds <= 0 {
		return llmCfg, errors.New("offline_training.llm.timeout_seconds must be positive")
	}
	if err := cfg.OfflineTraining.Validate(); err != nil {
		return llmCfg, err
	}
	return llmCfg, nil
}

// Classify runs the LLM teacher over stored articles, saves its label
// proposals, then syncs the review queue. The DB must already be
// migrated. Failed LLM batches are not an error: they are counted in
// Result.BatchesFailed (with Result.BatchErrors) and the proposals of the
// other batches stay saved. A review-queue sync failure is returned as
// an error matching ErrReviewQueueSync, with Result still filled in.
func Classify(ctx context.Context, db *sql.DB, cfg *config.Config, f ClassifyFlags, now time.Time) (ClassifyReport, error) {
	var rep ClassifyReport
	llmCfg, err := ClassifyLLMConfig(cfg, f)
	if err != nil {
		return rep, err
	}

	// A run value wins; otherwise the configured value, otherwise the
	// default — so offline_training.llm.batch_size/limit are never
	// silently ignored.
	batchSize, limit := f.BatchSize, f.Limit
	if batchSize == 0 {
		batchSize = defaultClassifyBatchSize
		if llmCfg.BatchSize > 0 {
			batchSize = llmCfg.BatchSize
		}
	}
	if limit == 0 {
		limit = defaultClassifyLimit
		if llmCfg.Limit > 0 {
			limit = llmCfg.Limit
		}
	}

	articlesRepo := sqliteadapter.NewArticleRepository(db)
	proposalsRepo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(db)
	llm := llmclient.New(LLMClientConfig(llmCfg))

	opts := editorialteacher.ClassifyOptions{
		Since: f.Since, BatchSize: batchSize, Limit: limit,
		Model: llmCfg.Model, Force: f.Force,
	}
	if f.All {
		opts.Limit = classifyAllLimit
	}

	result, err := editorialteacher.Classify(ctx, articlesRepo, llm, proposalsRepo, opts, now)
	if err != nil {
		return rep, err
	}
	rep.Result = result

	// The review-priority sync needs a promoted editorial model
	// to derive each article's NB zone/thresholds. That model
	// artifact is optional when editorial classification is
	// disabled (cfg.Editorial.Mode == "disabled"), matching the
	// guard in engine/run.go -- so skip the sync
	// entirely in that case, and also degrade gracefully (skip,
	// don't fail) if the model fails to load for any other
	// reason: the LLM-classification work above
	// already succeeded and already spent the LLM calls, so
	// failing here would be wasteful and confusing.
	if cfg.Editorial.Mode == "disabled" {
		rep.SyncSkipped = "editorial.mode is disabled (no promoted editorial model expected)"
		return rep, nil
	}
	editorialModel, loadErr := editorialmodel.Load(cfg.Model.Path)
	if loadErr != nil {
		rep.SyncSkipped = fmt.Sprintf("failed to load editorial model at %s: %v", cfg.Model.Path, loadErr)
		return rep, nil
	}
	sourcesRepo := sqliteadapter.NewSourceRepository(db)
	signalsRepo := sqlitereviewqueue.NewReviewSignalsRepository(db)
	queueRepo := sqlitereviewqueue.NewReviewQueueRepository(db)
	hardExamplesRepo := sqliteadapter.NewHardExampleRepository(db)
	syncResult, err := editorialreview.Sync(ctx, proposalsRepo, articlesRepo, sourcesRepo, signalsRepo, queueRepo, hardExamplesRepo, editorialModel, reviewScoringConfig(cfg.OfflineTraining.ReviewScoring), now)
	if err != nil {
		return rep, &stageError{stage: ErrReviewQueueSync, err: fmt.Errorf("sync review queue: %w", err)}
	}
	rep.Sync = &syncResult
	return rep, nil
}

// reviewScoringConfig converts offline_training.review_scoring into
// reviewpriority.Config.
func reviewScoringConfig(rs config.ReviewScoringConfig) reviewpriority.Config {
	return reviewpriority.Config{
		Weights: reviewpriority.Weights{
			Disagreement: rs.Weights.Disagreement, NBUncertainty: rs.Weights.NBUncertainty,
			LLMUncertainty: rs.Weights.LLMUncertainty, NovelVocabulary: rs.Weights.NovelVocabulary,
			UnknownEntities: rs.Weights.UnknownEntities, NewSource: rs.Weights.NewSource, Recency: rs.Weights.Recency,
			HardMining: rs.Weights.HardMining,
		},
		LLMConfidenceAutoLabelThreshold: rs.LLMConfidenceAutoLabelThreshold,
		LLMConfidenceUncertainThreshold: rs.LLMConfidenceUncertainThreshold,
		NewSourceWindowHours:            rs.NewSourceWindowHours,
		RecencyWindowHours:              rs.RecencyWindowHours,
	}
}

// ClassifyEvalFlags are the inputs of one `editorial classify-eval` run.
type ClassifyEvalFlags struct {
	// DatasetPath is the dataset with human labels to evaluate against.
	DatasetPath string
	// Fold is dev, holdout or all.
	Fold string
	// PolicyFile, when non-empty, replaces the embedded publication policy.
	PolicyFile string
	// MissesOut, when non-empty, is the CSV the confident discards of
	// human publishes are written to.
	MissesOut string
	// BaseURL and Model override offline_training.llm when non-empty.
	BaseURL, Model string
	// Sample evaluates a random sample of this many examples (0 = all).
	Sample int
	// BatchSize is the articles per LLM request: 0 = offline_training.llm
	// value, else 20.
	BatchSize int
	// Seed seeds Sample.
	Seed int64
	// Threshold is the confidence at or above which a discard is confident.
	Threshold float64
	// IncludeImported also evaluates telegramimport labels.
	IncludeImported bool
}

// ClassifyEvalReport is everything a classify-eval run reports.
type ClassifyEvalReport struct {
	// Examples is the number of examples evaluated.
	Examples int
	// Fold is the fold evaluated.
	Fold string
	// Model is the LLM model used.
	Model string
	// Report is the raw evaluation.
	Report editorialteacher.EvalReport
	// Summary is Report summarized at the run's threshold.
	Summary editorialteacher.EvalSummary
	// MissesWritten is the misses CSV path, "" when none was written.
	MissesWritten string
}

// ClassifyEval measures the LLM teacher prompt against human-labeled
// dataset examples without touching the operational database. On an
// evaluation error the report still carries Examples, Fold and Model; on
// a misses-CSV error (matching ErrWriteMisses) it is complete except for
// MissesWritten.
func ClassifyEval(ctx context.Context, cfg *config.Config, f ClassifyEvalFlags) (ClassifyEvalReport, error) {
	var rep ClassifyEvalReport
	llmCfg := cfg.OfflineTraining.LLM
	if f.BaseURL != "" {
		llmCfg.BaseURL = f.BaseURL
	}
	if f.Model != "" {
		llmCfg.Model = f.Model
	}
	if llmCfg.BaseURL == "" || llmCfg.Model == "" || llmCfg.TimeoutSeconds <= 0 {
		return rep, errors.New("offline_training.llm base_url, model and timeout_seconds are required")
	}

	policy := editorialteacher.PublicationPolicy
	if f.PolicyFile != "" {
		raw, err := os.ReadFile(f.PolicyFile) // #nosec G304 -- operator-provided path
		if err != nil {
			return rep, fmt.Errorf("read --policy-file: %w", err)
		}
		policy = string(raw)
	}

	dataset, err := editorialtrain.LoadDataset(f.DatasetPath)
	if err != nil {
		return rep, err
	}
	// telegramimport labels mean "was posted to the channel", not a
	// human editorial judgment, so they are excluded by default.
	var examples []editorialteacher.EvalExample
	for _, ex := range dataset.Examples {
		if !f.IncludeImported && ex.LabelSource == "telegramimport" {
			continue
		}
		if ex.Label != "publish" && ex.Label != "reject" {
			continue
		}
		examples = append(examples, editorialteacher.EvalExample{
			ArticleID: ex.ArticleID, Title: ex.Title, Summary: ex.Body,
			SourceType: ex.SourceType, Domain: ex.Domain, Truth: ex.Label,
		})
	}
	examples = editorialteacher.SelectEvalExamples(examples, f.Fold, f.Sample, f.Seed)
	if len(examples) == 0 {
		return rep, fmt.Errorf("no examples selected from %s", f.DatasetPath)
	}
	rep.Examples, rep.Fold, rep.Model = len(examples), f.Fold, llmCfg.Model

	llm := llmclient.New(LLMClientConfig(llmCfg))
	batchSize := f.BatchSize
	if batchSize == 0 {
		batchSize = defaultClassifyBatchSize
		if llmCfg.BatchSize > 0 {
			batchSize = llmCfg.BatchSize
		}
	}
	report, err := editorialteacher.Evaluate(ctx, llm, policy, examples, batchSize)
	if err != nil {
		return rep, err
	}
	rep.Report, rep.Summary = report, report.Summarize(f.Threshold)

	if f.MissesOut != "" {
		if err := writeEvalMisses(f.MissesOut, report.Predictions, f.Threshold); err != nil {
			return rep, &stageError{stage: ErrWriteMisses, err: err}
		}
		rep.MissesWritten = f.MissesOut
	}
	return rep, nil
}

// writeEvalMisses writes the confident discards whose human label is
// publish to path as CSV, most confident first.
func writeEvalMisses(path string, preds []editorialteacher.EvalPrediction, threshold float64) error {
	var misses []editorialteacher.EvalPrediction
	for _, p := range preds {
		if p.Label == "discard" && p.Confidence >= threshold && p.Truth == "publish" {
			misses = append(misses, p)
		}
	}
	sort.Slice(misses, func(i, j int) bool { return misses[i].Confidence > misses[j].Confidence })
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- operator-provided path
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"article_id", "confidence", "domain", "title", "llm_reason"})
	for _, p := range misses {
		_ = w.Write([]string{p.ArticleID, fmt.Sprintf("%.2f", p.Confidence), p.Domain, p.Title, p.Reason})
	}
	w.Flush()
	return w.Error()
}
