// Package training defines the value objects used to train and describe the
// offline editorial-quality classifier. The training pipeline itself lands
// in a later phase.
package training

import (
	"errors"
	"time"
)

// ErrAlreadyLabeled is returned by HumanLabelRepository.Label when the
// target article already has an active revision -- callers must use
// Relabel instead.
var ErrAlreadyLabeled = errors.New("training: article already has an active label")

// ErrNotLabeled is returned by HumanLabelRepository.Relabel/Retract when
// the target article has no active revision to supersede.
var ErrNotLabeled = errors.New("training: article has no active label")

// ErrRevisionConflict is returned by HumanLabelRepository.Relabel/Retract
// when the article's active revision is not the expectedRevision the
// caller passed in -- another writer superseded it first. Callers should
// re-read via GetActive and decide whether to retry.
var ErrRevisionConflict = errors.New("training: active label revision changed concurrently")

// Example is a single human-labeled training example. Rows built from this
// type are never deleted by retention.
type Example struct {
	ArticleID string
	Label     string
	// Reason further categorizes why Label was assigned — "" for a plain
	// publish, or one of "quality"|"duplicate"|"same_event"|
	// "substantial_update" for a reject/publish driven by a specific
	// editorial-review action (see domain/reviewdecision). Purely
	// descriptive: domain/editorial's classifier is still trained on the
	// binary Label alone.
	Reason    string
	LabeledAt time.Time
	LabeledBy string
	Notes     string

	// Revision is this call's resulting revision (from Label/Relabel),
	// or the article's current active revision (from GetActive) --
	// 1-based, per article_id.
	Revision int
	// Supersedes is the revision number this one replaced, or 0 for a
	// first Label.
	Supersedes int
	// ContentHash is set by the repository (sha256 of the article's
	// title+body at this revision) -- callers building an Example to
	// pass to Label/Relabel leave it zero; it is only meaningful on a
	// value read back from the repository.
	ContentHash string
	// Source identifies what wrote this revision -- one of "cli:label"
	// (the `label set` command), "cli:label-resolve-conflict" (`label
	// resolve-conflict`), "cli:train-review" (`train review`, resolving
	// an LLM-teacher proposal), "editorial_cycle" (an interactive review
	// decision), or "telegramimport" (a label derived from an imported
	// Telegram channel post). Required on every Label/Relabel call.
	Source string
	// SessionID is the owning editorial_cycle session id, empty for
	// CLI-originated labels.
	SessionID string
}

// EditorialExample is one durable, denormalized snapshot of a
// human-labeled article: the label plus every field
// domain/editorial.ExtractFeatures needs to recompute the exact same
// feature vector, captured at label time so it never depends on the
// operational articles row surviving retention.
// Rows built from this type are never deleted — not by retention, and
// not by any application code path: HumanLabelRepository's
// Label/Relabel/Retract only ever append a new revision and flip the
// prior active row's `active` flag off (revision history).
// At most one revision per article_id is active at a time, enforced by
// the partial unique index idx_editorial_examples_one_active, and
// ExportDataset reads exactly those active rows.
//
// editorialcycle.ApplyDecision reads the current active revision via
// GetActive before deciding what to write, and supersedes or retracts
// precisely that revision. This closes the gap this comment used to
// describe: an unrelated later decision on the same article_id can no
// longer silently clear an earlier surviving example, because nothing
// is cleared — the earlier revision stays, marked superseded, visible
// via `label history <article-id>`.
type EditorialExample struct {
	ArticleID string // informational only; the article row it was copied from may no longer exist
	Label     string
	Reason    string
	LabeledBy string
	LabeledAt time.Time
	Notes     string

	Revision    int
	Supersedes  int
	ContentHash string
	Source      string
	SessionID   string

	Title       string
	Body        string
	SourceID    string
	SourceType  string
	Domain      string
	ContentType string
	PublishedAt time.Time
	FetchedAt   time.Time

	ClusterID         string // audit only; not a training feature
	Author            string
	ExternalID        string
	OriginalURL       string
	CanonicalURL      string
	ClassifierVersion string // provenance only; not read by the trainer
	DictionaryVersion string // provenance only; not read by the trainer
}

// Conflict is one unresolved contradiction human_label_history's
// migration backfill (0026) found and could not auto-resolve -- two or
// more rows for the same article_id, in the same table, sharing an
// identical labeled_at but disagreeing on label. See HumanLabelRepository.
// ListUnresolvedConflicts.
type Conflict struct {
	ID         int
	TableName  string // "human_labels" | "editorial_examples"
	ArticleID  string
	Detail     string // "<id>:<label>:<labeled_at> | <id>:<label>:<labeled_at> | ..."
	DetectedAt time.Time
}

// ModelMetadata describes a trained model artifact — one row in
// model_registry.
type ModelMetadata struct {
	Version              string
	Kind                 string
	Path                 string
	Algorithm            string // "naive_bayes" | "logistic_regression" | "linear_svm" | "nbsvm"
	FeatureSchemaVersion string
	DatasetHash          string
	Thresholds           string // JSON: editorial.Thresholds
	Config               string // JSON: training hyperparameters
	ArtifactHash         string // sha256 of the promoted artifact directory's contents
	Status               string // "candidate" | "validated" | "promoted" | "retired"
	TrainedAt            time.Time
	PromotedAt           time.Time
	Metrics              string // JSON
}

// Promoted reports whether this model version is the one currently in
// production use. Promoted versions are never deleted by retention.
// Status is authoritative (not PromotedAt, which is audit-only timestamp
// data).
func (m ModelMetadata) Promoted() bool {
	return m.Status == "promoted"
}

// ShadowDisagreement is one article where the production model and a
// candidate model disagreed on Zone during an offline shadow-comparison
// run — persisted for a human to inspect, never
// used to auto-promote or auto-reject anything.
type ShadowDisagreement struct {
	RunID             string
	ArticleID         string
	Title             string
	ProductionVersion string
	ProductionZone    string
	ProductionScore   float64
	CandidateVersion  string
	CandidateZone     string
	CandidateScore    float64
	CreatedAt         time.Time
}

// Run is one execution of the offline training pipeline (`train run`).
// Report holds the full rendered textual report (see
// application/trainreport) for later lookup; Status "running" is written
// at start and never left in that state on success or failure — the
// caller always transitions it to "succeeded" or "failed" before
// returning.
type Run struct {
	ID             string
	StartedAt      time.Time
	FinishedAt     time.Time
	ModelVersion   string
	DatasetSummary string // JSON
	Status         string // "running" | "succeeded" | "failed"
	Report         string
}

// HardExampleCandidate is one article where the promoted NB model and
// the LLM teacher strongly disagreed, found by
// `train mine`. Kind is "negative" (NB confidently predicts publish,
// LLM confidently predicts discard — exposes vocabulary that
// incorrectly triggers publication) or "positive" (the reverse —
// reveals new players/coaches/terminology the classifier doesn't know
// about yet). ResolvedAt is nil until a human label lands on
// ArticleID, at which point the candidate no longer counts toward
// review-priority boost (domain/reviewpriority) or the retrain trigger
// (application/retraintrigger.Signals.HardExampleCount).
type HardExampleCandidate struct {
	ArticleID     string
	Kind          string // "negative" | "positive"
	NBScore       float64
	LLMLabel      string
	LLMConfidence float64
	MinedAt       time.Time
	ResolvedAt    *time.Time
}
