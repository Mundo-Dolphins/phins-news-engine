package publishing_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

var policy = publishing.FreshnessPolicy{
	HardNewsHours: 72,
	SocialHours:   24,
	AnalysisHours: 168,
	DefaultHours:  72,
}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		publishedAt time.Time
		contentType article.ContentType
		want        bool
	}{
		{name: "fresh hard news is accepted", publishedAt: now.Add(-10 * time.Hour), contentType: article.ContentTypeArticle, want: true},
		{name: "stale hard news is rejected", publishedAt: now.Add(-100 * time.Hour), contentType: article.ContentTypeArticle, want: false},
		{name: "social post older than 24h is stale", publishedAt: now.Add(-25 * time.Hour), contentType: article.ContentTypeSocialPost, want: false},
		{name: "social post within 24h is accepted", publishedAt: now.Add(-1 * time.Hour), contentType: article.ContentTypeSocialPost, want: true},
		{name: "video uses the longer analysis window", publishedAt: now.Add(-150 * time.Hour), contentType: article.ContentTypeVideo, want: true},
		{name: "unknown content type uses default window", publishedAt: now.Add(-80 * time.Hour), contentType: article.ContentTypeUnknown, want: false},
		{name: "future published_at is treated as fresh", publishedAt: now.Add(1 * time.Hour), contentType: article.ContentTypeArticle, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := article.Article{PublishedAt: tt.publishedAt, ContentType: tt.contentType}
			got := publishing.Decide(a, policy, now)
			if got != tt.want {
				t.Errorf("Decide() = %v, want %v", got, tt.want)
			}
		})
	}
}
