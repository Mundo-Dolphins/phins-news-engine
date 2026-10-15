// Package sqliteentityproposal implements ports/entityproposalport.EntityProposalRepository
// against SQLite. It has two callers: the tools CLI (the
// "entities" CLI commands — read/write proposals directly
// for offline batch discovery and review) and
// the service binary itself, when entity_extraction.fallback.enabled is
// true — the online resolution fallback checks this same table as its
// cache and writes successful resolutions back into it as evidence. It
// is still kept out of the adapters/outbound/sqlite package
// deliberately: EntityProposalRepository's dependency on the entity-
// discovery domain packages should only reach the service binary's
// dependency graph when the operator has actually opted into the online
// fallback, not unconditionally alongside every other repository in
// package sqlite.
package sqliteentityproposal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/entityproposalport"
)

// EntityProposalRepository implements ports/entityproposalport.EntityProposalRepository
// against SQLite.
type EntityProposalRepository struct {
	db *sql.DB
}

// NewEntityProposalRepository builds an EntityProposalRepository over an
// already-open, already-migrated database.
func NewEntityProposalRepository(db *sql.DB) *EntityProposalRepository {
	return &EntityProposalRepository{db: db}
}

var _ entityproposalport.EntityProposalRepository = (*EntityProposalRepository)(nil)

// withTx runs fn inside its own short-lived transaction on db, guaranteeing
// its multiple statements commit or roll back together. Unlike the
// executor/withTx pair in adapters/outbound/sqlite,
// EntityProposalRepository is only ever called standalone from CLI
// commands, never wrapped by the application's UnitOfWork.Do, so there is
// no need to join an outer transaction stashed on ctx — a plain
// BeginTx/Rollback/Commit is sufficient.
func withTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqliteentityproposal: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqliteentityproposal: commit transaction: %w", err)
	}
	return nil
}

// wrapNotFound classifies a raw repository error: if err is (or wraps)
// sql.ErrNoRows, it returns an error that satisfies errors.Is against both
// domainErr and sql.ErrNoRows. Any other error is returned unchanged.
func wrapNotFound(domainErr, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(domainErr, err)
	}
	return err
}

const sqliteTimeFormat = time.RFC3339Nano

type sqliteNullTime struct {
	Time  time.Time
	Valid bool
}

func (t *sqliteNullTime) Scan(value any) error {
	parsed, valid, err := parseSQLiteTime(value)
	if err != nil {
		return err
	}
	t.Time = parsed
	t.Valid = valid
	return nil
}

func sqliteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeFormat)
}

func parseSQLiteTime(value any) (time.Time, bool, error) {
	switch v := value.(type) {
	case nil:
		return time.Time{}, false, nil
	case time.Time:
		if v.IsZero() {
			return time.Time{}, false, nil
		}
		return v.UTC(), true, nil
	case string:
		return parseSQLiteTimeString(v)
	case []byte:
		return parseSQLiteTimeString(string(v))
	default:
		return time.Time{}, false, fmt.Errorf("sqliteentityproposal: unsupported timestamp type %T", value)
	}
}

func parseSQLiteTimeString(value string) (time.Time, bool, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return time.Time{}, false, nil
	}
	if idx := strings.Index(s, " m=+"); idx >= 0 {
		s = s[:idx]
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999 -0700 -0700",
		"2006-01-02 15:04:05 -0700 -0700",
		"2006-01-02 15:04:05.999999999 -0700",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			return parsed.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("sqliteentityproposal: parse timestamp %q", value)
}

