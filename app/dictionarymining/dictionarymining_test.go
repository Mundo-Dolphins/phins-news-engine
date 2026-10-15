package dictionarymining_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionarymining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func testTextConfig() normalization.TextConfig {
	return normalization.TextConfig{
		StripEmojis: true, StripHashtags: true, StripMentions: true, StripInlineURLs: true,
		ShingleSize: 3, MinTokenLength: 3,
		StopWords: []string{"the", "and", "with", "for", "from", "that", "this"},
	}
}

func emptyMatcher(t *testing.T) dictionary.Matcher {
	t.Helper()
	m, err := dictionary.Prepare(dictionary.Dictionary{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	return m
}

func TestExtractCandidates_FindsCapitalizedPhraseAsPersonGuess(t *testing.T) {
	articles := []article.Article{
		{ID: "a1", Title: "Ezo Quarrington throws for 300 yards", Summary: "The quarterback had a big game."},
	}
	got := dictionarymining.ExtractCandidates(articles, testTextConfig(), emptyMatcher(t))

	found := false
	for _, c := range got {
		if c.Term == "Ezo Quarrington" {
			found = true
			if c.CategoryGuess != "nfl_people_context" {
				t.Errorf("CategoryGuess = %q, want %q", c.CategoryGuess, "nfl_people_context")
			}
			if c.FirstSeenArticleID != "a1" {
				t.Errorf("FirstSeenArticleID = %q, want %q", c.FirstSeenArticleID, "a1")
			}
		}
	}
	if !found {
		t.Errorf("ExtractCandidates() = %+v, want a candidate for %q", got, "Ezo Quarrington")
	}
}

func TestExtractCandidates_CountsSupportAcrossArticles(t *testing.T) {
	articles := []article.Article{
		{ID: "a1", Title: "Training camp update", Summary: ""},
		{ID: "a2", Title: "Another training camp report", Summary: ""},
	}
	got := dictionarymining.ExtractCandidates(articles, testTextConfig(), emptyMatcher(t))

	for _, c := range got {
		if c.Term == "training camp" {
			if c.SupportCount != 2 {
				t.Errorf("SupportCount for %q = %d, want 2", c.Term, c.SupportCount)
			}
			return
		}
	}
	t.Errorf("ExtractCandidates() = %+v, want a %q candidate with support 2", got, "training camp")
}

func TestExtractCandidates_ExcludesTermsAlreadyInDictionary(t *testing.T) {
	dict := dictionary.Dictionary{
		Categories: []dictionary.Category{
			{Name: "official_names", Weight: 10, Entries: []dictionary.Entry{{CanonicalName: "Isotopes", Type: dictionary.EntryTypeOfficialName}}},
		},
	}
	matcher, err := dictionary.Prepare(dict)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	articles := []article.Article{{ID: "a1", Title: "Isotopes win big", Summary: ""}}

	got := dictionarymining.ExtractCandidates(articles, testTextConfig(), matcher)
	for _, c := range got {
		if c.Term == "Isotopes" || c.Term == "isotopes" {
			t.Errorf("ExtractCandidates() = %+v, want %q excluded (already in dictionary)", got, c.Term)
		}
	}
}

func TestExtractCandidates_ExcludesCapitalizedPhraseAlreadyInDictionary(t *testing.T) {
	dict := dictionary.Dictionary{
		Categories: []dictionary.Category{
			{Name: "person", Weight: 1, Entries: []dictionary.Entry{{CanonicalName: "Ezo Quarrington", Type: dictionary.EntryTypePerson}}},
		},
	}
	matcher, err := dictionary.Prepare(dict)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	articles := []article.Article{
		{ID: "a1", Title: "Ezo Quarrington throws for 300 yards", Summary: ""},
	}

	got := dictionarymining.ExtractCandidates(articles, testTextConfig(), matcher)
	for _, c := range got {
		if c.Term == "Ezo Quarrington" {
			t.Errorf("ExtractCandidates() = %+v, want %q excluded (already in dictionary)", got, c.Term)
		}
	}
}

func TestExtractCandidates_GenericTokensHaveNoCategoryGuess(t *testing.T) {
	articles := []article.Article{{ID: "a1", Title: "Training camp update begins", Summary: ""}}
	got := dictionarymining.ExtractCandidates(articles, testTextConfig(), emptyMatcher(t))

	for _, c := range got {
		if c.Term == "training" && c.CategoryGuess != "" {
			t.Errorf("CategoryGuess for generic token %q = %q, want empty", c.Term, c.CategoryGuess)
		}
	}
}

func TestExtractCandidatesWithFilter_RemovesConfiguredNoise(t *testing.T) {
	articles := []article.Article{{
		ID:      "a1",
		Title:   "Press Conference September 11am",
		Summary: "Lakeside Health Subscribe ahead of explosive play highlights",
	}}
	filter := dictionarymining.FilterConfig{
		IgnoredTerms: []string{
			"press conference", "september", "ahead", "explosive", "explosive play", "highlights",
		},
		IgnoredSubstrings: []string{"subscribe", "lakeside health"},
	}

	got := dictionarymining.ExtractCandidatesWithFilter(articles, testTextConfig(), emptyMatcher(t), filter)
	for _, c := range got {
		switch c.Term {
		case "Press Conference", "september", "11am", "Lakeside Health Subscribe", "ahead", "explosive", "explosive play", "highlights":
			t.Errorf("ExtractCandidatesWithFilter() kept noisy candidate %+v", c)
		}
	}
}

func TestExtractCandidatesWithFilter_KeepsUnignoredSingleTokenName(t *testing.T) {
	articles := []article.Article{{ID: "a1", Title: "Slowik joins the staff", Summary: ""}}
	got := dictionarymining.ExtractCandidatesWithFilter(articles, testTextConfig(), emptyMatcher(t), dictionarymining.FilterConfig{
		IgnoredTerms: []string{"staff"},
	})

	for _, c := range got {
		if c.Term == "slowik" {
			return
		}
	}
	t.Errorf("ExtractCandidatesWithFilter() = %+v, want slowik to remain reviewable", got)
}

func TestExtractCandidatesWithFilter_RemovesIgnoredTokenFamilies(t *testing.T) {
	articles := []article.Article{{ID: "a1", Title: "Week preview appeared first on IsotopesTalk com", Summary: ""}}
	got := dictionarymining.ExtractCandidatesWithFilter(articles, testTextConfig(), emptyMatcher(t), dictionarymining.FilterConfig{
		IgnoredTokens: []string{"appeared", "com", "isotopestalk", "first", "preview", "week"},
	})

	for _, c := range got {
		t.Errorf("ExtractCandidatesWithFilter() kept token-family noise %+v", c)
	}
}

func TestExtractCandidatesWithFilter_RemovesAllCapsPhrasesAndNumericTerms(t *testing.T) {
	articles := []article.Article{{ID: "a1", Title: "PREVIEW SHOW for 2026 regular season", Summary: ""}}
	got := dictionarymining.ExtractCandidatesWithFilter(articles, testTextConfig(), emptyMatcher(t), dictionarymining.FilterConfig{})

	for _, c := range got {
		switch c.Term {
		case "PREVIEW SHOW", "2026", "2026 regular":
			t.Errorf("ExtractCandidatesWithFilter() kept structural noise %+v", c)
		}
	}
}

func TestExtractCandidatesWithFilter_AppliesCategoryGuessOverride(t *testing.T) {
	articles := []article.Article{{ID: "a1", Title: "Brockway Badgers visit Springfield", Summary: ""}}
	got := dictionarymining.ExtractCandidatesWithFilter(articles, testTextConfig(), emptyMatcher(t), dictionarymining.FilterConfig{
		CategoryGuesses: map[string]string{"brockway badgers": "rivals"},
	})

	for _, c := range got {
		if c.Term == "Brockway Badgers" {
			if c.CategoryGuess != "rivals" {
				t.Errorf("CategoryGuess = %q, want rivals", c.CategoryGuess)
			}
			return
		}
	}
	t.Errorf("ExtractCandidatesWithFilter() = %+v, want Brockway Badgers candidate", got)
}

type fakeReviewDecisions struct {
	decisions []out.ReviewDecision
}

func (f *fakeReviewDecisions) Save(context.Context, out.ReviewDecision) error { return nil }
func (f *fakeReviewDecisions) ListBySession(_ context.Context, sessionID string) ([]out.ReviewDecision, error) {
	var got []out.ReviewDecision
	for _, d := range f.decisions {
		if d.SessionID == sessionID {
			got = append(got, d)
		}
	}
	return got, nil
}

func (f *fakeReviewDecisions) ListRecentByAction(context.Context, string, time.Time) ([]string, error) {
	return nil, nil
}

type fakeArticles struct {
	byID map[string]article.Article
}

func (f *fakeArticles) Save(context.Context, article.Article) error { return nil }
func (f *fakeArticles) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f.byID[id]
	if !ok {
		return article.Article{}, fmt.Errorf("not found: %w", sql.ErrNoRows)
	}
	return a, nil
}

func (f *fakeArticles) DeleteByStatusOlderThan(context.Context, []article.Status, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeArticles) Count(context.Context) (int, error) { return 0, nil }
func (f *fakeArticles) ListCandidatesForReview(context.Context, time.Time, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticles) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticles) FindByCanonicalURL(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

func TestSelect_OnlyMinesPublishLabeledArticles(t *testing.T) {
	deps := dictionarymining.Deps{
		ReviewDecisions: &fakeReviewDecisions{decisions: []out.ReviewDecision{
			{SessionID: "sess-1", ArticleID: "published", Label: "publish"},
			{SessionID: "sess-1", ArticleID: "rejected", Label: "reject"},
		}},
		Articles: &fakeArticles{byID: map[string]article.Article{
			"published": {ID: "published", Title: "Training camp begins", Summary: ""},
			"rejected":  {ID: "rejected", Title: "Sponsored content here", Summary: ""},
		}},
	}

	got, err := dictionarymining.Select(context.Background(), deps, "sess-1", emptyMatcher(t), testTextConfig(), dictionarymining.FilterConfig{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	for _, c := range got {
		if c.FirstSeenArticleID == "rejected" {
			t.Errorf("Select() included a candidate first seen in a rejected article: %+v", c)
		}
	}
}

type erroringReviewDecisions struct{}

func (erroringReviewDecisions) Save(context.Context, out.ReviewDecision) error { return nil }
func (erroringReviewDecisions) ListBySession(context.Context, string) ([]out.ReviewDecision, error) {
	return nil, fmt.Errorf("boom")
}

func (erroringReviewDecisions) ListRecentByAction(context.Context, string, time.Time) ([]string, error) {
	return nil, nil
}

func TestSelect_PropagatesReviewDecisionListError(t *testing.T) {
	deps := dictionarymining.Deps{
		ReviewDecisions: erroringReviewDecisions{},
		Articles:        &fakeArticles{byID: map[string]article.Article{}},
	}

	_, err := dictionarymining.Select(context.Background(), deps, "sess-1", emptyMatcher(t), testTextConfig(), dictionarymining.FilterConfig{})
	if err == nil {
		t.Fatal("Select() error = nil, want non-nil when ListBySession fails")
	}
}

func TestSelect_SkipsOrphanedArticleReference(t *testing.T) {
	deps := dictionarymining.Deps{
		ReviewDecisions: &fakeReviewDecisions{decisions: []out.ReviewDecision{
			{SessionID: "sess-1", ArticleID: "missing", Label: "publish"},
		}},
		Articles: &fakeArticles{byID: map[string]article.Article{}},
	}

	got, err := dictionarymining.Select(context.Background(), deps, "sess-1", emptyMatcher(t), testTextConfig(), dictionarymining.FilterConfig{})
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Select() = %+v, want empty when the only referenced article is missing", got)
	}
}

func TestExtractCandidates_PreservesNamePunctuation(t *testing.T) {
	for _, name := range []string{"Da'Rel Ashcombe", "Da’Rel Ashcombe", "D'Ambry Thistlecombe", "Jean-Pierre Martin", "Jean‑Pierre Martin", "Elan-Ra St Quillon", "Western A&M", "Procter & Gamble"} {
		t.Run(name, func(t *testing.T) {
			want := strings.NewReplacer("’", "'", "‑", "-").Replace(name)
			got := dictionarymining.ExtractCandidates([]article.Article{{ID: "a1", Title: name + " returns"}}, testTextConfig(), emptyMatcher(t))
			found := false
			for _, c := range got {
				if c.Term == want && c.CategoryGuess == "nfl_people_context" {
					found = true
				}
				switch c.Term {
				case "rel", "rel ashcombe", "Rel Ashcombe", "ambry", "pierre", "elan", "ra":
					t.Errorf("split name into %+v", c)
				}
			}
			if !found {
				t.Errorf("missing intact name %q in %+v", want, got)
			}
		})
	}
}

func TestExtractCandidatesWithFilter_ExcludesCatalogMentionsAndFragments(t *testing.T) {
	filter := dictionarymining.FilterConfig{KnownEntityTerms: []string{
		"Da'Rel Ashcombe", "Ashcombe", "Capital City Capitals", "Capitals", "Western A&M", "Jean-Pierre Martin", "Ezo Quarrington",
	}}
	articles := []article.Article{{ID: "a1", Title: "training Da’Rel Ashcombe returns", Summary: "Capital City Capitals visit Western A&amp;M. Jean-Pierre Martin meets Ezo Quarrington. ashcombe returns"}}
	got := dictionarymining.ExtractCandidatesWithFilter(articles, testTextConfig(), emptyMatcher(t), filter)
	for _, c := range got {
		key := strings.ToLower(c.Term)
		for _, fragment := range []string{"ashcombe", "rel", "kansas", "city", "capitals", "texas", "a&m", "jean", "pierre", "martin", "ezo", "quarrington"} {
			if strings.Contains(key, fragment) {
				t.Errorf("catalog mention leaked into %+v", c)
			}
		}
		if key == "training returns" {
			t.Errorf("bigram bridged removed entity: %+v", c)
		}
	}
	found := false
	for _, c := range got {
		if c.Term == "returns" {
			found = true
		}
	}
	if !found {
		t.Errorf("lost surrounding non-entity text: %+v", got)
	}
}

func TestExtractCandidatesWithFilter_CatalogTermsHaveWordBoundaries(t *testing.T) {
	got := dictionarymining.ExtractCandidatesWithFilter([]article.Article{{Title: "Browns browning Brown"}}, testTextConfig(), emptyMatcher(t), dictionarymining.FilterConfig{KnownEntityTerms: []string{"Brown"}})
	terms := make(map[string]bool)
	for _, c := range got {
		terms[c.Term] = true
	}
	if !terms["browns"] || !terms["browning"] || terms["brown"] {
		t.Errorf("incorrect catalog boundaries: %+v", got)
	}
}
