// Package editorialtrain implements offline training for the editorial
// classifier: dataset loading/splitting, feature-schema pruning, the four
// candidate algorithms (Naive Bayes, logistic regression, linear SVM,
// NB-SVM), metrics, and threshold selection. Imported only by
// the tools CLI, which keeps
// the service binary free of every dependency this package needs.
package editorialtrain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// TrainingExample is one denormalized, feature-ready labeled row — article
// content + label, loaded from a dataset file (never live SQLite; see
// Task 21's `train export`, which builds that file).
type TrainingExample struct {
	ArticleID       string    `yaml:"article_id"`
	Title           string    `yaml:"title"`
	Body            string    `yaml:"body"`
	SourceID        string    `yaml:"source_id"`
	SourceType      string    `yaml:"source_type"`
	Domain          string    `yaml:"domain"`
	ContentType     string    `yaml:"content_type"`
	PublishedAt     time.Time `yaml:"published_at"`
	FetchedAt       time.Time `yaml:"fetched_at"`
	ClusterID       string    `yaml:"cluster_id"`
	IsPrimarySource bool      `yaml:"is_primary_source"`
	Label           string    `yaml:"label"` // "publish" | "reject"
	LabelSource     string    `yaml:"label_source"`
	// Tier records whether this example is "gold" (human-labeled, via
	// ExportDataset) or "silver" (confident-but-unreviewed LLM
	// proposal, via FetchSilverExamples) — see editoriallabelproposal.
	// Tier. Beyond provenance, NaiveBayesTrainer weights Silver rows by
	// offline_training.model_training.silver_weight.
	Tier string `yaml:"tier"`
	// Reason mirrors training.Example.Reason — audit-only, never read by
	// the trainer itself.
	Reason    string    `yaml:"reason"`
	LabeledAt time.Time `yaml:"labeled_at"`
	// Revision and ContentHash mirror training.EditorialExample's fields
	// -- audit-only, never read by the trainer -- recording exactly
	// which label revision and article content version this row came
	// from, so a training run's dataset file is traceable back to a
	// specific point in a specific article's label history.
	Revision    int    `yaml:"revision"`
	ContentHash string `yaml:"content_hash"`
}

type datasetFile struct {
	Examples []TrainingExample `yaml:"examples"`
}

// isSilverTier reports whether tier is exactly
// editoriallabelproposal.TierSilver's string value. Any other value --
// including the empty string -- is treated as Gold-like: both
// WriteManifest (below) and clustercap.go's tierRank use this helper so
// an empty Tier resolves consistently to "not Silver" across the
// package, rather than one call site treating it as Gold and the other
// as lowest-priority Silver.
func isSilverTier(tier string) bool {
	return tier == string(editoriallabelproposal.TierSilver)
}

// Dataset is a labeled set of training examples, plus the raw source
// file's bytes (unexported) so Hash() is literally the dataset file's own
// checksum — the value model_registry.dataset_hash stores.
type Dataset struct {
	Examples []TrainingExample
	// UnparseableSourceIDCount counts examples whose SourceID failed
	// source.ParseID at load time (see toArticle's doc comment: those rows
	// silently degrade to a zero-value source.ID rather than aborting the
	// load). This is the observable counterpart to that silent degrade —
	// callers (e.g. `train run`) should surface it when non-zero.
	UnparseableSourceIDCount int
	raw                      []byte
}

// LoadDataset reads and parses a YAML dataset file (see Task 21's `train
// export` for how one gets produced from live data).
func LoadDataset(path string) (Dataset, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided CLI flag, not user input
	if err != nil {
		return Dataset{}, fmt.Errorf("editorialtrain: read %s: %w", path, err)
	}
	var f datasetFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return Dataset{}, fmt.Errorf("editorialtrain: parse %s: %w", path, err)
	}
	unparseable := 0
	for _, ex := range f.Examples {
		if _, err := source.ParseID(ex.SourceID); err != nil {
			unparseable++
		}
	}
	return Dataset{Examples: f.Examples, raw: data, UnparseableSourceIDCount: unparseable}, nil
}

