// Package reddit fetches subreddit RSS feeds from Reddit's public .rss
// endpoints (no authentication). Ported from v1's reddit/fetcher.go.
//
// An image post's picture is exposed as article.MetadataMediaImages when its
// "[link]" target is an https URL on an allowed image host (default
// i.redd.it) with a .jpg, .jpeg or .png extension.
package reddit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sourcefilter"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/normalization"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/rssclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const defaultBaseURL = "https://www.reddit.com"

// defaultImageHosts are the hosts whose image links become media metadata.
var defaultImageHosts = []string{"i.redd.it"}

// imageTypes maps the accepted image extensions to their MIME type.
var imageTypes = map[string]string{".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png"}

// postLinkRe finds the target of the "[link]" anchor in a post's content.
var postLinkRe = regexp.MustCompile(`<a href="([^"]+)">\[link\]</a>`)

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
	BaseURL    string
	feeds      []FeedConfig
	clientCfg  rssclient.Config
	logger     appout.Logger
	imageHosts []string
}

// NewFetcher builds a ready Fetcher pointed at the real reddit.com.
func NewFetcher(feeds []FeedConfig, clientCfg rssclient.Config, logger appout.Logger) *Fetcher {
	return &Fetcher{BaseURL: defaultBaseURL, feeds: feeds, clientCfg: clientCfg, logger: logger, imageHosts: defaultImageHosts}
}

// WithImageHosts replaces the hosts whose image links become media metadata.
func (f *Fetcher) WithImageHosts(hosts ...string) *Fetcher {
	f.imageHosts = hosts
	return f
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
			Metadata:    postImages(item.Content, f.imageHosts),
			Status:      article.StatusFetched,
		})
	}
	return out, nil
}

// postImages returns the metadata for the post's linked image, or nil when
// the post links no accepted image (articles keep a nil Metadata).
func postImages(content string, hosts []string) map[string]string {
	m := postLinkRe.FindStringSubmatch(content)
	if m == nil {
		return nil
	}
	u, err := url.Parse(m[1])
	if err != nil || u.Scheme != "https" || !hostAllowed(u.Hostname(), hosts) {
		return nil
	}
	typ, ok := imageTypes[strings.ToLower(path.Ext(u.Path))]
	if !ok {
		return nil
	}
	b, err := json.Marshal([]struct {
		URL  string `json:"url"`
		Type string `json:"type"`
	}{{URL: u.String(), Type: typ}})
	if err != nil {
		return nil
	}
	return map[string]string{article.MetadataMediaImages: string(b)}
}

func hostAllowed(host string, hosts []string) bool {
	for _, h := range hosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}
