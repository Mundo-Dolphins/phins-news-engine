package modeltraining_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

func TestDirSize_SumsFileSizes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("1234567890"), 0o600); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	got, err := modeltraining.DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize() error = %v", err)
	}
	if got != 15 {
		t.Errorf("DirSize() = %d, want 15", got)
	}
}

func TestDirSize_MissingDirReturnsError(t *testing.T) {
	if _, err := modeltraining.DirSize(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("DirSize() error = nil, want an error for a missing directory")
	}
}

func TestLoadPromotedReport_EmptyPathReturnsNil(t *testing.T) {
	if got := modeltraining.LoadPromotedReport(""); got != nil {
		t.Errorf("LoadPromotedReport(\"\") = %+v, want nil", got)
	}
}

func TestLoadPromotedReport_MissingDirReturnsNil(t *testing.T) {
	if got := modeltraining.LoadPromotedReport(filepath.Join(t.TempDir(), "does-not-exist")); got != nil {
		t.Errorf("LoadPromotedReport() = %+v, want nil for a missing model directory", got)
	}
}

func TestLoadPromotedReport_ValidModelReturnsTestReport(t *testing.T) {
	dir := writeSampleModelWithReport(t, editorialtrain.Report{
		Overall: editorialtrain.BinaryMetrics{TP: 10, FP: 1, FN: 1, TN: 88},
	})

	got := modeltraining.LoadPromotedReport(dir)
	if got == nil {
		t.Fatal("LoadPromotedReport() = nil, want a non-nil report")
	} else if got.Overall.TP != 10 {
		t.Errorf("got.Overall.TP = %d, want 10", got.Overall.TP)
	}
}

func writeSampleModelWithReport(t *testing.T, testReport editorialtrain.Report) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "model")
	m := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}}
	metricsJSON, err := json.Marshal(struct {
		Validate editorialtrain.Report
		Test     editorialtrain.Report
	}{testReport, testReport})
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "promoted", MetricsSummary: string(metricsJSON)}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(dir, m, manifest, string(metricsJSON), schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return dir
}

type fakeRegistry struct {
	registered []training.ModelMetadata
}

func (f *fakeRegistry) Register(_ context.Context, m training.ModelMetadata) error {
	f.registered = append(f.registered, m)
	return nil
}

func (f *fakeRegistry) Get(context.Context, string) (training.ModelMetadata, error) {
	return training.ModelMetadata{}, fmt.Errorf("not implemented in fake")
}

func (f *fakeRegistry) ListByStatus(_ context.Context, status string) ([]training.ModelMetadata, error) {
	var got []training.ModelMetadata
	for _, m := range f.registered {
		if m.Status == status {
			got = append(got, m)
		}
	}
	return got, nil
}

func sampleDataset(t *testing.T) editorialtrain.Dataset {
	t.Helper()
	var examples []editorialtrain.TrainingExample
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		examples = append(examples, editorialtrain.TrainingExample{
			ArticleID: fmt.Sprintf("p%d", i), Title: "isotopes win big game today", Label: "publish", PublishedAt: base.AddDate(0, 0, i),
		})
	}
	for i := 0; i < 10; i++ {
		examples = append(examples, editorialtrain.TrainingExample{
			ArticleID: fmt.Sprintf("r%d", i), Title: "sponsored shop now huge discount", Label: "reject", PublishedAt: base.AddDate(0, 0, i),
		})
	}
	return editorialtrain.Dataset{Examples: examples}
}

