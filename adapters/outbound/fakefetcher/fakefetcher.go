// Package fakefetcher provides an in-memory Fetcher for phases before real
// network fetchers (RSS/web/social) are implemented.
package fakefetcher

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Fetcher returns a fixed, in-memory list of articles. It never touches the
// network.
type Fetcher struct {
	articles []article.Article
}

// New builds a Fetcher that returns articles on every Fetch call.
func New(articles []article.Article) *Fetcher {
	return &Fetcher{articles: articles}
}

var _ appout.Fetcher = (*Fetcher)(nil)

// Fetch returns the configured articles, or nil if ctx is already done.
func (f *Fetcher) Fetch(ctx context.Context) ([]article.Article, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.articles, nil
}
