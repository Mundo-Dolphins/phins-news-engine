package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classification"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
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

func (f fakeRecentStories) ListRecentClustered(context.Context, time.Time, string, int) ([]out.RecentStory, error) {
	return f.stories, nil
}

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

func TestProcessCycle_StoryGuardWithholdsVerbatimTitleFromAnotherSource(t *testing.T) {
	now := time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC)
	cascade := defaultCascade()
	cascade.StoryGuard = storyguard.Policy{Enabled: true, Window: 48 * time.Hour, MaxPerStory: 2, MinSharedTokens: 4, OverlapThreshold: 0.65, VerbatimOverlap: 0.75}
	recent := fakeRecentStories{stories: []out.RecentStory{
		{ArticleID: "tweet", SourceID: "twitter:wire", Title: "Isotopes release LB, sign DE from their own practice squad"},
	}}
	run := func(title string, id source.ID) (in.ProcessCycleResult, article.Article) {
		articles := &fakeArticleRepo{}
		uc := newRulesTestCycle(fakeFetcher{articles: []article.Article{{
			Title: title, OriginalURL: "https://example.com/" + title[:8],
			Source:      source.Source{ID: id},
			ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-time.Hour),
		}}}, articles, &fakeDecisionRepo{}, now, dictionary.Matcher{}, cascade)
		uc.SetRecentStories(recent)
		result, err := uc.Run(context.Background())
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		return result, articles.lastSaved()
	}

	// One prior message is below max_per_story, but another source repeating
	// the headline is withheld anyway.
	result, saved := run("[USA Today: Isotopes Wire] - Isotopes release LB, sign DE from their own practice squad", mustID(t, source.TypeRSS, "sportspyder"))
	if result.Accepted != 0 || saved.StatusReason != "story_repeat_limit" {
		t.Fatalf("verbatim repeat: result %+v reason %q, want withheld as story_repeat_limit", result, saved.StatusReason)
	}
	// The same source reposting is not a second source: only max_per_story applies.
	if result, _ := run("Isotopes release LB, sign DE from their own practice squad", mustID(t, source.TypeTwitter, "wire")); result.Accepted != 1 {
		t.Fatalf("same-source repeat: result %+v, want accepted", result)
	}
}

func TestProcessCycle_ClusterGateDeniesRelatedCoverageExceptFirstOfficialVoice(t *testing.T) {
	now := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	cascade := defaultCascade()
	cascade.LexicalJoin = storyguard.Policy{Enabled: true, Window: 48 * time.Hour, MinSharedTokens: 4, OverlapThreshold: 0.65}
	recent := fakeRecentStories{stories: []out.RecentStory{
		{ArticleID: "a", ClusterID: "cluster-ross", Title: "Montgomery Burns sells 2.1% of the Isotopes at record valuation"},
	}}
	official := mustID(t, source.TypeTwitter, "isotopes")
	mirror := mustID(t, source.TypeInstagram, "isotopes")
	policies := &fakeSourcePolicyResolver{policies: map[string]sourcepolicy.Policy{
		official.String(): {SourceID: official.String(), Authority: sourcepolicy.AuthorityOfficialTeam},
		mirror.String():   {SourceID: mirror.String(), Authority: sourcepolicy.AuthorityOfficialTeam},
	}}
	run := func(id source.ID, holder string) (in.ProcessCycleResult, article.Article) {
		gate := &fakeClusterPublicationRepository{
			state: map[string]string{"cluster-ross": "confirmed"},
			rows:  map[string]out.ClusterPublicationReservation{"cluster-ross": {ClusterID: "cluster-ross", ArticleID: "a", SourceID: holder}},
		}
		articles := &fakeArticleRepo{}
		uc := usecase.NewProcessCycle(
			fakeFetcher{articles: []article.Article{{
				Title: "Montgomery Burns sells sliver of Isotopes at record valuation", OriginalURL: "https://example.com/" + id.String(),
				Source: source.Source{ID: id}, ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-time.Hour),
			}}}, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
			&fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, nil, nil, cascade, fakeClock{now: now}, fakeLogger{},
			&fakeSourcePolicyRepository{}, policies, gate, &fakeSeenItemsRepository{}, &fakeSourcePolicyCooldownRepository{}, &fakeUnknownEntityRepository{},
			nil,
		)
		uc.SetRecentStories(recent)
		result, err := uc.Run(context.Background())
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		return result, articles.lastSaved()
	}

	// A reworded story from an ordinary source is a republication: denied.
	result, saved := run(mustID(t, source.TypeTwitter, "somebody"), "twitter:other")
	if result.Accepted != 0 || saved.StatusReason != "cluster_gate_denied_duplicate" {
		t.Errorf("ordinary related_coverage: result %+v reason %q, want denied by the cluster gate", result, saved.StatusReason)
	}
	// The first official voice on a cluster others already covered still publishes.
	if result, _ := run(official, "twitter:other"); result.Accepted != 1 {
		t.Errorf("first official voice: result %+v, want accepted", result)
	}
	// The same story mirrored to another official platform does not.
	if result, _ := run(mirror, official.String()); result.Accepted != 0 {
		t.Errorf("official mirror: result %+v, want denied", result)
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

func TestProcessCycle_LexicalJoinPutsRewrittenStoryInTheExistingCluster(t *testing.T) {
	now := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	cascade := defaultCascade()
	cascade.LexicalJoin = storyguard.Policy{Enabled: true, Window: 48 * time.Hour, MinSharedTokens: 4, OverlapThreshold: 0.65}
	recent := fakeRecentStories{stories: []out.RecentStory{
		{ArticleID: "a", ClusterID: "cluster-ross", Title: "Montgomery Burns sells 2.1% of the Isotopes at record valuation"},
		{ArticleID: "b", ClusterID: "cluster-aikman", Title: "Kent Brockman no longer officially working with Isotopes"},
	}}
	run := func(title string, join bool) (article.Article, in.ProcessCycleResult) {
		c := cascade
		if !join {
			c.LexicalJoin.Enabled = false
		}
		articles := &fakeArticleRepo{}
		uc := newRulesTestCycle(fakeFetcher{articles: []article.Article{{
			Title: title, OriginalURL: "https://example.com/" + title[:6],
			ContentType: article.ContentTypeArticle, PublishedAt: now.Add(-time.Hour),
		}}}, articles, &fakeDecisionRepo{}, now, dictionary.Matcher{}, c)
		uc.SetRecentStories(recent)
		result, err := uc.Run(context.Background())
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		return articles.lastSaved(), result
	}

	saved, result := run("Montgomery Burns sells sliver of Isotopes at record valuation", true)
	if saved.ClusterID != "cluster-ross" || saved.ClusterMemberRole != string(clustering.MemberRoleRelatedCoverage) {
		t.Errorf("cluster = %q role = %q, want cluster-ross/related_coverage", saved.ClusterID, saved.ClusterMemberRole)
	}
	if result.Accepted != 1 {
		t.Errorf("result = %+v, want the second point of view accepted (the story guard, not the cluster gate, bounds repeats)", result)
	}
	if saved, _ := run("Montgomery Burns sells sliver of Isotopes at record valuation", false); saved.ClusterID == "cluster-ross" {
		t.Error("lexical join disabled: the article must not join the cluster")
	}
	if saved, _ := run("Bart Simpson ruled out for Sunday", true); saved.ClusterID == "cluster-ross" || saved.ClusterID == "cluster-aikman" {
		t.Errorf("unrelated article joined %q", saved.ClusterID)
	}
}
