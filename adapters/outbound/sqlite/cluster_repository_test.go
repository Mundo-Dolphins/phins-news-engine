package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// mustSaveArticleWithPublishedAt is a variant of mustSaveArticle (see
// decision_repository_test.go) that lets candidate-lookup tests control
// PublishedAt directly, since FindCandidatesByEventKeys bounds its query by
// articles.published_at.
func mustSaveArticleWithPublishedAt(t *testing.T, articles *sqliteadapter.ArticleRepository, id string, publishedAt time.Time) {
	t.Helper()
	a := article.Article{
		ID:           id,
		OriginalURL:  "https://example.com/" + id,
		CanonicalURL: "https://example.com/" + id,
		Source:       source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		FetchedAt:    publishedAt,
		PublishedAt:  publishedAt,
		Status:       article.StatusFetched,
	}
	if err := articles.Save(context.Background(), a); err != nil {
		t.Fatalf("Save(article %s) error = %v", id, err)
	}
}

func TestClusterRepository_CreateClusterSetsCurrentClusterID(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	now := time.Now()
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusClosed, now); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	got, err := articles.FindByID(ctx, "a1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.ClusterID != "c1" {
		t.Errorf("ClusterID = %q, want %q", got.ClusterID, "c1")
	}

	var role string
	if err := db.QueryRowContext(ctx, `SELECT role FROM event_cluster_members WHERE cluster_id = 'c1' AND article_id = 'a1'`).Scan(&role); err != nil {
		t.Fatalf("select role: %v", err)
	}
	if role != string(clustering.MemberRoleFirstReport) {
		t.Errorf("founding member role = %q, want %q", role, clustering.MemberRoleFirstReport)
	}
}

func TestClusterRepository_AddMember(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")
	mustSaveArticle(t, articles, "a2")

	now := time.Now()
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, now); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if err := repo.AddMember(ctx, "c1", "a2", clustering.MemberRoleSubstantialUpdate, 0.73, now.Add(time.Minute)); err != nil {
		t.Fatalf("AddMember() error = %v", err)
	}

	got, err := articles.FindByID(ctx, "a2")
	if err != nil {
		t.Fatalf("FindByID(a2) error = %v", err)
	}
	if got.ClusterID != "c1" {
		t.Errorf("a2 ClusterID = %q, want %q", got.ClusterID, "c1")
	}

	var memberCount int
	if err := db.QueryRowContext(ctx, `SELECT member_count FROM event_clusters WHERE id = 'c1'`).Scan(&memberCount); err != nil {
		t.Fatalf("select member_count: %v", err)
	}
	if memberCount != 2 {
		t.Errorf("member_count = %d, want 2", memberCount)
	}

	var role string
	var score float64
	if err := db.QueryRowContext(ctx, `SELECT role, clustering_score FROM event_cluster_members WHERE cluster_id = 'c1' AND article_id = 'a2'`).Scan(&role, &score); err != nil {
		t.Fatalf("select role, clustering_score: %v", err)
	}
	if role != string(clustering.MemberRoleSubstantialUpdate) {
		t.Errorf("role = %q, want %q", role, clustering.MemberRoleSubstantialUpdate)
	}
	if score != 0.73 {
		t.Errorf("clustering_score = %v, want 0.73", score)
	}
}

// TestClusterRepository_AddMember_ReopensClosedCluster proves the core
// "late-arriving news" behavior: joining a closed cluster reopens it in
// place (same cluster_id) rather than requiring a separate reopen call or
// silently leaving it closed — see the design doc's "Late-arriving news"
// section for why this must be the same cluster_id (preserving its
// existing publication gate).
func TestClusterRepository_AddMember_ReopensClosedCluster(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")
	mustSaveArticle(t, articles, "a2")

	now := time.Now()
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, now); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	closedAt := now.Add(time.Hour)
	if _, err := db.ExecContext(ctx, `UPDATE event_clusters SET status = 'closed', closed_at = ?, close_reason = 'inactivity_ttl' WHERE id = 'c1'`, closedAt); err != nil {
		t.Fatalf("close cluster c1: %v", err)
	}

	reopenAt := closedAt.Add(time.Hour)
	if err := repo.AddMember(ctx, "c1", "a2", clustering.MemberRoleSubstantialUpdate, 0.8, reopenAt); err != nil {
		t.Fatalf("AddMember() error = %v", err)
	}

	var status string
	var closedAtCol sql.NullTime
	var reopenedCount int
	err := db.QueryRowContext(ctx, `SELECT status, closed_at, reopened_count FROM event_clusters WHERE id = 'c1'`).
		Scan(&status, &closedAtCol, &reopenedCount)
	if err != nil {
		t.Fatalf("select after reopen: %v", err)
	}
	if status != "open" {
		t.Errorf("status after AddMember on a closed cluster = %q, want %q", status, "open")
	}
	if closedAtCol.Valid {
		t.Errorf("closed_at after reopen = %v, want NULL", closedAtCol.Time)
	}
	if reopenedCount != 1 {
		t.Errorf("reopened_count after one reopen = %d, want 1", reopenedCount)
	}
}

