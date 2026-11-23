package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

// DeliveredPublication is one delivered outbox row with the article behind it.
type DeliveredPublication struct {
	ArticleID   string
	Destination publishing.Destination
	SentAt      time.Time
	// Title, Summary, URL and SourceID come from the article. URL is the
	// canonical URL, or the original one when that is empty.
	Title, Summary, URL, SourceID string
}

// PublicationLog reads what was delivered. It is read-only.
type PublicationLog interface {
	// DeliveredBetween lists rows delivered on dest with sent_at in [from, to), oldest first.
	DeliveredBetween(ctx context.Context, dest publishing.Destination, from, to time.Time) ([]DeliveredPublication, error)
}

// LoggedArticle is one stored article as ArticleLog returns it.
type LoggedArticle struct {
	ID, SourceID, Title, Summary, URL string
	PublishedAt                       time.Time
}

// ArticleLog reads stored articles. It is read-only.
type ArticleLog interface {
	// ArticlesBetween lists stored articles of the given sources with
	// published_at in [from, to), oldest first, any status. An empty source
	// list returns an empty list.
	ArticlesBetween(ctx context.Context, sourceIDs []string, from, to time.Time) ([]LoggedArticle, error)
}
