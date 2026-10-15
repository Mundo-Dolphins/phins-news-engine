package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func TestArticleRepository_SaveAndFindByID(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()

	a := article.Article{
		ID:                "a1",
		OriginalURL:       "https://example.com/story",
		CanonicalURL:      "https://example.com/story",
		Source:            source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		FetchedAt:         time.Now().Truncate(time.Second),
		PublishedAt:       time.Now().Add(-time.Hour).Truncate(time.Second),
		ContentType:       article.ContentTypeArticle,
		Status:            article.StatusPendingPublication,
		StatusReason:      "fresh and related",
		ClassifierVersion: "placeholder-v0",
		DictionaryVersion: "placeholder-v0",
		EditorialScore:    50,
		RelevanceScore:    8.5,
		QualityThreshold:  10,
		FreshnessMaxAge:   72 * time.Hour,
		Metadata:          map[string]string{"k": "v"},
	}
	if err := repo.Save(ctx, a); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	var storedPublishedAt, storedFetchedAt string
	if err := db.QueryRowContext(ctx, `SELECT published_at, fetched_at FROM articles WHERE id = 'a1'`).Scan(&storedPublishedAt, &storedFetchedAt); err != nil {
		t.Fatalf("read stored timestamps: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, storedPublishedAt); err != nil {
		t.Fatalf("stored published_at = %q, want RFC3339Nano: %v", storedPublishedAt, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, storedFetchedAt); err != nil {
		t.Fatalf("stored fetched_at = %q, want RFC3339Nano: %v", storedFetchedAt, err)
	}

	got, err := repo.FindByID(ctx, "a1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.Status != article.StatusPendingPublication {
		t.Errorf("Status = %v, want %v", got.Status, article.StatusPendingPublication)
	}
	if got.StatusReason != "fresh and related" {
		t.Errorf("StatusReason = %q, want %q", got.StatusReason, "fresh and related")
	}
	if got.ClusterID != "" {
		t.Errorf("ClusterID = %q, want empty (no cluster assigned)", got.ClusterID)
	}
	if got.ClassifierVersion != "placeholder-v0" || got.DictionaryVersion != "placeholder-v0" {
		t.Errorf("ClassifierVersion/DictionaryVersion = %q/%q, want placeholder-v0/placeholder-v0", got.ClassifierVersion, got.DictionaryVersion)
	}
	if got.EditorialScore != 50 {
		t.Errorf("EditorialScore = %v, want 50", got.EditorialScore)
	}
	if got.RelevanceScore != 8.5 {
		t.Errorf("RelevanceScore = %v, want 8.5", got.RelevanceScore)
	}
	if got.QualityThreshold != 10 {
		t.Errorf("QualityThreshold = %v, want 10", got.QualityThreshold)
	}
	if got.FreshnessMaxAge != 72*time.Hour {
		t.Errorf("FreshnessMaxAge = %v, want %v", got.FreshnessMaxAge, 72*time.Hour)
	}
	if got.Metadata["k"] != "v" {
		t.Errorf("Metadata[k] = %q, want %q", got.Metadata["k"], "v")
	}
}

func TestArticleRepository_FindByIDParsesLegacyTextTimestamps(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()

	a := article.Article{
		ID:           "legacy-time",
		OriginalURL:  "https://example.com/legacy-time",
		CanonicalURL: "https://example.com/legacy-time",
		Source:       source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		FetchedAt:    time.Date(2026, 9, 7, 17, 55, 50, 0, time.UTC),
		PublishedAt:  time.Date(2026, 9, 7, 17, 41, 20, 0, time.UTC),
		ContentType:  article.ContentTypeArticle,
		Status:       article.StatusPendingPublication,
	}
	if err := repo.Save(ctx, a); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE articles
		SET published_at = '2026-09-07 13:41:20 -0400 -0400',
		    fetched_at = '2026-09-08 08:05:55.371852705 +0200 CEST m=+251435.003763364'
		WHERE id = 'legacy-time'
	`); err != nil {
		t.Fatalf("seed legacy timestamps: %v", err)
	}

	got, err := repo.FindByID(ctx, "legacy-time")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	wantPublished := time.Date(2026, 9, 7, 17, 41, 20, 0, time.UTC)
	wantFetched := time.Date(2026, 9, 8, 6, 5, 55, 371852705, time.UTC)
	if !got.PublishedAt.Equal(wantPublished) {
		t.Errorf("PublishedAt = %v, want %v", got.PublishedAt, wantPublished)
	}
	if !got.FetchedAt.Equal(wantFetched) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, wantFetched)
	}
}

func TestArticleRepository_FindByID_NotFound(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewArticleRepository(db)

	_, err := repo.FindByID(context.Background(), "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("FindByID() error = %v, want wrapped sql.ErrNoRows", err)
	}
}

func TestArticleRepository_SaveUpsertsOnConflict(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()

	a := article.Article{ID: "a1", OriginalURL: "https://example.com/story", CanonicalURL: "https://example.com/story", Source: source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}, FetchedAt: time.Now(), PublishedAt: time.Now(), Status: article.StatusFetched}
	if err := repo.Save(ctx, a); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	a.Status = article.StatusPublished
	if err := repo.Save(ctx, a); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 1 {
		t.Errorf("Count() = %d, want 1 (upsert, not duplicate row)", count)
	}

	got, err := repo.FindByID(ctx, "a1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.Status != article.StatusPublished {
		t.Errorf("Status after upsert = %v, want %v", got.Status, article.StatusPublished)
	}
}

func TestArticleRepository_DeleteByStatusOlderThan(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()

	now := time.Now()
	testSource := source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	old := article.Article{ID: "old", OriginalURL: "https://example.com/old", CanonicalURL: "https://example.com/old", Source: testSource, FetchedAt: now.Add(-48 * time.Hour), PublishedAt: now, Status: article.StatusRejectedStale}
	recentSameStatus := article.Article{ID: "recent", OriginalURL: "https://example.com/recent", CanonicalURL: "https://example.com/recent", Source: testSource, FetchedAt: now, PublishedAt: now, Status: article.StatusRejectedStale}
	oldPublished := article.Article{ID: "old-published", OriginalURL: "https://example.com/old-published", CanonicalURL: "https://example.com/old-published", Source: testSource, FetchedAt: now.Add(-48 * time.Hour), PublishedAt: now, Status: article.StatusPublished}

	for _, a := range []article.Article{old, recentSameStatus, oldPublished} {
		if err := repo.Save(ctx, a); err != nil {
			t.Fatalf("Save(%s) error = %v", a.ID, err)
		}
	}

	deleted, err := repo.DeleteByStatusOlderThan(ctx, []article.Status{article.StatusRejectedStale}, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteByStatusOlderThan() error = %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteByStatusOlderThan() = %d, want 1 (only the old rejected_stale row)", deleted)
	}

	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 2 {
		t.Errorf("Count() after delete = %d, want 2 (recent rejected + old published survive)", count)
	}

	if _, err := repo.FindByID(ctx, "old-published"); err != nil {
		t.Errorf("old published article was deleted, want it to survive: %v", err)
	}
}

func TestArticleRepository_DeleteByStatusOlderThan_EmptyStatusesIsNoop(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewArticleRepository(db)

	deleted, err := repo.DeleteByStatusOlderThan(context.Background(), nil, time.Now())
	if err != nil {
		t.Fatalf("DeleteByStatusOlderThan() error = %v", err)
	}
	if deleted != 0 {
		t.Errorf("DeleteByStatusOlderThan(nil) = %d, want 0", deleted)
	}
}

func TestArticleRepository_RoundTripsPublicationPolicyFields(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()

	a := article.Article{
		ID: "a1", CanonicalURL: "https://example.com/a1", PublishedAt: time.Now(),
		Source:    source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		FetchedAt: time.Now(), Status: article.StatusPendingPublication,
		PrimaryEntity: "Jane Doe", ClusterMemberRole: "substantial_update", IsTrustedSource: true,
	}
	if err := repo.Save(ctx, a); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := repo.FindByID(ctx, "a1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.PrimaryEntity != "Jane Doe" || got.ClusterMemberRole != "substantial_update" || !got.IsTrustedSource {
		t.Errorf("got = %+v, want PrimaryEntity=Jane Doe ClusterMemberRole=substantial_update IsTrustedSource=true", got)
	}
}

func TestArticleRepository_ListCandidatesForReview_ExcludesLabeledAndOldAndWrongStatus(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	ctx := context.Background()
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	testSource := source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	seed := []article.Article{
		{ID: "too-old", OriginalURL: "https://example.com/1", CanonicalURL: "https://example.com/1", Source: testSource, FetchedAt: since.Add(-time.Hour), PublishedAt: since, Status: article.StatusClustered},
		{ID: "wrong-status", OriginalURL: "https://example.com/2", CanonicalURL: "https://example.com/2", Source: testSource, FetchedAt: since.Add(time.Hour), PublishedAt: since, Status: article.StatusFetched},
		{ID: "already-labeled", OriginalURL: "https://example.com/3", CanonicalURL: "https://example.com/3", Source: testSource, FetchedAt: since.Add(time.Hour), PublishedAt: since, Status: article.StatusClustered},
		{ID: "candidate", OriginalURL: "https://example.com/4", CanonicalURL: "https://example.com/4", Source: testSource, FetchedAt: since.Add(2 * time.Hour), PublishedAt: since, Status: article.StatusPendingPublication},
	}
	for _, a := range seed {
		if err := articles.Save(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", a.ID, err)
		}
	}
	if err := labels.Label(ctx, training.Example{ArticleID: "already-labeled", Label: "publish", LabeledAt: since}); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	got, err := articles.ListCandidatesForReview(ctx, since, 10)
	if err != nil {
		t.Fatalf("ListCandidatesForReview() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "candidate" {
		t.Fatalf("ListCandidatesForReview() = %+v, want only [candidate]", got)
	}
}

func TestArticleRepository_ListSince_OrdersByFetchedAtAscAndExcludesOlder(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	testSource := source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	seed := []article.Article{
		{ID: "too-old", OriginalURL: "https://example.com/1", CanonicalURL: "https://example.com/1", Source: testSource, FetchedAt: since.Add(-time.Hour), PublishedAt: since, Status: article.StatusRejectedNotRelated},
		{ID: "second", OriginalURL: "https://example.com/2", CanonicalURL: "https://example.com/2", Source: testSource, FetchedAt: since.Add(2 * time.Hour), PublishedAt: since, Status: article.StatusPendingPublication},
		{ID: "first", OriginalURL: "https://example.com/3", CanonicalURL: "https://example.com/3", Source: testSource, FetchedAt: since.Add(1 * time.Hour), PublishedAt: since, Status: article.StatusRejectedNotRelated},
	}
	for _, a := range seed {
		if err := articles.Save(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", a.ID, err)
		}
	}

	got, err := articles.ListSince(ctx, since, 10)
	if err != nil {
		t.Fatalf("ListSince() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (too-old excluded)", len(got))
	}
	if got[0].ID != "first" || got[1].ID != "second" {
		t.Fatalf("got IDs = [%s, %s], want [first, second] (ascending fetched_at)", got[0].ID, got[1].ID)
	}
}

func TestArticleRepository_ListSince_RespectsLimit(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	testSource := source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	for i, id := range []string{"a", "b", "c"} {
		a := article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id, Source: testSource,
			FetchedAt: since.Add(time.Duration(i) * time.Hour), PublishedAt: since, Status: article.StatusRejectedNotRelated,
		}
		if err := articles.Save(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	got, err := articles.ListSince(ctx, since, 2)
	if err != nil {
		t.Fatalf("ListSince() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
}

func TestArticleRepository_ListUnprocessedByEntityDiscovery_AppliesLimitAfterFiltering(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	testSource := source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	for i, id := range []string{"a", "b", "c", "d"} {
		a := article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id, Source: testSource,
			FetchedAt: since.Add(time.Duration(i) * time.Hour), PublishedAt: since, Status: article.StatusRejectedNotRelated,
		}
		if err := articles.Save(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// a and b are processed for v1; c is processed only for another version.
	for _, row := range [][2]string{{"a", "v1"}, {"b", "v1"}, {"c", "v0"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO entity_discovery_processed_articles (article_id, extractor_version, processed_at) VALUES (?, ?, ?)`,
			row[0], row[1], since); err != nil {
			t.Fatalf("seed processed %v: %v", row, err)
		}
	}

	got, err := articles.ListUnprocessedByEntityDiscovery(ctx, since, "v1", 1)
	if err != nil {
		t.Fatalf("ListUnprocessedByEntityDiscovery() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("got = %+v, want only [c] (oldest unprocessed for v1; limit counts unprocessed articles only)", got)
	}
}

func TestArticleRepository_FindByCanonicalURL_Found(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := articles.Save(ctx, article.Article{
		ID: "a1", OriginalURL: "https://example.com/1", CanonicalURL: "https://example.com/1",
		Source:    source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		FetchedAt: now, PublishedAt: now, Status: article.StatusPublished,
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}

	got, err := articles.FindByCanonicalURL(ctx, "https://example.com/1")
	if err != nil {
		t.Fatalf("FindByCanonicalURL() error = %v", err)
	}
	if got.ID != "a1" {
		t.Errorf("FindByCanonicalURL().ID = %q, want %q", got.ID, "a1")
	}
}

func TestArticleRepository_FindByCanonicalURL_NotFound(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)

	_, err := articles.FindByCanonicalURL(context.Background(), "https://example.com/missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindByCanonicalURL() error = %v, want wrapped sql.ErrNoRows", err)
	}
}

