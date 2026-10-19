package bluesky

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sourcefilter"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const maxPerFeed = 50

// AccountConfig is one Bluesky account whose public author feed is
// fetched.
type AccountConfig struct {
	Handle          string
	Name            string
	SourceType      string
	MaxAge          time.Duration
	BlockedURLs     []string
	BlockedKeywords []string
	KeepKeywords    []string
	SkipAuthors     []string
}

// FeedConfig is one curated Bluesky feed.
type FeedConfig struct {
	URI             string
	Name            string
	SourceType      string
	MaxAge          time.Duration
	BlockedURLs     []string
	BlockedKeywords []string
	KeepKeywords    []string
	SkipAuthors     []string
}

// Fetcher fetches every configured account's author feed and every
// configured curated feed.
type Fetcher struct {
	accounts []AccountConfig
	feeds    []FeedConfig
	client   *Client
	logger   appout.Logger
}

// NewFetcher builds a ready Fetcher.
func NewFetcher(accounts []AccountConfig, feeds []FeedConfig, client *Client, logger appout.Logger) *Fetcher {
	return &Fetcher{accounts: accounts, feeds: feeds, client: client, logger: logger}
}

// sourceOptions is the subset of AccountConfig/FeedConfig parsePosts needs,
// unified so both call sites share one code path.
type sourceOptions struct {
	ID              source.ID
	Name            string
	SourceType      string
	MaxAge          time.Duration
	BlockedURLs     []string
	BlockedKeywords []string
	KeepKeywords    []string
	SkipAuthors     []string
}

// Fetch fetches every configured account and curated feed. A single
// account/feed's failure is logged and skipped — it never aborts the
// others.
func (f *Fetcher) Fetch(ctx context.Context) ([]article.Article, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var all []article.Article

	for _, acc := range f.accounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, err := source.NewID(source.TypeBluesky, acc.Handle)
		if err != nil {
			f.logger.Warn("bluesky: invalid account handle", "handle", acc.Handle, "err", err.Error())
			continue
		}
		posts, err := f.client.GetAuthorFeed(ctx, acc.Handle, maxPerFeed)
		if err != nil {
			f.logger.Warn("bluesky: author feed fetch failed", "handle", acc.Handle, "err", err.Error())
			continue
		}
		name := acc.Name
		if name == "" {
			name = acc.Handle
		}
		items := f.parsePosts(posts, sourceOptions{
			ID: id, Name: name, SourceType: acc.SourceType, MaxAge: acc.MaxAge,
			BlockedURLs: acc.BlockedURLs, BlockedKeywords: acc.BlockedKeywords, KeepKeywords: acc.KeepKeywords, SkipAuthors: acc.SkipAuthors,
		})
		f.logger.Info("bluesky: author feed processed", "handle", acc.Handle, "raw_count", len(posts), "kept", len(items))
		all = append(all, items...)
	}

	for _, feed := range f.feeds {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := feed.Name
		if name == "" {
			name = feed.URI
		}
		id, err := source.NewID(source.TypeBluesky, "feed-"+source.Slug(name))
		if err != nil {
			f.logger.Warn("bluesky: invalid feed name", "feed", feed.Name, "err", err.Error())
			continue
		}
		posts, err := f.client.GetFeed(ctx, feed.URI, maxPerFeed)
		if err != nil {
			f.logger.Warn("bluesky: curated feed fetch failed", "feed", feed.Name, "err", err.Error())
			continue
		}
		items := f.parsePosts(posts, sourceOptions{
			ID: id, Name: name, SourceType: feed.SourceType, MaxAge: feed.MaxAge,
			BlockedURLs: feed.BlockedURLs, BlockedKeywords: feed.BlockedKeywords, KeepKeywords: feed.KeepKeywords, SkipAuthors: feed.SkipAuthors,
		})
		f.logger.Info("bluesky: curated feed processed", "feed", feed.Name, "raw_count", len(posts), "kept", len(items))
		all = append(all, items...)
	}

	return all, nil
}

