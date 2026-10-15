package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SourceRepository implements ports/out.SourceRepository against SQLite.
type SourceRepository struct {
	db *sql.DB
}

// NewSourceRepository builds a SourceRepository over an already-open,
// already-migrated database.
func NewSourceRepository(db *sql.DB) *SourceRepository {
	return &SourceRepository{db: db}
}

var _ appout.SourceRepository = (*SourceRepository)(nil)

// Save inserts s, or updates it in place if its ID already exists.
// FirstSeenAt is written only on first insert -- an update (ON CONFLICT)
// never overwrites the originally recorded value, even if the caller
// passes a zero or different FirstSeenAt on a later Save.
func (r *SourceRepository) Save(ctx context.Context, s source.Source) error {
	firstSeen := s.FirstSeenAt
	if firstSeen.IsZero() {
		firstSeen = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sources (id, name, type, domain, url, first_seen_at) VALUES (?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, type=excluded.type, domain=excluded.domain, url=excluded.url
	`, s.ID.String(), s.Name, string(s.Type), s.Domain, s.URL, sqliteTime(firstSeen))
	if err != nil {
		return fmt.Errorf("sqlite: save source %s: %w", s.ID, err)
	}
	return nil
}

// List returns every stored source, ordered by ID.
func (r *SourceRepository) List(ctx context.Context) ([]source.Source, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, type, domain, url, first_seen_at FROM sources ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list sources: %w", err)
	}
	defer rows.Close()

	var result []source.Source
	for rows.Next() {
		var s source.Source
		var id, typ string
		var firstSeen sqliteNullTime
		if err := rows.Scan(&id, &s.Name, &typ, &s.Domain, &s.URL, &firstSeen); err != nil {
			return nil, fmt.Errorf("sqlite: scan source: %w", err)
		}
		parsedID, err := source.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("sqlite: scan source: invalid id %q: %w", id, err)
		}
		s.ID = parsedID
		s.Type = source.Type(typ)
		if firstSeen.Valid {
			s.FirstSeenAt = firstSeen.Time
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate sources: %w", err)
	}
	return result, nil
}