// Hash returns the sha256 (hex) of the dataset file's raw bytes as loaded
// by LoadDataset — model_registry.dataset_hash records this so a promoted
// model's training data is byte-identifiable later.
func (d Dataset) Hash() string {
	sum := sha256.Sum256(d.raw)
	return hex.EncodeToString(sum[:])
}

// ExcludeSources returns a dataset copy without examples from configured
// first-party sources, and recomputes the auditable dataset hash.
func (d Dataset) ExcludeSources(ids map[string]bool) Dataset {
	if len(ids) == 0 {
		return d
	}
	examples := make([]TrainingExample, 0, len(d.Examples))
	for _, ex := range d.Examples {
		if !ids[ex.SourceID] {
			examples = append(examples, ex)
		}
	}
	raw, _ := yaml.Marshal(datasetFile{Examples: examples})
	d.Examples, d.raw, d.UnparseableSourceIDCount = examples, raw, 0
	for _, ex := range examples {
		if _, err := source.ParseID(ex.SourceID); err != nil {
			d.UnparseableSourceIDCount++
		}
	}
	return d
}

// NewTrainingExample constructs a TrainingExample from article data, a
// label, and a Tier ("gold"/"silver"), used by `train export` to build
// denormalized dataset files for offline training. The reason parameter
// is audit-only, never read by the trainer itself.
func NewTrainingExample(a article.Article, isPrimarySource bool, label, tier, reason string, labeledAt time.Time) TrainingExample {
	return TrainingExample{
		ArticleID: a.ID, Title: a.Title, Body: a.Summary,
		SourceID: a.Source.ID.String(), SourceType: string(a.Source.Type), Domain: a.Domain,
		ContentType: string(a.ContentType), PublishedAt: a.PublishedAt, FetchedAt: a.FetchedAt,
		ClusterID: a.ClusterID, IsPrimarySource: isPrimarySource, Label: label, Tier: tier, Reason: reason, LabeledAt: labeledAt,
	}
}

// toArticle reconstructs the article.Article shape editorial.ExtractFeatures
// needs from a denormalized TrainingExample — the inverse of
// NewTrainingExample.
func (e TrainingExample) toArticle() article.Article {
	// e.SourceID is the canonical "platform:identifier" form for real
	// exported data (see NewTrainingExample), but a dataset file can carry
	// legacy rows exported before source_id namespacing or a
	// hand-authored fixture label that was never namespaced at all.
	// Features() has no error return, and is called from 8 separate
	// training-algorithm call sites (naive_bayes, logisticregression,
	// linearsvm, nbsvm, metrics, thresholds, schema — see their
	// restrictToSchema(ex.Features(), ...) callers), so propagating a hard
	// failure here would be disproportionate to one malformed row: this
	// package has no out.Logger (or any other logging facility) threaded
	// through it to report the degrade audibly, so this is intentionally
	// silent at the call site — a row whose source_id doesn't parse simply
	// trains with a zero source.ID (Source.ID.String() == "", so the
	// editorial.ExtractFeatures "meta:source_id:*" feature is omitted for
	// that row, exactly as it is for any article with no source at all),
	// never aborting the batch.
	id, err := source.ParseID(e.SourceID)
	if err != nil {
		id = source.ID{}
	}
	return article.Article{
		ID: e.ArticleID, Title: e.Title, Summary: e.Body,
		Source:      source.Source{ID: id, Type: source.Type(e.SourceType)},
		Domain:      e.Domain,
		ContentType: article.ContentType(e.ContentType),
		PublishedAt: e.PublishedAt, FetchedAt: e.FetchedAt, ClusterID: e.ClusterID,
	}
}

// Features extracts e's feature vector via editorial.ExtractFeatures — the
// exact same function Evaluate uses at runtime, so training and inference
// can never disagree about what a feature means.
func (e TrainingExample) Features() editorial.FeatureVector {
	return editorial.ExtractFeatures(e.toArticle(), e.IsPrimarySource)
}

