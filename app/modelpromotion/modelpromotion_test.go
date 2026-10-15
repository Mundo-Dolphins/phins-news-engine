package modelpromotion_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

func passingConfig() editorialtrain.GateConfig {
	return editorialtrain.GateConfig{
		MinPrecision: 0.7, MaxRecallRegression: 0.05, MinPRAUC: 0.5,
		MaxFalsePositiveRate: 0.5, MinPerSourcePrecision: 0.6, MinSourceExamples: 5,
		MaxArtifactSizeBytes: 1_048_576, MaxInferenceNanos: 1_000_000,
	}
}

func writeCandidateWithReport(t *testing.T, testReport editorialtrain.Report) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}}
	metricsJSON, err := json.Marshal(struct {
		Validate editorialtrain.Report
		Test     editorialtrain.Report
	}{testReport, testReport})
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(dir, m, manifest, string(metricsJSON), schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return dir
}

func writeCandidate(t *testing.T, dir string, m editorial.Model, manifest editorialmodel.Manifest) {
	t.Helper()
	schema := editorialmodel.FeatureSchema{Version: "fs-test"}
	if err := editorialmodel.Save(dir, m, manifest, "{}", schema); err != nil {
		t.Fatalf("writeCandidate: Save() error = %v", err)
	}
}

func TestCheckGates_PassingCandidate(t *testing.T) {
	dir := writeCandidateWithReport(t, editorialtrain.Report{
		Overall: editorialtrain.BinaryMetrics{TP: 90, FP: 5, FN: 5, TN: 900}, PRAUC: 0.9,
	})
	result, err := modelpromotion.CheckGates(dir, passingConfig(), modelpromotion.PromotionCriteria{}, nil)
	if err != nil {
		t.Fatalf("CheckGates() error = %v", err)
	}
	if !result.Passed {
		t.Errorf("CheckGates() = %+v, want Passed=true", result)
	}
}

func TestCheckGates_FailingCandidate(t *testing.T) {
	dir := writeCandidateWithReport(t, editorialtrain.Report{
		Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 99, FN: 5, TN: 900}, PRAUC: 0.1,
	})
	result, err := modelpromotion.CheckGates(dir, passingConfig(), modelpromotion.PromotionCriteria{}, nil)
	if err != nil {
		t.Fatalf("CheckGates() error = %v", err)
	}
	if result.Passed {
		t.Error("CheckGates() Passed = true, want false for a low-precision, low-PRAUC candidate")
	}
}

func TestCheckGates_MissingDirReturnsError(t *testing.T) {
	if _, err := modelpromotion.CheckGates(filepath.Join(t.TempDir(), "missing"), passingConfig(), modelpromotion.PromotionCriteria{}, nil); err == nil {
		t.Error("CheckGates() error = nil, want an error for a missing candidate directory")
	}
}

type fakeRegistry struct {
	registered []training.ModelMetadata
}

func (f *fakeRegistry) Register(_ context.Context, m training.ModelMetadata) error {
	for i, existing := range f.registered {
		if existing.Version == m.Version {
			f.registered[i] = m
			return nil
		}
	}
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

type fakeUOW struct{}

func (fakeUOW) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func TestPromote_WritesArtifactAndUpdatesRegistry(t *testing.T) {
	candidateDir := writeCandidateWithReport(t, editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1}})
	modelPath := filepath.Join(t.TempDir(), "promoted")
	registry := &fakeRegistry{registered: []training.ModelMetadata{{Version: "placeholder-v0", Status: "promoted"}}}

	err := modelpromotion.Promote(context.Background(), modelpromotion.Deps{Registry: registry, UOW: fakeUOW{}}, candidateDir, "v1.0.0", modelPath)
	if err != nil {
		t.Fatalf("Promote() error = %v", err)
	}

	m, err := editorialmodel.Load(modelPath)
	if err != nil {
		t.Fatalf("editorialmodel.Load(%s) error = %v", modelPath, err)
	}
	if m.Version != "v1.0.0" {
		t.Errorf("promoted model Version = %q, want %q", m.Version, "v1.0.0")
	}

	var promotedCount, retiredCount int
	for _, r := range registry.registered {
		switch r.Status {
		case "promoted":
			promotedCount++
			if r.Version != "v1.0.0" {
				t.Errorf("promoted row Version = %q, want %q", r.Version, "v1.0.0")
			}
		case "retired":
			retiredCount++
		}
	}
	if promotedCount != 1 {
		t.Errorf("promotedCount = %d, want exactly 1", promotedCount)
	}
	if retiredCount != 1 {
		t.Errorf("retiredCount = %d, want exactly 1 (placeholder-v0 retired)", retiredCount)
	}
}

func TestPromote_EmptyModelPathFails(t *testing.T) {
	candidateDir := writeCandidateWithReport(t, editorialtrain.Report{})
	err := modelpromotion.Promote(context.Background(), modelpromotion.Deps{Registry: &fakeRegistry{}, UOW: fakeUOW{}}, candidateDir, "v1", "")
	if err == nil {
		t.Error("Promote() error = nil, want an error when modelPath is empty")
	}
}

