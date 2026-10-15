package activelearning_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/activelearning"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestScore_AmbiguousZoneScoresHigherThanConfident(t *testing.T) {
	ambiguous := activelearning.Signals{HasPriorEvent: true, EventScore: 50, EventThreshold: 50, EventType: "editorial_ambiguous"}
	confident := activelearning.Signals{HasPriorEvent: true, EventScore: 95, EventThreshold: 50, EventType: "editorial_shadow"}

	got1 := activelearning.Score(ambiguous)
	got2 := activelearning.Score(confident)
	if got1 <= got2 {
		t.Errorf("Score(ambiguous) = %v, Score(confident) = %v, want ambiguous strictly higher", got1, got2)
	}
}

func TestScore_NovelSourceScoresHigherThanWellLabeledSource(t *testing.T) {
	novel := activelearning.Signals{SourceLabelCount: 0}
	wellKnown := activelearning.Signals{SourceLabelCount: 50}

	got1 := activelearning.Score(novel)
	got2 := activelearning.Score(wellKnown)
	if got1 <= got2 {
		t.Errorf("Score(novel source) = %v, Score(well-known source) = %v, want novel strictly higher", got1, got2)
	}
}

func TestScore_StaleSourceScoresHigherThanRecentlyLabeled(t *testing.T) {
	stale := activelearning.Signals{DaysSinceSourceLabel: 90, HasSourceLabelHistory: true}
	fresh := activelearning.Signals{DaysSinceSourceLabel: 1, HasSourceLabelHistory: true}

	got1 := activelearning.Score(stale)
	got2 := activelearning.Score(fresh)
	if got1 <= got2 {
		t.Errorf("Score(stale) = %v, Score(fresh) = %v, want stale strictly higher", got1, got2)
	}
}

func TestScore_ZeroSignalsIsFiniteAndNonNegative(t *testing.T) {
	got := activelearning.Score(activelearning.Signals{})
	if got < 0 {
		t.Errorf("Score(zero value) = %v, want >= 0", got)
	}
}

func TestScore_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		s    activelearning.Signals
	}{
		{
			name: "event score below threshold still clamps distance to non-negative",
			s:    activelearning.Signals{HasPriorEvent: true, EventScore: 10, EventThreshold: 50},
		},
		{
			name: "distance beyond 50 clamps closeness contribution to zero, not negative",
			s:    activelearning.Signals{HasPriorEvent: true, EventScore: 200, EventThreshold: 50},
		},
		{
			name: "entity label count beyond the novelty decay floor clamps to zero, not negative",
			s:    activelearning.Signals{EntityLabelCount: 100},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activelearning.Score(tt.s)
			if got < 0 {
				t.Errorf("Score(%+v) = %v, want >= 0", tt.s, got)
			}
		})
	}
}

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

type fakeArticles struct {
	candidates []article.Article
	// byID, when set, backs FindByID lookups by article ID (used to give
	// labeled articles a Source/PrimaryEntity distinct from the candidate
	// pool). missingIDs simulates an orphaned human_labels row — a label
	// whose article was deleted by retention.
	byID      map[string]article.Article
	missingID map[string]bool
	listErr   error
}

func (f *fakeArticles) Save(context.Context, article.Article) error { return nil }
func (f *fakeArticles) FindByID(_ context.Context, id string) (article.Article, error) {
	if f.missingID[id] {
		return article.Article{}, errors.New("fakeArticles: not found")
	}
	if a, ok := f.byID[id]; ok {
		return a, nil
	}
	return article.Article{}, nil
}

func (f *fakeArticles) DeleteByStatusOlderThan(context.Context, []article.Status, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeArticles) Count(context.Context) (int, error) { return 0, nil }
func (f *fakeArticles) ListCandidatesForReview(context.Context, time.Time, int) ([]article.Article, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.candidates, nil
}

func (f *fakeArticles) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticles) FindByCanonicalURL(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

type fakeLabels struct {
	examples []training.Example
	err      error
}

func (f *fakeLabels) Label(context.Context, training.Example) error        { return nil }
func (f *fakeLabels) Relabel(context.Context, training.Example, int) error { return nil }
func (f *fakeLabels) Retract(context.Context, string, int) error           { return nil }
func (f *fakeLabels) GetActive(context.Context, string) (training.Example, bool, error) {
	return training.Example{}, false, nil
}

func (f *fakeLabels) ListByArticle(context.Context, string) ([]training.Example, error) {
	return nil, nil
}

func (f *fakeLabels) ListAll(context.Context) ([]training.Example, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.examples, nil
}

func (f *fakeLabels) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}
func (f *fakeLabels) CountUnresolvedConflicts(context.Context) (int, error) { return 0, nil }
func (f *fakeLabels) ResolveConflict(context.Context, string, training.Example) error {
	return nil
}

func (f *fakeLabels) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return nil, nil
}
func (f *fakeLabels) CountEditorialExamples(context.Context) (int, error) { return 0, nil }

type fakeEvents struct {
	byArticle map[string][]out.Event
	err       error
}