// ExportDataset builds training examples from every stored durable
// editorial example (domain/training.EditorialExample) — a
// denormalized snapshot captured at label time, independent of whether
// the underlying articles row still exists. Every example is tagged
// Tier=gold. Returns an empty slice (never an error) when there are no
// human labels yet — `train export` (Task 5) checks the combined
// Gold+Silver total, not this alone. See `train export`
// (cli/cmd/train/train.go).
func ExportDataset(ctx context.Context, labels out.HumanLabelRepository, trustedSourceIDs map[string]bool, excludedSourceIDs ...map[string]bool) ([]TrainingExample, error) {
	all, err := labels.ListEditorialExamples(ctx)
	if err != nil {
		return nil, err
	}

	examples := make([]TrainingExample, 0, len(all))
	for _, e := range all {
		if len(excludedSourceIDs) > 0 && excludedSourceIDs[0][e.SourceID] {
			continue
		}
		// The editorial-quality gate runs after relevance and clustering.
		// Upstream outcomes are not quality labels: unrelated articles and
		// non-selected cluster members never reached this classifier.
		if e.Source == "pipeline_backfill" && e.Reason != string(article.StatusRejectedQuality) {
			continue
		}
		examples = append(examples, TrainingExample{
			ArticleID: e.ArticleID, Title: e.Title, Body: e.Body,
			SourceID: e.SourceID, SourceType: e.SourceType, Domain: e.Domain, ContentType: e.ContentType,
			PublishedAt: e.PublishedAt, FetchedAt: e.FetchedAt, ClusterID: e.ClusterID,
			IsPrimarySource: trustedSourceIDs[e.SourceID], Label: e.Label, LabelSource: e.Source, Tier: string(editoriallabelproposal.TierGold), Reason: e.Reason, LabeledAt: e.LabeledAt,
			Revision: e.Revision, ContentHash: e.ContentHash,
		})
	}
	return examples, nil
}

// MergeTrainingExamples combines snapshots while refusing to guess when
// the same article has conflicting labels or content. Matching duplicate
// snapshots are included once.
func MergeTrainingExamples(groups ...[]TrainingExample) ([]TrainingExample, error) {
	merged := make([]TrainingExample, 0)
	byID := make(map[string]TrainingExample)
	for _, group := range groups {
		for _, ex := range group {
			if ex.ArticleID == "" {
				merged = append(merged, ex)
				continue
			}
			prior, exists := byID[ex.ArticleID]
			if !exists {
				byID[ex.ArticleID] = ex
				merged = append(merged, ex)
				continue
			}
			contentMatches := prior.ContentHash != "" && prior.ContentHash == ex.ContentHash
			if prior.ContentHash == "" || ex.ContentHash == "" {
				contentMatches = prior.Title == ex.Title && prior.Body == ex.Body
			}
			if prior.Label != ex.Label || !contentMatches {
				return nil, fmt.Errorf("editorialtrain: cannot merge datasets: article %s has conflicting label or content snapshots", ex.ArticleID)
			}
		}
	}
	return merged, nil
}