// TestClusterRepository_AddMember_OpenClusterDoesNotIncrementReopenedCount
// is the negative case: joining an already-open cluster is not a reopen.
func TestClusterRepository_AddMember_OpenClusterDoesNotIncrementReopenedCount(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")
	mustSaveArticle(t, articles, "a2")

	now := time.Now()
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, now); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if err := repo.AddMember(ctx, "c1", "a2", clustering.MemberRoleConfirmation, 0.8, now.Add(time.Minute)); err != nil {
		t.Fatalf("AddMember() error = %v", err)
	}

	var status string
	var reopenedCount int
	if err := db.QueryRowContext(ctx, `SELECT status, reopened_count FROM event_clusters WHERE id = 'c1'`).Scan(&status, &reopenedCount); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "open" || reopenedCount != 0 {
		t.Errorf("status/reopened_count = %q/%d, want open/0", status, reopenedCount)
	}
}

func TestClusterRepository_DeleteClosedOlderThan_ClearsCurrentClusterID(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	now := time.Now()
	old := now.Add(-48 * time.Hour)
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusClosed, old); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE event_clusters SET closed_at = ? WHERE id = 'c1'`, old); err != nil {
		t.Fatalf("set closed_at: %v", err)
	}

	deleted, err := repo.DeleteClosedOlderThan(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteClosedOlderThan() error = %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteClosedOlderThan() = %d, want 1", deleted)
	}

	got, err := articles.FindByID(ctx, "a1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.ClusterID != "" {
		t.Errorf("ClusterID after cluster deletion = %q, want empty (ON DELETE SET NULL)", got.ClusterID)
	}
}

// TestClusterRepository_DeleteClosedOlderThan_UsesClosedAtNotLastUpdatedAt
// is the regression this task exists to add: a cluster with an old
// last_updated_at but a recent closed_at (it sat open a long time, then
// just closed) must NOT be purged yet.
func TestClusterRepository_DeleteClosedOlderThan_UsesClosedAtNotLastUpdatedAt(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	now := time.Now()
	oldActivity := now.Add(-30 * 24 * time.Hour)
	recentClose := now.Add(-time.Hour)
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusClosed, oldActivity); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE event_clusters SET closed_at = ? WHERE id = 'c1'`, recentClose); err != nil {
		t.Fatalf("set closed_at: %v", err)
	}

	deleted, err := repo.DeleteClosedOlderThan(ctx, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteClosedOlderThan() error = %v", err)
	}
	if deleted != 0 {
		t.Errorf("DeleteClosedOlderThan() = %d, want 0 (closed_at is recent even though last_updated_at is old)", deleted)
	}
}

// TestClusterRepository_Migration_NewTablesAndColumnsExist is the explicit
// migration assertion required by the brief: 0008_event_clustering.sql
// applies cleanly (implicitly proven by every test in this file using
// newTestDB), and the specific new tables/columns it adds exist.
func TestClusterRepository_Migration_NewTablesAndColumnsExist(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	wantTables := []string{
		"event_representations",
		"event_keys",
		"event_clustering_decisions",
		"event_cluster_representative_history",
	}
	for _, table := range wantTables {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}

	wantColumns := map[string][]string{
		"event_cluster_members": {"role", "clustering_score"},
		"event_clusters":        {"representative_score", "representative_evidence_json", "representative_rules_version"},
	}
	for table, columns := range wantColumns {
		rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan column name: %v", err)
			}
			found[name] = true
		}
		rows.Close()
		for _, col := range columns {
			if !found[col] {
				t.Errorf("table %s missing column %q", table, col)
			}
		}
	}
}

func sampleRepresentation(articleID string) eventsignals.Representation {
	eventDate := time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC)
	return eventsignals.Representation{
		ArticleID:       articleID,
		SourceID:        "src-1",
		Domain:          "isotopes.example.com",
		ContentType:     article.ContentTypeArticle,
		PublishedAt:     eventDate,
		FetchedAt:       eventDate.Add(5 * time.Minute),
		Entities:        []string{"Tavion Dale", "Springfield Isotopes"},
		PrimaryEntity:   "Tavion Dale",
		PrimaryTeam:     "Springfield Isotopes",
		Opponent:        "Shelbyville Sharks",
		Competition:     "NFL",
		EventDate:       eventDate,
		EventWindow:     6 * time.Hour,
		EventType:       eventsignals.EventTypeTransaction,
		Action:          eventsignals.ActionSign,
		ContentSignal:   eventsignals.ContentSignalConfirmation,
		ApproximateKeys: []string{"transaction:sign:tavion-dale:shelbyville-sharks:2026-07-20", "transaction:sign:tavion-dale", "tavion-dale"},
		Evidence:        []eventsignals.Evidence{`action "sign" matched phrase "signs"`, `entity "Tavion Dale" matched phrase "Dale" in title`},
	}
}