// Upsert implements ports/entityproposalport.EntityProposalRepository.
func (r *EntityProposalRepository) Upsert(ctx context.Context, p entityproposal.Proposal) (string, error) {
	var finalID string
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		var existingID string
		var occurrences int
		var confidence float64
		var metadataJSON, existingSource string
		var firstSeen, lastSeen sqliteNullTime
		row := tx.QueryRowContext(ctx, `
			SELECT id, occurrences, confidence, metadata_json, first_seen, last_seen, source
			FROM entity_proposals
			WHERE normalized_name = ? AND entity_type = ? AND status = 'pending'
		`, p.NormalizedName, string(p.EntityType))
		err := row.Scan(&existingID, &occurrences, &confidence, &metadataJSON, &firstSeen, &lastSeen, &existingSource)
		switch {
		case err == sql.ErrNoRows:
			// A row auto-rejected (non-NFL, not an NFL team, noise, or a
			// duplicate of an existing proposal) must not be re-queued as a new pending row when
			// the same name is rediscovered.
			var rejectedID string
			rErr := tx.QueryRowContext(ctx, `
				SELECT id FROM entity_proposals
				WHERE normalized_name = ? AND entity_type = ? AND status = 'rejected' AND decision_reason IN (?, ?, ?, ?)
				ORDER BY created_at DESC LIMIT 1
			`, p.NormalizedName, string(p.EntityType), entityproposal.ReasonAutoRejectedNonNFL, entityproposal.ReasonDuplicateOfExisting,
				entityproposal.ReasonNotAnNFLTeam, entityproposal.ReasonAutoRejectedNoise).Scan(&rejectedID)
			if rErr == nil {
				finalID = rejectedID
				return nil
			}
			if rErr != sql.ErrNoRows {
				return fmt.Errorf("select auto-rejected proposal: %w", rErr)
			}
			finalID = p.ID
			if finalID == "" {
				finalID = uuid.NewString()
			}
			metaJSON, mErr := json.Marshal(p.Metadata)
			if mErr != nil {
				return fmt.Errorf("marshal metadata: %w", mErr)
			}
			source := p.Source
			if source == "" {
				source = entityproposal.ProposalSourceDiscovery
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO entity_proposals (
					id, canonical_name, normalized_name, entity_type, kind, target_entity_id,
					metadata_json, confidence, occurrences, status, first_seen, last_seen,
					extractor_version, created_at, source
				) VALUES (?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?)
			`, finalID, p.CanonicalName, p.NormalizedName, string(p.EntityType), string(p.Kind), p.TargetEntityID,
				string(metaJSON), p.Confidence, p.Occurrences, string(entityproposal.StatusPending), sqliteTime(p.FirstSeen), sqliteTime(p.LastSeen),
				p.ExtractorVersion, sqliteTime(p.CreatedAt), string(source)); err != nil {
				return fmt.Errorf("insert entity_proposals: %w", err)
			}
			return r.writeAliasesAndSources(ctx, tx, finalID, p.Aliases, p.Sources, p.WebSources)
		case err != nil:
			return fmt.Errorf("select existing pending proposal: %w", err)
		default:
			finalID = existingID
			mergedMeta := map[string]string{}
			_ = json.Unmarshal([]byte(metadataJSON), &mergedMeta)
			for k, v := range p.Metadata {
				if _, ok := mergedMeta[k]; !ok && v != "" {
					mergedMeta[k] = v
				}
			}
			metaJSON, mErr := json.Marshal(mergedMeta)
			if mErr != nil {
				return fmt.Errorf("marshal merged metadata: %w", mErr)
			}
			newConfidence := confidence
			if p.Confidence > newConfidence {
				newConfidence = p.Confidence
			}
			newFirstSeen := firstSeen.Time
			if p.FirstSeen.Before(newFirstSeen) {
				newFirstSeen = p.FirstSeen
			}
			newLastSeen := lastSeen.Time
			if p.LastSeen.After(newLastSeen) {
				newLastSeen = p.LastSeen
			}
			mergedSource := entityproposal.ProposalSource(existingSource)
			if mergedSource == "" {
				mergedSource = entityproposal.ProposalSourceDiscovery
			}
			if mergedSource != entityproposal.ProposalSourceDiscovery && p.Source == entityproposal.ProposalSourceDiscovery {
				mergedSource = entityproposal.ProposalSourceDiscovery
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE entity_proposals
				SET occurrences = occurrences + ?, confidence = ?, metadata_json = ?, first_seen = ?, last_seen = ?, source = ?
				WHERE id = ?
			`, p.Occurrences, newConfidence, string(metaJSON), sqliteTime(newFirstSeen), sqliteTime(newLastSeen), string(mergedSource), existingID); err != nil {
				return fmt.Errorf("update entity_proposals: %w", err)
			}
			return r.writeAliasesAndSources(ctx, tx, existingID, p.Aliases, p.Sources, p.WebSources)
		}
	})
	if err != nil {
		return "", fmt.Errorf("sqliteentityproposal: upsert entity proposal: %w", err)
	}
	return finalID, nil
}

