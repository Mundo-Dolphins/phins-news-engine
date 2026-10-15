package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SimilarityIndexRepository implements ports/out.SimilarityIndexRepository
// against SQLite, backed by the article_dedup_terms inverted index (migration 0006).
type SimilarityIndexRepository struct {
	db *sql.DB
}

// NewSimilarityIndexRepository builds a SimilarityIndexRepository over an
// already-open, already-migrated database.
func NewSimilarityIndexRepository(db *sql.DB) *SimilarityIndexRepository {
	return &SimilarityIndexRepository{db: db}
}

var _ appout.SimilarityIndexRepository = (*SimilarityIndexRepository)(nil)

// IndexTerms implements ports/out.SimilarityIndexRepository.
func (r *SimilarityIndexRepository) IndexTerms(ctx context.Context, articleID string, terms []string) error {
	if len(terms) == 0 {
		return nil
	}
	ex := executor(ctx, r.db)
	seen := make(map[string]bool, len(terms))
	for _, term := range terms {
		if seen[term] {
			continue
		}
		seen[term] = true
		if _, err := ex.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO article_dedup_terms (article_id, term) VALUES (?, ?)`,
			articleID, term,
		); err != nil {
			return fmt.Errorf("sqlite: index term %q for %s: %w", term, articleID, err)
		}
	}
	return nil
}

// FindCandidates implements ports/out.SimilarityIndexRepository. It filters
// entirely in SQL — shared-term count via the inverted index, time window,
// content type, and excluded statuses — before any similarity score is
// computed by the caller.
func (r *SimilarityIndexRepository) FindCandidates(ctx context.Context, filter appout.CandidateFilter) ([]appout.Candidate, error) {
	if len(filter.Terms) == 0 {
		return nil, nil
	}

	termPlaceholders := make([]string, len(filter.Terms))
	args := make([]any, 0, len(filter.Terms)+len(filter.ExcludeStatuses)+4)
	for i, t := range filter.Terms {
		termPlaceholders[i] = "?"
		args = append(args, t)
	}

	var statusClause string
	if len(filter.ExcludeStatuses) > 0 {
		placeholders := make([]string, len(filter.ExcludeStatuses))
		for i := range filter.ExcludeStatuses {
			placeholders[i] = "?"
		}
		statusClause = "AND a.status NOT IN (" + strings.Join(placeholders, ",") + ")"
	}

	query := fmt.Sprintf(`
		SELECT a.id, a.current_cluster_id, a.title, a.content_type, a.source_id, a.published_at, a.status
		FROM article_dedup_terms t
		JOIN articles a ON a.id = t.article_id
		WHERE t.term IN (%s)
		  AND a.fetched_at >= ?
		  AND a.content_type = ?
		  %s
		GROUP BY a.id
		HAVING COUNT(DISTINCT t.term) >= ?
		ORDER BY a.fetched_at DESC
		LIMIT ?
	`, strings.Join(termPlaceholders, ","), statusClause)

	args = append(args, filter.WindowStart, string(filter.ContentType))
	for _, s := range filter.ExcludeStatuses {
		args = append(args, string(s))
	}
	args = append(args, filter.MinSharedTerms, filter.Limit)

	rows, err := executor(ctx, r.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: find candidates: %w", err)
	}
	defer rows.Close()

	var candidates []appout.Candidate
	for rows.Next() {
		var c appout.Candidate
		var clusterID sql.NullString
		var publishedAt sqliteNullTime
		var contentType, status string
		if err := rows.Scan(&c.ArticleID, &clusterID, &c.Title, &contentType, &c.SourceID, &publishedAt, &status); err != nil {
			return nil, fmt.Errorf("sqlite: scan candidate: %w", err)
		}
		c.ClusterID = clusterID.String
		c.ContentType = article.ContentType(contentType)
		c.Status = article.Status(status)
		if publishedAt.Valid {
			c.PublishedAt = publishedAt.Time
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate candidates: %w", err)
	}
	return candidates, nil
}
