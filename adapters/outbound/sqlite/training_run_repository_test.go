package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

func TestTrainingRunRepository_SaveAndLatest(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewTrainingRunRepository(db)
	ctx := context.Background()

	// training_runs.model_version has a FOREIGN KEY to model_registry.version
	// (schema predates this task, migration 0005), so exercise Save through
	// registered versions the way `train run` would in production.
	registry := sqliteadapter.NewModelRegistryRepository(db)
	for _, version := range []string{"nb-v13", "nb-v14"} {
		if err := registry.Register(ctx, training.ModelMetadata{Version: version, Kind: "editorial-classifier"}); err != nil {
			t.Fatalf("Register(%q) error = %v", version, err)
		}
	}

	older := training.Run{
		ID: "run-1", StartedAt: time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, 9, 20, 3, 5, 0, 0, time.UTC),
		ModelVersion: "nb-v13", DatasetSummary: `{"gold":900,"silver":280}`,
		Status: "succeeded", Report: "Training run 2026-09-20T03:00:00Z\n...",
	}
	if err := repo.Save(ctx, older); err != nil {
		t.Fatalf("Save() older error = %v", err)
	}

	newer := training.Run{
		ID: "run-2", StartedAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, 9, 22, 3, 6, 0, 0, time.UTC),
		ModelVersion: "nb-v14", DatasetSummary: `{"gold":912,"silver":292}`,
		Status: "succeeded", Report: "Training run 2026-09-22T03:00:00Z\n...",
	}
	if err := repo.Save(ctx, newer); err != nil {
		t.Fatalf("Save() newer error = %v", err)
	}

	got, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if got == nil {
		t.Fatal("Latest() = nil, want the newer run")
	}
	if got.ID != "run-2" || got.ModelVersion != "nb-v14" || got.DatasetSummary != `{"gold":912,"silver":292}` {
		t.Errorf("Latest() = %+v, want run-2's fields", *got)
	}
}

func TestTrainingRunRepository_Latest_NoRuns(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewTrainingRunRepository(db)

	got, err := repo.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if got != nil {
		t.Errorf("Latest() = %+v, want nil for an empty table", got)
	}
}

// TestTrainingRunRepository_LatestSucceeded_SkipsFailedRun exercises the
// fix for the review finding that a failed run's empty DatasetSummary
// silently reset trainrun.GatherAutoSignals' Gold/Silver baseline to
// zero: LatestSucceeded must skip a later "failed" row and return the
// most recent "succeeded" one instead, unlike Latest which is unaware
// of Status.
func TestTrainingRunRepository_LatestSucceeded_SkipsFailedRun(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewTrainingRunRepository(db)
	ctx := context.Background()

	registry := sqliteadapter.NewModelRegistryRepository(db)
	if err := registry.Register(ctx, training.ModelMetadata{Version: "nb-v13", Kind: "editorial-classifier"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	succeeded := training.Run{
		ID: "run-1", StartedAt: time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC),
		FinishedAt:     time.Date(2026, 9, 20, 3, 5, 0, 0, time.UTC),
		ModelVersion:   "nb-v13",
		DatasetSummary: `{"gold":900,"silver":280}`,
		Status:         "succeeded", Report: "Training run 2026-09-20T03:00:00Z\n...",
	}
	if err := repo.Save(ctx, succeeded); err != nil {
		t.Fatalf("Save() succeeded error = %v", err)
	}

	failed := training.Run{
		ID: "run-2", StartedAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 9, 22, 3, 0, 5, 0, time.UTC),
		Status:     "failed", Report: "trainrun: build: ollama unavailable",
	}
	if err := repo.Save(ctx, failed); err != nil {
		t.Fatalf("Save() failed error = %v", err)
	}

	got, err := repo.LatestSucceeded(ctx)
	if err != nil {
		t.Fatalf("LatestSucceeded() error = %v", err)
	}
	if got == nil {
		t.Fatal("LatestSucceeded() = nil, want run-1 (the only succeeded run)")
	}
	if got.ID != "run-1" || got.DatasetSummary != `{"gold":900,"silver":280}` {
		t.Errorf("LatestSucceeded() = %+v, want run-1's fields, not the later failed run-2", *got)
	}

	// Sanity check the two methods now genuinely diverge: Latest is
	// Status-blind and must return the later failed row.
	latest, err := repo.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if latest == nil || latest.ID != "run-2" {
		t.Errorf("Latest() = %+v, want run-2 (the most recent row regardless of status)", latest)
	}
}

func TestTrainingRunRepository_LatestSucceeded_NoRuns(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewTrainingRunRepository(db)

	got, err := repo.LatestSucceeded(context.Background())
	if err != nil {
		t.Fatalf("LatestSucceeded() error = %v", err)
	}
	if got != nil {
		t.Errorf("LatestSucceeded() = %+v, want nil for an empty table", got)
	}
}