func (f *Fetcher) parsePosts(posts []feedViewPost, opts sourceOptions) []article.Article {
	now := time.Now().UTC()
	seen := make(map[string]struct{}, len(posts))
	out := make([]article.Article, 0, len(posts))
	for _, p := range posts {
		a, ok := parsePost(p, opts, now)
		if !ok {
			continue
		}
		if _, dup := seen[a.ExternalID]; dup {
			continue
		}
		seen[a.ExternalID] = struct{}{}
		out = append(out, a)
	}
	return out
}

func parsePost(p feedViewPost, opts sourceOptions, now time.Time) (article.Article, bool) {
	text := strings.TrimSpace(p.Post.Record.Text)
	if len(text) < 10 {
		return article.Article{}, false
	}

	authorHandle := p.Post.Author.Handle
	if authorHandle == "" {
		authorHandle = opts.ID.String()
	}
	authorName := p.Post.Author.DisplayName
	if authorName == "" {
		authorName = authorHandle
	}

	if sourcefilter.IsSkippedAuthor(authorHandle, opts.SkipAuthors) {
		return article.Article{}, false
	}

	createdAt, err := time.Parse(time.RFC3339, p.Post.Record.CreatedAt)
	if err != nil {
		createdAt = now
	} else {
		createdAt = createdAt.UTC()
	}
	if opts.MaxAge > 0 && now.Sub(createdAt) > opts.MaxAge {
		return article.Article{}, false
	}

	externalURL := extractExternalURL(p.Post.Record)
	postID := extractPostID(p.Post.URI)
	postURL := fmt.Sprintf("https://bsky.app/profile/%s/post/%s", authorHandle, postID)
	primaryURL := postURL
	if externalURL != "" {
		primaryURL = externalURL
	}

	candidates := []string{text, primaryURL, externalURL, postURL}
	if sourcefilter.ContainsBlockedURL(candidates, opts.BlockedURLs) {
		return article.Article{}, false
	}
	if sourcefilter.ContainsBlockedKeywordUnless(candidates, opts.BlockedKeywords, opts.KeepKeywords) {
		return article.Article{}, false
	}

	return article.Article{
		ExternalID:  p.Post.URI,
		OriginalURL: primaryURL,
		Domain:      hostOf(primaryURL),
		Source: source.Source{
			ID:     opts.ID,
			Name:   opts.Name,
			Type:   source.TypeBluesky,
			Domain: "bsky.app",
			URL:    fmt.Sprintf("https://bsky.app/profile/%s", authorHandle),
		},
		Title:       canonicalTitle(text, externalURL),
		Summary:     text,
		Author:      authorName,
		PublishedAt: createdAt,
		FetchedAt:   now,
		ContentType: sourcefilter.ContentTypeFor(opts.SourceType),
		Status:      article.StatusFetched,
	}, true
}

// extractExternalURL prefers a richtext link facet's URI, falling back to
// the record's embedded external link.
func extractExternalURL(rec postRecord) string {
	for _, ft := range rec.Facets {
		for _, feat := range ft.Features {
			if feat.Type == "app.bsky.richtext.facet#link" && feat.URI != "" {
				return feat.URI
			}
		}
	}
	if rec.Embed != nil && rec.Embed.External != nil {
		return rec.Embed.External.URI
	}
	return ""
}

// extractPostID returns the last path component of an AT URI
// (at://did:plc:xxx/app.bsky.feed.post/rkey).
func extractPostID(uri string) string {
	parts := strings.Split(uri, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return uri
}

var inlineHTTPURLRe = regexp.MustCompile(`https?://\S+`)

// canonicalTitle returns a short, link-stripped title line from a post's
// text when it links to an external article, or the full text otherwise.
func canonicalTitle(text, externalURL string) string {
	text = strings.TrimSpace(text)
	if text == "" || externalURL == "" {
		return text
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimSpace(inlineHTTPURLRe.ReplaceAllString(line, ""))
		line = strings.TrimSpace(strings.Trim(line, "-|:"))
		if line != "" {
			return line
		}
	}
	return text
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