func (f *fakeEvents) Append(context.Context, out.Event) error { return nil }
func (f *fakeEvents) ListByArticle(_ context.Context, articleID string) ([]out.Event, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byArticle[articleID], nil
}

func (f *fakeEvents) ListByEventTypesSince(context.Context, []string, time.Time, int) ([]out.Event, error) {
	return nil, nil
}
func (f *fakeEvents) DeleteOlderThan(context.Context, time.Time) (int, error) { return 0, nil }

type fakeReviewDecisions struct {
	recentByAction map[string][]string
	err            error
}

func (f *fakeReviewDecisions) Save(context.Context, out.ReviewDecision) error { return nil }
func (f *fakeReviewDecisions) ListBySession(context.Context, string) ([]out.ReviewDecision, error) {
	return nil, nil
}

func (f *fakeReviewDecisions) ListRecentByAction(_ context.Context, action string, _ time.Time) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.recentByAction[action], nil
}

func TestSelect_RanksAmbiguousCandidateAboveConfidentCandidate(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	articles := &fakeArticles{candidates: []article.Article{
		{ID: "confident", Source: source.Source{ID: mustID(t, source.TypeRSS, "s1")}},
		{ID: "ambiguous", Source: source.Source{ID: mustID(t, source.TypeRSS, "s1")}},
	}}
	events := &fakeEvents{byArticle: map[string][]out.Event{
		"confident": {{ArticleID: "confident", EventType: "editorial_shadow", Score: 95, Threshold: 50}},
		"ambiguous": {{ArticleID: "ambiguous", EventType: "editorial_ambiguous", Score: 50, Threshold: 50}},
	}}
	deps := activelearning.Deps{
		Articles:        articles,
		Labels:          &fakeLabels{},
		Events:          events,
		ReviewDecisions: &fakeReviewDecisions{},
		Clock:           fakeClock{now: now},
	}

	got, err := activelearning.Select(context.Background(), deps, activelearning.Options{
		Since: now.Add(-24 * time.Hour), PoolLimit: 10, SampleSize: 10, AmbiguousCooldown: 14 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(got) != 2 || got[0].Article.ID != "ambiguous" {
		t.Fatalf("Select() = %+v, want [ambiguous, confident] in that order", got)
	}
}

func TestSelect_ExcludesArticlesUnderAmbiguousCooldown(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	articles := &fakeArticles{candidates: []article.Article{
		{ID: "cooldown", Source: source.Source{ID: mustID(t, source.TypeRSS, "s1")}},
		{ID: "eligible", Source: source.Source{ID: mustID(t, source.TypeRSS, "s1")}},
	}}
	deps := activelearning.Deps{
		Articles: articles,
		Labels:   &fakeLabels{},
		Events:   &fakeEvents{byArticle: map[string][]out.Event{}},
		ReviewDecisions: &fakeReviewDecisions{recentByAction: map[string][]string{
			"ambiguous": {"cooldown"},
		}},
		Clock: fakeClock{now: now},
	}

	got, err := activelearning.Select(context.Background(), deps, activelearning.Options{
		Since: now.Add(-24 * time.Hour), PoolLimit: 10, SampleSize: 10, AmbiguousCooldown: 14 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(got) != 1 || got[0].Article.ID != "eligible" {
		t.Fatalf("Select() = %+v, want only [eligible]", got)
	}
}

func TestSelect_CapsAtSampleSize(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	articles := &fakeArticles{candidates: []article.Article{
		{ID: "a1"}, {ID: "a2"}, {ID: "a3"},
	}}
	deps := activelearning.Deps{
		Articles: articles, Labels: &fakeLabels{}, Events: &fakeEvents{byArticle: map[string][]out.Event{}},
		ReviewDecisions: &fakeReviewDecisions{}, Clock: fakeClock{now: now},
	}

	got, err := activelearning.Select(context.Background(), deps, activelearning.Options{
		Since: now.Add(-24 * time.Hour), PoolLimit: 10, SampleSize: 2, AmbiguousCooldown: 14 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Select()) = %d, want 2 (SampleSize cap)", len(got))
	}
}

func TestSelect_PropagatesListCandidatesForReviewError(t *testing.T) {
	wantErr := errors.New("list candidates boom")
	deps := activelearning.Deps{
		Articles:        &fakeArticles{listErr: wantErr},
		Labels:          &fakeLabels{},
		Events:          &fakeEvents{byArticle: map[string][]out.Event{}},
		ReviewDecisions: &fakeReviewDecisions{},
		Clock:           fakeClock{now: time.Now()},
	}

	_, err := activelearning.Select(context.Background(), deps, activelearning.Options{PoolLimit: 10, SampleSize: 10})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Select() error = %v, want wrapping %v", err, wantErr)
	}
}

func TestSelect_PropagatesListRecentByActionError(t *testing.T) {
	wantErr := errors.New("cooldown lookup boom")
	deps := activelearning.Deps{
		Articles:        &fakeArticles{},
		Labels:          &fakeLabels{},
		Events:          &fakeEvents{byArticle: map[string][]out.Event{}},
		ReviewDecisions: &fakeReviewDecisions{err: wantErr},
		Clock:           fakeClock{now: time.Now()},
	}

	_, err := activelearning.Select(context.Background(), deps, activelearning.Options{PoolLimit: 10, SampleSize: 10})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Select() error = %v, want wrapping %v", err, wantErr)
	}
}

func TestSelect_PropagatesListAllLabelsError(t *testing.T) {
	wantErr := errors.New("list labels boom")
	deps := activelearning.Deps{
		Articles:        &fakeArticles{},
		Labels:          &fakeLabels{err: wantErr},
		Events:          &fakeEvents{byArticle: map[string][]out.Event{}},
		ReviewDecisions: &fakeReviewDecisions{},
		Clock:           fakeClock{now: time.Now()},
	}

	_, err := activelearning.Select(context.Background(), deps, activelearning.Options{PoolLimit: 10, SampleSize: 10})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Select() error = %v, want wrapping %v", err, wantErr)
	}
}

func TestSelect_PropagatesListByArticleEventsError(t *testing.T) {
	wantErr := errors.New("list events boom")
	deps := activelearning.Deps{
		Articles:        &fakeArticles{candidates: []article.Article{{ID: "a1"}}},
		Labels:          &fakeLabels{},
		Events:          &fakeEvents{err: wantErr},
		ReviewDecisions: &fakeReviewDecisions{},
		Clock:           fakeClock{now: time.Now()},
	}

	_, err := activelearning.Select(context.Background(), deps, activelearning.Options{PoolLimit: 10, SampleSize: 10})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Select() error = %v, want wrapping %v", err, wantErr)
	}
}

