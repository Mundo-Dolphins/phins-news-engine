// Package sqlite implements the application's driven ports (repositories
// and UnitOfWork) against the operational SQLite database.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ArticleRepository implements ports/out.ArticleRepository against SQLite.
type ArticleRepository struct {
	db *sql.DB
}

// NewArticleRepository builds an ArticleRepository over an already-open,
// already-migrated database.
func NewArticleRepository(db *sql.DB) *ArticleRepository {
	return &ArticleRepository{db: db}
}

var _ appout.ArticleRepository = (*ArticleRepository)(nil)

const articleColumns = `
	id, external_id, original_url, canonical_url,
	source_id, source_name, source_type, domain,
	title, summary, author, language,
	published_at, fetched_at, content_type, metadata,
	status, status_reason, current_cluster_id,
	classifier_version, dictionary_version,
	editorial_score, quality_threshold_used, freshness_max_age_used, relevance_score,
	primary_entity, cluster_member_role, is_trusted_source`

// Save inserts a, or updates it in place if its ID already exists.
func (r *ArticleRepository) Save(ctx context.Context, a article.Article) error {
	metadata, err := json.Marshal(a.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: marshal metadata for %s: %w", a.ID, err)
	}

	_, err = executor(ctx, r.db).ExecContext(
		ctx, `
		INSERT INTO articles (`+articleColumns+`)
		VALUES (?,?,?,?, ?,?,?,?, ?,?,?,?, ?,?,?,?, ?,?,?, ?,?, ?,?,?,?, ?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			external_id=excluded.external_id,
			original_url=excluded.original_url,
			canonical_url=excluded.canonical_url,
			source_id=excluded.source_id,
			source_name=excluded.source_name,
			source_type=excluded.source_type,
			domain=excluded.domain,
			title=excluded.title,
			summary=excluded.summary,
			author=excluded.author,
			language=excluded.language,
			published_at=excluded.published_at,
			fetched_at=excluded.fetched_at,
			content_type=excluded.content_type,
			metadata=excluded.metadata,
			status=excluded.status,
			status_reason=excluded.status_reason,
			current_cluster_id=excluded.current_cluster_id,
			classifier_version=excluded.classifier_version,
			dictionary_version=excluded.dictionary_version,
			editorial_score=excluded.editorial_score,
			quality_threshold_used=excluded.quality_threshold_used,
			freshness_max_age_used=excluded.freshness_max_age_used,
			relevance_score=excluded.relevance_score,
			primary_entity=excluded.primary_entity,
			cluster_member_role=excluded.cluster_member_role,
			is_trusted_source=excluded.is_trusted_source,
			updated_at=CURRENT_TIMESTAMP
	`,
		a.ID, a.ExternalID, a.OriginalURL, a.CanonicalURL,
		a.Source.ID.String(), a.Source.Name, string(a.Source.Type), a.Domain,
		a.Title, a.Summary, a.Author, a.Language,
		nullableTime(a.PublishedAt), sqliteTime(a.FetchedAt), string(a.ContentType), string(metadata),
		string(a.Status), a.StatusReason, nullableString(a.ClusterID),
		nullableString(a.ClassifierVersion), nullableString(a.DictionaryVersion),
		a.EditorialScore, a.QualityThreshold, int64(a.FreshnessMaxAge.Seconds()), a.RelevanceScore,
		a.PrimaryEntity, a.ClusterMemberRole, a.IsTrustedSource,
	)
	if err != nil {
		return fmt.Errorf("sqlite: save article %s: %w", a.ID, err)
	}
	return nil
}

// FindByID returns the article with the given ID, or a wrapped
// sql.ErrNoRows if none exists.
func (r *ArticleRepository) FindByID(ctx context.Context, id string) (article.Article, error) {
	row := executor(ctx, r.db).QueryRowContext(ctx, `SELECT `+articleColumns+` FROM articles WHERE id = ?`, id)
	a, err := scanArticleRow(row)
	if err != nil {
		return article.Article{}, fmt.Errorf("sqlite: find article %s: %w", id, err)
	}
	return a, nil
}

