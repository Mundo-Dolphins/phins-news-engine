package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestEditorialCycleSessionRepository_CreateUpdateGetLatestRunning(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEditorialCycleSessionRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	s := out.EditorialCycleSession{
		ID: "sess-1", StartedAt: now, UpdatedAt: now,
		Status: "running", CurrentStep: "review", FlagsJSON: `{"sample_size":50}`,
	}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	s.CurrentStep = "review_done"
	s.UpdatedAt = now.Add(time.Hour)
	if err := repo.Update(ctx, s); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err := repo.GetLatestRunning(ctx)
	if err != nil {
		t.Fatalf("GetLatestRunning() error = %v", err)
	}
	if got.ID != "sess-1" || got.CurrentStep != "review_done" {
		t.Errorf("GetLatestRunning() = %+v, want CurrentStep %q", got, "review_done")
	}
}

func TestEditorialCycleSessionRepository_GetLatestRunning_NoneRunning(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEditorialCycleSessionRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	completed := out.EditorialCycleSession{ID: "sess-done", StartedAt: now, UpdatedAt: now, Status: "completed", CurrentStep: "review"}
	if err := repo.Create(ctx, completed); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	_, err := repo.GetLatestRunning(ctx)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetLatestRunning() error = %v, want wrapped sql.ErrNoRows", err)
	}
}

func TestEditorialCycleSessionRepository_GetLatestCompleted(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEditorialCycleSessionRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	running := out.EditorialCycleSession{ID: "sess-running", StartedAt: now, UpdatedAt: now, Status: "running", CurrentStep: "review"}
	if err := repo.Create(ctx, running); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	older := out.EditorialCycleSession{ID: "sess-older-done", StartedAt: now, UpdatedAt: now, Status: "completed", CurrentStep: "review"}
	if err := repo.Create(ctx, older); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	newer := out.EditorialCycleSession{ID: "sess-newer-done", StartedAt: now, UpdatedAt: now.Add(time.Hour), Status: "completed", CurrentStep: "review"}
	if err := repo.Create(ctx, newer); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := repo.GetLatestCompleted(ctx)
	if err != nil {
		t.Fatalf("GetLatestCompleted() error = %v", err)
	}
	if got.ID != "sess-newer-done" {
		t.Errorf("GetLatestCompleted().ID = %q, want %q", got.ID, "sess-newer-done")
	}
}

func TestEditorialCycleSessionRepository_GetLatestResumable_MatchesRunningPausedFailed_NotCompletedOrCancelled(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEditorialCycleSessionRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// All five statuses are seeded to prove GetLatestResumable both
	// matches every resumable one (running/paused/failed) and excludes
	// every terminal one (completed/cancelled), regardless of recency.
	tests := []struct {
		status    string
		updatedAt time.Time
	}{
		{status: "cancelled", updatedAt: now},
		{status: "completed", updatedAt: now.Add(time.Hour)},
		{status: "failed", updatedAt: now.Add(2 * time.Hour)},
		{status: "paused", updatedAt: now.Add(3 * time.Hour)},
		{status: "running", updatedAt: now.Add(4 * time.Hour)},
	}
	for _, tt := range tests {
		s := out.EditorialCycleSession{
			ID: "sess-" + tt.status, StartedAt: now, UpdatedAt: tt.updatedAt,
			Status: tt.status, CurrentStep: "review",
		}
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("Create(%s) error = %v", tt.status, err)
		}
	}

	// The most recently updated resumable session is "sess-running"
	// (updated_at = now+4h) — even though "sess-completed" is more
	// recently updated than "sess-failed" and "sess-paused", it must
	// never surface here because completed/cancelled are terminal.
	got, err := repo.GetLatestResumable(ctx)
	if err != nil {
		t.Fatalf("GetLatestResumable() error = %v", err)
	}
	if got.ID != "sess-running" {
		t.Errorf("GetLatestResumable().ID = %q, want %q", got.ID, "sess-running")
	}

	// Now mark "sess-running" completed, leaving "sess-paused" as the
	// most recently updated resumable row.
	running := out.EditorialCycleSession{
		ID: "sess-running", StartedAt: now, UpdatedAt: now.Add(5 * time.Hour),
		Status: "completed", CurrentStep: "review",
	}
	if err := repo.Update(ctx, running); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err = repo.GetLatestResumable(ctx)
	if err != nil {
		t.Fatalf("GetLatestResumable() (after completing sess-running) error = %v", err)
	}
	if got.ID != "sess-paused" {
		t.Errorf("GetLatestResumable().ID = %q, want %q", got.ID, "sess-paused")
	}

	// Finally, resolve every resumable session and confirm a wrapped
	// sql.ErrNoRows comes back.
	for _, id := range []string{"sess-paused", "sess-failed"} {
		if err := repo.Update(ctx, out.EditorialCycleSession{
			ID: id, StartedAt: now, UpdatedAt: now.Add(6 * time.Hour),
			Status: "cancelled", CurrentStep: "review",
		}); err != nil {
			t.Fatalf("Update(%s) error = %v", id, err)
		}
	}
	_, err = repo.GetLatestResumable(ctx)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetLatestResumable() error = %v, want wrapped sql.ErrNoRows", err)
	}
}

