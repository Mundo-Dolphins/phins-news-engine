package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
)

func TestShadowComparisonRepository_SaveAndListByRun(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewShadowComparisonRepository(db)
	ctx := context.Background()

	rows := []training.ShadowDisagreement{
		{
			RunID: "run-1", ArticleID: "a1", Title: "Isotopes sign new WR",
			ProductionVersion: "nb-2026-09-01", ProductionZone: "reject", ProductionScore: 20,
			CandidateVersion: "nb-2026-09-08-candidate", CandidateZone: "accept", CandidateScore: 80,
			CreatedAt: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		},
		{
			RunID: "run-1", ArticleID: "a2", Title: "Unrelated NFL rumor",
			ProductionVersion: "nb-2026-09-01", ProductionZone: "accept", ProductionScore: 90,
			CandidateVersion: "nb-2026-09-08-candidate", CandidateZone: "reject", CandidateScore: 10,
			CreatedAt: time.Date(2026, 9, 21, 0, 0, 1, 0, time.UTC),
		},
	}
	if err := repo.SaveDisagreements(ctx, rows); err != nil {
		t.Fatalf("SaveDisagreements() error = %v", err)
	}

	// A different run's rows must not leak into this run's listing.
	if err := repo.SaveDisagreements(ctx, []training.ShadowDisagreement{
		{RunID: "run-2", ArticleID: "a3", Title: "other run", CreatedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("SaveDisagreements() for run-2 error = %v", err)
	}

	got, err := repo.ListByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListByRun() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByRun() returned %d rows, want 2", len(got))
	}
	if got[0].ArticleID != "a2" || got[1].ArticleID != "a1" {
		t.Errorf("ListByRun() order = [%s, %s], want [a2, a1] (most recently created first)", got[0].ArticleID, got[1].ArticleID)
	}
	if got[1].ProductionZone != "reject" || got[1].CandidateZone != "accept" || got[1].CandidateScore != 80 {
		t.Errorf("got[1] = %+v, want the a1 row's fields preserved", got[1])
	}
}

func TestShadowComparisonRepository_SaveDisagreements_UpsertsOnConflict(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewShadowComparisonRepository(db)
	ctx := context.Background()

	row := training.ShadowDisagreement{RunID: "run-1", ArticleID: "a1", Title: "v1", CandidateScore: 1, CreatedAt: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)}
	if err := repo.SaveDisagreements(ctx, []training.ShadowDisagreement{row}); err != nil {
		t.Fatalf("first SaveDisagreements() error = %v", err)
	}
	row.Title = "v2"
	row.CandidateScore = 2
	if err := repo.SaveDisagreements(ctx, []training.ShadowDisagreement{row}); err != nil {
		t.Fatalf("second SaveDisagreements() error = %v", err)
	}

	got, err := repo.ListByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListByRun() error = %v", err)
	}
	if len(got) != 1 || got[0].Title != "v2" || got[0].CandidateScore != 2 {
		t.Errorf("ListByRun() = %+v, want a single upserted row with Title=v2 CandidateScore=2", got)
	}
}
