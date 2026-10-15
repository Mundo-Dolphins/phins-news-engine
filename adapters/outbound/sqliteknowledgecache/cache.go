// Package sqliteknowledgecache implements ports/out.KnowledgeCache against
// SQLite. It is kept as its own small package, like
// sqlitewebsearchcache, so its dependencies only reach
// the tools CLI's dependency graph, never the service binary's.
package sqliteknowledgecache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// timeFormat is fixed-width UTC (nine fractional digits), unlike
// time.RFC3339Nano which trims zeros — fixed width keeps lexicographic
// comparison (used by Purge) equal to chronological comparison.
const timeFormat = "2006-01-02T15:04:05.000000000Z"

// Cache implements out.KnowledgeCache.
type Cache struct {
	db *sql.DB
}

var _ out.KnowledgeCache = (*Cache)(nil)

// NewCache builds a Cache over an already-open, already-migrated database.
func NewCache(db *sql.DB) *Cache { return &Cache{db: db} }

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeFormat, s) }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetSearch implements out.KnowledgeCache. Expired rows are returned too.
func (c *Cache) GetSearch(ctx context.Context, key out.SearchKey) (out.CachedSearch, bool, error) {
	var resultsJSON, expiresAt string
	var negative int
	err := c.db.QueryRowContext(ctx,
		`SELECT results_json, negative, expires_at FROM knowledge_search_cache WHERE query_hash = ?`,
		key.Hash()).Scan(&resultsJSON, &negative, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out.CachedSearch{}, false, nil
	}
	if err != nil {
		return out.CachedSearch{}, false, fmt.Errorf("sqliteknowledgecache: get search: %w", err)
	}
	expires, err := parseTime(expiresAt)
	if err != nil {
		return out.CachedSearch{}, false, fmt.Errorf("sqliteknowledgecache: parse expires_at: %w", err)
	}
	var candidates []knowledge.Candidate
	if err := json.Unmarshal([]byte(resultsJSON), &candidates); err != nil {
		return out.CachedSearch{}, false, fmt.Errorf("sqliteknowledgecache: unmarshal results: %w", err)
	}
	return out.CachedSearch{Candidates: candidates, Negative: negative == 1, ExpiresAt: expires}, true, nil
}

// PutSearch implements out.KnowledgeCache.
func (c *Cache) PutSearch(ctx context.Context, key out.SearchKey, v out.CachedSearch, now time.Time) error {
	candidates := v.Candidates
	if candidates == nil {
		candidates = []knowledge.Candidate{}
	}
	data, err := json.Marshal(candidates)
	if err != nil {
		return fmt.Errorf("sqliteknowledgecache: marshal results: %w", err)
	}
	_, err = c.db.ExecContext(ctx, `
		INSERT INTO knowledge_search_cache (query_hash, provider, language, limit_n, query, results_json, negative, searched_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(query_hash) DO UPDATE SET
			results_json = excluded.results_json, negative = excluded.negative,
			searched_at = excluded.searched_at, expires_at = excluded.expires_at
	`, key.Hash(), key.Provider, key.Language, key.Limit, key.Query, string(data), boolToInt(v.Negative), formatTime(now), formatTime(v.ExpiresAt))
	if err != nil {
		return fmt.Errorf("sqliteknowledgecache: put search: %w", err)
	}
	return nil
}

// GetEntity implements out.KnowledgeCache. Expired rows are returned too.
func (c *Cache) GetEntity(ctx context.Context, key out.EntityKey) (out.CachedEntity, bool, error) {
	var entityJSON, expiresAt string
	var negative int
	err := c.db.QueryRowContext(ctx,
		`SELECT entity_json, negative, expires_at FROM knowledge_entity_cache WHERE provider = ? AND language = ? AND entity_id = ?`,
		key.Provider, key.Language, key.ID).Scan(&entityJSON, &negative, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out.CachedEntity{}, false, nil
	}
	if err != nil {
		return out.CachedEntity{}, false, fmt.Errorf("sqliteknowledgecache: get entity %s: %w", key.ID, err)
	}
	expires, err := parseTime(expiresAt)
	if err != nil {
		return out.CachedEntity{}, false, fmt.Errorf("sqliteknowledgecache: parse expires_at: %w", err)
	}
	var entity knowledge.Entity
	if err := json.Unmarshal([]byte(entityJSON), &entity); err != nil {
		return out.CachedEntity{}, false, fmt.Errorf("sqliteknowledgecache: unmarshal entity %s: %w", key.ID, err)
	}
	return out.CachedEntity{Entity: entity, Negative: negative == 1, ExpiresAt: expires}, true, nil
}

// PutEntity implements out.KnowledgeCache.
func (c *Cache) PutEntity(ctx context.Context, key out.EntityKey, v out.CachedEntity, now time.Time) error {
	data, err := json.Marshal(v.Entity)
	if err != nil {
		return fmt.Errorf("sqliteknowledgecache: marshal entity %s: %w", key.ID, err)
	}
	_, err = c.db.ExecContext(ctx, `
		INSERT INTO knowledge_entity_cache (provider, language, entity_id, entity_json, negative, searched_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, language, entity_id) DO UPDATE SET
			entity_json = excluded.entity_json, negative = excluded.negative,
			searched_at = excluded.searched_at, expires_at = excluded.expires_at
	`, key.Provider, key.Language, key.ID, string(data), boolToInt(v.Negative), formatTime(now), formatTime(v.ExpiresAt))
	if err != nil {
		return fmt.Errorf("sqliteknowledgecache: put entity %s: %w", key.ID, err)
	}
	return nil
}

// Purge implements out.KnowledgeCache.
func (c *Cache) Purge(ctx context.Context, expiredBefore time.Time) (int64, error) {
	cutoff := formatTime(expiredBefore)
	var total int64
	// Literal statements (not table-name concatenation) keep gosec G202 quiet.
	for _, purge := range []struct{ table, stmt string }{
		{"knowledge_search_cache", `DELETE FROM knowledge_search_cache WHERE expires_at < ?`},
		{"knowledge_entity_cache", `DELETE FROM knowledge_entity_cache WHERE expires_at < ?`},
	} {
		res, err := c.db.ExecContext(ctx, purge.stmt, cutoff)
		if err != nil {
			return total, fmt.Errorf("sqliteknowledgecache: purge %s: %w", purge.table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("sqliteknowledgecache: purge %s rows: %w", purge.table, err)
		}
		total += n
	}
	return total, nil
}
