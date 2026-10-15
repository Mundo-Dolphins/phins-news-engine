// Package rss fetches configured RSS/Atom feeds using rssclient, applying
// v1's blocked/required keyword and blocked-author filters via
// sourcefilter. If configured, it can retry a failed direct feed fetch via
// the Scraping Service so 403-prone domains don't disappear entirely.
package rss

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sourcefilter"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/netguard"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/mmcdole/gofeed"
)

// FeedConfig is one configured RSS/Atom feed.
type FeedConfig struct {
	Name             string
	URL              string
	SourceType       string
	MaxAge           time.Duration
	BlockedKeywords  []string
	RequiredKeywords []string
	BlockedAuthors   []string
}

// Fetcher fetches every configured feed and returns their articles
// combined. It does not implement out.Fetcher directly — compositefetcher
// wraps it.
type Fetcher struct {
	feeds     []FeedConfig
	clientCfg rssclient.Config
	scraper   appout.Scraper
	logger    appout.Logger
}

// NewFetcher builds a ready Fetcher.
func NewFetcher(feeds []FeedConfig, clientCfg rssclient.Config, logger appout.Logger) *Fetcher {
	return NewFetcherWithScraper(feeds, clientCfg, nil, logger)
}

// NewFetcherWithScraper builds a Fetcher that falls back to scraper when
// the direct RSS request fails.
func NewFetcherWithScraper(feeds []FeedConfig, clientCfg rssclient.Config, scraper appout.Scraper, logger appout.Logger) *Fetcher {
	return &Fetcher{feeds: feeds, clientCfg: clientCfg, scraper: scraper, logger: logger}
}

// Fetch fetches every configured feed. A single feed's failure is logged
// and skipped — it never aborts the others.
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
			f.logger.Warn("rss: feed fetch failed", "feed", feed.Name, "err", err.Error())
			continue
		}
		f.logger.Info("rss: articles fetched", "feed", feed.Name, "count", len(items))
		all = append(all, items...)
	}
	return all, nil
}

func (f *Fetcher) fetchFeed(ctx context.Context, feed FeedConfig) ([]article.Article, error) {
	id, err := source.NewID(source.TypeRSS, feed.Name)
	if err != nil {
		return nil, fmt.Errorf("rss: invalid feed name %q: %w", feed.Name, err)
	}
	parsed, err := rssclient.Fetch(ctx, f.clientCfg, feed.URL)
	if err != nil {
		// A URL refused by the host policy is never handed to the
		// scraper: that would only move the request elsewhere.
		if f.scraper == nil || errors.Is(err, netguard.ErrDisallowedURL) {
			return nil, err
		}
		directErr := err
		f.logger.Warn("rss: direct feed fetch failed; trying scraping service", "feed", feed.Name, "err", directErr.Error())
		parsed, err = f.fetchFeedViaScraper(ctx, feed)
		if err != nil {
			return nil, fmt.Errorf("direct fetch failed (%v); scraping service fallback failed: %w", directErr, err)
		}
		f.logger.Info("rss: scraping service fallback succeeded", "feed", feed.Name)
	}

	now := time.Now().UTC()
	seen := make(map[string]struct{})
	var out []article.Article

	for _, item := range parsed.Items {
		if item.Link == "" && item.GUID == "" {
			continue
		}

		var authorName string
		if item.Author != nil {
			authorName = item.Author.Name
		}
		if sourcefilter.IsSkippedAuthor(authorName, feed.BlockedAuthors) {
			continue
		}
		if sourcefilter.ContainsBlockedKeyword([]string{item.Title, item.Description}, feed.BlockedKeywords) {
			continue
		}
		if !sourcefilter.ContainsRequiredKeyword([]string{item.Title, item.Description}, feed.RequiredKeywords) {
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

		out = append(out, article.Article{
			ExternalID:  externalID,
			OriginalURL: originalURL,
			Domain:      hostOf(originalURL),
			Source: source.Source{
				ID:     id,
				Name:   feed.Name,
				Type:   source.TypeRSS,
				Domain: hostOf(feed.URL),
				URL:    feed.URL,
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

func (f *Fetcher) fetchFeedViaScraper(ctx context.Context, feed FeedConfig) (*gofeed.Feed, error) {
	result, err := f.scraper.Scrape(ctx, appout.ScrapeRequest{URL: feed.URL, Mode: appout.ScrapeModeAuto})
	if err != nil {
		return nil, fmt.Errorf("scrape %s: %w", netguard.RedactURL(feed.URL), netguard.RedactError(err))
	}
	return rssclient.ParseBytes([]byte(result.HTML), f.clientCfg.ResponseLimitBytes)
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
