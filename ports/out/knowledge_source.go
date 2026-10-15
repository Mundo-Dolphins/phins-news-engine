// ports/out/knowledge_source.go
package out

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
)

// ErrTransient marks a provider failure that may succeed later (network
// error, timeout, HTTP 429/5xx after the bounded retry). Only errors
// wrapping it may be answered from stale cache.
var ErrTransient = errors.New("knowledge: transient provider failure")

// KnowledgeSource is the driven port to a structured external knowledge
// provider, used only by offline tooling (the tools CLI) —
// never by the ingestion pipeline (the service binary). It must not
// decide relevance: it only returns data.
type KnowledgeSource interface {
	// Search returns at most limit candidates in provider order.
	Search(ctx context.Context, query string, limit int) ([]knowledge.Candidate, error)
	// Get returns one Entity per existing id, in input order. Unknown ids
	// are omitted.
	Get(ctx context.Context, ids []string) ([]knowledge.Entity, error)
}

// SearchKey identifies one cached search. Query must already be
// normalized by the caller.
type SearchKey struct {
	Provider string
	Language string
	Limit    int
	Query    string
}

// Hash returns the stable cache key for k.
func (k SearchKey) Hash() string {
	sum := sha256.Sum256([]byte(k.Provider + "\x00" + k.Language + "\x00" + strconv.Itoa(k.Limit) + "\x00" + k.Query))
	return hex.EncodeToString(sum[:])
}

// EntityKey identifies one cached entity.
type EntityKey struct {
	Provider string
	Language string
	ID       string
}

// CachedSearch is a stored search result. Negative means the provider
// returned nothing. ExpiresAt is returned even when already past so the
// caller can decide between a fresh hit and a stale fallback.
type CachedSearch struct {
	Candidates []knowledge.Candidate
	Negative   bool
	ExpiresAt  time.Time
}

// CachedEntity is a stored entity lookup; Negative means the provider
// reported the id as missing.
type CachedEntity struct {
	Entity    knowledge.Entity
	Negative  bool
	ExpiresAt time.Time
}

// KnowledgeCache persists KnowledgeSource results. Get* return expired
// rows too (the decorator compares ExpiresAt with its clock). now is the
// time the row was written.
type KnowledgeCache interface {
	GetSearch(ctx context.Context, key SearchKey) (CachedSearch, bool, error)
	PutSearch(ctx context.Context, key SearchKey, v CachedSearch, now time.Time) error
	GetEntity(ctx context.Context, key EntityKey) (CachedEntity, bool, error)
	PutEntity(ctx context.Context, key EntityKey, v CachedEntity, now time.Time) error
	// Purge deletes rows whose ExpiresAt is before the given instant and
	// returns how many were removed.
	Purge(ctx context.Context, expiredBefore time.Time) (int64, error)
}
