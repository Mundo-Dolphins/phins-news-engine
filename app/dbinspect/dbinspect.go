// Package dbinspect counts the rows the `sqlite inspect` command and the
// sqlite.inspect admin action report on the operational database.
package dbinspect

import (
	"context"
	"database/sql"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
)

// Counts are the row counts reported by [Inspect].
type Counts struct {
	Articles      int
	Sources       int
	OutboxPending int
	OutboxLeased  int
	OpenClusters  int
}

// Inspect counts articles, sources, pending and leased outbox rows and open
// clusters. It reads only; the caller owns migrating the database.
func Inspect(ctx context.Context, db *sql.DB) (Counts, error) {
	var c Counts
	var err error
	if c.Articles, err = sqliteadapter.NewArticleRepository(db).Count(ctx); err != nil {
		return Counts{}, err
	}
	sources, err := sqliteadapter.NewSourceRepository(db).List(ctx)
	if err != nil {
		return Counts{}, err
	}
	c.Sources = len(sources)
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_outbox WHERE status = 'pending'`).Scan(&c.OutboxPending); err != nil {
		return Counts{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_outbox WHERE status = 'leased'`).Scan(&c.OutboxLeased); err != nil {
		return Counts{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM event_clusters WHERE status = 'open'`).Scan(&c.OpenClusters); err != nil {
		return Counts{}, err
	}
	return c, nil
}