func TestSelect_SkipsOrphanedLabelWhenComputingSourceAndEntitySignals(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	articles := &fakeArticles{
		candidates: []article.Article{
			{ID: "candidate", Source: source.Source{ID: mustID(t, source.TypeRSS, "s1")}, PrimaryEntity: "isotopes"},
		},
		byID: map[string]article.Article{
			"labeled1": {ID: "labeled1", Source: source.Source{ID: mustID(t, source.TypeRSS, "s1")}, PrimaryEntity: "isotopes"},
		},
		missingID: map[string]bool{"orphan1": true},
	}
	labels := &fakeLabels{examples: []training.Example{
		{ArticleID: "labeled1", LabeledAt: now.Add(-5 * 24 * time.Hour)},
		// orphan1 has no matching article (deleted by retention) — Select
		// must skip it rather than fail, mirroring `train export`'s own
		// skip-on-missing-article behavior.
		{ArticleID: "orphan1", LabeledAt: now},
	}}
	deps := activelearning.Deps{
		Articles:        articles,
		Labels:          labels,
		Events:          &fakeEvents{byArticle: map[string][]out.Event{}},
		ReviewDecisions: &fakeReviewDecisions{},
		Clock:           fakeClock{now: now},
	}

	got, err := activelearning.Select(context.Background(), deps, activelearning.Options{
		Since: now.Add(-24 * time.Hour), PoolLimit: 10, SampleSize: 10, AmbiguousCooldown: 14 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Select() = %+v, want exactly 1 candidate", got)
	}
	sig := got[0].Signals
	if sig.SourceLabelCount != 1 {
		t.Errorf("SourceLabelCount = %d, want 1 (orphan1's label must not count)", sig.SourceLabelCount)
	}
	if sig.EntityLabelCount != 1 {
		t.Errorf("EntityLabelCount = %d, want 1 (orphan1's label must not count)", sig.EntityLabelCount)
	}
	if !sig.HasSourceLabelHistory {
		t.Error("HasSourceLabelHistory = false, want true (source s1 has one valid label)")
	}
	if sig.DaysSinceSourceLabel != 5 {
		t.Errorf("DaysSinceSourceLabel = %v, want 5", sig.DaysSinceSourceLabel)
	}
}

func mustID(t *testing.T, platform source.Type, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", platform, raw, err)
	}
	return id
}

func TestSelect_ExcludesPolicySourcesBeforeSampleLimit(t *testing.T) {
	now := time.Now()
	deps := activelearning.Deps{
		Articles: &fakeArticles{candidates: []article.Article{
			{ID: "official", Source: source.Source{ID: mustID(t, source.TypeTwitter, "sprisotopes")}},
			{ID: "reviewable", Source: source.Source{ID: mustID(t, source.TypeRSS, "isotopes-news"), Name: "Springfield Isotopes Official"}},
		}},
		Labels: &fakeLabels{}, Events: &fakeEvents{}, ReviewDecisions: &fakeReviewDecisions{}, Clock: fakeClock{now: now},
		ExcludedSourceIDs: map[string]bool{"twitter:sprisotopes": true},
	}
	got, err := activelearning.Select(context.Background(), deps, activelearning.Options{Since: now.Add(-time.Hour), PoolLimit: 10, SampleSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Article.ID != "reviewable" {
		t.Fatalf("selected %+v, want the reviewable article (match source IDs, not display names)", got)
	}
}
