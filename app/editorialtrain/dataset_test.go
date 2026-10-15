package editorialtrain_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// fakeLabelRepo backs out.HumanLabelRepository for ExportDataset's tests
// — only ListEditorialExamples' behavior (and, for conflict reporting,
// CountUnresolvedConflicts) matters here, so every other method is a
// no-op.
type fakeLabelRepo struct {
	examples     []training.EditorialExample
	conflicts    int
	activeLabels []training.Example
}

func (f *fakeLabelRepo) Label(context.Context, training.Example) error        { return nil }
func (f *fakeLabelRepo) Relabel(context.Context, training.Example, int) error { return nil }
func (f *fakeLabelRepo) Retract(context.Context, string, int) error           { return nil }
func (f *fakeLabelRepo) GetActive(context.Context, string) (training.Example, bool, error) {
	return training.Example{}, false, nil
}

func (f *fakeLabelRepo) ListByArticle(context.Context, string) ([]training.Example, error) {
	return nil, nil
}

func (f *fakeLabelRepo) ListAll(context.Context) ([]training.Example, error) {
	return f.activeLabels, nil
}

func (f *fakeLabelRepo) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return f.examples, nil
}

func (f *fakeLabelRepo) CountEditorialExamples(context.Context) (int, error) {
	return len(f.examples), nil
}

func (f *fakeLabelRepo) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}

func (f *fakeLabelRepo) CountUnresolvedConflicts(context.Context) (int, error) {
	return f.conflicts, nil
}

func (f *fakeLabelRepo) ResolveConflict(context.Context, string, training.Example) error {
	return nil
}

func writeDatasetFile(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dataset.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write dataset file: %v", err)
	}
	return path
}

const sampleDatasetYAML = `
examples:
  - article_id: a1
    title: "Isotopes sign RB to extension"
    body: "The team announced the signing today."
    source_id: src-1
    source_type: rss
    domain: isotopes.example.com
    content_type: article
    published_at: 2026-01-01T00:00:00Z
    fetched_at: 2026-01-01T01:00:00Z
    cluster_id: c1
    is_primary_source: true
    label: publish
    labeled_at: 2026-01-02T00:00:00Z
  - article_id: a2
    title: "Shop our Isotopes gear now"
    body: "Sponsored content, 20% off everything."
    source_id: src-2
    source_type: web
    domain: shop.example.com
    content_type: article
    published_at: 2026-01-01T02:00:00Z
    fetched_at: 2026-01-01T03:00:00Z
    label: reject
    labeled_at: 2026-01-02T01:00:00Z
`

func TestLoadDataset_ParsesExamples(t *testing.T) {
	path := writeDatasetFile(t, sampleDatasetYAML)
	d, err := editorialtrain.LoadDataset(path)
	if err != nil {
		t.Fatalf("LoadDataset() error = %v", err)
	}
	if len(d.Examples) != 2 {
		t.Fatalf("len(Examples) = %d, want 2", len(d.Examples))
	}
	if d.Examples[0].ArticleID != "a1" || d.Examples[0].Label != "publish" || d.Examples[0].ClusterID != "c1" || !d.Examples[0].IsPrimarySource {
		t.Errorf("Examples[0] = %+v, unexpected", d.Examples[0])
	}
	if d.Examples[1].Label != "reject" || d.Examples[1].IsPrimarySource {
		t.Errorf("Examples[1] = %+v, unexpected", d.Examples[1])
	}
}

