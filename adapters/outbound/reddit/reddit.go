// Package reddit fetches subreddit RSS feeds from Reddit's public .rss
// endpoints (no authentication). Ported from v1's reddit/fetcher.go.
package reddit

import (
	"context"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sourcefilter"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const defaultBaseURL = "https://www.reddit.com"

// FeedConfig is one subreddit's public .rss feed.
type FeedConfig struct {
	Subreddit  string
	Mode       string
	SourceType string
	MaxAge     time.Duration
}

// Fetcher fetches every configured subreddit feed. BaseURL defaults to
// Reddit's real host; tests override it to point at an httptest.Server.
type Fetcher struct {
	BaseURL   string
	feeds     []FeedConfig
	clientCfg rssclient.Config
	logger    appout.Logger
}

// NewFetcher builds a ready Fetcher pointed at the real reddit.com.
func NewFetcher(feeds []FeedConfig, clientCfg rssclient.Config, logger appout.Logger) *Fetcher {
	return &Fetcher{BaseURL: defaultBaseURL, feeds: feeds, clientCfg: clientCfg, logger: logger}
}

// Fetch fetches every configured subreddit. A single subreddit's failure is
// logged and skipped — it never aborts the others.
func (f *Fetcher) Fetch(ctx context.Context) ([]article.Article, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var all []article.Article
	for _, feed := range f.feeds {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := f.fetchFeed(ctx, feed)
		if err != nil {
			f.logger.Warn("reddit: feed fetch failed", "subreddit", feed.Subreddit, "err", err.Error())
			continue
		}
		f.logger.Info("reddit: articles fetched", "subreddit", feed.Subreddit, "count", len(items))
		all = append(all, items...)
	}
	return all, nil
}

func (f *Fetcher) fetchFeed(ctx context.Context, feed FeedConfig) ([]article.Article, error) {
	id, err := source.NewID(source.TypeReddit, "reddit-"+feed.Subreddit)
	if err != nil {
		return nil, fmt.Errorf("reddit: invalid subreddit %q: %w", feed.Subreddit, err)
	}
	mode := feed.Mode
	if mode == "" {
		mode = "new"
	}
	feedURL := fmt.Sprintf("%s/r/%s/%s/.rss", f.BaseURL, feed.Subreddit, mode)

	parsed, err := rssclient.Fetch(ctx, f.clientCfg, feedURL)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	seen := make(map[string]struct{})
	var out []article.Article

	for _, item := range parsed.Items {
		if item.Link == "" && item.GUID == "" {
			continue
		}

		var publishedAt time.Time
		if item.PublishedParsed != nil {
			publishedAt = item.PublishedParsed.UTC()
		} else if item.UpdatedParsed != nil {
			publishedAt = item.UpdatedParsed.UTC()
		}
		if !publishedAt.IsZero() && feed.MaxAge > 0 && now.Sub(publishedAt) > feed.MaxAge {
			continue
		}

		externalID := item.GUID
		if externalID == "" {
			externalID = item.Link
		}
		if _, ok := seen[externalID]; ok {
			continue
		}
		seen[externalID] = struct{}{}

		if publishedAt.IsZero() {
			publishedAt = now
		}

		originalURL := item.Link
		if originalURL == "" {
			originalURL = item.GUID
		}

		var authorName string
		if item.Author != nil {
			authorName = item.Author.Name
		}

		out = append(out, article.Article{
			ExternalID:  externalID,
			OriginalURL: originalURL,
			Domain:      "reddit.com",
			Source: source.Source{
				ID:     id,
				Name:   "r/" + feed.Subreddit,
				Type:   source.TypeReddit,
				Domain: "reddit.com",
				URL:    fmt.Sprintf("https://www.reddit.com/r/%s/", feed.Subreddit),
			},
			Title:       item.Title,
			Summary:     normalization.StripHTML(item.Description),
			Author:      authorName,
			PublishedAt: publishedAt,
			FetchedAt:   now,
			ContentType: sourcefilter.ContentTypeFor(feed.SourceType),
			Status:      article.StatusFetched,
		})
	}
	return out, nil
}