// TestArticleRepository_FindByCanonicalURL_NotFoundSatisfiesDomainError
// asserts a true "no such row" result also satisfies errors.Is against the
// application-layer domain sentinel (article.ErrNotFound), not just
// sql.ErrNoRows — application code (telegramimport) must be able to
// recognize "not found" without importing database/sql. Both must hold at
// once: existing adapter-level tests and callers still rely on
// errors.Is(err, sql.ErrNoRows).
func TestArticleRepository_FindByCanonicalURL_NotFoundSatisfiesDomainError(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)

	_, err := articles.FindByCanonicalURL(context.Background(), "https://example.com/missing")
	if !errors.Is(err, article.ErrNotFound) {
		t.Errorf("FindByCanonicalURL() error = %v, want wrapped article.ErrNotFound", err)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindByCanonicalURL() error = %v, want wrapped sql.ErrNoRows too", err)
	}
}

// TestArticleRepository_FindByCanonicalURL_ClosedDBIsNotClassifiedAsNotFound
// reproduces a real repository failure that must never be interpreted as
// "URL not found": querying through an already-closed *sql.DB fails with a
// generic "sql: database is closed" error, not sql.ErrNoRows.
func TestArticleRepository_FindByCanonicalURL_ClosedDBIsNotClassifiedAsNotFound(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err := articles.FindByCanonicalURL(context.Background(), "https://example.com/x")
	if err == nil {
		t.Fatal("FindByCanonicalURL() error = nil, want error from a closed database")
	}
	if errors.Is(err, article.ErrNotFound) {
		t.Errorf("FindByCanonicalURL() error = %v, must NOT satisfy errors.Is(err, article.ErrNotFound)", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindByCanonicalURL() error = %v, must NOT satisfy errors.Is(err, sql.ErrNoRows)", err)
	}
}

// TestArticleRepository_FindByCanonicalURL_CorruptDBIsNotClassifiedAsNotFound
// reproduces querying a file that isn't a valid SQLite database (e.g.
// truncated/corrupted on disk) — this must propagate as a real error, not
// be swallowed as "not found".
func TestArticleRepository_FindByCanonicalURL_CorruptDBIsNotClassifiedAsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("this is not a valid sqlite database file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	db, err := infrasqlite.Open(path, time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	articles := sqliteadapter.NewArticleRepository(db)

	_, err = articles.FindByCanonicalURL(context.Background(), "https://example.com/x")
	if err == nil {
		t.Fatal("FindByCanonicalURL() error = nil, want error querying a corrupt database file")
	}
	if errors.Is(err, article.ErrNotFound) {
		t.Errorf("FindByCanonicalURL() error = %v, must NOT satisfy errors.Is(err, article.ErrNotFound)", err)
	}
}

// TestArticleRepository_FindByCanonicalURL_CanceledContextIsNotClassifiedAsNotFound
// reproduces a real driver-level cancellation: querying through an
// already-canceled context must fail (with context.Canceled somewhere in
// its chain), never be classified as "not found".
func TestArticleRepository_FindByCanonicalURL_CanceledContextIsNotClassifiedAsNotFound(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := articles.FindByCanonicalURL(ctx, "https://example.com/x")
	if err == nil {
		t.Fatal("FindByCanonicalURL() error = nil, want an error from an already-canceled context")
	}
	if errors.Is(err, article.ErrNotFound) {
		t.Errorf("FindByCanonicalURL() error = %v, must NOT satisfy errors.Is(err, article.ErrNotFound)", err)
	}
}

// TestArticleRepository_FindByCanonicalURL_LockedDBIsNotClassifiedAsNotFound
// reproduces a real "database is locked" failure: a second connection to
// the same file holds an exclusive lock, so a query through a connection
// with a very short busy timeout fails with SQLITE_BUSY, not sql.ErrNoRows.
func TestArticleRepository_FindByCanonicalURL_LockedDBIsNotClassifiedAsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	copyMigratedTestDB(t, path)
	setup, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	// Close the setup connection before taking an exclusive lock on the
	// file below — an idle-but-open WAL connection can otherwise itself
	// contend for the lock and make this test flaky.
	if err := setup.Close(); err != nil {
		t.Fatalf("Close() setup error = %v", err)
	}

	blocker, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() blocker error = %v", err)
	}
	t.Cleanup(func() { _ = blocker.Close() })
	ctx := context.Background()
	if _, err := blocker.ExecContext(ctx, `PRAGMA locking_mode=EXCLUSIVE`); err != nil {
		t.Fatalf("set locking_mode error = %v", err)
	}
	// EXCLUSIVE locking_mode only takes effect on the next database access.
	if _, err := blocker.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		t.Fatalf("trigger exclusive lock error = %v", err)
	}

	reader, err := infrasqlite.Open(path, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Open() reader error = %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	articles := sqliteadapter.NewArticleRepository(reader)

	_, err = articles.FindByCanonicalURL(ctx, "https://example.com/x")
	if err == nil {
		t.Fatal("FindByCanonicalURL() error = nil, want a locked-database error")
	}
	if errors.Is(err, article.ErrNotFound) {
		t.Errorf("FindByCanonicalURL() error = %v, must NOT satisfy errors.Is(err, article.ErrNotFound)", err)
	}
}

