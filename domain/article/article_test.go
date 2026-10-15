package article_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

func TestIsReprocessable(t *testing.T) {
	tests := []struct {
		status article.Status
		want   bool
	}{
		{article.StatusRejectedValidation, true},
		{article.StatusRejectedDuplicate, true},
		{article.StatusRejectedNotRelated, true},
		{article.StatusRejectedQuality, true},
		{article.StatusRejectedStale, true},
		{article.StatusDiscarded, true},
		{article.StatusPendingPublication, false},
		{article.StatusPublishing, false},
		{article.StatusPublished, false},
		{article.StatusPublishFailed, false},
		{article.StatusPendingRetry, false},
		{article.StatusRetainedNoPublish, false},
		{article.StatusFetched, false},
		{article.StatusNormalized, false},
		{article.StatusClusterCandidate, false},
		{article.StatusClustered, false},
		{article.Status(""), false},
	}
	for _, tt := range tests {
		if got := article.IsReprocessable(tt.status); got != tt.want {
			t.Errorf("IsReprocessable(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   article.Article
		want article.Article
	}{
		{
			name: "trims whitespace",
			in: article.Article{
				Title:       "  Ezo throws four TDs  ",
				Summary:     "  great game  ",
				Author:      " Beat Writer ",
				OriginalURL: "https://example.com/story",
			},
			want: article.Article{
				Title:        "Ezo throws four TDs",
				Summary:      "great game",
				Author:       "Beat Writer",
				OriginalURL:  "https://example.com/story",
				CanonicalURL: "https://example.com/story",
			},
		},
		{
			name: "fills canonical url from original when missing",
			in: article.Article{
				OriginalURL: "https://example.com/story/",
			},
			want: article.Article{
				OriginalURL:  "https://example.com/story/",
				CanonicalURL: "https://example.com/story/",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := article.Normalize(tt.in)
			if got.Title != tt.want.Title {
				t.Errorf("Title = %q, want %q", got.Title, tt.want.Title)
			}
			if got.Summary != tt.want.Summary {
				t.Errorf("Summary = %q, want %q", got.Summary, tt.want.Summary)
			}
			if got.Author != tt.want.Author {
				t.Errorf("Author = %q, want %q", got.Author, tt.want.Author)
			}
			if got.CanonicalURL != tt.want.CanonicalURL {
				t.Errorf("CanonicalURL = %q, want %q", got.CanonicalURL, tt.want.CanonicalURL)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		in      article.Article
		wantErr bool
	}{
		{
			name:    "valid article passes",
			in:      article.Article{CanonicalURL: "https://example.com/story", PublishedAt: valid},
			wantErr: false,
		},
		{
			name:    "missing canonical url fails",
			in:      article.Article{PublishedAt: valid},
			wantErr: true,
		},
		{
			name:    "missing published_at fails",
			in:      article.Article{CanonicalURL: "https://example.com/story"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := article.Validate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStatusRetainedNoPublish_IsDistinctFromRejectedDuplicate(t *testing.T) {
	if article.StatusRetainedNoPublish == article.StatusRejectedDuplicate {
		t.Fatal("StatusRetainedNoPublish must be distinct from StatusRejectedDuplicate")
	}
	if article.StatusRetainedNoPublish != "retained_no_publish" {
		t.Errorf("StatusRetainedNoPublish = %q, want %q", article.StatusRetainedNoPublish, "retained_no_publish")
	}
}

func TestNormalize_LeavesBodyUntouched(t *testing.T) {
	a := article.Article{Title: "  T  ", Body: "  raw body text  "}
	got := article.Normalize(a)
	if got.Body != "  raw body text  " {
		t.Errorf("Body = %q, want unchanged (extraction is responsible for its own trimming)", got.Body)
	}
}