func TestClusterRepository_SaveRepresentation_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	rep := sampleRepresentation("a1")
	now := time.Date(2026, 7, 20, 15, 6, 0, 0, time.UTC)
	if err := repo.SaveRepresentation(ctx, rep, "rules-v1", now); err != nil {
		t.Fatalf("SaveRepresentation() error = %v", err)
	}

	var rulesVersion, eventType, primaryEntity, primaryAction, repJSON string
	var eventDateWindowNanos int64
	var createdAt time.Time
	err := db.QueryRowContext(ctx, `
		SELECT rules_version, event_type, primary_entity, primary_action, event_date_window, representation_json, created_at
		FROM event_representations WHERE article_id = 'a1'
	`).Scan(&rulesVersion, &eventType, &primaryEntity, &primaryAction, &eventDateWindowNanos, &repJSON, &createdAt)
	if err != nil {
		t.Fatalf("select event_representations: %v", err)
	}

	if rulesVersion != "rules-v1" {
		t.Errorf("rules_version = %q, want %q", rulesVersion, "rules-v1")
	}
	if eventType != string(eventsignals.EventTypeTransaction) {
		t.Errorf("event_type = %q, want %q", eventType, eventsignals.EventTypeTransaction)
	}
	if primaryEntity != "Tavion Dale" {
		t.Errorf("primary_entity = %q, want %q", primaryEntity, "Tavion Dale")
	}
	if primaryAction != string(eventsignals.ActionSign) {
		t.Errorf("primary_action = %q, want %q", primaryAction, eventsignals.ActionSign)
	}
	if time.Duration(eventDateWindowNanos) != rep.EventWindow {
		t.Errorf("event_date_window = %v, want %v", time.Duration(eventDateWindowNanos), rep.EventWindow)
	}
	if !createdAt.Equal(now) {
		t.Errorf("created_at = %v, want %v", createdAt, now)
	}

	var got eventsignals.Representation
	if err := json.Unmarshal([]byte(repJSON), &got); err != nil {
		t.Fatalf("json.Unmarshal(representation_json): %v", err)
	}

	if got.ArticleID != rep.ArticleID || got.SourceID != rep.SourceID || got.Domain != rep.Domain || got.ContentType != rep.ContentType {
		t.Errorf("identity fields mismatch: got %+v, want fields from %+v", got, rep)
	}
	if !got.PublishedAt.Equal(rep.PublishedAt) || !got.FetchedAt.Equal(rep.FetchedAt) || !got.EventDate.Equal(rep.EventDate) {
		t.Errorf("time fields mismatch: got PublishedAt=%v FetchedAt=%v EventDate=%v, want %v/%v/%v",
			got.PublishedAt, got.FetchedAt, got.EventDate, rep.PublishedAt, rep.FetchedAt, rep.EventDate)
	}
	if got.EventWindow != rep.EventWindow {
		t.Errorf("EventWindow = %v, want %v", got.EventWindow, rep.EventWindow)
	}
	if len(got.Entities) != len(rep.Entities) {
		t.Fatalf("Entities = %v, want %v", got.Entities, rep.Entities)
	}
	for i := range rep.Entities {
		if got.Entities[i] != rep.Entities[i] {
			t.Errorf("Entities[%d] = %q, want %q", i, got.Entities[i], rep.Entities[i])
		}
	}
	if got.PrimaryEntity != rep.PrimaryEntity || got.PrimaryTeam != rep.PrimaryTeam || got.Opponent != rep.Opponent || got.Competition != rep.Competition {
		t.Errorf("entity fields mismatch: got %+v, want fields from %+v", got, rep)
	}
	if got.EventType != rep.EventType || got.Action != rep.Action || got.ContentSignal != rep.ContentSignal {
		t.Errorf("classification fields mismatch: got %+v, want fields from %+v", got, rep)
	}
	if len(got.ApproximateKeys) != len(rep.ApproximateKeys) {
		t.Fatalf("ApproximateKeys = %v, want %v", got.ApproximateKeys, rep.ApproximateKeys)
	}
	for i := range rep.ApproximateKeys {
		if got.ApproximateKeys[i] != rep.ApproximateKeys[i] {
			t.Errorf("ApproximateKeys[%d] = %q, want %q", i, got.ApproximateKeys[i], rep.ApproximateKeys[i])
		}
	}
	if len(got.Evidence) != len(rep.Evidence) {
		t.Fatalf("Evidence = %v, want %v", got.Evidence, rep.Evidence)
	}
	for i := range rep.Evidence {
		if got.Evidence[i] != rep.Evidence[i] {
			t.Errorf("Evidence[%d] = %q, want %q", i, got.Evidence[i], rep.Evidence[i])
		}
	}
}

// TestClusterRepository_SaveRepresentation_Replaces confirms a second
// SaveRepresentation call for the same ArticleID replaces rather than
// duplicates the row (event_representations.article_id is a PRIMARY KEY).
func TestClusterRepository_SaveRepresentation_Replaces(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")

	rep := sampleRepresentation("a1")
	if err := repo.SaveRepresentation(ctx, rep, "rules-v1", time.Now()); err != nil {
		t.Fatalf("SaveRepresentation() #1 error = %v", err)
	}
	rep.PrimaryEntity = "Jaylen Wickersham"
	if err := repo.SaveRepresentation(ctx, rep, "rules-v2", time.Now()); err != nil {
		t.Fatalf("SaveRepresentation() #2 error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_representations WHERE article_id = 'a1'`).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count for a1 = %d, want 1 (replace, not duplicate)", count)
	}

	var primaryEntity, rulesVersion string
	if err := db.QueryRowContext(ctx, `SELECT primary_entity, rules_version FROM event_representations WHERE article_id = 'a1'`).Scan(&primaryEntity, &rulesVersion); err != nil {
		t.Fatalf("select: %v", err)
	}
	if primaryEntity != "Jaylen Wickersham" || rulesVersion != "rules-v2" {
		t.Errorf("row not replaced: primary_entity=%q rules_version=%q, want %q/%q", primaryEntity, rulesVersion, "Jaylen Wickersham", "rules-v2")
	}
}

