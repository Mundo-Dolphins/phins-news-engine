package editorial_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// The fixture config (testsupport.WriteFixture) sets editorial_rollout's
// canary_min_days: 7 and canary_min_articles: 100 — see helpers.go.
const (
	canaryTestMinDays     = 7
	canaryTestMinArticles = 100
)

// seedArticleForCanary saves a minimal article row so
// article_processing_events' FK (article_id REFERENCES articles(id)) is
// satisfied before appending events for it — mirrors
// editorialmetrics_test.go's seedArticleForMetrics.
func seedArticleForCanary(t *testing.T, articles *sqliteadapter.ArticleRepository, id string) {
	t.Helper()
	a := article.Article{
		ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
		Source:      source.Source{ID: testsupport.MustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		FetchedAt:   time.Now(),
		PublishedAt: time.Now(),
		Status:      article.StatusFetched,
	}
	if err := articles.Save(context.Background(), a); err != nil {
		t.Fatalf("seed article %s: %v", id, err)
	}
}

// seedCanaryEvents opens the fixture's real, migrated SQLite database,
// seeds an article row for every distinct ArticleID referenced by events,
// and appends each event via the real DecisionRepository — mirrors
// editorialmetrics_test.go's seedEditorialEvents.
func seedCanaryEvents(t *testing.T, configPath string, events []out.Event) {
	t.Helper()
	dbPath := filepath.Join(filepath.Dir(configPath), "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	articles := sqliteadapter.NewArticleRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	ctx := context.Background()

	seeded := map[string]bool{}
	for i, e := range events {
		if !seeded[e.ArticleID] {
			seedArticleForCanary(t, articles, e.ArticleID)
			seeded[e.ArticleID] = true
		}
		if err := decisions.Append(ctx, e); err != nil {
			t.Fatalf("append event %d for %s: %v", i, e.ArticleID, err)
		}
	}
}

// withCanaryNow pins editorial.CanaryNow to a fixed instant for the
// duration of the calling test, restoring the real-clock default
// afterward. evaluateCanaryCriteria's event query is unbounded (it scans
// the enforced arm's entire recorded history, not a window sized by
// canary_min_days — see its doc comment for why), so this seam is no
// longer needed to hit an exact query-boundary tie; it remains purely so
// ElapsedDays is computed against a value the test itself controls,
// keeping "PASS"/"FAIL" assertions deterministic regardless of how long
// seeding + command dispatch actually takes.
func withCanaryNow(t *testing.T, fixed time.Time) {
	t.Helper()
	prev := editorial.CanaryNow
	editorial.CanaryNow = func() time.Time { return fixed }
	t.Cleanup(func() { editorial.CanaryNow = prev })
}

// canaryPassingFixtureEvents builds canaryTestMinArticles enforced-arm
// (mode=canary, reason=editorial:canary_reject) rejection events, the
// earliest dated canaryTestMinDays+1 days before fixedNow — comfortably
// past the day-gate floor, and deliberately OLDER than
// canaryTestMinDays, which is exactly the timestamp the original
// (buggy) implementation could never observe: that version bounded its
// own query to events within the last canaryTestMinDays days, so an
// event this old would have been silently excluded from the query
// entirely, ArticleCount would have undercounted it, and ElapsedDays
// would have topped out at canaryTestMinDays-1 regardless. This fixture
// is the regression check for that bug (see evaluateCanaryCriteria's
// doc comment) as well as the ordinary PASS scenario.
func canaryPassingFixtureEvents(fixedNow time.Time) []out.Event {
	earliest := fixedNow.AddDate(0, 0, -(canaryTestMinDays + 1))
	events := make([]out.Event, 0, canaryTestMinArticles)
	for i := 0; i < canaryTestMinArticles; i++ {
		// Exactly one event (i==0) pins the earliest timestamp; the rest
		// are dated at fixedNow and only count toward the article-count
		// gate.
		occurredAt := fixedNow
		if i == 0 {
			occurredAt = earliest
		}
		events = append(events, out.Event{
			ArticleID:  articleIDForCanary(i),
			EventType:  "editorial",
			ReasonCode: "editorial:canary_reject",
			Mode:       "canary",
			Score:      5,
			OccurredAt: occurredAt,
		})
	}
	return events
}

// canaryFailingFixtureEvents builds too few enforced-arm events (below
// canaryTestMinArticles) — a real, implemented gate (unlike the deferred
// precision/false-positive-rate gates), so status/promote must report FAIL.
func canaryFailingFixtureEvents(fixedNow time.Time) []out.Event {
	events := make([]out.Event, 0, 3)
	for i := 0; i < 3; i++ {
		events = append(events, out.Event{
			ArticleID:  articleIDForCanary(i),
			EventType:  "editorial",
			ReasonCode: "editorial:canary_reject",
			Mode:       "canary",
			Score:      5,
			OccurredAt: fixedNow,
		})
	}
	return events
}

func articleIDForCanary(i int) string {
	return "canary-a" + strconv.Itoa(i)
}

// TestEditorialCanary_Status_CountsEnforcedArmVetoRejections proves the
// enforced arm's veto-triggered rejections count toward the article-count
// gate. The veto gate fires before the score is ever consulted, so a
// veto rejection is as real an enforced-arm decision as a score reject —
// omitting it undercounts enforced-arm activity and makes both gates
// read optimistically. The non-terminal veto event (ToStatus unchanged,
// i.e. the log-only twin recorded even in the shadow arm) must NOT count.
func TestEditorialCanary_Status_CountsEnforcedArmVetoRejections(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	fixedNow := time.Now().UTC()
	withCanaryNow(t, fixedNow)

	events := canaryPassingFixtureEvents(fixedNow)
	// Drop one score-reject and replace it with a terminal veto
	// rejection: the count must stay at canaryTestMinArticles (PASS).
	events = events[:len(events)-1]
	events = append(events,
		out.Event{
			ArticleID: articleIDForCanary(canaryTestMinArticles), EventType: "editorial_veto",
			ReasonCode: "veto:noise", Mode: "canary", OccurredAt: fixedNow,
			FromStatus: article.StatusFetched, ToStatus: article.StatusRejectedQuality,
		},
		// Log-only veto event (status unchanged) — must not be counted.
		out.Event{
			ArticleID: articleIDForCanary(canaryTestMinArticles + 1), EventType: "editorial_veto",
			ReasonCode: "veto:noise", Mode: "canary", OccurredAt: fixedNow,
			FromStatus: article.StatusFetched, ToStatus: article.StatusFetched,
		},
	)
	seedCanaryEvents(t, configPath, events)

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "canary", "status")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	if !strings.Contains(got, "articles="+strconv.Itoa(canaryTestMinArticles)+" ") {
		t.Errorf("output = %q, want articles=%d (the terminal veto rejection counted, the log-only one not)", got, canaryTestMinArticles)
	}
	if !strings.Contains(got, "PASS") {
		t.Errorf("output = %q, want PASS", got)
	}
}

// TestEditorialCanary_Status_ReportsPassWhenCriteriaMet also doubles as
// the regression check for the "day-gate structurally unsatisfiable"
// bug: canaryPassingFixtureEvents' earliest event is canaryTestMinDays+1
// days old, a timestamp the original bounded-query implementation would
// have excluded entirely (see that fixture's doc comment), which would
// have made this test fail against the old code.
func TestEditorialCanary_Status_ReportsPassWhenCriteriaMet(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	fixedNow := time.Now().UTC()
	withCanaryNow(t, fixedNow)
	seedCanaryEvents(t, configPath, canaryPassingFixtureEvents(fixedNow))

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "canary", "status")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	if !strings.Contains(got, "PASS") {
		t.Errorf("output = %q, want it to report PASS", got)
	}
	assertUnevaluatedCriteriaAreLabelled(t, got)
}

