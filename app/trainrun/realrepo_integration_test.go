package trainrun_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainrun"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// newMigratedDB opens a real, migrated SQLite database with
// PRAGMA foreign_keys=ON -- exactly what production runs against. The
// fake TrainingRunRepository used by the other tests in this package
// enforces no constraints, which is why the training_runs.model_version
// FOREIGN KEY violation on the reject and failure paths went unnoticed
// through nine task reviews.
func newMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := infrasqlite.Open(filepath.Join(t.TempDir(), "test.db"), 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db
}

// TestTrainRun_RejectPath_PersistsThroughRealRepository is the missing
// test class: the reject path is a documented non-error outcome, so its
// Run row must actually reach the real database (where model_version has
// a FOREIGN KEY to model_registry.version).
func TestTrainRun_RejectPath_PersistsThroughRealRepository(t *testing.T) {
	db := newMigratedDB(t)
	runRepo := sqliteadapter.NewTrainingRunRepository(db)

	proposals := &fakeProposalRepo{}
	deps, registry, _ := baseDeps(proposals)
	deps.RunRepo = runRepo
	impossibleGates := editorialtrain.GateConfig{
		MinPrecision: 1.0, MaxRecallRegression: 0.0, MinPRAUC: 1.0, MaxFalsePositiveRate: 0.0,
		MinPerSourcePrecision: 1.0, MinSourceExamples: 1000, MaxArtifactSizeBytes: 10_000_000, MaxInferenceNanos: 1_000_000_000,
	}
	opts := baseOptions(t, impossibleGates, modelpromotion.PromotionCriteria{})

	ctx := context.Background()
	outcome, err := trainrun.Run(ctx, deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Run() error = %v (a gate rejection must be a successful run, not an error)", err)
	}
	if outcome.Run.Status != "succeeded" {
		t.Errorf("Run.Status = %q, want \"succeeded\"", outcome.Run.Status)
	}
	if outcome.Run.ModelVersion != "" {
		t.Errorf("Run.ModelVersion = %q, want \"\" (no candidate was promoted; the placeholder belongs in the report text only)", outcome.Run.ModelVersion)
	}
	if !strings.Contains(outcome.Report, "Recommendation: REJECT") {
		t.Errorf("Report = %q, want it to contain %q", outcome.Report, "Recommendation: REJECT")
	}
	if !strings.Contains(outcome.Report, "Candidate (none) vs. production") {
		t.Errorf("Report = %q, want the rendered report to name the candidate as (none)", outcome.Report)
	}
	for _, m := range registry.registered {
		if m.Status == "promoted" {
			t.Errorf("registry.registered contains a \"promoted\" row %+v, want none", m)
		}
	}

	got, err := runRepo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if got == nil {
		t.Fatal("Latest() = nil, want the rejected run's persisted row")
	}
	if got.ID != outcome.Run.ID || got.Status != "succeeded" || got.ModelVersion != "" {
		t.Errorf("Latest() = %+v, want the rejected run with an empty ModelVersion", *got)
	}
	if !strings.Contains(got.Report, "Recommendation: REJECT") {
		t.Errorf("Latest().Report = %q, want the rendered reject report", got.Report)
	}
}

// TestTrainRun_StageFailure_PersistsThroughRealRepository asserts the
// whole point of persisting failed runs -- the audit row -- actually
// survives the real repository's constraints.
func TestTrainRun_StageFailure_PersistsThroughRealRepository(t *testing.T) {
	db := newMigratedDB(t)
	runRepo := sqliteadapter.NewTrainingRunRepository(db)

	proposals := &fakeProposalRepo{listByStatusErr: fmt.Errorf("llmclient: connect: dial tcp 127.0.0.1:11434: connect: connection refused")}
	deps, _, _ := baseDeps(proposals)
	deps.RunRepo = runRepo
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	ctx := context.Background()
	_, err := trainrun.Run(ctx, deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Run() error = nil, want an error naming the analyze stage")
	}
	if !strings.HasPrefix(err.Error(), "trainrun: analyze:") {
		t.Fatalf("Run() error = %q, want it to start with %q", err.Error(), "trainrun: analyze:")
	}
	if strings.Contains(err.Error(), "failed to save failed run") {
		t.Fatalf("Run() error = %q, want the failed run's audit row to persist, not to be dropped", err.Error())
	}

	got, err := runRepo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if got == nil {
		t.Fatal("Latest() = nil, want the failed run's audit row")
	}
	if got.Status != "failed" || got.ModelVersion != "" {
		t.Errorf("Latest() = %+v, want Status=\"failed\" with an empty ModelVersion", *got)
	}
	if !strings.Contains(got.Report, "trainrun: analyze:") {
		t.Errorf("Latest().Report = %q, want it to record the analyze-stage failure", got.Report)
	}
}