func TestArticleRepository_FindByCanonicalURL_MultipleMatchesReturnsOldest(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	ctx := context.Background()
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	testSource := source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	if err := articles.Save(ctx, article.Article{
		ID: "newer", OriginalURL: "https://example.com/dup", CanonicalURL: "https://example.com/dup",
		Source: testSource, FetchedAt: newer, PublishedAt: newer, Status: article.StatusPublished,
	}); err != nil {
		t.Fatalf("seed newer: %v", err)
	}
	if err := articles.Save(ctx, article.Article{
		ID: "older", OriginalURL: "https://example.com/dup", CanonicalURL: "https://example.com/dup",
		Source: testSource, FetchedAt: older, PublishedAt: older, Status: article.StatusPublished,
	}); err != nil {
		t.Fatalf("seed older: %v", err)
	}

	got, err := articles.FindByCanonicalURL(ctx, "https://example.com/dup")
	if err != nil {
		t.Fatalf("FindByCanonicalURL() error = %v", err)
	}
	if got.ID != "older" {
		t.Errorf("FindByCanonicalURL().ID = %q, want %q (oldest published_at)", got.ID, "older")
	}
}

func TestArticleRepository_ListByStatusUnlabeled_ExcludesAlreadyLabeled(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewArticleRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	ctx := context.Background()

	testSource := source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	unlabeled := article.Article{ID: "a-unlabeled", Title: "t1", Status: article.StatusRejectedNotRelated, FetchedAt: time.Now().UTC(), Source: testSource}
	labeled := article.Article{ID: "a-labeled", Title: "t2", Status: article.StatusRejectedNotRelated, FetchedAt: time.Now().UTC(), Source: testSource}
	wrongStatus := article.Article{ID: "a-wrong-status", Title: "t3", Status: article.StatusPublished, FetchedAt: time.Now().UTC(), Source: testSource}
	for _, a := range []article.Article{unlabeled, labeled, wrongStatus} {
		if err := repo.Save(ctx, a); err != nil {
			t.Fatalf("Save(%s) error = %v", a.ID, err)
		}
	}
	if err := labels.Label(ctx, training.Example{ArticleID: "a-labeled", Label: "reject", LabeledAt: time.Now().UTC(), Source: "cli:label"}); err != nil {
		t.Fatalf("Label(a-labeled) error = %v", err)
	}

	got, err := repo.ListByStatusUnlabeled(ctx, []article.Status{article.StatusRejectedNotRelated}, 10)
	if err != nil {
		t.Fatalf("ListByStatusUnlabeled() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "a-unlabeled" {
		t.Errorf("ListByStatusUnlabeled() = %+v, want exactly [a-unlabeled]", got)
	}
}

func TestArticleRepository_ListByClusterID_ExcludesSelfAndOtherClusters(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// current_cluster_id has a foreign key onto event_clusters(id), so
	// cluster membership must be established via ClusterRepository rather
	// than by setting article.Article.ClusterID directly on Save.
	testSource := source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}
	seed := []article.Article{
		{ID: "self", OriginalURL: "https://example.com/1", CanonicalURL: "https://example.com/1", Source: testSource, FetchedAt: now, PublishedAt: now, Status: article.StatusClustered},
		{ID: "sibling", OriginalURL: "https://example.com/2", CanonicalURL: "https://example.com/2", Source: testSource, FetchedAt: now, PublishedAt: now, Status: article.StatusClustered},
		{ID: "other-cluster", OriginalURL: "https://example.com/3", CanonicalURL: "https://example.com/3", Source: testSource, FetchedAt: now, PublishedAt: now, Status: article.StatusClustered},
	}
	for _, a := range seed {
		if err := articles.Save(ctx, a); err != nil {
			t.Fatalf("seed %s: %v", a.ID, err)
		}
	}
	if err := clusters.CreateCluster(ctx, "c1", "self", clustering.StatusOpen, now); err != nil {
		t.Fatalf("create cluster c1: %v", err)
	}
	if err := clusters.AddMember(ctx, "c1", "sibling", clustering.MemberRoleSubstantialUpdate, 0.9, now); err != nil {
		t.Fatalf("add sibling to c1: %v", err)
	}
	if err := clusters.CreateCluster(ctx, "c2", "other-cluster", clustering.StatusOpen, now); err != nil {
		t.Fatalf("create cluster c2: %v", err)
	}

	got, err := articles.ListByClusterID(ctx, "c1", "self", 10)
	if err != nil {
		t.Fatalf("ListByClusterID() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "sibling" {
		t.Fatalf("ListByClusterID() = %+v, want only [sibling]", got)
	}
}
