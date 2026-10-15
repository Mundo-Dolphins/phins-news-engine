package sqliteeditoriallabelproposal_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editoriallabelproposal"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var migratedTestDBTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-sqliteeditoriallabelproposal-test-template-")
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

func TestSaveAndGetLatestByArticleRoundTrips(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(newTestDB(t))
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	p := editoriallabelproposal.Proposal{
		ArticleID:     "a1",
		Label:         "publish",
		Confidence:    0.97,
		Reason:        "Direct Isotopes roster transaction",
		Model:         "qwen3:4b",
		PromptVersion: "editorial-teacher-v1",
		PolicyVersion: "publication-policy-v1",
		Status:        editoriallabelproposal.StatusPending,
		CreatedAt:     now,
	}

	id, err := repo.Save(ctx, p)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if id == "" {
		t.Fatal("Save() returned empty id")
	}

	got, ok, err := repo.GetLatestByArticle(ctx, "a1")
	if err != nil {
		t.Fatalf("GetLatestByArticle() error = %v", err)
	}
	if !ok {
		t.Fatal("GetLatestByArticle() ok = false, want true")
	}
	if got.ID != id || got.ArticleID != "a1" || got.Label != "publish" || got.Confidence != 0.97 ||
		got.Reason != p.Reason || got.Model != p.Model || got.PromptVersion != p.PromptVersion ||
		got.PolicyVersion != p.PolicyVersion || got.Status != editoriallabelproposal.StatusPending {
		t.Errorf("GetLatestByArticle() = %+v, want fields matching %+v (id=%s)", got, p, id)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}
}

func TestGetLatestByArticleReturnsFalseWhenAbsent(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(newTestDB(t))

	_, ok, err := repo.GetLatestByArticle(ctx, "missing")
	if err != nil {
		t.Fatalf("GetLatestByArticle() error = %v", err)
	}
	if ok {
		t.Fatal("GetLatestByArticle() ok = true, want false")
	}
}

func TestSaveRejectsSecondPendingProposalForSameArticle(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(newTestDB(t))
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	p := editoriallabelproposal.Proposal{
		ArticleID: "a1", Label: "publish", Confidence: 0.9, Model: "m", PromptVersion: "v1",
		PolicyVersion: "p1", Status: editoriallabelproposal.StatusPending, CreatedAt: now,
	}

	if _, err := repo.Save(ctx, p); err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}
	if _, err := repo.Save(ctx, p); !errors.Is(err, editoriallabelproposal.ErrPendingProposalExists) {
		t.Errorf("Save(second) error = %v, want ErrPendingProposalExists", err)
	}
}

func TestSaveAllowsNewPendingProposalAfterPriorOneDecided(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(newTestDB(t))
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	p := editoriallabelproposal.Proposal{
		ArticleID: "a1", Label: "discard", Confidence: 0.9, Model: "m", PromptVersion: "v1",
		PolicyVersion: "p1", Status: editoriallabelproposal.StatusPending, CreatedAt: now,
	}

	firstID, err := repo.Save(ctx, p)
	if err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}
	if err := repo.UpdateStatus(ctx, firstID, editoriallabelproposal.StatusApproved, now.Add(time.Hour)); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}

	p.Label = "publish"
	p.PromptVersion = "v2"
	p.CreatedAt = now.Add(2 * time.Hour)
	secondID, err := repo.Save(ctx, p)
	if err != nil {
		t.Fatalf("Save(second) error = %v", err)
	}

	got, ok, err := repo.GetLatestByArticle(ctx, "a1")
	if err != nil || !ok {
		t.Fatalf("GetLatestByArticle() = %+v, %v, %v", got, ok, err)
	}
	if got.ID != secondID || got.Label != "publish" {
		t.Errorf("GetLatestByArticle() = %+v, want the second (latest) proposal", got)
	}
}

func TestUpdateStatusReturnsErrNotFoundForMissingID(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(newTestDB(t))

	err := repo.UpdateStatus(ctx, "missing", editoriallabelproposal.StatusApproved, time.Now())
	if !errors.Is(err, editoriallabelproposal.ErrNotFound) {
		t.Errorf("UpdateStatus() error = %v, want ErrNotFound", err)
	}
}