func TestPromote_MissingCandidateDirFails(t *testing.T) {
	modelPath := filepath.Join(t.TempDir(), "promoted")
	err := modelpromotion.Promote(context.Background(), modelpromotion.Deps{Registry: &fakeRegistry{}, UOW: fakeUOW{}},
		filepath.Join(t.TempDir(), "missing"), "v1", modelPath)
	if err == nil {
		t.Error("Promote() error = nil, want an error for a missing candidate directory")
	}
}

func TestPromote_EmptyVersionUsesManifestVersion(t *testing.T) {
	candidateDir := writeCandidateWithReport(t, editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1}})
	modelPath := filepath.Join(t.TempDir(), "promoted")
	registry := &fakeRegistry{}

	if err := modelpromotion.Promote(context.Background(), modelpromotion.Deps{Registry: registry, UOW: fakeUOW{}}, candidateDir, "", modelPath); err != nil {
		t.Fatalf("Promote() error = %v", err)
	}

	m, err := editorialmodel.Load(modelPath)
	if err != nil {
		t.Fatalf("editorialmodel.Load(%s) error = %v", modelPath, err)
	}
	if m.Version != "v1" {
		t.Errorf("promoted model Version = %q, want the candidate's own manifest version %q", m.Version, "v1")
	}
}

type fakeErrRegistry struct{}

func (fakeErrRegistry) Register(context.Context, training.ModelMetadata) error {
	return fmt.Errorf("register failed")
}

func (fakeErrRegistry) Get(context.Context, string) (training.ModelMetadata, error) {
	return training.ModelMetadata{}, fmt.Errorf("not implemented in fake")
}

func (fakeErrRegistry) ListByStatus(context.Context, string) ([]training.ModelMetadata, error) {
	return nil, fmt.Errorf("list by status failed")
}

func TestPromote_RegistryTransactionErrorPropagates(t *testing.T) {
	candidateDir := writeCandidateWithReport(t, editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1}})
	modelPath := filepath.Join(t.TempDir(), "promoted")

	err := modelpromotion.Promote(context.Background(), modelpromotion.Deps{Registry: fakeErrRegistry{}, UOW: fakeUOW{}}, candidateDir, "v1.0.0", modelPath)
	if err == nil {
		t.Error("Promote() error = nil, want an error when the registry transaction fails")
	}
}

