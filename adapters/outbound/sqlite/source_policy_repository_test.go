package sqlite_test

import (
	"context"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestSourcePolicyRepository_SaveAndByArticle(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSourcePolicyRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	decidedAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	rec := out.SourcePolicyDecisionRecord{
		ID: "spd1", ArticleID: "a1", SourceID: "sprisotopes", Authority: "official_team",
		PolicyVersion: "1", ClusterID: "", DedupMatchType: "near", MemberRole: "confirmation",
		Novelty: "confirmation", DecisionCode: "official_confirmation_no_publish",
		WouldPublishStandard: false, WouldPublishSpecial: false, Mode: "enforce",
		Explanation: "test", DecidedAt: decidedAt,
	}
	if err := repo.Save(ctx, rec); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := repo.ByArticle(ctx, "a1")
	if err != nil {
		t.Fatalf("ByArticle() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(ByArticle()) = %d, want 1", len(got))
	}
	if got[0] != rec {
		t.Errorf("ByArticle()[0] = %+v, want %+v", got[0], rec)
	}
}

func TestSourcePolicyRepository_ListByRange_FiltersBySourceAndMode(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSourcePolicyRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")
	mustSaveArticle(t, articles, "a2")

	base := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	must := func(rec out.SourcePolicyDecisionRecord) {
		t.Helper()
		if err := repo.Save(ctx, rec); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}
	must(out.SourcePolicyDecisionRecord{ID: "1", ArticleID: "a1", SourceID: "sprisotopes", Mode: "shadow", DecisionCode: "x", DecidedAt: base.Add(1 * time.Hour)})
	must(out.SourcePolicyDecisionRecord{ID: "2", ArticleID: "a2", SourceID: "other-source", Mode: "shadow", DecisionCode: "x", DecidedAt: base.Add(2 * time.Hour)})
	must(out.SourcePolicyDecisionRecord{ID: "3", ArticleID: "a1", SourceID: "sprisotopes", Mode: "enforce", DecisionCode: "x", DecidedAt: base.Add(3 * time.Hour)})
	must(out.SourcePolicyDecisionRecord{ID: "4", ArticleID: "a1", SourceID: "sprisotopes", Mode: "shadow", DecisionCode: "x", DecidedAt: base.Add(30 * time.Hour)}) // outside range

	got, err := repo.ListByRange(ctx, base, base.Add(24*time.Hour), "sprisotopes", "shadow")
	if err != nil {
		t.Fatalf("ListByRange() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("ListByRange() = %+v, want just record 1", got)
	}

	all, err := repo.ListByRange(ctx, base, base.Add(24*time.Hour), "", "")
	if err != nil {
		t.Fatalf("ListByRange() error = %v", err)
	}
	// Records 1, 2, and 3 all fall within [base, base+24h) regardless of
	// source/mode; only record 4 (base+30h) is outside the range. Unfiltered
	// (sourceID="" and mode="") means no source/mode filter is applied, so
	// all three in-range records come back.
	if len(all) != 3 {
		t.Errorf("len(ListByRange unfiltered) = %d, want 3", len(all))
	}
}

func TestSourcePolicyRepository_SaveAndByArticle_RoundTripsCooldownOutcome(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewSourcePolicyRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	decidedAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	rec := out.SourcePolicyDecisionRecord{
		ID: "d1", ArticleID: "a1", SourceID: "s1", DecisionCode: "publish_official_confirmation",
		Mode: "enforce", CooldownOutcome: "reserved", DecidedAt: decidedAt,
	}
	if err := repo.Save(ctx, rec); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := repo.ByArticle(ctx, "a1")
	if err != nil {
		t.Fatalf("ByArticle() error = %v", err)
	}
	if len(got) != 1 || got[0].CooldownOutcome != "reserved" {
		t.Fatalf("ByArticle() = %+v, want one record with CooldownOutcome=reserved", got)
	}
}
