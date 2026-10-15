package editorialcycle_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

// fakeLabelRepo backs out.HumanLabelRepository for RunTraining's tests —
// only ListEditorialExamples' behavior matters here, so every other
// method is a no-op. Mirrors editorialtrain's dataset_test.go fake of
// the same name (a fresh copy, since test packages cannot import one
// another).
type fakeLabelRepo struct {
	examples []training.EditorialExample
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
func (f *fakeLabelRepo) ListAll(context.Context) ([]training.Example, error) { return nil, nil }
func (f *fakeLabelRepo) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}
func (f *fakeLabelRepo) CountUnresolvedConflicts(context.Context) (int, error) { return 0, nil }
func (f *fakeLabelRepo) ResolveConflict(context.Context, string, training.Example) error {
	return nil
}

func (f *fakeLabelRepo) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return f.examples, nil
}

func (f *fakeLabelRepo) CountEditorialExamples(context.Context) (int, error) {
	return len(f.examples), nil
}

// fakeModelRegistry backs out.ModelRegistryRepository for RunTraining's
// tests — Register just needs to succeed and remember what it was given;
// Get/ListByStatus are never exercised by RunTraining, so they are
// no-ops.
type fakeModelRegistry struct {
	registered []training.ModelMetadata
}

func (f *fakeModelRegistry) Register(_ context.Context, m training.ModelMetadata) error {
	f.registered = append(f.registered, m)
	return nil
}

func (f *fakeModelRegistry) Get(context.Context, string) (training.ModelMetadata, error) {
	return training.ModelMetadata{}, errors.New("fakeModelRegistry: not found")
}

func (f *fakeModelRegistry) ListByStatus(context.Context, string) ([]training.ModelMetadata, error) {
	return nil, nil
}

// seedTenPublishTenReject builds 20 durable editorial examples matching
// the shape cli/cmd/train_test.go's
// writeTrainRunDataset helper uses: 10 "isotopes win big game today"
// articles labeled publish, 10 "sponsored shop now huge discount"
// articles labeled reject — enough signal for even a trivial classifier
// to pass promotion gates. This package's tests cannot import the cmd
// package, so this is a fresh local copy of that same data shape.
func seedTenPublishTenReject() []training.EditorialExample {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var examples []training.EditorialExample
	for i := 0; i < 10; i++ {
		id := "p" + string(rune('a'+i))
		examples = append(examples, training.EditorialExample{
			ArticleID: id, Label: "publish", Title: "isotopes win big game today",
			LabeledAt: base.AddDate(0, 0, i), PublishedAt: base.AddDate(0, 0, i), FetchedAt: base.AddDate(0, 0, i),
		})
	}
	for i := 0; i < 10; i++ {
		id := "r" + string(rune('a'+i))
		examples = append(examples, training.EditorialExample{
			ArticleID: id, Label: "reject", Title: "sponsored shop now huge discount",
			LabeledAt: base.AddDate(0, 0, i), PublishedAt: base.AddDate(0, 0, i), FetchedAt: base.AddDate(0, 0, i),
		})
	}
	return examples
}

func TestRunTraining_ProducesAWinnerWhenOneCandidatePasses(t *testing.T) {
	ctx := context.Background()
	labels := &fakeLabelRepo{examples: seedTenPublishTenReject()}
	registry := &fakeModelRegistry{}

	result, err := editorialcycle.RunTraining(ctx, editorialcycle.TrainingDeps{Labels: labels, Registry: registry},
		editorialcycle.TrainingConfig{
			DatasetPath: filepath.Join(t.TempDir(), "dataset.yml"),
			OutDir:      t.TempDir(),
			TrainCfg: editorialtrain.TrainConfig{
				Split:      editorialtrain.SplitConfig{TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15},
				Algorithms: []string{"naive_bayes"},
				Thresholds: editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.5, RejectTargetPrecision: 0.5},
				Seed:       1,
			},
			GateCfg: editorialtrain.GateConfig{
				MaxArtifactSizeBytes: 1 << 20, MaxInferenceNanos: 1 << 30, MaxFalsePositiveRate: 1.0,
			},
		})
	if err != nil {
		t.Fatalf("RunTraining() error = %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if !result.HasWinner {
		t.Error("HasWinner = false, want true when the sole candidate passes its gates")
	}
	if result.Winner.Candidate.Model.Algorithm != "naive_bayes" {
		t.Errorf("Winner.Candidate.Model.Algorithm = %q, want %q", result.Winner.Candidate.Model.Algorithm, "naive_bayes")
	}
	if len(registry.registered) != 1 {
		t.Errorf("len(registry.registered) = %d, want 1 candidate registered", len(registry.registered))
	}
}

func TestRunTraining_NoLabelsReturnsError(t *testing.T) {
	_, err := editorialcycle.RunTraining(context.Background(), editorialcycle.TrainingDeps{
		Labels: &fakeLabelRepo{}, Registry: &fakeModelRegistry{},
	}, editorialcycle.TrainingConfig{DatasetPath: filepath.Join(t.TempDir(), "d.yml"), OutDir: t.TempDir()})
	if err == nil {
		t.Error("RunTraining() error = nil, want an error when there are no labels to export")
	}
}

func TestRunTraining_ExcludesSourceBypassExamples(t *testing.T) {
	dir := t.TempDir()
	examples := seedTenPublishTenReject()
	// Previously labeled official content must be excluded alongside new content.
	examples = append(examples, training.EditorialExample{
		ArticleID: "official", SourceID: "twitter:sprisotopes", Title: "Gettin loose", Label: "publish", LabeledAt: time.Now(),
	})
	path := filepath.Join(dir, "dataset.yml")
	_, err := editorialcycle.RunTraining(context.Background(), editorialcycle.TrainingDeps{
		Labels: &fakeLabelRepo{examples: examples}, Registry: &fakeModelRegistry{},
	}, editorialcycle.TrainingConfig{
		ExcludedSourceIDs: map[string]bool{"twitter:sprisotopes": true},
		DatasetPath:       path, OutDir: filepath.Join(dir, "candidates"),
		TrainCfg: editorialtrain.TrainConfig{
			Split:      editorialtrain.SplitConfig{TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15},
			Algorithms: []string{"naive_bayes"},
			Thresholds: editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.5, RejectTargetPrecision: 0.5},
			Seed:       1,
		},
		GateCfg: editorialtrain.GateConfig{MaxArtifactSizeBytes: 1 << 20, MaxInferenceNanos: 1 << 30, MaxFalsePositiveRate: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := editorialtrain.LoadDataset(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Examples) != 20 {
		t.Fatalf("exported %d examples, want 20", len(dataset.Examples))
	}
	for _, e := range dataset.Examples {
		if e.SourceID == "twitter:sprisotopes" {
			t.Fatal("exported official source into model training dataset")
		}
	}
}