// TestTrainRun_EvaluateStageFailure_NoPromotedRow covers finding 3: an
// evaluate-stage failure must be reported as such and must leave no
// *promoted* registry row behind (candidate rows registered by
// modeltraining.RunAndRegister before the verdict are expected and
// harmless). The failure is forced by pointing CandidatesDir at a path
// that cannot be created (a regular file), which fails when
// RunAndRegister writes the first candidate's artifacts.
func TestTrainRun_EvaluateStageFailure_NoPromotedRow(t *testing.T) {
	db := newMigratedDB(t)
	runRepo := sqliteadapter.NewTrainingRunRepository(db)

	proposals := &fakeProposalRepo{}
	deps, registry, _ := baseDeps(proposals)
	deps.RunRepo = runRepo
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{})

	// A regular file where the candidates directory should go: every
	// attempt to create a candidate subdirectory under it fails.
	blocker := filepath.Join(t.TempDir(), "candidates-blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	opts.CandidatesDir = blocker

	ctx := context.Background()
	_, err := trainrun.Run(ctx, deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Run() error = nil, want an error naming the evaluate stage")
	}
	if !strings.HasPrefix(err.Error(), "trainrun: evaluate:") {
		t.Fatalf("Run() error = %q, want it to start with %q", err.Error(), "trainrun: evaluate:")
	}
	for _, m := range registry.registered {
		if m.Status == "promoted" {
			t.Errorf("registry.registered contains a \"promoted\" row %+v, want none after an evaluate-stage failure", m)
		}
	}
	if _, statErr := os.Stat(opts.ModelPath); statErr == nil {
		t.Errorf("production model directory %s exists, want it untouched", opts.ModelPath)
	}

	got, err := runRepo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if got == nil || got.Status != "failed" {
		t.Fatalf("Latest() = %+v, want the failed evaluate run's audit row", got)
	}
}

// TestTrainRun_PromoteStageFailure_NoPromotedRow covers finding 3's
// promote-stage case as far as trainrun controls it: modelpromotion.Promote
// copies the candidate artifact to opts.ModelPath before its registry
// transaction, so the failure is forced at the copy boundary by making
// opts.ModelPath un-writable (a directory whose parent is a regular
// file). trainrun must report a promote-stage failure, persist the
// audit row, and leave no promoted registry row.
func TestTrainRun_PromoteStageFailure_NoPromotedRow(t *testing.T) {
	db := newMigratedDB(t)
	runRepo := sqliteadapter.NewTrainingRunRepository(db)

	proposals := &fakeProposalRepo{}
	deps, registry, _ := baseDeps(proposals)
	deps.RunRepo = runRepo
	opts := baseOptions(t, permissiveGateCfg(), modelpromotion.PromotionCriteria{MinPrecision: 0.0})

	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	opts.ModelPath = filepath.Join(blocker, "current")

	ctx := context.Background()
	_, err := trainrun.Run(ctx, deps, opts, time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Run() error = nil, want an error naming the promote stage")
	}
	if !strings.HasPrefix(err.Error(), "trainrun: promote:") {
		t.Fatalf("Run() error = %q, want it to start with %q", err.Error(), "trainrun: promote:")
	}
	for _, m := range registry.registered {
		if m.Status == "promoted" {
			t.Errorf("registry.registered contains a \"promoted\" row %+v, want none after a promote-stage failure", m)
		}
	}

	got, err := runRepo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if got == nil || got.Status != "failed" {
		t.Fatalf("Latest() = %+v, want the failed promote run's audit row", got)
	}
}
