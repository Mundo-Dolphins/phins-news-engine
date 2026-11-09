package editorialmetrics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialmetrics"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var testNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

// fakeDecisions returns events filtered by event type, like the real
// repository (since and limit are recorded, not applied).
type fakeDecisions struct {
	out.DecisionRepository
	events   []out.Event
	gotTypes []string
	gotSince time.Time
	gotLimit int
}

func (f *fakeDecisions) ListByEventTypesSince(_ context.Context, types []string, since time.Time, limit int) ([]out.Event, error) {
	f.gotTypes, f.gotSince, f.gotLimit = types, since, limit
	want := map[string]bool{}
	for _, t := range types {
		want[t] = true
	}
	var res []out.Event
	for _, e := range f.events {
		if want[e.EventType] {
			res = append(res, e)
		}
	}
	return res, nil
}

func TestEvaluateCanaryCountsTerminalVetoOnly(t *testing.T) {
	decisions := &fakeDecisions{events: []out.Event{
		{ArticleID: "a1", EventType: "editorial", ReasonCode: "editorial:canary_reject", Mode: "canary", OccurredAt: testNow.AddDate(0, 0, -3)},
		{ArticleID: "a2", EventType: "editorial_veto", ToStatus: article.StatusRejectedQuality, Mode: "canary", OccurredAt: testNow.AddDate(0, 0, -5)},
		// log-only veto twin: ToStatus unchanged, not enforced-arm.
		{ArticleID: "a3", EventType: "editorial_veto", ToStatus: article.StatusFetched, Mode: "canary", OccurredAt: testNow.AddDate(0, 0, -9)},
		// shadow-arm event inside canary mode.
		{ArticleID: "a4", EventType: "editorial_canary_shadow", Mode: "canary", OccurredAt: testNow.AddDate(0, 0, -9)},
		// enforce mode, not canary.
		{ArticleID: "a5", EventType: "editorial", ReasonCode: "editorial:canary_reject", Mode: "enforce", OccurredAt: testNow.AddDate(0, 0, -9)},
	}}
	rollout := config.EditorialRolloutConfig{CanaryMinArticles: 2, CanaryMinDays: 5}
	got, err := editorialmetrics.EvaluateCanary(context.Background(), decisions, rollout, testNow)
	if err != nil {
		t.Fatalf("EvaluateCanary: %v", err)
	}
	if got.ArticleCount != 2 || got.ElapsedDays != 5 || !got.Passed || len(got.Failures) != 0 {
		t.Fatalf("result = %+v, want 2 articles, 5 days, passed", got)
	}
	if !decisions.gotSince.IsZero() {
		t.Errorf("since = %v, want the unbounded zero time", decisions.gotSince)
	}

	rollout.CanaryMinArticles = 3
	got, err = editorialmetrics.EvaluateCanary(context.Background(), decisions, rollout, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if got.Passed || len(got.Failures) != 1 || got.Failures[0] != "article count 2 < min 3" {
		t.Fatalf("result = %+v, want one article-count failure", got)
	}
}

func TestMetricsRates(t *testing.T) {
	since := testNow.Add(-24 * time.Hour)
	decisions := &fakeDecisions{events: []out.Event{
		{EventType: "editorial", ReasonCode: "editorial:reject_score", Mode: "enforce", Score: 5},
		{EventType: "editorial", ReasonCode: "editorial:canary_ambiguous_reject", Mode: "canary", Score: 45},
		{EventType: "editorial_ambiguous", Mode: "enforce", Score: 55},
		{EventType: "editorial_shadow", Mode: "shadow", Score: 120},
		{EventType: "unrelated", Mode: "enforce", Score: 1},
	}}
	got, err := editorialmetrics.Metrics(context.Background(), decisions, since)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if got.Total != 4 || got.RejectRate != 0.5 || got.AmbiguousRate != 0.5 {
		t.Fatalf("report = %+v, want total 4, reject 0.5, ambiguous 0.5", got)
	}
	if got.ByMode["enforce"] != 2 || got.ByMode["canary"] != 1 || got.ByMode["shadow"] != 1 {
		t.Errorf("ByMode = %v", got.ByMode)
	}
	if len(got.ByReasonCode) != 2 {
		t.Errorf("ByReasonCode = %v, want the two non-empty codes", got.ByReasonCode)
	}
	if got.ByScoreDecile != [10]int{1, 0, 0, 0, 1, 1, 0, 0, 0, 1} {
		t.Errorf("ByScoreDecile = %v", got.ByScoreDecile)
	}
	if !got.Since.Equal(since) || decisions.gotLimit != 100000 {
		t.Errorf("since = %v limit = %d", got.Since, decisions.gotLimit)
	}
}

type fakeArticles map[string]article.Article

func (f fakeArticles) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f[id]
	if !ok {
		return article.Article{}, errors.New("not found")
	}
	return a, nil
}

