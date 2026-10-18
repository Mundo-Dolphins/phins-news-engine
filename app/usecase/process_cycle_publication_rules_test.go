package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/storyguard"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func newRulesTestCycle(fetcher fakeFetcher, articles *fakeArticleRepo, decisions *fakeDecisionRepo, now time.Time, matcher dictionary.Matcher, cascade in.DedupCascadeConfig) *usecase.ProcessCycle {
	freshness := freshness
	freshness.LiveUpdates = publishing.LiveUpdatePolicy{MaxAge: time.Hour}
	return usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		&fakeClusterRepo{}, decisions, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, matcher, nil, nil, cascade, fakeClock{now: now}, fakeLogger{},
		&fakeSourcePolicyRepository{}, &fakeSourcePolicyResolver{}, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{},
		nil,
	)
}

// "isotopes" + an injury-report term scores as related through the
// ambiguous-term path, but only "Springfield Isotopes" names the team
// outright.
func TestProcessCycle_AnchorPolicyRejectsRelatedVerdictWithoutAnchor(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	id := mustID(t, source.TypeTwitter, "somebody")
	run := func(anchorCategories ...string) (in.ProcessCycleResult, article.Status) {
		cascade := defaultCascade()
		cascade.Classification = in.ClassificationSettings{Enabled: true, Anchor: classification.AnchorPolicy{Enabled: true, Categories: map[string]bool{}}}
		for _, c := range anchorCategories {
			cascade.Classification.Anchor.Categories[c] = true
		}
		articles := &fakeArticleRepo{}
		uc := newRulesTestCycle(fakeFetcher{articles: []article.Article{{
			Title: "Isotopes injury report", OriginalURL: "https://example.com/isotopes-injuries",
			Source: source.Source{ID: id, Type: source.TypeTwitter}, ContentType: article.ContentTypeSocialPost,
			PublishedAt: now.Add(-time.Hour),
		}}}, articles, &fakeDecisionRepo{}, now, classificationMatcher(t), cascade)
		result, err := uc.Run(context.Background())
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		return result, articles.lastSaved().Status
	}

	if result, status := run("official_names"); result.Accepted != 0 || status != article.StatusRejectedNotRelated {
		t.Errorf("anchor only on official_names: result %+v status %q, want rejected_not_related", result, status)
	}
	if result, _ := run("official_names", "ambiguous_team_terms"); result.Accepted != 1 {
		t.Errorf("ambiguous_team_terms also anchors: result %+v, want accepted", result)
	}
}

type fakeRecentStories struct{ stories []out.RecentStory }

func (f fakeRecentStories) ListRecentlyPublishable(context.Context, time.Time, string, int) ([]out.RecentStory, error) {
	return f.stories, nil
}

func TestProcessCycle_StoryGuardWithholdsThirdMessageAboutSameStory(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	cascade := defaultCascade()
	cascade.StoryGuard = storyguard.Policy{Enabled: true, Window: 48 * time.Hour, MaxPerStory: 2, MinSharedTokens: 4, OverlapThreshold: 0.65}
	recent := fakeRecentStories{stories: []out.RecentStory{
		{ArticleID: "a", Title: "Montgomery Burns sells 2.1% of the Isotopes at record valuation"},
		{ArticleID: "b", Title: "[NBC Sports] - Montgomery Burns sells sliver of Isotopes at record valuation"},
	}}
	fetch := func(title string) fakeFetcher {
		return fakeFetcher{articles: []article.Article{{
			Title: title, OriginalURL: "https://example.com/" + title[:5],
			ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-time.Hour),
		}}}
	}

	articles := &fakeArticleRepo{}
	uc := newRulesTestCycle(fetch("Isotopes owner Montgomery Burns sells stake at record valuation"), articles, &fakeDecisionRepo{}, now, dictionary.Matcher{}, cascade)
	uc.SetRecentStories(recent)
	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 0 || result.Rejected != 1 {
		t.Fatalf("result = %+v, want the third message withheld", result)
	}
	if saved := articles.lastSaved(); saved.Status != article.StatusRetainedNoPublish || saved.StatusReason != "story_repeat_limit" {
		t.Errorf("saved = %q/%q, want retained_no_publish/story_repeat_limit", saved.Status, saved.StatusReason)
	}

	articles = &fakeArticleRepo{}
	uc = newRulesTestCycle(fetch("Kent Brockman no longer officially working with Isotopes"), articles, &fakeDecisionRepo{}, now, dictionary.Matcher{}, cascade)
	uc.SetRecentStories(recent)
	if result, err = uc.Run(context.Background()); err != nil || result.Accepted != 1 {
		t.Fatalf("unrelated story: result %+v err %v, want accepted", result, err)
	}
}

func TestProcessCycle_SocialPostsDuringLiveGameGetShortWindow(t *testing.T) {
	now := time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC)
	state := func(phase matchstate.Phase) matchstate.State {
		return matchstate.State{Identity: matchstate.Identity{MatchID: "nfl-wk5", HomeTeam: "MIA", AwayTeam: "CIN"}, Phase: phase}
	}
	id := mustID(t, source.TypeTwitter, "beatreporter")
	run := func(provider out.MatchStateProvider, contentType article.ContentType, age time.Duration) in.ProcessCycleResult {
		cascade := defaultCascade()
		uc := newRulesTestCycle(fakeFetcher{articles: []article.Article{{
			Title: "Another sack for Springfield on 3rd and long", OriginalURL: "https://example.com/sack",
			Source: source.Source{ID: id, Type: source.TypeTwitter}, ContentType: contentType, PublishedAt: now.Add(-age),
		}}}, &fakeArticleRepo{}, &fakeDecisionRepo{}, now, dictionary.Matcher{}, cascade)
		if provider != nil {
			uc.SetMatchStateProvider(provider)
		}
		result, err := uc.Run(context.Background())
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		return result
	}

	live := fakeMatchStateProvider{state: state(matchstate.Q3), ok: true}
	if r := run(live, article.ContentTypeSocialPost, 2*time.Hour); r.Accepted != 0 {
		t.Errorf("two-hour-old social post during a live game: %+v, want withheld as stale", r)
	}
	if r := run(live, article.ContentTypeSocialPost, 20*time.Minute); r.Accepted != 1 {
		t.Errorf("fresh social post during a live game: %+v, want accepted", r)
	}
	if r := run(live, article.ContentTypeArticle, 2*time.Hour); r.Accepted != 1 {
		t.Errorf("article during a live game: %+v, want accepted (only social commentary is shortened)", r)
	}
	if r := run(fakeMatchStateProvider{state: state(matchstate.Final), ok: true}, article.ContentTypeSocialPost, 2*time.Hour); r.Accepted != 1 {
		t.Errorf("social post after the final whistle: %+v, want accepted (finished-game news keeps the normal window)", r)
	}
	if r := run(nil, article.ContentTypeSocialPost, 2*time.Hour); r.Accepted != 1 {
		t.Errorf("no match-state provider: %+v, want accepted", r)
	}
}