// DeleteByStatusOlderThan removes articles whose status is one of statuses
// and whose fetched_at is before cutoff.
func (r *ArticleRepository) DeleteByStatusOlderThan(ctx context.Context, statuses []article.Status, cutoff time.Time) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses)+1)
	for i, s := range statuses {
		placeholders[i] = "?"
		args = append(args, string(s))
	}
	args = append(args, sqliteTime(cutoff))

	query := fmt.Sprintf(`DELETE FROM articles WHERE status IN (%s) AND fetched_at < ?`, strings.Join(placeholders, ","))
	res, err := executor(ctx, r.db).ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("sqlite: delete articles by status older than %s: %w", cutoff, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlite: rows affected: %w", err)
	}
	return int(n), nil
}

// Count returns the total number of stored articles.
func (r *ArticleRepository) Count(ctx context.Context) (int, error) {
	var count int
	if err := executor(ctx, r.db).QueryRowContext(ctx, `SELECT COUNT(1) FROM articles`).Scan(&count); err != nil {
		return 0, fmt.Errorf("sqlite: count articles: %w", err)
	}
	return count, nil
}

// ListCandidatesForReview implements ports/out.ArticleRepository.
func (r *ArticleRepository) ListCandidatesForReview(ctx context.Context, since time.Time, limit int) ([]article.Article, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT `+articleColumns+` FROM articles
		WHERE fetched_at >= ?
		  AND status IN ('clustered','pending_publication','published','rejected_quality')
		  AND id NOT IN (SELECT article_id FROM human_labels)
		ORDER BY fetched_at ASC
		LIMIT ?
	`, sqliteTime(since), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list review candidates since %s: %w", since, err)
	}
	defer rows.Close()
	return scanArticles(rows)
}

// ListSince returns up to limit articles fetched at or after since, in
// ascending fetched_at order, regardless of status — unlike
// ListCandidatesForReview, it does not filter by status or exclude
// human-labeled articles. Used by backtestcandidate to read v2's own real
// historical traffic (title/summary/source and the actual recorded
// status) for replay against a candidate dictionary/editorial model; not
// part of ports/out.ArticleRepository since only that offline tool needs
// it.
func (r *ArticleRepository) ListSince(ctx context.Context, since time.Time, limit int) ([]article.Article, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT `+articleColumns+` FROM articles
		WHERE fetched_at >= ?
		ORDER BY fetched_at ASC
		LIMIT ?
	`, sqliteTime(since), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list articles since %s: %w", since, err)
	}
	defer rows.Close()
	return scanArticles(rows)
}

// ListUnprocessedByEntityDiscovery returns up to limit articles fetched at
// or after since that have no entity_discovery_processed_articles row for
// extractorVersion, in ascending fetched_at order. The processed check is
// part of the query so limit counts only articles still to process; filtering
// after ListSince's LIMIT would let an already-processed prefix starve every
// later run. Used by `entities discover`; not part of
// ports/out.ArticleRepository for the same reason as ListSince.
func (r *ArticleRepository) ListUnprocessedByEntityDiscovery(ctx context.Context, since time.Time, extractorVersion string, limit int) ([]article.Article, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT `+articleColumns+` FROM articles
		WHERE fetched_at >= ?
		  AND NOT EXISTS (
			SELECT 1 FROM entity_discovery_processed_articles p
			WHERE p.article_id = articles.id AND p.extractor_version = ?
		  )
		ORDER BY fetched_at ASC
		LIMIT ?
	`, sqliteTime(since), extractorVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list unprocessed articles for entity discovery since %s: %w", since, err)
	}
	defer rows.Close()
	return scanArticles(rows)
}

// ListByStatusUnlabeled returns up to limit articles whose status is one
// of statuses and that have no row in human_labels yet, oldest-fetched
// first. Used by `train backfill-rejects` to source reject-labeled
// training examples from real pipeline outcomes — not part of
// ports/out.ArticleRepository since only that offline tool needs it (same
// posture as ListSince).
func (r *ArticleRepository) ListByStatusUnlabeled(ctx context.Context, statuses []article.Status, limit int) ([]article.Article, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses)+1)
	for i, s := range statuses {
		placeholders[i] = "?"
		args = append(args, string(s))
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT `+articleColumns+` FROM articles
		WHERE status IN (%s)
		  AND id NOT IN (SELECT article_id FROM human_labels)
		ORDER BY fetched_at ASC
		LIMIT ?
	`, strings.Join(placeholders, ","))
	rows, err := executor(ctx, r.db).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list unlabeled articles by status: %w", err)
	}
	defer rows.Close()
	return scanArticles(rows)
}

