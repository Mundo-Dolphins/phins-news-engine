// Package youtube fetches YouTube channel uploads via a local RSS bridge
// (never the Scraping Service). Ported from v1's
// youtube/fetcher.go — the simplest of the six new fetchers.
package youtube

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sourcefilter"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// AccountConfig is one YouTube channel fetched via the RSS bridge's
// per-channel endpoint.
type AccountConfig struct {
	Name       string
	ChannelID  string
	SourceType string
	MaxAge     time.Duration
}

// Fetcher fetches every configured YouTube channel.
type Fetcher struct {
	baseURL   string
	accounts  []AccountConfig
	clientCfg rssclient.Config
	logger    appout.Logger
}

// NewFetcher builds a ready Fetcher.
func NewFetcher(baseURL string, accounts []AccountConfig, clientCfg rssclient.Config, logger appout.Logger) *Fetcher {
	return &Fetcher{baseURL: strings.TrimRight(baseURL, "/"), accounts: accounts, clientCfg: clientCfg, logger: logger}
}

// Fetch fetches every configured channel. A single channel's failure is
// logged and skipped — it never aborts the others.
func (f *Fetcher) Fetch(ctx context.Context) ([]article.Article, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var all []article.Article
	for _, acc := range f.accounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := f.fetchChannel(ctx, acc)
		if err != nil {
			f.logger.Warn("youtube: channel fetch failed", "channel", acc.ChannelID, "err", err.Error())
			continue
		}
		f.logger.Info("youtube: articles fetched", "channel", acc.ChannelID, "count", len(items))
		all = append(all, items...)
	}
	return all, nil
}

func (f *Fetcher) fetchChannel(ctx context.Context, acc AccountConfig) ([]article.Article, error) {
	id, err := source.NewID(source.TypeYouTube, acc.ChannelID)
	if err != nil {
		return nil, fmt.Errorf("youtube: invalid channel_id %q: %w", acc.ChannelID, err)
	}
	feedURL := fmt.Sprintf("%s/channel/%s", f.baseURL, acc.ChannelID)
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
		if !publishedAt.IsZero() && acc.MaxAge > 0 && now.Sub(publishedAt) > acc.MaxAge {
			continue
		}
		if sourcefilter.IsBridgeErrorItem(item.Title, item.Description) {
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

		name := acc.Name
		if name == "" {
			name = acc.ChannelID
		}
		originalURL := item.Link
		if originalURL == "" {
			originalURL = item.GUID
		}

		out = append(out, article.Article{
			ExternalID:  externalID,
			OriginalURL: firstHTTPURL(originalURL),
			Domain:      "youtube.com",
			Source: source.Source{
				ID:     id,
				Name:   name,
				Type:   source.TypeYouTube,
				Domain: "youtube.com",
				URL:    "https://www.youtube.com/channel/" + acc.ChannelID,
			},
			Title:       item.Title,
			Summary:     normalization.StripHTML(item.Description),
			PublishedAt: publishedAt,
			FetchedAt:   now,
			ContentType: sourcefilter.ContentTypeFor(acc.SourceType),
			Status:      article.StatusFetched,
		})
	}
	return out, nil
}

func firstHTTPURL(value string) string {
	if isHTTPURL(value) {
		return value
	}
	return ""
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return parsed.Host != ""
}
