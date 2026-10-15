package sqlitereviewqueue_test

import (
	"context"
	"testing"
	"time"

	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
)

func TestReviewSignalsRepository_CountUnknownEntities(t *testing.T) {
	db := newTestDB(t)
	repo := sqlitereviewqueue.NewReviewSignalsRepository(db)
	ctx := context.Background()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Create articles (required for foreign key constraint)
	for _, articleID := range []string{"article-1", "article-2", "article-with-no-occurrences"} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO articles (id, original_url, canonical_url, fetched_at, status)
			VALUES (?, 'http://example.com', 'http://example.com', ?, 'fetched')
		`, articleID, now); err != nil {
			t.Fatalf("seed articles: %v", err)
		}
	}

	for i := 0; i < 3; i++ {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO unknown_entity_occurrences (id, article_id, candidate_text, normalized_text, possible_type, confidence, reason, field, rules_version, created_at)
			VALUES (?, 'article-1', 'X', 'x', 'person', 0.5, 'r', 'title', 'v1', ?)
		`, "occ-"+string(rune('a'+i)), now); err != nil {
			t.Fatalf("seed unknown_entity_occurrences: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO unknown_entity_occurrences (id, article_id, candidate_text, normalized_text, possible_type, confidence, reason, field, rules_version, created_at)
		VALUES ('occ-other', 'article-2', 'Y', 'y', 'person', 0.5, 'r', 'title', 'v1', ?)
	`, now); err != nil {
		t.Fatalf("seed unrelated unknown_entity_occurrences: %v", err)
	}

	got, err := repo.CountUnknownEntities(ctx, "article-1")
	if err != nil {
		t.Fatalf("CountUnknownEntities() error = %v", err)
	}
	if got != 3 {
		t.Errorf("CountUnknownEntities() = %d, want 3", got)
	}

	got, err = repo.CountUnknownEntities(ctx, "article-with-no-occurrences")
	if err != nil {
		t.Fatalf("CountUnknownEntities() error = %v", err)
	}
	if got != 0 {
		t.Errorf("CountUnknownEntities() = %d, want 0", got)
	}
}

func TestReviewSignalsRepository_CountPendingDictionaryProposals(t *testing.T) {
	db := newTestDB(t)
	repo := sqlitereviewqueue.NewReviewSignalsRepository(db)
	ctx := context.Background()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	seedProposal := func(id, status string) {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO classifier_dictionary_proposals (id, kind, category, value, normalized_value, operation, target_entry, metadata_json, confidence, occurrences, status, first_seen, last_seen, analyzer_version, created_at)
			VALUES (?, 'keyword', 'team', 'Isotopes', 'isotopes', 'add', '', '{}', 0.9, 1, ?, ?, ?, 'v1', ?)
		`, id, status, now, now, now); err != nil {
			t.Fatalf("seed classifier_dictionary_proposals: %v", err)
		}
	}
	seedProposal("proposal-pending", "pending")
	seedProposal("proposal-approved", "approved")

	link := func(proposalID, articleID string) {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO classifier_dictionary_proposal_sources (proposal_id, article_id, article_title) VALUES (?, ?, 'title')
		`, proposalID, articleID); err != nil {
			t.Fatalf("seed classifier_dictionary_proposal_sources: %v", err)
		}
	}
	link("proposal-pending", "article-1")
	link("proposal-approved", "article-1")
	link("proposal-pending", "article-2")

	got, err := repo.CountPendingDictionaryProposals(ctx, "article-1")
	if err != nil {
		t.Fatalf("CountPendingDictionaryProposals() error = %v", err)
	}
	if got != 1 {
		t.Errorf("CountPendingDictionaryProposals() = %d, want 1 (only the pending one counts)", got)
	}
}