func TestPromote_PreservesOutgoingArtifactAsPreviousSlot(t *testing.T) {
	base := t.TempDir()
	modelPath := filepath.Join(base, "models", "editorial", "current")
	previousPath := filepath.Join(base, "models", "editorial", "previous")
	candidateDir := filepath.Join(base, "candidate")

	// Promote once (nothing to preserve yet — this is the "first
	// promotion" state).
	firstModel := editorial.Model{Version: "v1", Algorithm: "naive_bayes", Weights: map[string]float64{}}
	writeCandidate(t, candidateDir, firstModel, editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes"})
	deps := modelpromotion.Deps{Registry: &fakeRegistry{}, UOW: &fakeUOW{}}
	if err := modelpromotion.Promote(context.Background(), deps, candidateDir, "v1", modelPath); err != nil {
		t.Fatalf("first Promote() error = %v", err)
	}
	if _, err := os.Stat(previousPath); !os.IsNotExist(err) {
		t.Fatal("previous/ exists after the very first promotion, want absent")
	}

	// Promote a second, different model — now current/ (v1) must move to
	// previous/.
	secondModel := editorial.Model{Version: "v2", Algorithm: "naive_bayes", Weights: map[string]float64{}}
	writeCandidate(t, candidateDir, secondModel, editorialmodel.Manifest{ID: "v2", Version: "v2", Algorithm: "naive_bayes"})
	if err := modelpromotion.Promote(context.Background(), deps, candidateDir, "v2", modelPath); err != nil {
		t.Fatalf("second Promote() error = %v", err)
	}

	current, err := editorialmodel.Load(modelPath)
	if err != nil || current.Version != "v2" {
		t.Fatalf("current model = %+v, err=%v, want version v2", current, err)
	}
	previous, err := editorialmodel.Load(previousPath)
	if err != nil || previous.Version != "v1" {
		t.Fatalf("previous model = %+v, err=%v, want version v1", previous, err)
	}
}

func TestRollback_SwapsCurrentAndPreviousAndReRegisters(t *testing.T) {
	base := t.TempDir()
	modelPath := filepath.Join(base, "models", "editorial", "current")
	candidateDir := filepath.Join(base, "candidate")
	registry := &fakeRegistry{}
	deps := modelpromotion.Deps{Registry: registry, UOW: fakeUOW{}}

	writeCandidate(t, candidateDir, editorial.Model{Version: "v1", Algorithm: "naive_bayes", Weights: map[string]float64{}}, editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes"})
	if err := modelpromotion.Promote(context.Background(), deps, candidateDir, "v1", modelPath); err != nil {
		t.Fatalf("Promote v1: %v", err)
	}
	writeCandidate(t, candidateDir, editorial.Model{Version: "v2", Algorithm: "naive_bayes", Weights: map[string]float64{}}, editorialmodel.Manifest{ID: "v2", Version: "v2", Algorithm: "naive_bayes"})
	if err := modelpromotion.Promote(context.Background(), deps, candidateDir, "v2", modelPath); err != nil {
		t.Fatalf("Promote v2: %v", err)
	}

	if err := modelpromotion.Rollback(context.Background(), deps, modelPath); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	current, err := editorialmodel.Load(modelPath)
	if err != nil || current.Version != "v1" {
		t.Fatalf("current after rollback = %+v, err=%v, want version v1", current, err)
	}
	previousPath := filepath.Join(base, "models", "editorial", "previous")
	previous, err := editorialmodel.Load(previousPath)
	if err != nil || previous.Version != "v2" {
		t.Fatalf("previous after rollback = %+v, err=%v, want version v2 (the rolled-back-from model)", previous, err)
	}
}

func TestRollback_RefusesWhenSwapTmpAlreadyExistsFromInterruptedAttempt(t *testing.T) {
	base := t.TempDir()
	modelPath := filepath.Join(base, "models", "editorial", "current")
	candidateDir := filepath.Join(base, "candidate")
	registry := &fakeRegistry{}
	deps := modelpromotion.Deps{Registry: registry, UOW: fakeUOW{}}

	writeCandidate(t, candidateDir, editorial.Model{Version: "v1", Algorithm: "naive_bayes", Weights: map[string]float64{}}, editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes"})
	if err := modelpromotion.Promote(context.Background(), deps, candidateDir, "v1", modelPath); err != nil {
		t.Fatalf("Promote v1: %v", err)
	}
	writeCandidate(t, candidateDir, editorial.Model{Version: "v2", Algorithm: "naive_bayes", Weights: map[string]float64{}}, editorialmodel.Manifest{ID: "v2", Version: "v2", Algorithm: "naive_bayes"})
	if err := modelpromotion.Promote(context.Background(), deps, candidateDir, "v2", modelPath); err != nil {
		t.Fatalf("Promote v2: %v", err)
	}

	// Simulate a stranded artifact left behind by a previously interrupted
	// Rollback: something already occupies the swapTmp path
	// (modelPath + ".rollback-tmp") before this Rollback call even starts.
	swapTmp := modelPath + ".rollback-tmp"
	if err := os.MkdirAll(swapTmp, 0o750); err != nil {
		t.Fatalf("create stray swapTmp dir: %v", err)
	}
	strayFile := filepath.Join(swapTmp, "stranded.txt")
	if err := os.WriteFile(strayFile, []byte("stranded artifact from a failed rollback"), 0o600); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	err := modelpromotion.Rollback(context.Background(), deps, modelPath)
	if err == nil {
		t.Fatal("Rollback() error = nil, want an error refusing to proceed when swapTmp is already occupied")
	}
	if !strings.Contains(err.Error(), swapTmp) || !strings.Contains(err.Error(), "already exists from an interrupted rollback") {
		t.Errorf("Rollback() error = %q, want it to name %q and explain it's from an interrupted rollback", err.Error(), swapTmp)
	}

	// The guard must fire before any swap step runs: modelPath (current)
	// and previousPath must both be completely untouched.
	current, err := editorialmodel.Load(modelPath)
	if err != nil || current.Version != "v2" {
		t.Fatalf("current after refused rollback = %+v, err=%v, want unchanged version v2", current, err)
	}
	previousPath := filepath.Join(base, "models", "editorial", "previous")
	previous, err := editorialmodel.Load(previousPath)
	if err != nil || previous.Version != "v1" {
		t.Fatalf("previous after refused rollback = %+v, err=%v, want unchanged version v1", previous, err)
	}

	// And the stray file itself must still be there, untouched — proving
	// the guard didn't clobber it on its way to refusing.
	if data, err := os.ReadFile(strayFile); err != nil || string(data) != "stranded artifact from a failed rollback" {
		t.Fatalf("stray file after refused rollback: data=%q err=%v, want it preserved untouched", data, err)
	}
}

func TestRollback_ErrorsWhenNoPreviousSlotExists(t *testing.T) {
	base := t.TempDir()
	modelPath := filepath.Join(base, "models", "editorial", "current")
	candidateDir := filepath.Join(base, "candidate")
	deps := modelpromotion.Deps{Registry: &fakeRegistry{}, UOW: fakeUOW{}}

	writeCandidate(t, candidateDir, editorial.Model{Version: "v1", Algorithm: "naive_bayes", Weights: map[string]float64{}}, editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes"})
	if err := modelpromotion.Promote(context.Background(), deps, candidateDir, "v1", modelPath); err != nil {
		t.Fatalf("Promote v1: %v", err)
	}

	if err := modelpromotion.Rollback(context.Background(), deps, modelPath); err == nil {
		t.Fatal("Rollback() error = nil, want an error (no previous/ slot exists yet — nothing to roll back to)")
	}
}