func (r *EntityProposalRepository) writeAliasesAndSources(ctx context.Context, tx *sql.Tx, proposalID string, aliases []string, sources []entityproposal.Source, webSources []entityproposal.WebSource) error {
	for _, a := range aliases {
		if a == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entity_proposal_aliases (proposal_id, alias) VALUES (?, ?)`, proposalID, a); err != nil {
			return fmt.Errorf("insert alias: %w", err)
		}
	}
	for _, s := range sources {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entity_proposal_sources (proposal_id, article_id, article_title) VALUES (?, ?, ?)`, proposalID, s.ArticleID, s.ArticleTitle); err != nil {
			return fmt.Errorf("insert source: %w", err)
		}
	}
	for _, ws := range webSources {
		if ws.URL == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entity_proposal_web_sources (proposal_id, domain, url, title) VALUES (?, ?, ?, ?)`, proposalID, ws.Domain, ws.URL, ws.Title); err != nil {
			return fmt.Errorf("insert web source: %w", err)
		}
	}
	return nil
}

// Get implements ports/entityproposalport.EntityProposalRepository.
func (r *EntityProposalRepository) Get(ctx context.Context, id string) (entityproposal.Proposal, error) {
	p, err := r.scanOne(ctx, id)
	if err != nil {
		return entityproposal.Proposal{}, fmt.Errorf("sqliteentityproposal: get entity proposal %s: %w", id, wrapNotFound(entityproposal.ErrNotFound, err))
	}
	return p, nil
}

// statusPriorityOrderBy is the deterministic tie-break appended to
// lookups that can otherwise return more than one row for the same
// logical identity (normalized_name+entity_type, or a shared alias):
// migration 0030's unique index on entity_proposals(normalized_name,
// entity_type) is partial (WHERE status = 'pending'), so an approved and
// a rejected row can legitimately coexist with the same identity. This
// prefers approved over pending over rejected, then the most recently
// created row, so a lookup is reproducible instead of depending on
// SQLite's arbitrary row order.
const statusPriorityOrderBy = `
	ORDER BY CASE status WHEN 'approved' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END, created_at DESC
	LIMIT 1
`

// FindByNormalizedName implements ports/entityproposalport.EntityProposalRepository.
func (r *EntityProposalRepository) FindByNormalizedName(ctx context.Context, normalizedName string, entityType entityproposal.EntityType) (entityproposal.Proposal, bool, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, canonical_name, normalized_name, entity_type, kind, target_entity_id,
		       metadata_json, confidence, occurrences, status, first_seen, last_seen,
		       extractor_version, created_at, decided_at, source, decision_reason,
		       verification_provider, verification_provider_id, verification_version
		FROM entity_proposals WHERE normalized_name = ? AND entity_type = ?
	`+statusPriorityOrderBy, normalizedName, string(entityType))
	p, err := scanProposalRow(row)
	if err == sql.ErrNoRows {
		return entityproposal.Proposal{}, false, nil
	}
	if err != nil {
		return entityproposal.Proposal{}, false, fmt.Errorf("sqliteentityproposal: find by normalized name: %w", err)
	}
	aliases, err := r.listAliases(ctx, p.ID)
	if err != nil {
		return entityproposal.Proposal{}, false, err
	}
	sources, err := r.listSources(ctx, p.ID)
	if err != nil {
		return entityproposal.Proposal{}, false, err
	}
	webSources, err := r.listWebSources(ctx, p.ID)
	if err != nil {
		return entityproposal.Proposal{}, false, err
	}
	p.Aliases = aliases
	p.Sources = sources
	p.WebSources = webSources
	return p, true, nil
}