func TestDataset_Hash_IsStableAndContentSensitive(t *testing.T) {
	path1 := writeDatasetFile(t, sampleDatasetYAML)
	path2 := writeDatasetFile(t, sampleDatasetYAML)
	d1, err := editorialtrain.LoadDataset(path1)
	if err != nil {
		t.Fatalf("LoadDataset(path1) error = %v", err)
	}
	d2, err := editorialtrain.LoadDataset(path2)
	if err != nil {
		t.Fatalf("LoadDataset(path2) error = %v", err)
	}
	if d1.Hash() != d2.Hash() {
		t.Errorf("Hash() differs for byte-identical files: %s vs %s", d1.Hash(), d2.Hash())
	}

	pathDifferent := writeDatasetFile(t, sampleDatasetYAML+"\n# trailing comment\n")
	dDifferent, err := editorialtrain.LoadDataset(pathDifferent)
	if err != nil {
		t.Fatalf("LoadDataset(pathDifferent) error = %v", err)
	}
	if d1.Hash() == dDifferent.Hash() {
		t.Error("Hash() identical for files with different bytes, want different hashes")
	}
}

func TestLoadDataset_CountsUnparseableSourceIDs(t *testing.T) {
	path := writeDatasetFile(t, sampleDatasetYAML)
	d, err := editorialtrain.LoadDataset(path)
	if err != nil {
		t.Fatalf("LoadDataset() error = %v", err)
	}
	// sampleDatasetYAML's source_id values ("src-1", "src-2") are bare,
	// pre-namespacing legacy values with no "platform:" prefix -- both fail
	// source.ParseID.
	if d.UnparseableSourceIDCount != 2 {
		t.Errorf("UnparseableSourceIDCount = %d, want 2 (both sample rows use pre-namespace bare source_id values)", d.UnparseableSourceIDCount)
	}

	namespaced := `
examples:
  - article_id: a1
    title: "T"
    body: "B"
    source_id: rss:src-1
    source_type: rss
    label: publish
    labeled_at: 2026-01-02T00:00:00Z
  - article_id: a2
    title: "T2"
    body: "B2"
    source_id: not-a-valid-source-id
    source_type: web
    label: reject
    labeled_at: 2026-01-02T00:00:00Z
`
	path2 := writeDatasetFile(t, namespaced)
	d2, err := editorialtrain.LoadDataset(path2)
	if err != nil {
		t.Fatalf("LoadDataset() error = %v", err)
	}
	if d2.UnparseableSourceIDCount != 1 {
		t.Errorf("UnparseableSourceIDCount = %d, want 1 (only a2's source_id fails to parse)", d2.UnparseableSourceIDCount)
	}
}

func TestNewTrainingExample_Denormalizes(t *testing.T) {
	published := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := article.Article{
		ID: "a1", Title: "T", Summary: "B", Domain: "d.com",
		Source: source.Source{ID: mustID(t, source.TypeRSS, "s1"), Type: source.TypeRSS}, ContentType: article.ContentTypeArticle,
		ClusterID: "c1", PublishedAt: published, FetchedAt: published.Add(time.Hour),
	}
	labeledAt := published.Add(24 * time.Hour)
	got := editorialtrain.NewTrainingExample(a, true, "publish", "silver", "substantial_update", labeledAt)
	want := editorialtrain.TrainingExample{
		ArticleID: "a1", Title: "T", Body: "B", SourceID: "rss:s1", SourceType: "rss",
		Domain: "d.com", ContentType: "article", PublishedAt: published, FetchedAt: published.Add(time.Hour),
		ClusterID: "c1", IsPrimarySource: true, Label: "publish", Tier: "silver", Reason: "substantial_update", LabeledAt: labeledAt,
	}
	if got != want {
		t.Errorf("NewTrainingExample() = %+v, want %+v", got, want)
	}
}

