package usecase_test

// This file covers two bugs: a rejected item was reclassified every poll
// (no signal ever marked it as decided) and an accepted item reappearing
// grew a new rejected_duplicate articles/article_processing_events row
// every poll (the item was assigned a fresh UUID before the existing
// alias/fingerprint cascade caught it). TestProcessCycle_SeenItems_*
// reproduces both against real SQLite, then asserts the fixed behavior.

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
)

func TestProcessCycle_SeenItems_RejectedItemIsNotReclassifiedOnIdenticalRefetch(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	rawItem := article.Article{
		ExternalID:  "guid-not-related",
		OriginalURL: "https://example.com/not-about-isotopes",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "feed-a"), Type: source.TypeRSS},
		Title:       "Completely unrelated news",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-time.Hour),
	}

	// Freshness rejection (article.StatusRejectedStale) is a deterministic,
	// config-free way to force the same terminal outcome on every fetch
	// without needing a real dictionary matcher — published far outside
	// the freshness window every time.
	stale := rawItem
	stale.PublishedAt = now.Add(-1000 * time.Hour)

	fetcher := fakeFetcher{articles: []article.Article{stale}}
	uc, articles := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now})

	result1, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if result1.Rejected != 1 || result1.Claimed != 1 {
		t.Fatalf("result1 = %+v, want Rejected=1 Claimed=1", result1)
	}

	countAfterFirst, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}

	uc2, _ := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now.Add(time.Hour)})
	result2, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result2.SeenSkipped != 1 || result2.Rejected != 0 || result2.Claimed != 0 {
		t.Fatalf("result2 = %+v, want SeenSkipped=1 Rejected=0 Claimed=0", result2)
	}

	countAfterSecond, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if countAfterSecond != countAfterFirst {
		t.Fatalf("articles count grew from %d to %d on an identical refetch, want unchanged", countAfterFirst, countAfterSecond)
	}

	// The first run legitimately writes two events for a freshness-rejected
	// article — an unconditional "clustering" audit event (Step 6 runs
	// before the freshness gate, and is not undone if freshness later
	// rejects) plus the "freshness" reject event itself — so the assertion
	// here is that the SeenSkipped refetch adds none of its own, not that
	// the total is 1.
	var eventCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM article_processing_events`).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 2 {
		t.Fatalf("article_processing_events rows = %d, want 2 (no new event on the identical refetch)", eventCount)
	}
}

func TestProcessCycle_SeenItems_AcceptedItemDoesNotGrowRejectedDuplicateRowsOnRefetch(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	raw := article.Article{
		ExternalID:  "guid-accepted",
		OriginalURL: "https://example.com/isotopes-sign-player",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "feed-a"), Type: source.TypeRSS},
		Title:       "Isotopes sign new player",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-time.Hour),
	}

	fetcher := fakeFetcher{articles: []article.Article{raw}}
	uc, articles := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now})

	result1, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if result1.Accepted != 1 {
		t.Fatalf("result1 = %+v, want Accepted=1", result1)
	}
	countAfterFirst, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}

	for i, delta := range []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour} {
		uc2, _ := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now.Add(delta)})
		result, err := uc2.Run(context.Background())
		if err != nil {
			t.Fatalf("refetch %d Run() error = %v", i, err)
		}
		if result.SeenSkipped != 1 || result.Duplicate != 0 || result.Rejected != 0 {
			t.Fatalf("refetch %d result = %+v, want SeenSkipped=1 Duplicate=0 Rejected=0", i, result)
		}
	}

	countAfterRefetches, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if countAfterRefetches != countAfterFirst {
		t.Fatalf("articles count grew from %d to %d across 3 identical refetches, want unchanged", countAfterFirst, countAfterRefetches)
	}
}

func TestProcessCycle_SeenItems_RejectedItemWithCorrectedContentIsReprocessedUsingTheSameArticleID(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	stale := article.Article{
		ExternalID:  "guid-corrected",
		OriginalURL: "https://example.com/story-corrected",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "feed-a"), Type: source.TypeRSS},
		Title:       "Original title",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1000 * time.Hour), // rejected as stale
	}
	fetcher1 := fakeFetcher{articles: []article.Article{stale}}
	uc1, articles := newIntegrationProcessCycle(db, fetcher1, fakeClock{now: now})
	result1, err := uc1.Run(context.Background())
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if result1.Rejected != 1 {
		t.Fatalf("result1 = %+v, want Rejected=1", result1)
	}

	var originalArticleID string
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM articles LIMIT 1`).Scan(&originalArticleID); err != nil {
		t.Fatalf("read original article id: %v", err)
	}

	corrected := stale
	corrected.Title = "Corrected title, now fresh"
	corrected.PublishedAt = now.Add(-time.Hour) // no longer stale: an edit AND now passes freshness
	fetcher2 := fakeFetcher{articles: []article.Article{corrected}}
	uc2, _ := newIntegrationProcessCycle(db, fetcher2, fakeClock{now: now.Add(2 * time.Hour)})
	result2, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result2.Accepted != 1 {
		t.Fatalf("result2 = %+v, want Accepted=1 (corrected content must be reprocessed and accepted)", result2)
	}

	count, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("articles count = %d, want 1 (the corrected item must reuse the original stable article id, not create a second row)", count)
	}
	got, err := articles.FindByID(context.Background(), originalArticleID)
	if err != nil {
		t.Fatalf("FindByID(%s) error = %v — the original stable id must still resolve to the (now-accepted) row", originalArticleID, err)
	}
	if got.Status != article.StatusPendingPublication {
		t.Fatalf("reused article status = %s, want pending_publication", got.Status)
	}
}