// FindByAlias implements ports/entityproposalport.EntityProposalRepository.
//
// entity_proposal_aliases stores aliases as raw display strings verbatim
// (the offline discovery flow writes them unnormalized), so this can't
// simply compare normalized_alias = ? in SQL without relying on SQLite's
// collation to replicate entityproposal.Normalize exactly. Instead it
// narrows to entityType candidate rows (small per-alias fan-out) via a
// join, then normalizes each alias in Go and compares. Ties across
// multiple matching proposals use the same status-priority ordering as
// FindByNormalizedName.
func (r *EntityProposalRepository) FindByAlias(ctx context.Context, normalizedAlias string, entityType entityproposal.EntityType) (entityproposal.Proposal, bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT p.id, p.canonical_name, p.normalized_name, p.entity_type, p.kind, p.target_entity_id,
		       p.metadata_json, p.confidence, p.occurrences, p.status, p.first_seen, p.last_seen,
		       p.extractor_version, p.created_at, p.decided_at, p.source, p.decision_reason,
		       p.verification_provider, p.verification_provider_id, p.verification_version, a.alias
		FROM entity_proposal_aliases a
		JOIN entity_proposals p ON p.id = a.proposal_id
		WHERE p.entity_type = ?
	`, string(entityType))
	if err != nil {
		return entityproposal.Proposal{}, false, fmt.Errorf("sqliteentityproposal: find by alias: %w", err)
	}
	defer rows.Close()

	var best entityproposal.Proposal
	found := false
	for rows.Next() {
		var p entityproposal.Proposal
		var entType, kind, status, source, alias string
		var firstSeen, lastSeen, createdAt, decidedAt sqliteNullTime
		var metadataJSON string
		if err := rows.Scan(&p.ID, &p.CanonicalName, &p.NormalizedName, &entType, &kind, &p.TargetEntityID,
			&metadataJSON, &p.Confidence, &p.Occurrences, &status, &firstSeen, &lastSeen,
			&p.ExtractorVersion, &createdAt, &decidedAt, &source, &p.DecisionReason,
			&p.Verification.Provider, &p.Verification.ProviderID, &p.Verification.Version, &alias); err != nil {
			return entityproposal.Proposal{}, false, fmt.Errorf("sqliteentityproposal: scan alias candidate: %w", err)
		}
		if entityproposal.Normalize(alias) != normalizedAlias {
			continue
		}
		p.EntityType = entityproposal.EntityType(entType)
		p.Kind = entityproposal.Kind(kind)
		p.Status = entityproposal.Status(status)
		p.Source = entityproposal.ProposalSource(source)
		p.FirstSeen = firstSeen.Time
		p.LastSeen = lastSeen.Time
		p.CreatedAt = createdAt.Time
		p.DecidedAt = decidedAt.Time
		p.Metadata = map[string]string{}
		_ = json.Unmarshal([]byte(metadataJSON), &p.Metadata)

		if !found || statusPriority(p.Status) < statusPriority(best.Status) ||
			(statusPriority(p.Status) == statusPriority(best.Status) && p.CreatedAt.After(best.CreatedAt)) {
			best = p
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return entityproposal.Proposal{}, false, fmt.Errorf("sqliteentityproposal: find by alias: %w", err)
	}
	if !found {
		return entityproposal.Proposal{}, false, nil
	}

	aliases, err := r.listAliases(ctx, best.ID)
	if err != nil {
		return entityproposal.Proposal{}, false, err
	}
	sources, err := r.listSources(ctx, best.ID)
	if err != nil {
		return entityproposal.Proposal{}, false, err
	}
	webSources, err := r.listWebSources(ctx, best.ID)
	if err != nil {
		return entityproposal.Proposal{}, false, err
	}
	best.Aliases = aliases
	best.Sources = sources
	best.WebSources = webSources
	return best, true, nil
}

// statusPriority mirrors statusPriorityOrderBy's SQL CASE for the
// Go-side tie-break FindByAlias needs. It's decided in Go, not SQL,
// because FindByAlias must normalize each candidate alias in Go first
// before it even knows which rows match at all.
func statusPriority(s entityproposal.Status) int {
	switch s {
	case entityproposal.StatusApproved:
		return 0
	case entityproposal.StatusPending:
		return 1
	default:
		return 2
	}
}

func (r *EntityProposalRepository) scanOne(ctx context.Context, id string) (entityproposal.Proposal, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, canonical_name, normalized_name, entity_type, kind, target_entity_id,
		       metadata_json, confidence, occurrences, status, first_seen, last_seen,
		       extractor_version, created_at, decided_at, source, decision_reason,
		       verification_provider, verification_provider_id, verification_version
		FROM entity_proposals WHERE id = ?
	`, id)
	p, err := scanProposalRow(row)
	if err != nil {
		return entityproposal.Proposal{}, err
	}
	aliases, err := r.listAliases(ctx, id)
	if err != nil {
		return entityproposal.Proposal{}, err
	}
	sources, err := r.listSources(ctx, id)
	if err != nil {
		return entityproposal.Proposal{}, err
	}
	webSources, err := r.listWebSources(ctx, id)
	if err != nil {
		return entityproposal.Proposal{}, err
	}
	p.Aliases = aliases
	p.Sources = sources
	p.WebSources = webSources
	return p, nil
}