func TestListByStatusReturnsOnlyMatchingOrderedByCreatedAt(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(newTestDB(t))
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	mk := func(articleID string, offset time.Duration) editoriallabelproposal.Proposal {
		return editoriallabelproposal.Proposal{
			ArticleID: articleID, Label: "publish", Confidence: 0.5, Model: "m", PromptVersion: "v1",
			PolicyVersion: "p1", Status: editoriallabelproposal.StatusPending, CreatedAt: base.Add(offset),
		}
	}

	idA2, err := repo.Save(ctx, mk("a2", time.Minute))
	if err != nil {
		t.Fatalf("Save(a2) error = %v", err)
	}
	idA1, err := repo.Save(ctx, mk("a1", 0))
	if err != nil {
		t.Fatalf("Save(a1) error = %v", err)
	}
	idA3, err := repo.Save(ctx, mk("a3", 2*time.Minute))
	if err != nil {
		t.Fatalf("Save(a3) error = %v", err)
	}
	if err := repo.UpdateStatus(ctx, idA3, editoriallabelproposal.StatusApproved, base.Add(3*time.Minute)); err != nil {
		t.Fatalf("UpdateStatus(a3) error = %v", err)
	}

	pending, err := repo.ListByStatus(ctx, editoriallabelproposal.StatusPending)
	if err != nil {
		t.Fatalf("ListByStatus() error = %v", err)
	}
	if len(pending) != 2 || pending[0].ID != idA1 || pending[1].ID != idA2 {
		t.Errorf("ListByStatus(pending) = %+v, want [a1(%s), a2(%s)] in created_at order", pending, idA1, idA2)
	}

	approved, err := repo.ListByStatus(ctx, editoriallabelproposal.StatusApproved)
	if err != nil {
		t.Fatalf("ListByStatus() error = %v", err)
	}
	if len(approved) != 1 || approved[0].ID != idA3 {
		t.Errorf("ListByStatus(approved) = %+v, want [a3(%s)]", approved, idA3)
	}
}

func TestFilterUnprocessedAndMarkArticlesProcessed(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(newTestDB(t))
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	unprocessed, err := repo.FilterUnprocessed(ctx, "v1", []string{"a1", "a2", "a3"})
	if err != nil {
		t.Fatalf("FilterUnprocessed() error = %v", err)
	}
	if len(unprocessed) != 3 {
		t.Errorf("FilterUnprocessed() = %v, want all 3 unprocessed", unprocessed)
	}

	if err := repo.MarkArticlesProcessed(ctx, "v1", []string{"a1", "a2"}, now); err != nil {
		t.Fatalf("MarkArticlesProcessed() error = %v", err)
	}

	unprocessed, err = repo.FilterUnprocessed(ctx, "v1", []string{"a1", "a2", "a3"})
	if err != nil {
		t.Fatalf("FilterUnprocessed() error = %v", err)
	}
	if len(unprocessed) != 1 || unprocessed[0] != "a3" {
		t.Errorf("FilterUnprocessed() = %v, want [a3]", unprocessed)
	}

	// A different prompt_version has not processed anything yet, even
	// for the same articles -- a prompt/policy bump makes them eligible
	// again.
	unprocessedV2, err := repo.FilterUnprocessed(ctx, "v2", []string{"a1", "a2"})
	if err != nil {
		t.Fatalf("FilterUnprocessed(v2) error = %v", err)
	}
	if len(unprocessedV2) != 2 {
		t.Errorf("FilterUnprocessed(v2) = %v, want both unprocessed under a new prompt version", unprocessedV2)
	}

	// Idempotent.
	if err := repo.MarkArticlesProcessed(ctx, "v1", []string{"a1"}, now); err != nil {
		t.Fatalf("MarkArticlesProcessed() (repeat) error = %v", err)
	}
}

func TestListLatestPerArticleReturnsOnlyMostRecentPerArticle(t *testing.T) {
	ctx := context.Background()
	repo := sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(newTestDB(t))
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	mk := func(articleID string, offset time.Duration, confidence float64) editoriallabelproposal.Proposal {
		return editoriallabelproposal.Proposal{
			ArticleID: articleID, Label: "publish", Confidence: confidence, Model: "m", PromptVersion: "v1",
			PolicyVersion: "p1", Status: editoriallabelproposal.StatusPending, CreatedAt: base.Add(offset),
		}
	}

	firstA1, err := repo.Save(ctx, mk("a1", 0, 0.4))
	if err != nil {
		t.Fatalf("Save(a1 first) error = %v", err)
	}
	if err := repo.UpdateStatus(ctx, firstA1, editoriallabelproposal.StatusRejected, base.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateStatus(a1 first) error = %v", err)
	}
	secondA1, err := repo.Save(ctx, mk("a1", 2*time.Minute, 0.9))
	if err != nil {
		t.Fatalf("Save(a1 second) error = %v", err)
	}
	idA2, err := repo.Save(ctx, mk("a2", time.Minute, 0.6))
	if err != nil {
		t.Fatalf("Save(a2) error = %v", err)
	}

	got, err := repo.ListLatestPerArticle(ctx)
	if err != nil {
		t.Fatalf("ListLatestPerArticle() error = %v", err)
	}
	byArticle := map[string]editoriallabelproposal.Proposal{}
	for _, p := range got {
		byArticle[p.ArticleID] = p
	}
	if len(byArticle) != 2 {
		t.Fatalf("ListLatestPerArticle() returned %d articles, want 2 (got %+v)", len(byArticle), got)
	}
	if byArticle["a1"].ID != secondA1 || byArticle["a1"].Confidence != 0.9 {
		t.Errorf("ListLatestPerArticle()[a1] = %+v, want the second (latest) proposal id=%s", byArticle["a1"], secondA1)
	}
	if byArticle["a2"].ID != idA2 {
		t.Errorf("ListLatestPerArticle()[a2] = %+v, want id=%s", byArticle["a2"], idA2)
	}
}