func TestClusterRepository_SaveEventKeys_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, time.Now()); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	keys := []string{"strongest-key", "middle-key", "weakest-key"}
	if err := repo.SaveEventKeys(ctx, "a1", "c1", keys, time.Now()); err != nil {
		t.Fatalf("SaveEventKeys() error = %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT event_key, cluster_id, strength FROM event_keys WHERE article_id = 'a1' ORDER BY strength ASC`)
	if err != nil {
		t.Fatalf("select event_keys: %v", err)
	}
	defer rows.Close()

	type row struct {
		key      string
		cluster  string
		strength int
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.key, &r.cluster, &r.strength); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != len(keys) {
		t.Fatalf("got %d event_keys rows, want %d", len(got), len(keys))
	}
	for i, k := range keys {
		if got[i].key != k {
			t.Errorf("row %d event_key = %q, want %q", i, got[i].key, k)
		}
		if got[i].cluster != "c1" {
			t.Errorf("row %d cluster_id = %q, want %q", i, got[i].cluster, "c1")
		}
		if got[i].strength != i {
			t.Errorf("row %d strength = %d, want %d (0 = strongest = ApproximateKeys[0])", i, got[i].strength, i)
		}
	}
}

// TestClusterRepository_SaveEventKeys_Idempotent confirms a second call for
// the same (article, key) pairs does not duplicate rows or error, matching
// SimilarityIndexRepository.IndexTerms' idempotency contract.
func TestClusterRepository_SaveEventKeys_Idempotent(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, time.Now()); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	keys := []string{"key-a", "key-b"}
	if err := repo.SaveEventKeys(ctx, "a1", "c1", keys, time.Now()); err != nil {
		t.Fatalf("SaveEventKeys() #1 error = %v", err)
	}
	if err := repo.SaveEventKeys(ctx, "a1", "c1", keys, time.Now()); err != nil {
		t.Fatalf("SaveEventKeys() #2 error = %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_keys WHERE article_id = 'a1'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("event_keys row count = %d, want 2 (idempotent)", count)
	}
}

func TestClusterRepository_SaveDecision_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, time.Now()); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	mustSaveArticle(t, articles, "a2")

	decidedAt := time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)
	d := clustering.Decision{
		Action:     clustering.ActionJoin,
		ClusterID:  "c1",
		MemberRole: clustering.MemberRoleConfirmation,
		Score:      0.87,
		Evidence: []clustering.Evidence{
			"entity match: Tavion Dale",
			"event type match: transaction",
		},
		Alternatives: []clustering.Alternative{
			{ClusterID: "c9", ArticleID: "a9", Score: 0.42, Relation: clustering.RelationDifferent},
		},
		RulesVersion: "clustering-v1",
		DecidedAt:    decidedAt,
	}
	if err := repo.SaveDecision(ctx, "a2", "c1", d); err != nil {
		t.Fatalf("SaveDecision() error = %v", err)
	}

	var articleID, chosenClusterID, action, memberRole, alternativesJSON, evidenceJSON, rulesVersion string
	var score float64
	var storedDecidedAt time.Time
	err := db.QueryRowContext(ctx, `
		SELECT article_id, chosen_cluster_id, action, member_role, score, alternatives_json, evidence_json, rules_version, decided_at
		FROM event_clustering_decisions WHERE article_id = 'a2'
	`).Scan(&articleID, &chosenClusterID, &action, &memberRole, &score, &alternativesJSON, &evidenceJSON, &rulesVersion, &storedDecidedAt)
	if err != nil {
		t.Fatalf("select event_clustering_decisions: %v", err)
	}

	if articleID != "a2" {
		t.Errorf("article_id = %q, want %q", articleID, "a2")
	}
	if chosenClusterID != "c1" {
		t.Errorf("chosen_cluster_id = %q, want %q", chosenClusterID, "c1")
	}
	if action != string(clustering.ActionJoin) {
		t.Errorf("action = %q, want %q", action, clustering.ActionJoin)
	}
	if memberRole != string(clustering.MemberRoleConfirmation) {
		t.Errorf("member_role = %q, want %q", memberRole, clustering.MemberRoleConfirmation)
	}
	if score != 0.87 {
		t.Errorf("score = %v, want 0.87", score)
	}
	if rulesVersion != "clustering-v1" {
		t.Errorf("rules_version = %q, want %q", rulesVersion, "clustering-v1")
	}
	if !storedDecidedAt.Equal(decidedAt) {
		t.Errorf("decided_at = %v, want %v", storedDecidedAt, decidedAt)
	}

	var gotEvidence []clustering.Evidence
	if err := json.Unmarshal([]byte(evidenceJSON), &gotEvidence); err != nil {
		t.Fatalf("json.Unmarshal(evidence_json): %v", err)
	}
	if len(gotEvidence) != len(d.Evidence) {
		t.Fatalf("Evidence = %v, want %v", gotEvidence, d.Evidence)
	}
	for i := range d.Evidence {
		if gotEvidence[i] != d.Evidence[i] {
			t.Errorf("Evidence[%d] = %q, want %q", i, gotEvidence[i], d.Evidence[i])
		}
	}

	var gotAlternatives []clustering.Alternative
	if err := json.Unmarshal([]byte(alternativesJSON), &gotAlternatives); err != nil {
		t.Fatalf("json.Unmarshal(alternatives_json): %v", err)
	}
	if len(gotAlternatives) != 1 {
		t.Fatalf("Alternatives = %v, want 1 entry", gotAlternatives)
	}
	if gotAlternatives[0] != d.Alternatives[0] {
		t.Errorf("Alternatives[0] = %+v, want %+v", gotAlternatives[0], d.Alternatives[0])
	}
}