func (r *EntityProposalRepository) listAliases(ctx context.Context, id string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT alias FROM entity_proposal_aliases WHERE proposal_id = ? ORDER BY alias`, id)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()
	var aliases []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("scan alias: %w", err)
		}
		aliases = append(aliases, a)
	}
	return aliases, rows.Err()
}

func (r *EntityProposalRepository) listSources(ctx context.Context, id string) ([]entityproposal.Source, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT article_id, article_title FROM entity_proposal_sources WHERE proposal_id = ? ORDER BY article_id`, id)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()
	var sources []entityproposal.Source
	for rows.Next() {
		var s entityproposal.Source
		if err := rows.Scan(&s.ArticleID, &s.ArticleTitle); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, s)
	}
	return sources, rows.Err()
}

func (r *EntityProposalRepository) listWebSources(ctx context.Context, id string) ([]entityproposal.WebSource, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT domain, url, title FROM entity_proposal_web_sources WHERE proposal_id = ? ORDER BY url`, id)
	if err != nil {
		return nil, fmt.Errorf("list web sources: %w", err)
	}
	defer rows.Close()
	var webSources []entityproposal.WebSource
	for rows.Next() {
		var ws entityproposal.WebSource
		if err := rows.Scan(&ws.Domain, &ws.URL, &ws.Title); err != nil {
			return nil, fmt.Errorf("scan web source: %w", err)
		}
		webSources = append(webSources, ws)
	}
	return webSources, rows.Err()
}

type rowScannerEP interface {
	Scan(dest ...any) error
}

func scanProposalRow(row rowScannerEP) (entityproposal.Proposal, error) {
	var p entityproposal.Proposal
	var entityType, kind, status, source string
	var firstSeen, lastSeen, createdAt sqliteNullTime
	var decidedAt sqliteNullTime
	var metadataJSON string
	if err := row.Scan(&p.ID, &p.CanonicalName, &p.NormalizedName, &entityType, &kind, &p.TargetEntityID,
		&metadataJSON, &p.Confidence, &p.Occurrences, &status, &firstSeen, &lastSeen,
		&p.ExtractorVersion, &createdAt, &decidedAt, &source, &p.DecisionReason,
		&p.Verification.Provider, &p.Verification.ProviderID, &p.Verification.Version); err != nil {
		return entityproposal.Proposal{}, err
	}
	p.EntityType = entityproposal.EntityType(entityType)
	p.Kind = entityproposal.Kind(kind)
	p.Status = entityproposal.Status(status)
	p.Source = entityproposal.ProposalSource(source)
	p.FirstSeen = firstSeen.Time
	p.LastSeen = lastSeen.Time
	p.CreatedAt = createdAt.Time
	p.DecidedAt = decidedAt.Time
	p.Metadata = map[string]string{}
	_ = json.Unmarshal([]byte(metadataJSON), &p.Metadata)
	return p, nil
}

// List implements ports/entityproposalport.EntityProposalRepository.
func (r *EntityProposalRepository) List(ctx context.Context, entityType, status string) ([]entityproposal.Proposal, error) {
	query := `
		SELECT id, canonical_name, normalized_name, entity_type, kind, target_entity_id,
		       metadata_json, confidence, occurrences, status, first_seen, last_seen,
		       extractor_version, created_at, decided_at, source, decision_reason,
		       verification_provider, verification_provider_id, verification_version
		FROM entity_proposals WHERE 1=1
	`
	var args []any
	if entityType != "" {
		query += ` AND entity_type = ?`
		args = append(args, entityType)
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqliteentityproposal: list entity proposals: %w", err)
	}
	defer rows.Close()

	var ids []string
	var proposals []entityproposal.Proposal
	for rows.Next() {
		p, err := scanProposalRow(rows)
		if err != nil {
			return nil, fmt.Errorf("sqliteentityproposal: scan entity proposal: %w", err)
		}
		proposals = append(proposals, p)
		ids = append(ids, p.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqliteentityproposal: list entity proposals: %w", err)
	}

	for i, id := range ids {
		aliases, err := r.listAliases(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("sqliteentityproposal: %w", err)
		}
		sources, err := r.listSources(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("sqliteentityproposal: %w", err)
		}
		webSources, err := r.listWebSources(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("sqliteentityproposal: %w", err)
		}
		proposals[i].Aliases = aliases
		proposals[i].Sources = sources
		proposals[i].WebSources = webSources
	}
	return proposals, nil
}

// UpdateStatus implements ports/entityproposalport.EntityProposalRepository.
func (r *EntityProposalRepository) UpdateStatus(ctx context.Context, id string, status entityproposal.Status, decidedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE entity_proposals SET status = ?, decided_at = ? WHERE id = ?
	`, string(status), sqliteTime(decidedAt), id)
	if err != nil {
		return fmt.Errorf("sqliteentityproposal: update entity proposal status %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqliteentityproposal: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sqliteentityproposal: update entity proposal status %s: %w", id, entityproposal.ErrNotFound)
	}
	return nil
}