// TestEditorialCycleSessionRepository_TotalCandidatesRoundTrips pins
// migration 0025's column. It is the only record of how many candidates
// a session froze — editorial_cycle_session_candidates rows cascade away
// with their articles, so counting the survivors can never recover it —
// and editorialcycle.StartOrResume's "every candidate was purged →
// cancelled" guard reads it, so it has to survive both the INSERT and
// every subsequent status transition's UPDATE.
func TestEditorialCycleSessionRepository_TotalCandidatesRoundTrips(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEditorialCycleSessionRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	s := out.EditorialCycleSession{
		ID: "sess-1", StartedAt: now, UpdatedAt: now, Status: "paused",
		CurrentStep: "review", FlagsJSON: `{"sample_size":7}`, TotalCandidates: 7,
	}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got, err := repo.GetLatestResumable(ctx)
	if err != nil {
		t.Fatalf("GetLatestResumable() error = %v", err)
	}
	if got.TotalCandidates != 7 {
		t.Errorf("GetLatestResumable().TotalCandidates = %d, want 7", got.TotalCandidates)
	}

	s.Status = "running"
	s.UpdatedAt = now.Add(time.Hour)
	if err := repo.Update(ctx, s); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got, err = repo.GetLatestRunning(ctx)
	if err != nil {
		t.Fatalf("GetLatestRunning() error = %v", err)
	}
	if got.TotalCandidates != 7 {
		t.Errorf("GetLatestRunning().TotalCandidates = %d, want the baseline preserved across the status transition", got.TotalCandidates)
	}

	// A session created before migration 0025 (or one that genuinely
	// froze nothing) reads back as 0 — the "no baseline recorded" value
	// that suppresses both the drift warning and the cancel guard.
	legacy := out.EditorialCycleSession{
		ID: "sess-legacy", StartedAt: now, UpdatedAt: now.Add(2 * time.Hour),
		Status: "paused", CurrentStep: "review", FlagsJSON: `{}`,
	}
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatalf("Create(legacy) error = %v", err)
	}
	got, err = repo.GetLatestResumable(ctx)
	if err != nil {
		t.Fatalf("GetLatestResumable() (legacy) error = %v", err)
	}
	if got.ID != "sess-legacy" || got.TotalCandidates != 0 {
		t.Errorf("GetLatestResumable() = %+v, want sess-legacy with TotalCandidates 0", got)
	}
}

func TestEditorialCycleSessionRepository_GetLatestCompleted_NoneCompleted(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewEditorialCycleSessionRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := repo.Create(ctx, out.EditorialCycleSession{ID: "sess-1", StartedAt: now, UpdatedAt: now, Status: "running", CurrentStep: "review"}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	_, err := repo.GetLatestCompleted(ctx)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetLatestCompleted() error = %v, want wrapped sql.ErrNoRows", err)
	}
}