// TestClusterRepository_UpdateRepresentative_SwitchesAndAudits covers the
// history mechanism end to end: the founding representative (set by
// CreateCluster, unscored) is switched with no history row created, and a
// second switch away from the first *scored* representative does create a
// history row for it.
func TestClusterRepository_UpdateRepresentative_SwitchesAndAudits(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()
	mustSaveArticle(t, articles, "a1")
	mustSaveArticle(t, articles, "a2")
	mustSaveArticle(t, articles, "a3")

	now := time.Now()
	if err := repo.CreateCluster(ctx, "c1", "a1", clustering.StatusOpen, now); err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}

	// First switch: a1 (unscored founding representative) -> a2.
	if err := repo.UpdateRepresentative(ctx, "c1", "a2", 0.6, []clustering.Evidence{"more complete"}, "rep-v1", now.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateRepresentative() #1 error = %v", err)
	}

	var repArticle, evidenceJSON, rulesVersion string
	var score sql.NullFloat64
	err := db.QueryRowContext(ctx, `SELECT representative_article_id, representative_score, representative_evidence_json, representative_rules_version FROM event_clusters WHERE id = 'c1'`).
		Scan(&repArticle, &score, &evidenceJSON, &rulesVersion)
	if err != nil {
		t.Fatalf("select event_clusters: %v", err)
	}
	if repArticle != "a2" {
		t.Errorf("representative_article_id = %q, want %q", repArticle, "a2")
	}
	if !score.Valid || score.Float64 != 0.6 {
		t.Errorf("representative_score = %+v, want 0.6", score)
	}
	if rulesVersion != "rep-v1" {
		t.Errorf("representative_rules_version = %q, want %q", rulesVersion, "rep-v1")
	}

	var historyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_cluster_representative_history WHERE cluster_id = 'c1'`).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if historyCount != 0 {
		t.Errorf("history count after first switch = %d, want 0 (founding representative had no score to archive)", historyCount)
	}

	// Second switch: a2 (now scored) -> a3. This one must be archived.
	if err := repo.UpdateRepresentative(ctx, "c1", "a3", 0.9, []clustering.Evidence{"even more complete"}, "rep-v2", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpdateRepresentative() #2 error = %v", err)
	}

	err = db.QueryRowContext(ctx, `SELECT representative_article_id, representative_score, representative_rules_version FROM event_clusters WHERE id = 'c1'`).
		Scan(&repArticle, &score, &rulesVersion)
	if err != nil {
		t.Fatalf("select event_clusters after 2nd switch: %v", err)
	}
	if repArticle != "a3" || !score.Valid || score.Float64 != 0.9 || rulesVersion != "rep-v2" {
		t.Errorf("after 2nd switch got article=%q score=%+v rules=%q, want a3/0.9/rep-v2", repArticle, score, rulesVersion)
	}

	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_cluster_representative_history WHERE cluster_id = 'c1'`).Scan(&historyCount); err != nil {
		t.Fatalf("count history after 2nd switch: %v", err)
	}
	if historyCount != 1 {
		t.Fatalf("history count after second switch = %d, want 1", historyCount)
	}

	var archivedArticle string
	var archivedScore float64
	var archivedRules string
	err = db.QueryRowContext(ctx, `SELECT article_id, score, rules_version FROM event_cluster_representative_history WHERE cluster_id = 'c1'`).
		Scan(&archivedArticle, &archivedScore, &archivedRules)
	if err != nil {
		t.Fatalf("select history row: %v", err)
	}
	if archivedArticle != "a2" || archivedScore != 0.6 || archivedRules != "rep-v1" {
		t.Errorf("archived history row = article=%q score=%v rules=%q, want a2/0.6/rep-v1", archivedArticle, archivedScore, archivedRules)
	}
}

