package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// seedArticlesForBench inserts n synthetic articles (a realistic rolling
// window at the 72h dedup lookback, several thousand rows across sources)
// plus matching article_dedup_terms rows, so FindCandidates has a
// non-trivial inverted index to query against instead of an empty table.
// Every article shares all three indexed terms, so a filter asking for 2 of
// {"isotopes","ezo"} always exercises the full JOIN + GROUP BY + HAVING +
// ORDER BY + LIMIT pipeline rather than short-circuiting on an empty result.
func seedArticlesForBench(b *testing.B, db *sql.DB, n int) {
	b.Helper()
	articles := sqliteadapter.NewArticleRepository(db)
	terms := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bench-article-%d", i)
		a := article.Article{
			ID:           id,
			OriginalURL:  "https://example.com/" + id,
			CanonicalURL: "https://example.com/" + id,
			Source:       source.Source{ID: mustID(b, source.TypeRSS, "bench-source"), Name: "Bench Source", Type: source.TypeRSS},
			Title:        fmt.Sprintf("Isotopes news item %d", i),
			ContentType:  article.ContentTypeArticle,
			Status:       article.StatusClusterCandidate,
			PublishedAt:  now.Add(-time.Duration(i) * time.Minute),
			FetchedAt:    now.Add(-time.Duration(i) * time.Minute),
		}
		if err := articles.Save(ctx, a); err != nil {
			b.Fatalf("seed article %d: %v", i, err)
		}
		if err := terms.IndexTerms(ctx, id, []string{"isotopes", "ezo", "touchdown"}); err != nil {
			b.Fatalf("seed terms %d: %v", i, err)
		}
	}
}

// seedOutboxForBench inserts n synthetic articles plus matching
// publication_outbox rows (via the real Enqueue path, mirroring the
// existing seeding pattern in outbox_repository_test.go), so ClaimBatch has
// a non-trivial pending queue — including varied editorial_score and
// is_trusted_source values, since ClaimBatch's claim UPDATE orders its
// candidate subquery by those columns via a JOIN onto articles. IDs start
// at startAt so this can be called more than once against the same DB
// (e.g. to top up a depleting pool mid-benchmark) without colliding with
// previously seeded rows.
func seedOutboxForBench(b *testing.B, db *sql.DB, startAt, n int) {
	b.Helper()
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bench-outbox-%d", startAt+i)
		a := article.Article{
			ID:              id,
			OriginalURL:     "https://example.com/" + id,
			CanonicalURL:    "https://example.com/" + id,
			Title:           fmt.Sprintf("Outbox item %d", i),
			ContentType:     article.ContentTypeArticle,
			Status:          article.StatusPendingPublication,
			PublishedAt:     now.Add(-time.Duration(i) * time.Minute),
			FetchedAt:       now.Add(-time.Duration(i) * time.Minute),
			EditorialScore:  float64(i % 100),
			IsTrustedSource: i%5 == 0,
		}
		if err := articles.Save(ctx, a); err != nil {
			b.Fatalf("seed article %d: %v", i, err)
		}
		if err := outbox.Enqueue(ctx, id, "{}", now); err != nil {
			b.Fatalf("seed outbox %d: %v", i, err)
		}
	}
}

// BenchmarkDeduplicationRepository_Exists measures the exact_fingerprints
// point lookup (PRIMARY KEY on fingerprint) used on every incoming article.
func BenchmarkDeduplicationRepository_Exists(b *testing.B) {
	db := newTestDB(b)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewDeduplicationRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	a := article.Article{
		ID:           "bench-fp-article",
		OriginalURL:  "https://example.com/bench-fp-article",
		CanonicalURL: "https://example.com/bench-fp-article",
		ContentType:  article.ContentTypeArticle,
		Status:       article.StatusNormalized,
		PublishedAt:  now,
		FetchedAt:    now,
	}
	if err := articles.Save(ctx, a); err != nil {
		b.Fatalf("seed article: %v", err)
	}
	if err := repo.Save(ctx, "bench-fingerprint-1", a.ID); err != nil {
		b.Fatalf("seed fingerprint: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.Exists(ctx, "bench-fingerprint-1"); err != nil {
			b.Fatalf("Exists() error = %v", err)
		}
	}
}

// BenchmarkSimilarityIndexRepository_FindCandidates measures the
// article_dedup_terms inverted-index candidate query against a 2000-row
// seeded index, with Limit matching configs/settings.yml's max_candidates.
func BenchmarkSimilarityIndexRepository_FindCandidates(b *testing.B) {
	db := newTestDB(b)
	seedArticlesForBench(b, db, 2000)
	repo := sqliteadapter.NewSimilarityIndexRepository(db)
	ctx := context.Background()

	filter := appout.CandidateFilter{
		WindowStart:    time.Now().UTC().Add(-72 * time.Hour),
		ContentType:    article.ContentTypeArticle,
		Terms:          []string{"isotopes", "ezo"},
		MinSharedTerms: 2,
		Limit:          50, // matches configs/settings.yml max_candidates
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.FindCandidates(ctx, filter); err != nil {
			b.Fatalf("FindCandidates() error = %v", err)
		}
	}
}

// BenchmarkOutboxRepository_ClaimBatch measures the reclaim+claim+select
// transaction against a sustained-backlog outbox. The pool starts at 500
// pending rows and is claimed at 10 rows/call, so it would be exhausted
// after ~50 calls on its own — but Go's benchmark auto-scaling reliably
// drives b.N into the thousands once each call is this fast, so without
// intervention >98% of measured iterations would hit an empty-pending-set
// poll (a real but cheap and different code path than the sustained-backlog
// claim cost this benchmark is meant to measure). To avoid that, every
// reseedEvery iterations the pool is topped up by another reseedAmount rows,
// wrapped in b.StopTimer()/b.StartTimer() so the reseed writes themselves
// aren't counted in the measurement. reseedEvery*claimSize=400 stays well
// below the initial 500-row seed, so the pool never drops below ~100
// pending rows right before a top-up — comfortably above the 10-row claim
// size for the whole b.N range. staleAfter is generous (10m) so no row is
// ever reclaimed mid-benchmark.
func BenchmarkOutboxRepository_ClaimBatch(b *testing.B) {
	db := newTestDB(b)
	repo := sqliteadapter.NewOutboxRepository(db)
	ctx := context.Background()

	const (
		initialSeed  = 500
		claimSize    = 10
		reseedEvery  = 40  // iterations; reseedEvery*claimSize=400 < initialSeed leaves margin
		reseedAmount = 500 // rows added per top-up
	)
	seedOutboxForBench(b, db, 0, initialSeed)
	nextID := initialSeed

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i > 0 && i%reseedEvery == 0 {
			b.StopTimer()
			seedOutboxForBench(b, db, nextID, reseedAmount)
			nextID += reseedAmount
			b.StartTimer()
		}
		if _, err := repo.ClaimBatch(ctx, fmt.Sprintf("bench-worker-%d", i), claimSize, 10*time.Minute, time.Now().UTC()); err != nil {
			b.Fatalf("ClaimBatch() error = %v", err)
		}
	}
}
