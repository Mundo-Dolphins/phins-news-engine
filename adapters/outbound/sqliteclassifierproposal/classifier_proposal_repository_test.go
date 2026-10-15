package sqliteclassifierproposal_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteclassifierproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteclassifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var migratedTestDBTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-sqliteclassifierproposal-test-template-")
	if err != nil {
		panic(err)
	}
	migratedTestDBTemplate = filepath.Join(templateDir, "migrated.db")
	db, err := infrasqlite.Open(migratedTestDBTemplate, 5*time.Second)
	if err != nil {
		panic(err)
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		_ = db.Close()
		panic(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		panic(err)
	}
	if err := db.Close(); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(templateDir)
	os.Exit(code)
}

func newTestDB(t testing.TB) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	src, err := os.Open(migratedTestDBTemplate)
	if err != nil {
		t.Fatalf("open template: %v", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		t.Fatalf("copy db: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRepositoryUpsertAggregatesPendingProposalAndStatus(t *testing.T) {
	ctx := context.Background()
	repo := sqliteclassifierproposal.NewClassifierProposalRepository(newTestDB(t))
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	p := classifierproposal.Proposal{
		Kind: classifierproposal.KindTerm, Category: "nfl_people_context", Value: "Jai Rene Mossbank",
		NormalizedValue: "jai rene mossbank", Operation: "add", Confidence: 0.7, Occurrences: 1,
		Status: classifierproposal.StatusPending, FirstSeen: now, LastSeen: now,
		AnalyzerVersion: "test", CreatedAt: now,
		Sources: []classifierproposal.Source{{ArticleID: "a1", ArticleTitle: "one"}},
	}
	id, err := repo.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("Upsert(first) error = %v", err)
	}
	p.Occurrences = 2
	p.Confidence = 0.9
	p.Sources = []classifierproposal.Source{{ArticleID: "a2", ArticleTitle: "two"}}
	id2, err := repo.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("Upsert(second) error = %v", err)
	}
	if id2 != id {
		t.Fatalf("second id = %q, want %q", id2, id)
	}
	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Occurrences != 3 || got.Confidence != 0.9 || len(got.Sources) != 2 {
		t.Fatalf("merged proposal = %+v, want occurrences=3 confidence=.9 sources=2", got)
	}
	if err := repo.UpdateStatus(ctx, id, classifierproposal.StatusRejected, now); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	got, err = repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get(after status) error = %v", err)
	}
	if got.Status != classifierproposal.StatusRejected {
		t.Errorf("Status = %q, want rejected", got.Status)
	}
}

func seedProposal(t *testing.T, repo *sqliteclassifierproposal.ClassifierProposalRepository, kind classifierproposal.Kind, value string, status classifierproposal.Status, createdAt time.Time) string {
	t.Helper()
	id, err := repo.Upsert(context.Background(), classifierproposal.Proposal{
		Kind: kind, Category: "nfl_people_context", Value: value, NormalizedValue: value, Operation: "add",
		Confidence: 0.5, Occurrences: 1, Status: classifierproposal.StatusPending,
		FirstSeen: createdAt, LastSeen: createdAt, AnalyzerVersion: "test", CreatedAt: createdAt,
		Sources: []classifierproposal.Source{{ArticleID: "src-" + value, ArticleTitle: value}},
	})
	if err != nil {
		t.Fatalf("Upsert(%s) error = %v", value, err)
	}
	if status != classifierproposal.StatusPending {
		if err := repo.UpdateStatus(context.Background(), id, status, createdAt); err != nil {
			t.Fatalf("UpdateStatus(%s) error = %v", value, err)
		}
	}
	return id
}

func TestRepositoryListFiltersByKindAndStatusInCreationOrder(t *testing.T) {
	ctx := context.Background()
	repo := sqliteclassifierproposal.NewClassifierProposalRepository(newTestDB(t))
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	seedProposal(t, repo, classifierproposal.KindTerm, "second", classifierproposal.StatusPending, now.Add(time.Hour))
	seedProposal(t, repo, classifierproposal.KindTerm, "first", classifierproposal.StatusPending, now)
	seedProposal(t, repo, classifierproposal.KindAlias, "alias", classifierproposal.StatusPending, now)
	seedProposal(t, repo, classifierproposal.KindTerm, "rejected", classifierproposal.StatusRejected, now)

	all, err := repo.List(ctx, "", "")
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("List(all) = %d proposals, want 4", len(all))
	}

	got, err := repo.List(ctx, string(classifierproposal.KindTerm), string(classifierproposal.StatusPending))
	if err != nil {
		t.Fatalf("List(term, pending) error = %v", err)
	}
	if len(got) != 2 || got[0].Value != "first" || got[1].Value != "second" {
		t.Fatalf("List(term, pending) = %+v, want [first second]", got)
	}
	if len(got[0].Sources) != 1 || got[0].Sources[0].ArticleID != "src-first" {
		t.Errorf("List did not load sources: %+v", got[0].Sources)
	}
}