// TestClusterRepository_FindCandidatesByEventKeys_Bounded is the required
// proof that candidate lookup is bounded by SQL filters (the event_key
// index and a time window/limit), not an application-side scan of every
// article: it seeds many unrelated articles/keys and confirms only the
// matching, in-window, non-excluded ones come back.
func TestClusterRepository_FindCandidatesByEventKeys_Bounded(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	targetKey := "transaction:sign:tavion-dale:shelbyville-sharks:2026-07-20"

	// Noise: 25 unrelated articles/clusters/keys that must never be
	// returned for targetKey.
	for i := 0; i < 25; i++ {
		id := "noise-" + string(rune('a'+i))
		mustSaveArticleWithPublishedAt(t, articles, id, base)
		if err := repo.CreateCluster(ctx, "cluster-"+id, id, clustering.StatusOpen, base); err != nil {
			t.Fatalf("CreateCluster(%s): %v", id, err)
		}
		rep := sampleRepresentation(id)
		if err := repo.SaveRepresentation(ctx, rep, "rules-v1", base); err != nil {
			t.Fatalf("SaveRepresentation(%s): %v", id, err)
		}
		if err := repo.SaveEventKeys(ctx, id, "cluster-"+id, []string{"unrelated-key-" + id}, base); err != nil {
			t.Fatalf("SaveEventKeys(%s): %v", id, err)
		}
	}

	// Matching, in-window article.
	mustSaveArticleWithPublishedAt(t, articles, "match-1", base)
	if err := repo.CreateCluster(ctx, "cluster-match-1", "match-1", clustering.StatusOpen, base); err != nil {
		t.Fatalf("CreateCluster(match-1): %v", err)
	}
	matchRep := sampleRepresentation("match-1")
	if err := repo.SaveRepresentation(ctx, matchRep, "rules-v1", base); err != nil {
		t.Fatalf("SaveRepresentation(match-1): %v", err)
	}
	if err := repo.SaveEventKeys(ctx, "match-1", "cluster-match-1", []string{targetKey, "weaker-key"}, base); err != nil {
		t.Fatalf("SaveEventKeys(match-1): %v", err)
	}

	// Matching key, but published before the window: must be excluded.
	tooOld := base.Add(-30 * 24 * time.Hour)
	mustSaveArticleWithPublishedAt(t, articles, "too-old", tooOld)
	if err := repo.CreateCluster(ctx, "cluster-too-old", "too-old", clustering.StatusOpen, tooOld); err != nil {
		t.Fatalf("CreateCluster(too-old): %v", err)
	}
	if err := repo.SaveEventKeys(ctx, "too-old", "cluster-too-old", []string{targetKey}, tooOld); err != nil {
		t.Fatalf("SaveEventKeys(too-old): %v", err)
	}

	// Matching key, but the caller's own article: must be excluded via
	// excludeArticleID.
	mustSaveArticleWithPublishedAt(t, articles, "self", base)
	if err := repo.CreateCluster(ctx, "cluster-self", "self", clustering.StatusOpen, base); err != nil {
		t.Fatalf("CreateCluster(self): %v", err)
	}
	if err := repo.SaveEventKeys(ctx, "self", "cluster-self", []string{targetKey}, base); err != nil {
		t.Fatalf("SaveEventKeys(self): %v", err)
	}

	windowStart := base.Add(-7 * 24 * time.Hour)
	got, err := repo.FindCandidatesByEventKeys(ctx, []string{targetKey}, windowStart, "self", 10)
	if err != nil {
		t.Fatalf("FindCandidatesByEventKeys() error = %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("FindCandidatesByEventKeys() returned %d candidates, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.ArticleID != "match-1" {
		t.Errorf("ArticleID = %q, want %q", c.ArticleID, "match-1")
	}
	if c.ClusterID != "cluster-match-1" {
		t.Errorf("ClusterID = %q, want %q", c.ClusterID, "cluster-match-1")
	}
	if c.Representation.ArticleID != "match-1" {
		t.Errorf("Representation.ArticleID = %q, want %q", c.Representation.ArticleID, "match-1")
	}
	if c.Representation.PrimaryEntity != matchRep.PrimaryEntity {
		t.Errorf("Representation.PrimaryEntity = %q, want %q", c.Representation.PrimaryEntity, matchRep.PrimaryEntity)
	}
	if c.DuplicateRelation != "" || c.DuplicateScore != 0 {
		t.Errorf("DuplicateRelation/DuplicateScore = %q/%v, want zero values (this repository has no dedup opinion)", c.DuplicateRelation, c.DuplicateScore)
	}
}

// TestClusterRepository_FindCandidatesByEventKeys_LimitBounds confirms the
// limit argument caps the result set even when more rows match.
func TestClusterRepository_FindCandidatesByEventKeys_LimitBounds(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	key := "shared-key"
	for i := 0; i < 5; i++ {
		id := "art-" + string(rune('a'+i))
		mustSaveArticleWithPublishedAt(t, articles, id, base)
		if err := repo.CreateCluster(ctx, "cluster-"+id, id, clustering.StatusOpen, base); err != nil {
			t.Fatalf("CreateCluster(%s): %v", id, err)
		}
		rep := sampleRepresentation(id)
		if err := repo.SaveRepresentation(ctx, rep, "rules-v1", base); err != nil {
			t.Fatalf("SaveRepresentation(%s): %v", id, err)
		}
		if err := repo.SaveEventKeys(ctx, id, "cluster-"+id, []string{key}, base); err != nil {
			t.Fatalf("SaveEventKeys(%s): %v", id, err)
		}
	}

	got, err := repo.FindCandidatesByEventKeys(ctx, []string{key}, base.Add(-time.Hour), "", 2)
	if err != nil {
		t.Fatalf("FindCandidatesByEventKeys() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("FindCandidatesByEventKeys() returned %d candidates, want 2 (limit)", len(got))
	}
}

// TestClusterRepository_FindCandidatesByEventKeys_BigClusterDoesNotCrowdOutOthers
// guards the saturation seen in production (2026-10-25: 352 of 635 create
// decisions had a nearly full candidate list): a cluster with many members
// matching a shared key used to take every slot, so an older cluster of the
// same story never reached clustering.Compare.
func TestClusterRepository_FindCandidatesByEventKeys_BigClusterDoesNotCrowdOutOthers(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	key := "shared-key"
	add := func(id, clusterID string, at time.Time, first bool) {
		t.Helper()
		mustSaveArticleWithPublishedAt(t, articles, id, at)
		if first {
			if err := repo.CreateCluster(ctx, clusterID, id, clustering.StatusOpen, at); err != nil {
				t.Fatalf("CreateCluster(%s): %v", clusterID, err)
			}
		} else if err := repo.AddMember(ctx, clusterID, id, clustering.MemberRoleRelatedCoverage, 0.8, at); err != nil {
			t.Fatalf("AddMember(%s): %v", id, err)
		}
		if err := repo.SaveRepresentation(ctx, sampleRepresentation(id), "rules-v1", at); err != nil {
			t.Fatalf("SaveRepresentation(%s): %v", id, err)
		}
		if err := repo.SaveEventKeys(ctx, id, clusterID, []string{key}, at); err != nil {
			t.Fatalf("SaveEventKeys(%s): %v", id, err)
		}
	}
	// Two older single-article clusters, then a big cluster that is newer.
	add("old-1", "cluster-old-1", base, true)
	add("old-2", "cluster-old-2", base.Add(time.Minute), true)
	for i := 0; i < 8; i++ {
		add(fmt.Sprintf("big-%d", i), "cluster-big", base.Add(time.Duration(10+i)*time.Minute), i == 0)
	}

	got, err := repo.FindCandidatesByEventKeys(ctx, []string{key}, base.Add(-time.Hour), "", 5)
	if err != nil {
		t.Fatalf("FindCandidatesByEventKeys() error = %v", err)
	}
	perCluster := map[string]int{}
	for _, c := range got {
		perCluster[c.ClusterID]++
	}
	if perCluster["cluster-old-1"] != 1 || perCluster["cluster-old-2"] != 1 {
		t.Errorf("candidates per cluster = %v, want both older clusters represented", perCluster)
	}
	if perCluster["cluster-big"] > 3 {
		t.Errorf("cluster-big supplied %d candidates, want at most 3", perCluster["cluster-big"])
	}
}

func mustCreateClusterAt(t *testing.T, repo *sqliteadapter.ClusterRepository, articles *sqliteadapter.ArticleRepository, clusterID, articleID string, lastUpdatedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	mustSaveArticle(t, articles, articleID)
	if err := repo.CreateCluster(ctx, clusterID, articleID, clustering.StatusOpen, lastUpdatedAt); err != nil {
		t.Fatalf("CreateCluster(%s): %v", clusterID, err)
	}
}

func TestClusterRepository_CloseInactiveBatch_ClosesOnlyEligibleClusters(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()

	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-96 * time.Hour)

	mustCreateClusterAt(t, repo, articles, "old-1", "a1", cutoff.Add(-time.Hour))
	mustCreateClusterAt(t, repo, articles, "old-2", "a2", cutoff)
	mustCreateClusterAt(t, repo, articles, "recent", "a3", cutoff.Add(time.Hour))

	closed, err := repo.CloseInactiveBatch(ctx, cutoff, "inactivity_ttl", 10, now)
	if err != nil {
		t.Fatalf("CloseInactiveBatch() error = %v", err)
	}
	if closed != 2 {
		t.Fatalf("CloseInactiveBatch() = %d, want 2 (old-1 and old-2, at/past the inclusive boundary)", closed)
	}

	for _, id := range []string{"old-1", "old-2"} {
		var status, closeReason string
		var closedAt sql.NullTime
		if err := db.QueryRowContext(ctx, `SELECT status, closed_at, close_reason FROM event_clusters WHERE id = ?`, id).Scan(&status, &closedAt, &closeReason); err != nil {
			t.Fatalf("select %s: %v", id, err)
		}
		if status != "closed" {
			t.Errorf("%s status = %q, want closed", id, status)
		}
		if !closedAt.Valid || !closedAt.Time.Equal(now) {
			t.Errorf("%s closed_at = %v, want %v", id, closedAt, now)
		}
		if closeReason != "inactivity_ttl" {
			t.Errorf("%s close_reason = %q, want inactivity_ttl", id, closeReason)
		}
	}

	var recentStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM event_clusters WHERE id = 'recent'`).Scan(&recentStatus); err != nil {
		t.Fatalf("select recent: %v", err)
	}
	if recentStatus != "open" {
		t.Errorf("recent status = %q, want open (one hour inside the cutoff)", recentStatus)
	}
}

func TestClusterRepository_CloseInactiveBatch_RespectsBatchSize(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()

	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-96 * time.Hour)
	for i := 0; i < 5; i++ {
		id := "c" + string(rune('a'+i))
		mustCreateClusterAt(t, repo, articles, id, id+"-art", cutoff.Add(-time.Hour))
	}

	closed, err := repo.CloseInactiveBatch(ctx, cutoff, "inactivity_ttl", 2, now)
	if err != nil {
		t.Fatalf("CloseInactiveBatch() error = %v", err)
	}
	if closed != 2 {
		t.Fatalf("CloseInactiveBatch() = %d, want 2 (batch size cap)", closed)
	}

	var closedCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_clusters WHERE status = 'closed'`).Scan(&closedCount); err != nil {
		t.Fatalf("count closed: %v", err)
	}
	if closedCount != 2 {
		t.Errorf("closed row count = %d, want 2", closedCount)
	}

	// A second call closes the remaining 3 — proves the job is resumable
	// with no extra state: the same eligibility predicate just matches
	// fewer rows each time.
	closed, err = repo.CloseInactiveBatch(ctx, cutoff, "inactivity_ttl", 10, now)
	if err != nil {
		t.Fatalf("CloseInactiveBatch() second call error = %v", err)
	}
	if closed != 3 {
		t.Errorf("CloseInactiveBatch() second call = %d, want 3 (the rest)", closed)
	}
}

// TestClusterRepository_Stats uses a 25-cluster age ladder (last_updated_at
// = now - 1h, now - 2h, ..., now - 25h) rather than the smaller fixture an
// earlier version of this test used: with only 5 samples,
// int(0.95*5)==4==len-1, so a correct nearest-rank p95 and a buggy
// implementation that just returns max are indistinguishable. With 25
// samples, p95 (index 23 -> the 24h-old cluster) and max (25h) land on
// different clusters, so this fixture actually exercises the percentile
// logic. See cluster_repository.go's Stats doc comment for the query's
// nearest-rank derivation (OFFSET CAST(0.95 * COUNT(*) AS INT) into ages
// ordered ascending via ORDER BY last_updated_at DESC).
func TestClusterRepository_Stats(t *testing.T) {
	db := newTestDB(t)
	articles := sqliteadapter.NewArticleRepository(db)
	repo := sqliteadapter.NewClusterRepository(db)
	ctx := context.Background()

	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	// The age ladder: 25 open clusters with distinct, known
	// last_updated_at values spanning 1h..25h ago. mustCreateClusterAt's
	// lastUpdatedAt argument is what CreateCluster stores as both
	// first_seen_at and last_updated_at — only last_updated_at matters
	// for this metric now.
	const ladderSize = 25
	for i := 1; i <= ladderSize; i++ {
		id := "ladder-" + itoa(i)
		mustCreateClusterAt(t, repo, articles, id, id+"-art", now.Add(-time.Duration(i)*time.Hour))
	}

	// Extra fixtures for OpenMemberCountMax/ReopenedTotal/ClosedCount,
	// kept at last_updated_at values in the low seconds/minutes — well
	// outside the ladder's 1h..25h range — so they cannot become the
	// true max or p95 and don't have to be accounted for in the
	// hand-computed expectations below.
	mustCreateClusterAt(t, repo, articles, "member-max", "member-max-art", now.Add(-2*time.Minute))
	mustSaveArticle(t, articles, "member-max-second")
	if err := repo.AddMember(ctx, "member-max", "member-max-second", clustering.MemberRoleConfirmation, 0.9, now.Add(-time.Second)); err != nil {
		t.Fatalf("AddMember (member-max): %v", err)
	}

	mustCreateClusterAt(t, repo, articles, "reopen-1", "reopen-1-art", now.Add(-3*time.Minute))
	if _, err := db.ExecContext(ctx, `UPDATE event_clusters SET status = 'closed', closed_at = ? WHERE id = 'reopen-1'`, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("close reopen-1: %v", err)
	}
	mustSaveArticle(t, articles, "reopen-1-second")
	if err := repo.AddMember(ctx, "reopen-1", "reopen-1-second", clustering.MemberRoleConfirmation, 0.9, now.Add(-2*time.Second)); err != nil {
		t.Fatalf("AddMember (reopen reopen-1): %v", err)
	}

	// One permanently closed cluster (must not count toward OpenCount or
	// open-cluster ages at all).
	mustCreateClusterAt(t, repo, articles, "closed-1", "closed-1-art", now.Add(-10*time.Minute))
	if _, err := db.ExecContext(ctx, `UPDATE event_clusters SET status = 'closed', closed_at = ? WHERE id = 'closed-1'`, now); err != nil {
		t.Fatalf("close closed-1: %v", err)
	}

	stats, err := repo.Stats(ctx, now)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}

	const wantOpenCount = ladderSize + 2 // ladder + member-max + reopen-1 (closed-1 excluded)
	if stats.OpenCount != wantOpenCount {
		t.Errorf("OpenCount = %d, want %d", stats.OpenCount, wantOpenCount)
	}
	if stats.ClosedCount != 1 {
		t.Errorf("ClosedCount = %d, want 1", stats.ClosedCount)
	}
	if stats.OpenMemberCountMax != 2 {
		t.Errorf("OpenMemberCountMax = %d, want 2 (member-max and reopen-1 both have 2 members)", stats.OpenMemberCountMax)
	}
	if stats.ReopenedTotal != 1 {
		t.Errorf("ReopenedTotal = %d, want 1 (only reopen-1 was closed then reopened)", stats.ReopenedTotal)
	}

	// Hand-derivation: 27 open clusters total (25 ladder + member-max +
	// reopen-1). Sorted by age ascending (youngest first, matching ORDER
	// BY last_updated_at DESC): [~1s (member-max), ~2s (reopen-1), 1h,
	// 2h, ..., 25h (the ladder, in order)]. Max age is unambiguously the
	// ladder's oldest entry: 25h. p95's OFFSET is
	// CAST(0.95*27 AS INT) = CAST(25.65 AS INT) = 25; the row at
	// zero-indexed offset 25 in that 27-element ascending list is the
	// ladder's 24h entry (index 0-1 are the two extras, index k+1 is the
	// ladder's k-hour entry, so index 25 -> k=24).
	wantMaxAge := (time.Duration(ladderSize) * time.Hour).Seconds()
	if stats.OpenAgeMaxSeconds < wantMaxAge-1 || stats.OpenAgeMaxSeconds > wantMaxAge+1 {
		t.Errorf("OpenAgeMaxSeconds = %v, want ~%v", stats.OpenAgeMaxSeconds, wantMaxAge)
	}
	wantP95Age := (24 * time.Hour).Seconds()
	if stats.OpenAgeP95Seconds < wantP95Age-1 || stats.OpenAgeP95Seconds > wantP95Age+1 {
		t.Errorf("OpenAgeP95Seconds = %v, want ~%v", stats.OpenAgeP95Seconds, wantP95Age)
	}
	// The n=25 ladder alone is enough to prove p95 != max (the bug this
	// fixture exists to catch: a buggy implementation that just returns
	// max for both).
	if stats.OpenAgeP95Seconds >= stats.OpenAgeMaxSeconds {
		t.Errorf("OpenAgeP95Seconds = %v, want strictly less than OpenAgeMaxSeconds = %v", stats.OpenAgeP95Seconds, stats.OpenAgeMaxSeconds)
	}
}

func TestClusterRepository_Stats_EmptyDatabase(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteadapter.NewClusterRepository(db)
	stats, err := repo.Stats(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats != (out.ClusterLifecycleStats{}) {
		t.Errorf("Stats() on empty DB = %+v, want zero value", stats)
	}
}