func TestProcessCycle_SeenItems_AcceptedItemWithEditedTitleIsNotReprocessed(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	raw := article.Article{
		ExternalID:  "guid-accepted-edit",
		OriginalURL: "https://example.com/isotopes-sign-player-2",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "feed-a"), Type: source.TypeRSS},
		Title:       "Isotopes sign new player",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-time.Hour),
	}
	fetcher1 := fakeFetcher{articles: []article.Article{raw}}
	uc1, articles := newIntegrationProcessCycle(db, fetcher1, fakeClock{now: now})
	if _, err := uc1.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	countBefore, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}

	edited := raw
	edited.Title = "Isotopes sign new player (updated headline)"
	fetcher2 := fakeFetcher{articles: []article.Article{edited}}
	uc2, _ := newIntegrationProcessCycle(db, fetcher2, fakeClock{now: now.Add(time.Hour)})
	result2, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result2.Revised != 1 || result2.Accepted != 0 {
		t.Fatalf("result2 = %+v, want Revised=1 Accepted=0 (a cosmetic edit to an already-committed item must never re-run accept)", result2)
	}
	countAfter, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if countAfter != countBefore {
		t.Fatalf("articles count changed from %d to %d, want unchanged", countBefore, countAfter)
	}
}

func TestProcessCycle_SeenItems_NoRemoteIDFallsBackToCanonicalURLAndStaysStable(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	// Shaped like the website adapter's output: no ExternalID at all.
	raw := article.Article{
		OriginalURL: "https://example.com/website-story",
		Source:      source.Source{ID: mustID(t, source.TypeWeb, "official-site"), Type: source.TypeWeb},
		Title:       "A website story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-time.Hour),
	}
	fetcher := fakeFetcher{articles: []article.Article{raw}}
	uc1, articles := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now})
	if _, err := uc1.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	countAfterFirst, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}

	uc2, _ := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now.Add(time.Hour)})
	result2, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result2.SeenSkipped != 1 {
		t.Fatalf("result2 = %+v, want SeenSkipped=1 (identity must fall back to canonical URL and remain stable across fetches)", result2)
	}
	countAfterSecond, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if countAfterSecond != countAfterFirst {
		t.Fatalf("articles count grew from %d to %d, want unchanged", countAfterFirst, countAfterSecond)
	}
}

func TestProcessCycle_SeenItems_TwoWorkersProcessingSameRawItemConcurrently(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	raw := article.Article{
		ExternalID:  "guid-race",
		OriginalURL: "https://example.com/race-story",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "feed-a"), Type: source.TypeRSS},
		Title:       "A racy story",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-time.Hour),
	}
	fetcher := fakeFetcher{articles: []article.Article{raw}}

	uc1, articles := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now})
	uc2, _ := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now})

	type runResult struct {
		accepted, seenSkipped int
		err                   error
	}
	results := make(chan runResult, 2)
	for _, uc := range []*usecase.ProcessCycle{uc1, uc2} {
		go func(uc *usecase.ProcessCycle) {
			r, err := uc.Run(context.Background())
			results <- runResult{accepted: r.Accepted, seenSkipped: r.SeenSkipped, err: err}
		}(uc)
	}
	r1, r2 := <-results, <-results
	if r1.err != nil || r2.err != nil {
		t.Fatalf("Run() errors: %v, %v", r1.err, r2.err)
	}
	totalAccepted := r1.accepted + r2.accepted
	if totalAccepted != 1 {
		t.Fatalf("totalAccepted = %d, want exactly 1 (SQLite's single-writer serialization must let only one worker claim and accept)", totalAccepted)
	}

	count, err := articles.Count(context.Background())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("articles count = %d, want 1", count)
	}
}