// SaveDataset marshals examples into the same YAML shape LoadDataset
// reads and writes it to path.
func SaveDataset(path string, examples []TrainingExample) error {
	data, err := yaml.Marshal(datasetFile{Examples: examples})
	if err != nil {
		return fmt.Errorf("editorialtrain: marshal dataset: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("editorialtrain: write dataset %s: %w", path, err)
	}
	return nil
}

// TrainingExampleSchemaVersion identifies the on-disk shape TrainingExample
// (de)serializes to/from — bump it whenever a field is added, removed, or
// its meaning changes, so a dataset manifest can be checked against the
// code that will read it.
const TrainingExampleSchemaVersion = "training-example-v2"

// DatasetManifest records a dataset export's provenance: what schema it
// was written in, when it was exported, what period of human labels it
// covers, its size/label/source distribution, and its own checksum (the
// same value Dataset.Hash() computes over the dataset file). Written by
// WriteManifest as a sibling `<dataset path>.manifest.json` — never
// committed to git (it travels with the equally-gitignored dataset file
// it describes).
type DatasetManifest struct {
	SchemaVersion string    `json:"schema_version"`
	ExportedAt    time.Time `json:"exported_at"`
	PeriodStart   time.Time `json:"period_start"`
	PeriodEnd     time.Time `json:"period_end"`
	TotalCount    int       `json:"total_count"`
	PublishCount  int       `json:"publish_count"`
	RejectCount   int       `json:"reject_count"`
	// GoldCount and SilverCount partition TotalCount by Tier.
	GoldCount     int            `json:"gold_count"`
	SilverCount   int            `json:"silver_count"`
	BySource      map[string]int `json:"by_source"`
	ByLabelSource map[string]int `json:"by_label_source"`
	DatasetSHA256 string         `json:"dataset_sha256"`
	// TopSource and TopSourceShare name the source contributing the most
	// examples and its fraction of TotalCount (see SourceConcentration).
	TopSource      string  `json:"top_source"`
	TopSourceShare float64 `json:"top_source_share"`
	// RelabeledCount counts examples whose Revision > 1 -- how many rows
	// in this export represent a reviewer's second-or-later decision on
	// an article, not its first.
	RelabeledCount int `json:"relabeled_count"`
	// UnresolvedConflictCount is the number of article_ids with a
	// legacy label conflict (see migration 0026) still unresolved at
	// export time -- those articles are entirely absent from this
	// dataset, not silently included with a guessed label.
	UnresolvedConflictCount int `json:"unresolved_conflict_count"`
	// SilverSkippedMissingArticleCount is how many otherwise-eligible
	// Silver candidates were dropped because their article no longer
	// exists in live storage (e.g. retention-deleted) by export time —
	// unlike Gold, Silver has no durable content snapshot to fall back
	// on. See FetchSilverExamples.
	SilverSkippedMissingArticleCount int `json:"silver_skipped_missing_article_count"`
}

// WriteManifest computes examples' distribution/period and writes a
// DatasetManifest to datasetPath+".manifest.json". hash is the dataset
// file's own sha256 (Dataset.Hash(), computed by the caller after
// SaveDataset+LoadDataset). now is the export timestamp (injected for
// deterministic tests). unresolvedConflicts is the current count from
// HumanLabelRepository.CountUnresolvedConflicts and
// silverSkippedMissingArticle is FetchSilverExamples' skip count, both
// threaded through by the caller since this package has no repository
// access of its own.
func WriteManifest(datasetPath string, examples []TrainingExample, hash string, now time.Time, unresolvedConflicts, silverSkippedMissingArticle int) error {
	m := DatasetManifest{
		SchemaVersion:                    TrainingExampleSchemaVersion,
		ExportedAt:                       now,
		BySource:                         map[string]int{},
		ByLabelSource:                    map[string]int{},
		DatasetSHA256:                    hash,
		UnresolvedConflictCount:          unresolvedConflicts,
		SilverSkippedMissingArticleCount: silverSkippedMissingArticle,
	}
	for _, ex := range examples {
		m.TotalCount++
		if ex.Label == "publish" {
			m.PublishCount++
		} else {
			m.RejectCount++
		}
		if isSilverTier(ex.Tier) {
			m.SilverCount++
		} else {
			m.GoldCount++
		}
		if ex.Revision > 1 {
			m.RelabeledCount++
		}
		m.BySource[ex.SourceID]++
		m.ByLabelSource[ex.LabelSource]++
		if m.PeriodStart.IsZero() || ex.LabeledAt.Before(m.PeriodStart) {
			m.PeriodStart = ex.LabeledAt
		}
		if ex.LabeledAt.After(m.PeriodEnd) {
			m.PeriodEnd = ex.LabeledAt
		}
	}

	m.TopSource, m.TopSourceShare = SourceConcentration(examples)

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("editorialtrain: marshal dataset manifest: %w", err)
	}
	if err := os.WriteFile(datasetPath+".manifest.json", data, 0o600); err != nil {
		return fmt.Errorf("editorialtrain: write dataset manifest for %s: %w", datasetPath, err)
	}
	return nil
}