func TestRunAndRegister_SavesAndRegistersEveryCandidateWithAGateVerdict(t *testing.T) {
	registry := &fakeRegistry{}
	outDir := t.TempDir()

	results, err := modeltraining.RunAndRegister(context.Background(), modeltraining.Deps{Registry: registry}, sampleDataset(t), editorialtrain.TrainConfig{
		Split:      editorialtrain.SplitConfig{TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15},
		Prune:      editorialtrain.PruneConfig{MinDocFreq: 0.01, MaxDocFreq: 0.9, MaxFeatures: 2000},
		Algorithms: []string{"naive_bayes"}, Seed: 42, Thresholds: editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.7, RejectTargetPrecision: 0.7}, SchemaVersion: "fs-test",
	}, outDir, editorialtrain.GateConfig{
		MinPrecision: 0.0, MaxRecallRegression: 1.0, MinPRAUC: 0.0, MaxFalsePositiveRate: 1.0,
		MinPerSourcePrecision: 0.0, MinSourceExamples: 1000, MaxArtifactSizeBytes: 10_000_000, MaxInferenceNanos: 1_000_000_000,
	}, nil)
	if err != nil {
		t.Fatalf("RunAndRegister() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]
	if r.Candidate.Model.Algorithm != "naive_bayes" {
		t.Errorf("Algorithm = %q, want %q", r.Candidate.Model.Algorithm, "naive_bayes")
	}
	if _, err := editorialmodel.Load(r.Dir); err != nil {
		t.Errorf("editorialmodel.Load(%s) error = %v — candidate wasn't saved to disk", r.Dir, err)
	}
	if len(registry.registered) != 1 || registry.registered[0].Status != "candidate" {
		t.Errorf("registry.registered = %+v, want exactly one candidate-status row", registry.registered)
	}
	if !r.Gates.Passed {
		t.Errorf("Gates.Passed = false (%v), want true given the deliberately permissive gate config in this test", r.Gates.Failures)
	}
}

func TestRunAndRegister_RecordsTrainingConfigInManifestAndRegistry(t *testing.T) {
	registry := &fakeRegistry{}
	outDir := t.TempDir()

	results, err := modeltraining.RunAndRegister(context.Background(), modeltraining.Deps{Registry: registry}, sampleDataset(t), editorialtrain.TrainConfig{
		Split:      editorialtrain.SplitConfig{TrainRatio: 0.7, ValidateRatio: 0.15, TestRatio: 0.15},
		Prune:      editorialtrain.PruneConfig{MinDocFreq: 0.01, MaxDocFreq: 0.9, MaxFeatures: 2000},
		Algorithms: []string{"naive_bayes"}, Seed: 42, Thresholds: editorialtrain.ThresholdConfig{AcceptTargetPrecision: 0.7, RejectTargetPrecision: 0.7}, SchemaVersion: "fs-test",
		BinaryFeatures: true, SilverWeight: 0.6,
	}, outDir, editorialtrain.GateConfig{
		MaxRecallRegression: 1.0, MaxFalsePositiveRate: 1.0, MinSourceExamples: 1000, MaxArtifactSizeBytes: 10_000_000, MaxInferenceNanos: 1_000_000_000,
	}, nil)
	if err != nil {
		t.Fatalf("RunAndRegister() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(results[0].Dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var manifest editorialmodel.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	for name, cfgJSON := range map[string]string{"manifest": manifest.Config, "registry": registry.registered[0].Config} {
		var cfg struct {
			Seed           int64   `json:"seed"`
			BinaryFeatures bool    `json:"binary_features"`
			SilverWeight   float64 `json:"silver_weight"`
		}
		if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
			t.Fatalf("%s config %q is not valid JSON: %v", name, cfgJSON, err)
		}
		if cfg.Seed != 42 || !cfg.BinaryFeatures || cfg.SilverWeight != 0.6 {
			t.Errorf("%s config = %+v, want seed 42, binary_features true, silver_weight 0.6", name, cfg)
		}
	}
}

func TestSelectWinner_PicksHighestPRAUCAmongPassers(t *testing.T) {
	results := []modeltraining.Result{
		{Version: "a", Gates: editorialtrain.GateResult{Passed: true}, Candidate: editorialtrain.Candidate{TestReport: editorialtrain.Report{PRAUC: 0.5}}},
		{Version: "b", Gates: editorialtrain.GateResult{Passed: true}, Candidate: editorialtrain.Candidate{TestReport: editorialtrain.Report{PRAUC: 0.9}}},
		{Version: "c", Gates: editorialtrain.GateResult{Passed: false}, Candidate: editorialtrain.Candidate{TestReport: editorialtrain.Report{PRAUC: 0.99}}}, // higher PR-AUC but failed gates
	}
	winner, ok := modeltraining.SelectWinner(results)
	if !ok || winner.Version != "b" {
		t.Errorf("SelectWinner() = %+v, ok=%v, want version \"b\"", winner, ok)
	}
}

func TestSelectWinner_NeverSelectsTrivialMajorityEvenIfItPassesGates(t *testing.T) {
	results := []modeltraining.Result{
		{Gates: editorialtrain.GateResult{Passed: true}, Candidate: editorialtrain.Candidate{Model: editorial.Model{Algorithm: "trivial_majority"}, TestReport: editorialtrain.Report{PRAUC: 0.99}}},
		{Gates: editorialtrain.GateResult{Passed: true}, Candidate: editorialtrain.Candidate{Model: editorial.Model{Algorithm: "naive_bayes"}, TestReport: editorialtrain.Report{PRAUC: 0.6}}},
	}
	winner, ok := modeltraining.SelectWinner(results)
	if !ok {
		t.Fatal("SelectWinner() ok = false, want true (naive_bayes passes gates)")
	}
	if winner.Candidate.Model.Algorithm != "naive_bayes" {
		t.Errorf("winner = %q, want %q (trivial_majority must never win even with a higher PR-AUC)", winner.Candidate.Model.Algorithm, "naive_bayes")
	}
}

func TestSelectWinner_NoPassersReturnsFalse(t *testing.T) {
	results := []modeltraining.Result{{Version: "a", Gates: editorialtrain.GateResult{Passed: false}}}
	_, ok := modeltraining.SelectWinner(results)
	if ok {
		t.Error("SelectWinner() ok = true, want false when no candidate passes gates")
	}
}

func TestSelectWinner_EmptyResultsReturnsFalse(t *testing.T) {
	_, ok := modeltraining.SelectWinner(nil)
	if ok {
		t.Error("SelectWinner(nil) ok = true, want false")
	}
}
