package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// Fetcher retrieves candidate articles from all configured sources.
type Fetcher interface {
	Fetch(ctx context.Context) ([]article.Article, error)
}
