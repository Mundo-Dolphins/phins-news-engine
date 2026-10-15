// Package sqlitewebsearchcache implements
// catalogverification.WebSearchCache against SQLite. Kept
// as its own small package, out of adapters/outbound/sqlite,
// the same way sqliteentityproposal is: its dependency on
// catalogverification/weblookup should only reach the tools CLI's
// dependency graph, never the service binary's.
package sqlitewebsearchcache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

// Cache implements catalogverification.WebSearchCache against SQLite.
type Cache struct {
	db *sql.DB
}

// NewCache builds a Cache over an already-open, already-migrated database.
func NewCache(db *sql.DB) *Cache {
	return &Cache{db: db}
}

const sqliteTimeFormat = time.RFC3339Nano

// Get implements catalogverification.WebSearchCache. An expired row is
// treated as a miss, not deleted here — a fresh Set on the same key
// overwrites it.
func (c *Cache) Get(ctx context.Context, queryHash string) ([]weblookup.SearchResult, bool, error) {
	var resultsJSON string
	var expiresAt string
	row := c.db.QueryRowContext(ctx, `SELECT results_json, expires_at FROM web_search_cache WHERE query_hash = ?`, queryHash)
	if err := row.Scan(&resultsJSON, &expiresAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("sqlitewebsearchcache: get %s: %w", queryHash, err)
	}

	expires, err := time.Parse(sqliteTimeFormat, expiresAt)
	if err != nil {
		return nil, false, fmt.Errorf("sqlitewebsearchcache: parse expires_at: %w", err)
	}
	if time.Now().UTC().After(expires) {
		return nil, false, nil
	}

	var results []weblookup.SearchResult
	if err := json.Unmarshal([]byte(resultsJSON), &results); err != nil {
		return nil, false, fmt.Errorf("sqlitewebsearchcache: unmarshal results: %w", err)
	}
	return results, true, nil
}

// Set implements catalogverification.WebSearchCache.
func (c *Cache) Set(ctx context.Context, queryHash, provider, query string, results []weblookup.SearchResult, ttl time.Duration) error {
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("sqlitewebsearchcache: marshal results: %w", err)
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	_, err = c.db.ExecContext(ctx, `
		INSERT INTO web_search_cache (query_hash, provider, query, results_json, searched_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(query_hash) DO UPDATE SET
			provider = excluded.provider, query = excluded.query, results_json = excluded.results_json,
			searched_at = excluded.searched_at, expires_at = excluded.expires_at
	`, queryHash, provider, query, string(resultsJSON), now.Format(sqliteTimeFormat), expiresAt.Format(sqliteTimeFormat))
	if err != nil {
		return fmt.Errorf("sqlitewebsearchcache: set %s: %w", queryHash, err)
	}
	return nil
}