// ListByClusterID implements ports/out.ArticleRepository.
func (r *ArticleRepository) ListByClusterID(ctx context.Context, clusterID, excludeArticleID string, limit int) ([]article.Article, error) {
	rows, err := executor(ctx, r.db).QueryContext(ctx, `
		SELECT `+articleColumns+` FROM articles
		WHERE current_cluster_id = ? AND id != ?
		ORDER BY fetched_at DESC
		LIMIT ?
	`, clusterID, excludeArticleID, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list articles by cluster %s: %w", clusterID, err)
	}
	defer rows.Close()
	return scanArticles(rows)
}

// FindByCanonicalURL implements ports/out.ArticleRepository.
func (r *ArticleRepository) FindByCanonicalURL(ctx context.Context, url string) (article.Article, error) {
	row := executor(ctx, r.db).QueryRowContext(ctx, `
		SELECT `+articleColumns+` FROM articles WHERE canonical_url = ? ORDER BY published_at ASC LIMIT 1
	`, url)
	a, err := scanArticleRow(row)
	if err != nil {
		return article.Article{}, fmt.Errorf("sqlite: find article by canonical url %s: %w", url, wrapNotFound(article.ErrNotFound, err))
	}
	return a, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting
// scanArticleRow back both a single-row lookup (FindByID) and a
// multi-row iteration (scanArticles).
type rowScanner interface {
	Scan(dest ...any) error
}

func scanArticleRow(row rowScanner) (article.Article, error) {
	var a article.Article
	var sourceID, sourceName, sourceType string
	var metadata string
	var publishedAt sqliteNullTime
	var fetchedAt sqliteNullTime
	var clusterID, classifierVersion, dictionaryVersion sql.NullString
	var freshnessSeconds int64
	var isTrustedSource int64

	if err := row.Scan(
		&a.ID, &a.ExternalID, &a.OriginalURL, &a.CanonicalURL,
		&sourceID, &sourceName, &sourceType, &a.Domain,
		&a.Title, &a.Summary, &a.Author, &a.Language,
		&publishedAt, &fetchedAt, &a.ContentType, &metadata,
		&a.Status, &a.StatusReason, &clusterID,
		&classifierVersion, &dictionaryVersion,
		&a.EditorialScore, &a.QualityThreshold, &freshnessSeconds, &a.RelevanceScore,
		&a.PrimaryEntity, &a.ClusterMemberRole, &isTrustedSource,
	); err != nil {
		return article.Article{}, err
	}

	parsedID, err := source.ParseID(sourceID)
	if err != nil {
		return article.Article{}, fmt.Errorf("sqlite: scan article %s: invalid source_id %q: %w", a.ID, sourceID, err)
	}
	a.Source = source.Source{ID: parsedID, Name: sourceName, Type: source.Type(sourceType)}
	if publishedAt.Valid {
		a.PublishedAt = publishedAt.Time
	}
	if fetchedAt.Valid {
		a.FetchedAt = fetchedAt.Time
	}
	a.ClusterID = clusterID.String
	a.ClassifierVersion = classifierVersion.String
	a.DictionaryVersion = dictionaryVersion.String
	a.FreshnessMaxAge = time.Duration(freshnessSeconds) * time.Second
	a.IsTrustedSource = isTrustedSource != 0

	if err := json.Unmarshal([]byte(metadata), &a.Metadata); err != nil {
		return article.Article{}, fmt.Errorf("sqlite: unmarshal metadata: %w", err)
	}
	return a, nil
}

func scanArticles(rows *sql.Rows) ([]article.Article, error) {
	var result []article.Article
	for rows.Next() {
		a, err := scanArticleRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: iterate articles: %w", err)
	}
	return result, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