func TestRepositoryGetAndUpdateStatusReportNotFound(t *testing.T) {
	ctx := context.Background()
	repo := sqliteclassifierproposal.NewClassifierProposalRepository(newTestDB(t))
	if _, err := repo.Get(ctx, "missing"); !errors.Is(err, classifierproposal.ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if err := repo.UpdateStatus(ctx, "missing", classifierproposal.StatusApproved, time.Now()); !errors.Is(err, classifierproposal.ErrNotFound) {
		t.Errorf("UpdateStatus(missing) error = %v, want ErrNotFound", err)
	}
}

func TestRepositoryProcessedArticlesArePerAnalyzerVersion(t *testing.T) {
	ctx := context.Background()
	repo := sqliteclassifierproposal.NewClassifierProposalRepository(newTestDB(t))
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	if got, err := repo.FilterUnprocessed(ctx, "v1", nil); err != nil || got != nil {
		t.Fatalf("FilterUnprocessed(empty) = %v, %v; want nil, nil", got, err)
	}
	// Marking twice is idempotent (INSERT OR IGNORE).
	for range 2 {
		if err := repo.MarkArticlesProcessed(ctx, "v1", []string{"a1", "a3"}, now); err != nil {
			t.Fatalf("MarkArticlesProcessed() error = %v", err)
		}
	}
	got, err := repo.FilterUnprocessed(ctx, "v1", []string{"a1", "a2", "a3", "a4"})
	if err != nil {
		t.Fatalf("FilterUnprocessed(v1) error = %v", err)
	}
	if len(got) != 2 || got[0] != "a2" || got[1] != "a4" {
		t.Fatalf("FilterUnprocessed(v1) = %v, want [a2 a4] in input order", got)
	}
	got, err = repo.FilterUnprocessed(ctx, "v2", []string{"a1", "a2"})
	if err != nil {
		t.Fatalf("FilterUnprocessed(v2) error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("FilterUnprocessed(v2) = %v, want both: processing is per analyzer version", got)
	}
}

func TestRepositoryUnknownAggregatesAndMetrics(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := sqliteclassifierproposal.NewClassifierProposalRepository(db)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	ts := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

	for _, a := range []struct {
		id      string
		fetched time.Time
	}{{"art-1", now}, {"art-2", now}, {"art-old", since.Add(-time.Hour)}} {
		if _, err := db.Exec(`INSERT INTO articles (id, original_url, canonical_url, title, fetched_at, status)
			VALUES (?, ?, ?, ?, ?, 'fetched')`, a.id, "https://example.com/"+a.id, "https://example.com/"+a.id, "Title "+a.id, ts(a.fetched)); err != nil {
			t.Fatalf("insert article %s: %v", a.id, err)
		}
	}
	for i, o := range []struct {
		article, text string
		at            time.Time
	}{
		{"art-1", "Jai Rene", now.Add(-2 * time.Hour)},
		{"art-2", "Jai Rene", now},
		{"art-1", "Solo Name", now},
		{"art-old", "Jai Rene", since.Add(-time.Hour)}, // before since: ignored
	} {
		if _, err := db.Exec(`INSERT INTO unknown_entity_occurrences (id, article_id, candidate_text, normalized_text, created_at)
			VALUES (?, ?, ?, lower(?), ?)`, fmt.Sprintf("occ-%d", i), o.article, o.text, o.text, ts(o.at)); err != nil {
			t.Fatalf("insert occurrence %d: %v", i, err)
		}
	}

	aggs, err := repo.ListUnknownAggregates(ctx, since, 0, 0) // defaults: min 2, limit 100
	if err != nil {
		t.Fatalf("ListUnknownAggregates() error = %v", err)
	}
	if len(aggs) != 1 {
		t.Fatalf("aggregates = %+v, want only the twice-seen candidate", aggs)
	}
	agg := aggs[0]
	if agg.Normalized != "jai rene" || agg.Occurrences != 2 || agg.ArticleID != "art-1" || agg.ArticleTitle != "Title art-1" {
		t.Errorf("aggregate = %+v", agg)
	}
	if !agg.FirstSeen.Equal(now.Add(-2*time.Hour)) || !agg.LastSeen.Equal(now) {
		t.Errorf("aggregate seen range = %v..%v, want %v..%v", agg.FirstSeen, agg.LastSeen, now.Add(-2*time.Hour), now)
	}
	if aggs, err := repo.ListUnknownAggregates(ctx, since, 1, 1); err != nil || len(aggs) != 1 || aggs[0].Normalized != "jai rene" {
		t.Errorf("ListUnknownAggregates(min 1, limit 1) = %+v, %v; want the most frequent only", aggs, err)
	}

	seedProposal(t, repo, classifierproposal.KindTerm, "approved", classifierproposal.StatusApproved, now)
	seedProposal(t, repo, classifierproposal.KindTerm, "rejected", classifierproposal.StatusRejected, now)
	seedProposal(t, repo, classifierproposal.KindTerm, "pending", classifierproposal.StatusPending, now)
	seedProposal(t, repo, classifierproposal.KindTerm, "too-old", classifierproposal.StatusApproved, since.Add(-time.Hour))
	if err := repo.MarkArticlesProcessed(ctx, "v1", []string{"art-1"}, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkArticlesProcessed(ctx, "v2", []string{"art-1"}, now); err != nil {
		t.Fatal(err)
	}

	m, err := repo.Metrics(ctx, since)
	if err != nil {
		t.Fatalf("Metrics() error = %v", err)
	}
	if m.AnalysisRunsTotal != 2 || m.ProposalsTotal != 3 || m.ProposalsApprovedTotal != 1 || m.ProposalsRejectedTotal != 1 {
		t.Errorf("proposal metrics = %+v, want runs=2 total=3 approved=1 rejected=1", m)
	}
	if m.UnknownEntityCount != 3 || m.EntityExtractionUnknownTotal != 3 || m.EntityExtractionTotal != 2 {
		t.Errorf("entity metrics = %+v, want unknown=3 extraction=2", m)
	}
}