func TestReviewMissingArticle(t *testing.T) {
	since := testNow.AddDate(0, 0, -7)
	decisions := &fakeDecisions{events: []out.Event{
		{ArticleID: "a1", EventType: "editorial_shadow", Score: 82.5, OccurredAt: testNow},
		{ArticleID: "gone", EventType: "editorial", Score: 3, OccurredAt: testNow.Add(-time.Minute)},
		{ArticleID: "a1", EventType: "editorial_shadow", Score: 70, OccurredAt: testNow.Add(-2 * time.Minute)},
	}}
	articles := fakeArticles{"a1": {ID: "a1", Title: "Kept", Status: article.StatusPublished}}
	got, err := editorialmetrics.Review(context.Background(), decisions, articles, since, 50)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if decisions.gotLimit != 50 || !decisions.gotSince.Equal(since) {
		t.Errorf("query since=%v limit=%d", decisions.gotSince, decisions.gotLimit)
	}
	if got.Counts["editorial_shadow"] != 2 || got.Counts["editorial"] != 1 {
		t.Errorf("Counts = %v", got.Counts)
	}
	if len(got.Rows) != 3 {
		t.Fatalf("Rows = %+v, want 3", got.Rows)
	}
	if r := got.Rows[0]; r.Title != "Kept" || r.ActualStatus != "published" || r.Score != 82.5 || r.EventType != "editorial_shadow" || !r.OccurredAt.Equal(testNow) {
		t.Errorf("row 0 = %+v", r)
	}
	if r := got.Rows[1]; r.Title != "(article not found)" || r.ActualStatus != "(unknown)" {
		t.Errorf("row 1 = %+v, want the missing-article placeholders", r)
	}
	if len(editorialmetrics.ReviewEventTypes) != 3 {
		t.Errorf("ReviewEventTypes = %v", editorialmetrics.ReviewEventTypes)
	}
}

func TestVetoImpactPerRuleCounts(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "noise-1", Title: "Isotopes spotted near the coast"},
		{ArticleID: "legit-1", Title: "Isotopes spotted at NFL practice"},
		{ArticleID: "both-1", Title: "Isotopes spotted with free bets"},
		{ArticleID: "promo-1", Title: "Free bets before kickoff"},
	}
	cfg := editorialveto.Config{Rules: []editorialveto.Rule{
		{ReasonCode: "veto:noise:spotted", Patterns: []string{"isotopes spotted"}, ExcludePatterns: []string{"NFL"}},
		{ReasonCode: "veto:gambling:promo", Patterns: []string{"free bets"}},
	}}
	got := editorialmetrics.VetoImpact(examples, cfg, "fixture.yml")
	if got.Dataset != "fixture.yml" || got.Total != 4 || got.Blocked != 3 || got.Reduction != 0.75 {
		t.Fatalf("report = %+v, want 3 of 4 blocked", got)
	}
	// both-1 counts under every matching rule, but once in Blocked.
	if got.ByReason["veto:noise:spotted"] != 2 || got.ByReason["veto:gambling:promo"] != 2 {
		t.Errorf("ByReason = %v", got.ByReason)
	}
	if ex := got.Examples["veto:noise:spotted"]; len(ex) != 2 || ex[0] != "noise-1" || ex[1] != "both-1" {
		t.Errorf("Examples = %v", got.Examples)
	}
}
