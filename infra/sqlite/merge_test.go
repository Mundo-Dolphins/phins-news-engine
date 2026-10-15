// infra/sqlite/merge_test.go
package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func openMergeTestDB(t *testing.T, name string) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := sqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db, path
}

func insertArticle(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO articles (id, original_url, canonical_url, fetched_at, status) VALUES (?, ?, ?, ?, 'published')`,
		id, "https://example.com/"+id, "https://example.com/"+id, time.Now(),
	); err != nil {
		t.Fatalf("insert article %s: %v", id, err)
	}
}

func TestMergeLocalOwnedTables_CarriesLocalRowsForward_DanglingArticleRefReportsWarning(t *testing.T) {
	previous, previousPath := openMergeTestDB(t, "previous.db")
	insertArticle(t, previous, "art-1")
	if _, err := previous.Exec(
		`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('cycle-1', ?, ?, 'completed', 'review', '{}')`,
		time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := previous.Exec(
		`INSERT INTO human_labels (article_id, label, labeled_at) VALUES ('art-1', 'publish', ?)`, time.Now(),
	); err != nil {
		t.Fatalf("seed human_labels: %v", err)
	}
	previous.Close()

	incoming, _ := openMergeTestDB(t, "incoming.db") // fresh snapshot: no articles, no local-owned rows

	warnings, err := sqlite.MergeLocalOwnedTables(context.Background(), incoming, previousPath)
	if err != nil {
		t.Fatalf("MergeLocalOwnedTables() error = %v", err)
	}

	var sessionCount int
	if err := incoming.QueryRow(`SELECT COUNT(1) FROM editorial_cycle_sessions WHERE id = 'cycle-1'`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Errorf("editorial_cycle_sessions count = %d, want 1 (carried forward from previous)", sessionCount)
	}
	var labelCount int
	if err := incoming.QueryRow(`SELECT COUNT(1) FROM human_labels WHERE article_id = 'art-1'`).Scan(&labelCount); err != nil {
		t.Fatalf("count labels: %v", err)
	}
	if labelCount != 1 {
		t.Errorf("human_labels count = %d, want 1 (carried forward even though art-1 is absent from the new snapshot's articles table)", labelCount)
	}
	if len(warnings) == 0 {
		t.Error("warnings = empty, want at least one for human_labels' dangling reference to art-1")
	}
}

func TestMergeLocalOwnedTables_CarriesHumanLabelConflictsForward(t *testing.T) {
	previous, previousPath := openMergeTestDB(t, "previous.db")
	if _, err := previous.Exec(
		`INSERT INTO human_label_conflicts (table_name, article_id, detail, detected_at) VALUES ('human_labels', 'art-1', '1:publish:x | 2:reject:x', ?)`,
		time.Now(),
	); err != nil {
		t.Fatalf("seed human_label_conflicts: %v", err)
	}
	previous.Close()

	incoming, _ := openMergeTestDB(t, "incoming.db") // fresh snapshot: production never writes this table

	if _, err := sqlite.MergeLocalOwnedTables(context.Background(), incoming, previousPath); err != nil {
		t.Fatalf("MergeLocalOwnedTables() error = %v", err)
	}

	var count int
	if err := incoming.QueryRow(`SELECT COUNT(1) FROM human_label_conflicts WHERE article_id = 'art-1' AND resolved_at IS NULL`).Scan(&count); err != nil {
		t.Fatalf("count conflicts: %v", err)
	}
	if count != 1 {
		t.Errorf("human_label_conflicts count = %d, want 1 (an unresolved legacy conflict must survive a sync, or its warning is silently lost)", count)
	}
}

func TestMergeLocalOwnedTables_UnionsRegistryTablesWithoutDuplicatingPlaceholder(t *testing.T) {
	previous, previousPath := openMergeTestDB(t, "previous.db")
	if _, err := previous.Exec(
		`INSERT INTO model_registry (version, kind, path, metrics) VALUES ('v1.2.3', 'editorial-classifier', '/models/v1.2.3', '{}')`,
	); err != nil {
		t.Fatalf("seed model_registry: %v", err)
	}
	previous.Close()

	incoming, _ := openMergeTestDB(t, "incoming.db") // already has migration 0005's 'placeholder-v0' seed row

	if _, err := sqlite.MergeLocalOwnedTables(context.Background(), incoming, previousPath); err != nil {
		t.Fatalf("MergeLocalOwnedTables() error = %v", err)
	}

	var count int
	if err := incoming.QueryRow(`SELECT COUNT(1) FROM model_registry`).Scan(&count); err != nil {
		t.Fatalf("count model_registry: %v", err)
	}
	if count != 2 {
		t.Errorf("model_registry count = %d, want 2 (placeholder-v0 kept once + v1.2.3 added)", count)
	}
}

func TestMergeLocalOwnedTables_ReplacesIncomingLocalOwnedRowsEntirely_NotUnion(t *testing.T) {
	incoming, _ := openMergeTestDB(t, "incoming.db")
	if _, err := incoming.Exec(
		`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('stale-leftover', ?, ?, 'completed', 'review', '{}')`,
		time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed stale session in incoming: %v", err)
	}

	previous, previousPath := openMergeTestDB(t, "previous.db")
	if _, err := previous.Exec(
		`INSERT INTO editorial_cycle_sessions (id, started_at, updated_at, status, current_step, flags_json) VALUES ('cycle-1', ?, ?, 'completed', 'review', '{}')`,
		time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed session in previous: %v", err)
	}
	previous.Close()

	if _, err := sqlite.MergeLocalOwnedTables(context.Background(), incoming, previousPath); err != nil {
		t.Fatalf("MergeLocalOwnedTables() error = %v", err)
	}

	rows, err := incoming.Query(`SELECT id FROM editorial_cycle_sessions`)
	if err != nil {
		t.Fatalf("query sessions: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 || ids[0] != "cycle-1" {
		t.Errorf("session ids = %v, want exactly [\"cycle-1\"] (incoming's stale-leftover replaced, not unioned)", ids)
	}
}

func TestMergeLocalOwnedTables_MissingPreviousDatabase_ReturnsError(t *testing.T) {
	incoming, _ := openMergeTestDB(t, "incoming.db")
	_, err := sqlite.MergeLocalOwnedTables(context.Background(), incoming, filepath.Join(t.TempDir(), "does-not-exist.db"))
	if err == nil {
		t.Fatal("MergeLocalOwnedTables() error = nil, want an error for a missing previous database")
	}
}

func TestMergeLocalOwnedTables_UnionsOfflineLLMProposalTables(t *testing.T) {
	now := time.Now().UTC()
	previous, previousPath := openMergeTestDB(t, "previous.db")
	for _, stmt := range []string{
		`INSERT INTO editorial_label_proposals (id, article_id, label, confidence, model, prompt_version, policy_version, status, created_at) VALUES ('p1', 'a1', 'publish', 0.97, 'qwen', 'v1', 'v1', 'pending', ?)`,
		`INSERT INTO editorial_label_analysis_processed_articles (article_id, prompt_version, processed_at) VALUES ('a1', 'v1', ?)`,
		`INSERT INTO entity_proposals (id, canonical_name, normalized_name, entity_type, kind, status, first_seen, last_seen, extractor_version, created_at) VALUES ('e-local', 'Jane Doe', 'jane doe', 'person', 'new', 'approved', ?, ?, 'v1', ?)`,
		`INSERT INTO entity_proposal_aliases (proposal_id, alias) VALUES ('e-local', 'J. Doe')`,
		`INSERT INTO entity_proposal_sources (proposal_id, article_id) VALUES ('e-local', 'a1')`,
		`INSERT INTO entity_proposal_web_sources (proposal_id, domain, url) VALUES ('e-local', 'league.example.com', 'https://league.example.com/x')`,
		`INSERT INTO entity_discovery_processed_articles (article_id, extractor_version, processed_at) VALUES ('a1', 'v1', ?)`,
	} {
		args := make([]any, strings.Count(stmt, "?"))
		for i := range args {
			args[i] = now
		}
		if _, err := previous.Exec(stmt, args...); err != nil {
			t.Fatalf("seed previous: %v\n%s", err, stmt)
		}
	}
	previous.Close()

	// Production's online entity fallback also writes entity_proposals,
	// so the incoming snapshot's own rows must survive alongside the
	// local ones rather than being replaced.
	incoming, _ := openMergeTestDB(t, "incoming.db")
	if _, err := incoming.Exec(
		`INSERT INTO entity_proposals (id, canonical_name, normalized_name, entity_type, kind, status, first_seen, last_seen, extractor_version, created_at, source) VALUES ('e-prod', 'John Roe', 'john roe', 'person', 'new', 'pending', ?, ?, 'v1', ?, 'fallback')`,
		now, now, now,
	); err != nil {
		t.Fatalf("seed incoming: %v", err)
	}

	if _, err := sqlite.MergeLocalOwnedTables(context.Background(), incoming, previousPath); err != nil {
		t.Fatalf("MergeLocalOwnedTables() error = %v", err)
	}

	for table, want := range map[string]int{
		"editorial_label_proposals":                   1,
		"editorial_label_analysis_processed_articles": 1,
		"entity_proposals":                            2,
		"entity_proposal_aliases":                     1,
		"entity_proposal_sources":                     1,
		"entity_proposal_web_sources":                 1,
		"entity_discovery_processed_articles":         1,
	} {
		var got int
		if err := incoming.QueryRow(`SELECT COUNT(1) FROM ` + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Errorf("%s count = %d, want %d", table, got, want)
		}
	}
	var status string
	if err := incoming.QueryRow(`SELECT status FROM entity_proposals WHERE id = 'e-local'`).Scan(&status); err != nil {
		t.Fatalf("read e-local: %v", err)
	}
	if status != "approved" {
		t.Errorf("e-local status = %q, want approved (local review decision preserved)", status)
	}
}

// event_rule_proposals holds the operator's review decisions on event rule
// proposals; production never writes it, so a sync must carry the local rows
// forward instead of wiping them with the snapshot's empty copy.
func TestMergeLocalOwnedTables_CarriesEventRuleProposalsForward(t *testing.T) {
	now := time.Now().UTC()
	previous, previousPath := openMergeTestDB(t, "previous.db")
	if _, err := previous.Exec(
		`INSERT INTO event_rule_proposals (id, kind, target_kind, target, phrase, evidence_json, status, prompt_version, created_at, decided_at) VALUES ('evr_local', 'phrase', 'action', 'sign', 'inks a deal', '["a1"]', 'rejected', 'event-rules-v1', ?, ?)`,
		now, now,
	); err != nil {
		t.Fatalf("seed previous: %v", err)
	}
	previous.Close()

	incoming, _ := openMergeTestDB(t, "incoming.db")
	if _, err := sqlite.MergeLocalOwnedTables(context.Background(), incoming, previousPath); err != nil {
		t.Fatalf("MergeLocalOwnedTables() error = %v", err)
	}
	var status string
	if err := incoming.QueryRow(`SELECT status FROM event_rule_proposals WHERE id = 'evr_local'`).Scan(&status); err != nil {
		t.Fatalf("read evr_local: %v (the local proposal must survive a sync)", err)
	}
	if status != "rejected" {
		t.Errorf("evr_local status = %q, want rejected (local decision preserved)", status)
	}
}

// A local database from before migration 0045 has no event_rule_proposals
// table; the merge has nothing to carry and must not fail the sync.
func TestMergeLocalOwnedTables_PreviousWithoutEventRuleProposalsTable(t *testing.T) {
	previous, previousPath := openMergeTestDB(t, "previous.db")
	if _, err := previous.Exec(`DROP TABLE event_rule_proposals`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	previous.Close()

	incoming, _ := openMergeTestDB(t, "incoming.db")
	if _, err := sqlite.MergeLocalOwnedTables(context.Background(), incoming, previousPath); err != nil {
		t.Fatalf("MergeLocalOwnedTables() error = %v, want a pre-0045 local database to merge", err)
	}
}
