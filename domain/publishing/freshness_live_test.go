package publishing

import (
	"regexp"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

func TestMaxAgeForLiveUpdates(t *testing.T) {
	policy := FreshnessPolicy{
		HardNewsHours: 72, SocialHours: 3, AnalysisHours: 168, DefaultHours: 72,
		LiveUpdates: LiveUpdatePolicy{MaxAge: 90 * time.Minute, Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)^\W*(?:touchdown|half-?time)\b`),
			regexp.MustCompile(`(?-i:\b[A-Z][a-z]+ \d{1,2}-\d{1,2} (?:Miami )?Dolphins\b)`),
		}, Exclude: []*regexp.Regexp{regexp.MustCompile(`(?i)\bfinal\b|\bbeat\b|\btake down\b`)}},
	}
	social := func(title string) article.Article {
		return article.Article{Title: title, ContentType: article.ContentTypeSocialPost}
	}
	tests := []struct {
		name string
		a    article.Article
		want time.Duration
	}{
		{"running score", social("Cincinnati Bengals 21-7 Miami Dolphins #PhinsUp"), 90 * time.Minute},
		{"halftime", social("Halftime in Minnesota"), 90 * time.Minute},
		{"final score is finished-game news", social("FINAL: Cincinnati Bengals 31-24 Miami Dolphins"), 3 * time.Hour},
		{"recap with score is finished-game news", social("Bengals beat Miami Dolphins 31-24"), 3 * time.Hour},
		{"record is not a score", social("the 0-3 Miami Dolphins meet the 3-0 Vikings"), 3 * time.Hour},
		{"final injury report", social("Final injury report of the week"), 3 * time.Hour},
		{"article never shortened", article.Article{Title: "Touchdown Dolphins", ContentType: article.ContentTypeArticle}, 72 * time.Hour},
	}
	for _, tc := range tests {
		if got := policy.MaxAgeFor(tc.a); got != tc.want {
			t.Errorf("%s: MaxAgeFor = %v, want %v", tc.name, got, tc.want)
		}
	}
	now := time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC)
	stale := social("Touchdown Dolphins")
	stale.PublishedAt = now.Add(-2 * time.Hour)
	if Decide(stale, policy, now) {
		t.Error("a two-hour-old live update must be stale")
	}
}
