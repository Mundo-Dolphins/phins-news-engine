package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// Event is one recorded step in an article's processing history — the
// structured detail that article.Article.Status/StatusReason summarize.
type Event struct {
	ArticleID         string
	OccurredAt        time.Time
	FromStatus        article.Status
	ToStatus          article.Status
	EventType         string
	ReasonCode        string
	ReasonDetail      string // JSON
	ClassifierVersion string
	DictionaryVersion string
	ConfigSnapshot    string // JSON
	Score             float64
	Threshold         float64 // applied accept threshold
	RejectThreshold   float64 // applied reject threshold
	Mode              string  // "disabled" | "shadow" | "canary" | "enforce" active when this decision was made
	ModelChecksum     string  // manifest.json's artifact_hash for the model that produced Score
	CycleID           string
}

// DecisionRepository records the auditable history of article processing
// decisions. Rows are pruned independently of whether the owning article
// still exists — see the audit_events_days retention window in ADR 0005.
type DecisionRepository interface {
	Append(ctx context.Context, e Event) error
	// ListByArticle returns every event for articleID, oldest first.
	ListByArticle(ctx context.Context, articleID string) ([]Event, error)
	// ListByEventTypesSince returns up to limit events whose EventType is
	// one of eventTypes and whose OccurredAt is at or after since, most
	// recent first — used by offline reporting tools (e.g. `editorial-review`)
	// rather than the pipeline's own hot path.
	ListByEventTypesSince(ctx context.Context, eventTypes []string, since time.Time, limit int) ([]Event, error)
	// DeleteOlderThan removes events whose occurred_at is before cutoff.
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int, error)
}