// assertUnevaluatedCriteriaAreLabelled pins the honesty of the two
// criteria this command cannot compute: precision and false-positive rate
// need a labeled ground truth article_processing_events doesn't carry, so
// they must read as "not evaluated" — printing "0.000" would let an
// operator mistake an unevaluated criterion for a perfect measured one,
// and a PASS verdict for a four-gate PASS.
func assertUnevaluatedCriteriaAreLabelled(t *testing.T, got string) {
	t.Helper()
	if strings.Contains(got, "precision=0.000") || strings.Contains(got, "false_positive_rate=0.000") {
		t.Errorf("output = %q, want precision/false_positive_rate reported as not evaluated, never as a measured 0.000", got)
	}
	if !strings.Contains(got, "precision=n/a (not evaluated)") || !strings.Contains(got, "false_positive_rate=n/a (not evaluated)") {
		t.Errorf("output = %q, want both precision and false_positive_rate labelled n/a (not evaluated)", got)
	}
	if !strings.Contains(got, "2 of 4 configured criteria evaluated") {
		t.Errorf("output = %q, want it to state how many configured criteria were actually evaluated", got)
	}
}

func TestEditorialCanary_Status_ReportsFailWhenBelowMinimums(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	fixedNow := time.Now().UTC()
	withCanaryNow(t, fixedNow)
	seedCanaryEvents(t, configPath, canaryFailingFixtureEvents(fixedNow))

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "canary", "status")
	if err != nil {
		t.Fatalf("Execute() error = %v, output: %s", err, got)
	}
	if !strings.Contains(got, "FAIL") {
		t.Errorf("output = %q, want it to report FAIL for insufficient article count/elapsed days", got)
	}
	if !strings.Contains(got, "article count") {
		t.Errorf("output = %q, want it to name the article-count gate as the failure reason", got)
	}
	assertUnevaluatedCriteriaAreLabelled(t, got)
}

func TestEditorialCanary_Promote_RefusesWhenCriteriaFailWithoutForce(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	fixedNow := time.Now().UTC()
	withCanaryNow(t, fixedNow)
	seedCanaryEvents(t, configPath, canaryFailingFixtureEvents(fixedNow))

	_, err := testsupport.Execute(t, "--config", configPath, "editorial", "canary", "promote")
	if err == nil {
		t.Fatal("Execute() error = nil, want refusal when canary criteria are failing and --force is not passed")
	}

	got, err := testsupport.Execute(t, "--config", configPath, "editorial", "canary", "promote", "--force")
	if err != nil {
		t.Fatalf("Execute() with --force error = %v, output: %s", err, got)
	}
	if !strings.Contains(got, "forced=true") {
		t.Errorf("output = %q, want it to report forced=true", got)
	}
	assertUnevaluatedCriteriaAreLabelled(t, got)
}