// FilterUnprocessed implements ports/entityproposalport.EntityProposalRepository.
func (r *EntityProposalRepository) FilterUnprocessed(ctx context.Context, extractorVersion string, articleIDs []string) ([]string, error) {
	if len(articleIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(articleIDs))
	args := make([]any, 0, len(articleIDs)+1)
	args = append(args, extractorVersion)
	for i, id := range articleIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT article_id FROM entity_discovery_processed_articles
		WHERE extractor_version = ? AND article_id IN (%s)
	`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, fmt.Errorf("sqliteentityproposal: filter unprocessed articles: %w", err)
	}
	defer rows.Close()

	processed := make(map[string]bool, len(articleIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqliteentityproposal: scan processed article id: %w", err)
		}
		processed[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqliteentityproposal: filter unprocessed articles: %w", err)
	}

	var unprocessed []string
	for _, id := range articleIDs {
		if !processed[id] {
			unprocessed = append(unprocessed, id)
		}
	}
	return unprocessed, nil
}

// MarkArticlesProcessed implements ports/entityproposalport.EntityProposalRepository.
func (r *EntityProposalRepository) MarkArticlesProcessed(ctx context.Context, extractorVersion string, articleIDs []string, processedAt time.Time) error {
	return withTx(ctx, r.db, func(tx *sql.Tx) error {
		for _, id := range articleIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO entity_discovery_processed_articles (article_id, extractor_version, processed_at)
				VALUES (?, ?, ?)
			`, id, extractorVersion, sqliteTime(processedAt)); err != nil {
				return fmt.Errorf("mark article %s processed: %w", id, err)
			}
		}
		return nil
	})
}

// UpdateVerification implements ports/entityproposalport.EntityProposalRepository.
func (r *EntityProposalRepository) UpdateVerification(ctx context.Context, p entityproposal.Proposal) error {
	metaJSON, err := json.Marshal(p.Metadata)
	if err != nil {
		return fmt.Errorf("sqliteentityproposal: update verification %s: marshal metadata: %w", p.ID, err)
	}
	var decidedAt any
	if p.Status != entityproposal.StatusPending {
		t := p.DecidedAt
		if t.IsZero() {
			t = time.Now()
		}
		decidedAt = sqliteTime(t)
	}
	err = withTx(ctx, r.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE entity_proposals
			SET entity_type = ?, canonical_name = ?, normalized_name = ?, metadata_json = ?,
			    confidence = ?, status = ?, decision_reason = ?, decided_at = ?,
			    verification_provider = ?, verification_provider_id = ?, verification_version = ?
			WHERE id = ? AND status = 'pending'
		`, string(p.EntityType), p.CanonicalName, p.NormalizedName, string(metaJSON),
			p.Confidence, string(p.Status), p.DecisionReason, decidedAt,
			p.Verification.Provider, p.Verification.ProviderID, p.Verification.Version, p.ID)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return fmt.Errorf("%w: %s %q is already a pending proposal", entityproposal.ErrConflict, p.EntityType, p.NormalizedName)
			}
			return fmt.Errorf("update entity_proposals: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if n == 0 {
			// Either no such row, or the row was already decided (by a
			// human or an earlier auto-reject): verification only ever
			// writes onto a still-pending row.
			var status string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM entity_proposals WHERE id = ?`, p.ID).Scan(&status); err == nil {
				return fmt.Errorf("%w: proposal %s is already %s", entityproposal.ErrConflict, p.ID, status)
			}
			return entityproposal.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM entity_proposal_aliases WHERE proposal_id = ?`, p.ID); err != nil {
			return fmt.Errorf("clear aliases: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM entity_proposal_web_sources WHERE proposal_id = ?`, p.ID); err != nil {
			return fmt.Errorf("clear web sources: %w", err)
		}
		return r.writeAliasesAndSources(ctx, tx, p.ID, p.Aliases, nil, p.WebSources)
	})
	if err != nil {
		return fmt.Errorf("sqliteentityproposal: update verification %s: %w", p.ID, err)
	}
	return nil
}
