package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var _ appout.PublicationLog = (*OutboxRepository)(nil)

// DeliveredBetween implements ports/out.PublicationLog. The delivery time is
// sent_at, or updated_at for a row delivered without one. Placeholder articles
// of timed events ("timed:") and tasks ("task:") are excluded; those ids match
// the prefixes the use cases give them.
func (r *OutboxRepository) DeliveredBetween(ctx context.Context, dest publishing.Destination, from, to time.Time) ([]appout.DeliveredPublication, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT o.article_id, o.destination, COALESCE(o.sent_at, o.updated_at), a.title, a.summary,
		       CASE WHEN a.canonical_url <> '' THEN a.canonical_url ELSE a.original_url END, a.source_id
		FROM publication_outbox o JOIN articles a ON a.id = o.article_id
		WHERE o.destination = ? AND o.status = 'delivered' AND COALESCE(o.sent_at, o.updated_at) >= ? AND COALESCE(o.sent_at, o.updated_at) < ?
		  AND a.id NOT LIKE 'timed:%' AND a.id NOT LIKE 'task:%'
		ORDER BY COALESCE(o.sent_at, o.updated_at), o.id
	`, string(dest.Normalized()), sqliteTime(from), sqliteTime(to))
	if err != nil {
		return nil, fmt.Errorf("sqlite: delivered between: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []appout.DeliveredPublication
	for rows.Next() {
		var p appout.DeliveredPublication
		var d string
		var sent sqliteNullTime
		if err := rows.Scan(&p.ArticleID, &d, &sent, &p.Title, &p.Summary, &p.URL, &p.SourceID); err != nil {
			return nil, fmt.Errorf("sqlite: scan delivered publication: %w", err)
		}
		p.Destination, p.SentAt = publishing.Destination(d), sent.Time
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: delivered between: %w", err)
	}
	return out, nil
}

// ArticleLog implements ports/out.ArticleLog.
type ArticleLog struct {
	db *sql.DB
}

// NewArticleLog builds an ArticleLog over an open, migrated database.
func NewArticleLog(db *sql.DB) *ArticleLog { return &ArticleLog{db: db} }

var _ appout.ArticleLog = (*ArticleLog)(nil)

// ArticlesBetween implements ports/out.ArticleLog.
func (l *ArticleLog) ArticlesBetween(ctx context.Context, sourceIDs []string, from, to time.Time) ([]appout.LoggedArticle, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(sourceIDs)+2)
	for _, id := range sourceIDs {
		args = append(args, id)
	}
	args = append(args, sqliteTime(from), sqliteTime(to))
	rows, err := executor(ctx, l.db).QueryContext(ctx, `
		SELECT id, source_id, title, summary,
		       CASE WHEN canonical_url <> '' THEN canonical_url ELSE original_url END, published_at
		FROM articles
		WHERE source_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(sourceIDs)), ",")+`)
		  AND published_at >= ? AND published_at < ?
		ORDER BY published_at, id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: articles between: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []appout.LoggedArticle
	for rows.Next() {
		var a appout.LoggedArticle
		var pub sqliteNullTime
		if err := rows.Scan(&a.ID, &a.SourceID, &a.Title, &a.Summary, &a.URL, &pub); err != nil {
			return nil, fmt.Errorf("sqlite: scan article: %w", err)
		}
		a.PublishedAt = pub.Time
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: articles between: %w", err)
	}
	return out, nil
}