func TestExportDataset_BuildsExamplesAndKeepsLabelProvenance(t *testing.T) {
	ctx := context.Background()
	labeledAt := time.Now()
	labels := &fakeLabelRepo{examples: []training.EditorialExample{
		{ArticleID: "a1", Label: "publish", Source: "telegramimport", Title: "t1", Body: "b1", SourceID: "s1", Domain: "d.com", LabeledAt: labeledAt, Revision: 1, ContentHash: "hash-a1"},
		{ArticleID: "a2", Label: "reject", Reason: "quality", Title: "t2", Body: "b2", SourceID: "s2", Domain: "d.com", LabeledAt: labeledAt, Revision: 2, ContentHash: "hash-a2"},
	}}

	examples, err := editorialtrain.ExportDataset(ctx, labels, map[string]bool{"s1": true})
	if err != nil {
		t.Fatalf("ExportDataset() error = %v", err)
	}
	if len(examples) != 2 {
		t.Fatalf("len(examples) = %d, want 2", len(examples))
	}
	if examples[0].ArticleID != "a1" || !examples[0].IsPrimarySource {
		t.Errorf("examples[0] = %+v, want a1 with IsPrimarySource=true (s1 is trusted)", examples[0])
	}
	if examples[1].ArticleID != "a2" || examples[1].IsPrimarySource || examples[1].Reason != "quality" {
		t.Errorf("examples[1] = %+v, want a2 with IsPrimarySource=false and Reason=quality", examples[1])
	}
	if examples[0].Revision != 1 || examples[0].ContentHash != "hash-a1" {
		t.Errorf("examples[0] Revision/ContentHash = %d/%q, want 1/hash-a1", examples[0].Revision, examples[0].ContentHash)
	}
	if examples[1].Revision != 2 {
		t.Errorf("examples[1].Revision = %d, want 2", examples[1].Revision)
	}
	if examples[0].Tier != "gold" || examples[1].Tier != "gold" {
		t.Errorf("examples Tier = %q/%q, want gold/gold (ExportDataset only sources human-labeled Gold examples)", examples[0].Tier, examples[1].Tier)
	}
	if examples[0].LabelSource != "telegramimport" {
		t.Errorf("examples[0].LabelSource = %q, want telegramimport", examples[0].LabelSource)
	}
}

func TestExportDataset_ExcludesPipelineOutcomesOutsideEditorialQuality(t *testing.T) {
	ctx := context.Background()
	labels := &fakeLabelRepo{examples: []training.EditorialExample{
		{ArticleID: "quality-reject", Label: "reject", Source: "pipeline_backfill", Reason: string(article.StatusRejectedQuality)},
		{ArticleID: "unrelated", Label: "reject", Source: "pipeline_backfill", Reason: string(article.StatusRejectedNotRelated)},
		{ArticleID: "cluster-member", Label: "reject", Source: "pipeline_backfill", Reason: string(article.StatusRetainedNoPublish)},
		{ArticleID: "channel-publish", Label: "publish", Source: "telegramimport"},
	}}
	got, err := editorialtrain.ExportDataset(ctx, labels, nil)
	if err != nil {
		t.Fatalf("ExportDataset() error = %v", err)
	}
	if len(got) != 2 || got[0].ArticleID != "quality-reject" || got[1].ArticleID != "channel-publish" {
		t.Errorf("ExportDataset() = %+v, want only direct editorial quality outcomes and Telegram publishes", got)
	}
}

func TestExportDataset_NoLabelsReturnsEmptySlice(t *testing.T) {
	ctx := context.Background()
	examples, err := editorialtrain.ExportDataset(ctx, &fakeLabelRepo{}, nil)
	if err != nil {
		t.Fatalf("ExportDataset() error = %v, want nil — zero Gold examples is valid when Silver examples may still fill the dataset", err)
	}
	if len(examples) != 0 {
		t.Errorf("examples = %+v, want empty", examples)
	}
}

func TestMergeTrainingExamplesDeduplicatesMatchingSnapshotAndRejectsConflicts(t *testing.T) {
	one := editorialtrain.TrainingExample{ArticleID: "a1", Title: "Isotopes sign player", Label: "publish", ContentHash: "hash-1"}
	merged, err := editorialtrain.MergeTrainingExamples([]editorialtrain.TrainingExample{one}, []editorialtrain.TrainingExample{one})
	if err != nil || len(merged) != 1 {
		t.Fatalf("MergeTrainingExamples() = %d examples, %v; want one matching snapshot", len(merged), err)
	}
	conflict := one
	conflict.Label = "reject"
	if _, err := editorialtrain.MergeTrainingExamples([]editorialtrain.TrainingExample{one}, []editorialtrain.TrainingExample{conflict}); err == nil {
		t.Fatal("MergeTrainingExamples() error = nil, want conflicting labels rejected")
	}
}

