package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

func TestModelRegistryRepository_SeedPlaceholderIsGettable(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewModelRegistryRepository(db)

	got, err := repo.Get(context.Background(), "placeholder-v0")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !got.Promoted() {
		t.Error("seeded placeholder-v0 should be promoted (migration 0009 sets it)")
	}
}

func TestModelRegistryRepository_RegisterAndPromote(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewModelRegistryRepository(db)
	ctx := context.Background()

	m := training.ModelMetadata{Version: "v1", Kind: "editorial-classifier", Path: "/models/v1.bin", Status: "candidate", TrainedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Metrics: `{"f1":0.9}`}
	if err := repo.Register(ctx, m); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	got, err := repo.Get(ctx, "v1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Promoted() {
		t.Error("Promoted() = true for status candidate, want false")
	}

	m.Status = "promoted"
	m.PromotedAt = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := repo.Register(ctx, m); err != nil {
		t.Fatalf("re-Register() with promotion error = %v", err)
	}
	got, err = repo.Get(ctx, "v1")
	if err != nil {
		t.Fatalf("Get() after promotion error = %v", err)
	}
	if !got.Promoted() {
		t.Error("Promoted() = false after setting Status=promoted, want true")
	}
	if got.Metrics != `{"f1":0.9}` {
		t.Errorf("Metrics = %q, want %q", got.Metrics, `{"f1":0.9}`)
	}
}

func TestModelRegistryRepository_Get_NotFound(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewModelRegistryRepository(db)

	_, err := repo.Get(context.Background(), "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get() error = %v, want wrapped sql.ErrNoRows", err)
	}
}

func TestModelRegistryRepository_RegisterAndGet_NewColumns(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewModelRegistryRepository(db)
	ctx := context.Background()

	m := training.ModelMetadata{
		Version: "v2", Kind: "editorial-classifier", Algorithm: "linear_svm",
		FeatureSchemaVersion: "fs-1", DatasetHash: "dh1", Thresholds: `{"reject":20,"accept":80}`,
		Config: `{"seed":42}`, ArtifactHash: "ah1", Status: "validated",
	}
	if err := repo.Register(ctx, m); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	got, err := repo.Get(ctx, "v2")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Algorithm != "linear_svm" || got.FeatureSchemaVersion != "fs-1" || got.DatasetHash != "dh1" ||
		got.Thresholds != `{"reject":20,"accept":80}` || got.Config != `{"seed":42}` || got.ArtifactHash != "ah1" || got.Status != "validated" {
		t.Errorf("Get() = %+v, want new columns to round-trip", got)
	}
}

func TestModelRegistryRepository_ListByStatus(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewModelRegistryRepository(db)
	ctx := context.Background()

	for _, m := range []training.ModelMetadata{
		{Version: "c1", Kind: "editorial-classifier", Status: "candidate"},
		{Version: "c2", Kind: "editorial-classifier", Status: "candidate"},
		{Version: "p1", Kind: "editorial-classifier", Status: "promoted"},
	} {
		if err := repo.Register(ctx, m); err != nil {
			t.Fatalf("Register(%s) error = %v", m.Version, err)
		}
	}

	candidates, err := repo.ListByStatus(ctx, "candidate")
	if err != nil {
		t.Fatalf("ListByStatus() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(candidates))
	}

	promoted, err := repo.ListByStatus(ctx, "promoted")
	if err != nil {
		t.Fatalf("ListByStatus(promoted) error = %v", err)
	}
	// placeholder-v0 (seeded by migration 0005/0009) is also status=promoted.
	if len(promoted) != 2 {
		t.Fatalf("len(promoted) = %d, want 2 (placeholder-v0 + p1)", len(promoted))
	}

	none, err := repo.ListByStatus(ctx, "retired")
	if err != nil {
		t.Fatalf("ListByStatus(retired) error = %v", err)
	}
	if len(none) != 0 {
		t.Errorf("len(none) = %d, want 0", len(none))
	}
}
