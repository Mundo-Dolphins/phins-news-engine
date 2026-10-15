// adapters/outbound/sqlite/generic_article_lister.go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

// GenericArticleLister lists articles by their stored event representation
// type (generic, or any typed one for the control sample), for event rule
// discovery.
type GenericArticleLister struct {
	db       *sql.DB
	articles *ArticleRepository
}

// NewGenericArticleLister builds a lister over db.
func NewGenericArticleLister(db *sql.DB) *GenericArticleLister {
	return &GenericArticleLister{db: db, articles: NewArticleRepository(db)}
}

// ListGeneric returns up to limit generic articles published at or after
// since, newest first, each with its current cluster id ("" when none).
func (l *GenericArticleLister) ListGeneric(ctx context.Context, since time.Time, limit int) ([]eventruleproposal.Sample, error) {
	return l.list(ctx, "er.event_type = 'generic'", "generic", since, limit)
}

// ListTyped returns up to limit articles with a non-generic event type,
// published at or after since, newest first: the control sample that lets
// the checker see a phrase retype an already-typed article.
func (l *GenericArticleLister) ListTyped(ctx context.Context, since time.Time, limit int) ([]eventruleproposal.Sample, error) {
	return l.list(ctx, "er.event_type <> 'generic'", "typed", since, limit)
}

// list runs the shared query; where is one of the two literal filters above.
func (l *GenericArticleLister) list(ctx context.Context, where, label string, since time.Time, limit int) ([]eventruleproposal.Sample, error) {
	// #nosec G202 -- where is one of two hardcoded literals, never input.
	rows, err := l.db.QueryContext(ctx, `
		SELECT a.id, COALESCE(a.current_cluster_id, '')
		FROM event_representations er
		JOIN articles a ON a.id = er.article_id
		WHERE `+where+` AND a.published_at >= ?
		ORDER BY a.published_at DESC, a.id ASC
		LIMIT ?`, sqliteTime(since), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list %s articles: %w", label, err)
	}
	type idCluster struct{ id, cluster string }
	var found []idCluster
	for rows.Next() {
		var ic idCluster
		if err := rows.Scan(&ic.id, &ic.cluster); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("sqlite: scan %s article: %w", label, err)
		}
		found = append(found, ic)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("sqlite: list %s articles: %w", label, err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]eventruleproposal.Sample, 0, len(found))
	for _, ic := range found {
		a, err := l.articles.FindByID(ctx, ic.id)
		if err != nil {
			return nil, err
		}
		out = append(out, eventruleproposal.Sample{Article: a, ClusterID: ic.cluster})
	}
	return out, nil
}