func TestWriteManifest_RecordsProvenanceAndDistribution(t *testing.T) {
	dir := t.TempDir()
	datasetPath := filepath.Join(dir, "dataset.yml")
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", SourceID: "src-a", Label: "publish", LabelSource: "telegramimport", LabeledAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)},
		{ArticleID: "a2", SourceID: "src-a", Label: "reject", LabelSource: "pipeline_backfill", LabeledAt: time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), Revision: 2},
		{ArticleID: "a3", SourceID: "src-b", Label: "publish", LabelSource: "cli:label", LabeledAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
	}

	if err := editorialtrain.WriteManifest(datasetPath, examples, "deadbeef", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), 2, 3); err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	data, err := os.ReadFile(datasetPath + ".manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m editorialtrain.DatasetManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if m.SchemaVersion != editorialtrain.TrainingExampleSchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q", m.SchemaVersion, editorialtrain.TrainingExampleSchemaVersion)
	}
	if m.TotalCount != 3 || m.PublishCount != 2 || m.RejectCount != 1 {
		t.Errorf("counts = total:%d publish:%d reject:%d, want 3/2/1", m.TotalCount, m.PublishCount, m.RejectCount)
	}
	if m.BySource["src-a"] != 2 || m.BySource["src-b"] != 1 {
		t.Errorf("BySource = %+v, want src-a:2 src-b:1", m.BySource)
	}
	if m.ByLabelSource["telegramimport"] != 1 || m.ByLabelSource["pipeline_backfill"] != 1 || m.ByLabelSource["cli:label"] != 1 {
		t.Errorf("ByLabelSource = %+v, want one row per label source", m.ByLabelSource)
	}
	if !m.PeriodStart.Equal(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)) || !m.PeriodEnd.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("period = %v..%v, want 2026-01-05..2026-02-01", m.PeriodStart, m.PeriodEnd)
	}
	if m.DatasetSHA256 != "deadbeef" {
		t.Errorf("DatasetSHA256 = %q, want %q", m.DatasetSHA256, "deadbeef")
	}
	if !m.ExportedAt.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ExportedAt = %v, want 2026-03-01", m.ExportedAt)
	}
	if m.UnresolvedConflictCount != 2 {
		t.Errorf("UnresolvedConflictCount = %d, want 2", m.UnresolvedConflictCount)
	}
	if m.SilverSkippedMissingArticleCount != 3 {
		t.Errorf("SilverSkippedMissingArticleCount = %d, want 3", m.SilverSkippedMissingArticleCount)
	}
	if m.GoldCount != 3 || m.SilverCount != 0 {
		t.Errorf("Gold/SilverCount = %d/%d, want 3/0 (none of this test's examples set Tier=silver)", m.GoldCount, m.SilverCount)
	}
	// a2 is the only example past its first revision, so it is the only
	// one that counts as relabeled.
	if m.RelabeledCount != 1 {
		t.Errorf("RelabeledCount = %d, want 1 (a2 is at revision 2)", m.RelabeledCount)
	}
}

func TestWriteManifest_EmptyTierCountsAsGold(t *testing.T) {
	dir := t.TempDir()
	datasetPath := filepath.Join(dir, "dataset.yml")
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", SourceID: "src-a", Label: "publish", LabeledAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)},
	}

	if err := editorialtrain.WriteManifest(datasetPath, examples, "deadbeef", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), 0, 0); err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}

	data, err := os.ReadFile(datasetPath + ".manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m editorialtrain.DatasetManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.GoldCount != 1 || m.SilverCount != 0 {
		t.Errorf("Gold/SilverCount = %d/%d, want 1/0 (an empty Tier resolves as Gold-like, consistent with clustercap.go's tierRank)", m.GoldCount, m.SilverCount)
	}
}

func mustID(t *testing.T, platform source.Type, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", platform, raw, err)
	}
	return id
}
