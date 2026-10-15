package usecase_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

// Regression fixtures for duplicate stories reaching Telegram
// because editorial bypass lists leaked into dedup/cluster gating.

const (
	officialWebID     = "springfield-isotopes-official"
	officialTwitterID = "sprisotopes"
)

func newBypassCycle(db *sql.DB, fetcher fakeFetcher, now time.Time, mutate func(*in.DedupCascadeConfig)) *usecase.ProcessCycle {
	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{
		Mode:                          "disabled",
		ModelBypassSourceIDs:          map[string]bool{"twitter:" + officialTwitterID: true},
		UnconditionalPublishSourceIDs: map[string]bool{"web:" + officialWebID: true},
	}
	if mutate != nil {
		mutate(&cascade)
	}
	return usecase.NewProcessCycle(fetcher, sqliteadapter.NewArticleRepository(db), sqliteadapter.NewDeduplicationRepository(db),
		sqliteadapter.NewAliasRepository(db), sqliteadapter.NewSimilarityIndexRepository(db), sqliteadapter.NewClusterRepository(db),
		sqliteadapter.NewDecisionRepository(db), sqliteadapter.NewOutboxRepository(db), sqliteadapter.NewUnitOfWork(db), freshness,
		dictionary.Matcher{}, nil, nil, cascade, fakeClock{now: now}, fakeLogger{}, sqliteadapter.NewSourcePolicyRepository(db),
		&fakeSourcePolicyResolver{}, sqliteadapter.NewClusterPublicationRepository(db), sqliteadapter.NewSeenItemsRepository(db),
		sqliteadapter.NewSourcePolicyCooldownRepository(db), sqliteadapter.NewUnknownEntityRepository(db), nil)
}

func outboxCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM publication_outbox`).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

func bypassArticle(t *testing.T, typ source.Type, raw, title, url, externalID string, published time.Time) article.Article {
	t.Helper()
	return article.Article{
		Title: title, OriginalURL: url, ExternalID: externalID, ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, typ, raw), Type: typ},
		PublishedAt: published,
	}
}

func TestProcessCycle_Integration_ExactDuplicateNeverRepublished_AcrossSourceKinds(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		typ  source.Type
		raw  string
	}{
		{"plain source", source.TypeRSS, "plain-feed"},
		{"model bypass source", source.TypeTwitter, officialTwitterID},
		{"unconditional source", source.TypeWeb, officialWebID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newIntegrationDB(t)
			mk := func(typ source.Type, raw, title string) article.Article {
				return bypassArticle(t, typ, raw, title, "https://example.com/harrowby-presser", "", now.Add(-time.Hour))
			}
			// The first sighting comes from another source, so the second one
			// cannot be short-circuited by its own source's seen-items identity
			// and must be stopped by exact dedup itself.
			uc1 := newBypassCycle(db, fakeFetcher{articles: []article.Article{mk(source.TypeRSS, "first-feed", "Harrowby meets with the media after the Badgers game")}}, now, nil)
			if _, err := uc1.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			// Later cycle, same canonical URL, even with a different title.
			uc2 := newBypassCycle(db, fakeFetcher{articles: []article.Article{mk(tc.typ, tc.raw, "Completely different wording about Ezo rehab progress")}}, now.Add(time.Hour), nil)
			res, err := uc2.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.Duplicate != 1 || res.Accepted != 0 {
				t.Errorf("second result = %+v, want duplicate=1 accepted=0", res)
			}
			if n := outboxCount(t, db); n != 1 {
				t.Errorf("outbox rows = %d, want 1", n)
			}
		})
	}
}

func TestProcessCycle_Integration_ExternalIDDuplicateNeverRepublished_BypassSource(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	mk := func(url string) article.Article {
		return bypassArticle(t, source.TypeTwitter, officialTwitterID, "Isotopes announce practice squad moves for Sunday", url, "tweet-1", now.Add(-time.Hour))
	}
	if _, err := newBypassCycle(db, fakeFetcher{articles: []article.Article{mk("https://twitter.com/sprisotopes/status/1")}}, now, nil).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := newBypassCycle(db, fakeFetcher{articles: []article.Article{mk("https://x.com/sprisotopes/status/1?s=20")}}, now.Add(time.Hour), nil).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 0 {
		t.Errorf("result = %+v, want accepted=0", res)
	}
	if n := outboxCount(t, db); n != 1 {
		t.Errorf("outbox rows = %d, want 1", n)
	}
}

func TestProcessCycle_Integration_SameBatchSameStory_OnlyOneInitialPublication(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const headline = "Springfield Isotopes Fans: Isotopes Vs Cypress Comets Predictions"
	social := bypassArticle(t, source.TypeTwitter, "thetopesreport", headline, "https://twitter.com/thetopesreport/status/1000000000000000001", "", now.Add(-20*time.Minute))
	// Same story re-served by an aggregator, wrapped with a bracketed prefix.
	wrapped := bypassArticle(t, source.TypeRSS, "news-aggregator", "[Fan Network: The Topes Report] - "+headline, "https://news-aggregator.example.com/nfl/springfield-isotopes/articles/10000001", "", now.Add(-10*time.Minute))

	for name, order := range map[string][]article.Article{"original first": {social, wrapped}, "wrapper first": {wrapped, social}} {
		t.Run(name, func(t *testing.T) {
			db := newIntegrationDB(t)
			res, err := newBypassCycle(db, fakeFetcher{articles: order}, now, nil).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.Accepted != 1 {
				t.Errorf("result = %+v, want accepted=1", res)
			}
			if n := outboxCount(t, db); n != 1 {
				t.Errorf("outbox rows = %d, want 1", n)
			}
		})
	}
}

func TestProcessCycle_Integration_ModelBypassSourceStillSubjectToNearDuplicateAndClusterGate(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const headline = "Game-day mailbag covers Jonah progress and the defensive scheme in Springfield"
	first := bypassArticle(t, source.TypeRSS, "si-feed", headline, "https://www.sportsmag.example.com/nfl/isotopes/mailbag", "", now.Add(-20*time.Minute))
	// Official bypass source cross-posts the same story under a new URL.
	mirror := bypassArticle(t, source.TypeTwitter, officialTwitterID, headline, "https://twitter.com/sprisotopes/status/42", "", now.Add(-10*time.Minute))
	db := newIntegrationDB(t)
	res, err := newBypassCycle(db, fakeFetcher{articles: []article.Article{first, mirror}}, now, nil).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 {
		t.Errorf("result = %+v, want accepted=1 (mirror must be blocked by near-dup/cluster gate)", res)
	}
	if n := outboxCount(t, db); n != 1 {
		t.Errorf("outbox rows = %d, want 1", n)
	}
}

func TestProcessCycle_Integration_TruncatedSocialHeadlineMatchesFullHeadline(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	full := bypassArticle(t, source.TypeRSS, "topesreport-feed", "NFL picks and odds: Isotopes vs. Comets predictions, full Week 4 2026 winners", "https://example.com/picks", "", now.Add(-20*time.Minute))
	trunc := bypassArticle(t, source.TypeTwitter, "thetopesreport", "NEW: NFL picks and odds: Isotopes vs. Comets predictions, full Week 4…", "https://twitter.com/thetopesreport/status/1", "", now.Add(-10*time.Minute))
	db := newIntegrationDB(t)
	res, err := newBypassCycle(db, fakeFetcher{articles: []article.Article{full, trunc}}, now, nil).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 {
		t.Errorf("result = %+v, want accepted=1", res)
	}
}

func TestProcessCycle_Integration_OriginalTargetURLAliasMatchesAggregatorItem(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	orig := bypassArticle(t, source.TypeRSS, "topesreport-feed", "Isotopes offense shows signs of life against the Comets", "https://topesreport.example.com/story", "", now.Add(-20*time.Minute))
	agg := bypassArticle(t, source.TypeRSS, "news-aggregator", "Totally different aggregator summary headline words here", "https://news-aggregator.example.com/articles/1", "", now.Add(-10*time.Minute))
	agg.Metadata = map[string]string{"origin_url": "https://topesreport.example.com/story"}
	db := newIntegrationDB(t)
	res, err := newBypassCycle(db, fakeFetcher{articles: []article.Article{orig, agg}}, now, nil).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 || res.Duplicate != 1 {
		t.Errorf("result = %+v, want accepted=1 duplicate=1", res)
	}
}

func officialConfirmationPolicy(id string) sourcepolicy.Policy {
	return sourcepolicy.Policy{
		SourceID: id, Authority: sourcepolicy.AuthorityOfficialTeam,
		AllowOfficialConfirmation: true, PublishConfirmationWithoutNewInformation: true,
	}
}

func TestProcessCycle_Integration_OfficialConfirmationAllowedOnce_CrossPostDenied(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	resolver := &fakeSourcePolicyResolver{mode: sourcepolicy.ModeEnforce, policies: map[string]sourcepolicy.Policy{
		"rss:official-site": officialConfirmationPolicy("rss:official-site"),
		"rss:official-feed": officialConfirmationPolicy("rss:official-feed"),
	}}
	run := func(raw, title, url string, at time.Time) in.ProcessCycleResult {
		f := fakeFetcher{articles: []article.Article{{
			Title: title, Source: source.Source{ID: mustID(t, source.TypeRSS, raw), Type: source.TypeRSS},
			OriginalURL: url, ContentType: article.ContentTypeArticle, PublishedAt: at.Add(-30 * time.Minute),
		}}}
		uc, _ := newIntegrationProcessCycleWithResolver(db, f, fakeClock{now: at}, resolver)
		res, err := uc.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	run("reporter-1", "Isotopes Sign Jane Doe To Bolster Secondary", "https://example.com/jane-original", now.Add(-3*time.Hour))
	confirm := run("official-site", "Springfield Officially Adds Defensive Back Jane Doe In Free Agency Move", "https://official.example.com/jane", now.Add(-2*time.Hour))
	crossPost := run("official-feed", "Isotopes Officially Add Defensive Back Jane Doe In Free Agency Move", "https://social.example.com/jane", now.Add(-1*time.Hour))

	var clusters int
	_ = db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM event_clusters`).Scan(&clusters)
	if clusters != 1 {
		t.Fatalf("clusters = %d, want 1 (fixture precondition)", clusters)
	}
	if confirm.Accepted != 1 {
		t.Errorf("official confirmation of a non-official report: %+v, want accepted=1", confirm)
	}
	if crossPost.Accepted != 0 {
		t.Errorf("official cross-post with no new information: %+v, want accepted=0", crossPost)
	}
	if n := outboxCount(t, db); n != 2 {
		t.Errorf("outbox rows = %d, want 2 (initial report + one official confirmation)", n)
	}
}
