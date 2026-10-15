package sqliteentityproposal_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

var migratedTestDBTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-sqliteentityproposal-test-template-")
	if err != nil {
		panic("create SQLite test template directory: " + err.Error())
	}

	migratedTestDBTemplate = filepath.Join(templateDir, "migrated.db")
	db, err := infrasqlite.Open(migratedTestDBTemplate, 5*time.Second)
	if err != nil {
		_ = os.RemoveAll(templateDir)
		panic("open SQLite test template: " + err.Error())
	}
	if err := infrasqlite.Migrate(context.Background(), db, migrations.FS); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("migrate SQLite test template: " + err.Error())
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(templateDir)
		panic("checkpoint SQLite test template: " + err.Error())
	}
	if err := db.Close(); err != nil {
		_ = os.RemoveAll(templateDir)
		panic("close SQLite test template: " + err.Error())
	}

	exitCode := m.Run()
	_ = os.RemoveAll(templateDir)
	os.Exit(exitCode)
}

// newTestDB copies the migrated template database into a fresh temp file
// per test and opens it, guaranteeing each test starts from an
// already-migrated, empty database.
func newTestDB(t testing.TB) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	copyMigratedTestDB(t, path)

	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func copyMigratedTestDB(t testing.TB, path string) {
	t.Helper()
	source, err := os.Open(migratedTestDBTemplate)
	if err != nil {
		t.Fatalf("open migrated SQLite test template: %v", err)
	}
	defer source.Close()

	destination, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create SQLite test database: %v", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatalf("copy migrated SQLite test template: %v", err)
	}
	if err := destination.Close(); err != nil {
		t.Fatalf("close copied SQLite test database: %v", err)
	}
}

func TestEntityProposalRepository_UpsertInsertsNewPendingProposal(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Jai'Rene' Mossbank", NormalizedName: "jairene mossbank",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{"position": "CB"}, Confidence: 0.9, Occurrences: 1,
		Aliases: []string{"Mossbank"}, Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "Isotopes sign CB Jai'Rene' Mossbank"}},
		Status: entityproposal.StatusPending, FirstSeen: now, LastSeen: now,
		ExtractorVersion: "entity-discovery-v1", CreatedAt: now,
	}
	id, err := repo.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.CanonicalName != p.CanonicalName || got.Occurrences != 1 || got.Metadata["position"] != "CB" {
		t.Fatalf("Get() = %+v, want a round trip of the inserted proposal", got)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "Mossbank" {
		t.Fatalf("Get().Aliases = %v, want [Mossbank]", got.Aliases)
	}
	if len(got.Sources) != 1 || got.Sources[0].ArticleID != "a1" {
		t.Fatalf("Get().Sources = %v, want one source a1", got.Sources)
	}
}

func TestEntityProposalRepository_UpsertMergesIntoExistingPendingProposal(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	day1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	first := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Jai'Rene' Mossbank", NormalizedName: "jairene mossbank",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Confidence: 0.8, Occurrences: 1, Aliases: []string{"Mossbank"},
		Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "t1"}},
		Status:  entityproposal.StatusPending, FirstSeen: day1, LastSeen: day1,
		ExtractorVersion: "entity-discovery-v1", CreatedAt: day1,
	}
	id1, err := repo.Upsert(ctx, first)
	if err != nil {
		t.Fatalf("first Upsert() error = %v", err)
	}

	second := first
	second.ID = "p2" // a different generated id from a later run — must still merge by identity
	second.Confidence = 0.95
	second.Occurrences = 2
	second.Aliases = []string{"J. Mossbank"}
	second.Sources = []entityproposal.Source{{ArticleID: "a2", ArticleTitle: "t2"}}
	second.FirstSeen, second.LastSeen = day2, day2
	id2, err := repo.Upsert(ctx, second)
	if err != nil {
		t.Fatalf("second Upsert() error = %v", err)
	}
	if id2 != id1 {
		t.Fatalf("second Upsert() id = %q, want the first proposal's id %q (merge, not a new row)", id2, id1)
	}

	got, err := repo.Get(ctx, id1)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Occurrences != 3 {
		t.Fatalf("Occurrences = %d, want 3 (1+2)", got.Occurrences)
	}
	if got.Confidence != 0.95 {
		t.Fatalf("Confidence = %v, want the higher value 0.95", got.Confidence)
	}
	if !got.FirstSeen.Equal(day1) || !got.LastSeen.Equal(day2) {
		t.Fatalf("FirstSeen/LastSeen = %v/%v, want %v/%v", got.FirstSeen, got.LastSeen, day1, day2)
	}
	wantAliases := map[string]bool{"Mossbank": true, "J. Mossbank": true}
	if len(got.Aliases) != 2 {
		t.Fatalf("Aliases = %v, want both Mossbank and J. Mossbank", got.Aliases)
	}
	for _, a := range got.Aliases {
		if !wantAliases[a] {
			t.Errorf("unexpected alias %q", a)
		}
	}
	if len(got.Sources) != 2 {
		t.Fatalf("Sources = %v, want both a1 and a2", got.Sources)
	}

	all, err := repo.List(ctx, "", "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List() returned %d proposals, want exactly 1 (no duplicate row from the merge)", len(all))
	}
}

func TestEntityProposalRepository_UpsertDoesNotMergeIntoDecidedProposal(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Pierce Quenby", NormalizedName: "pierce quenby",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Confidence: 0.9, Occurrences: 1, Status: entityproposal.StatusPending,
		FirstSeen: now, LastSeen: now, ExtractorVersion: "entity-discovery-v1", CreatedAt: now,
	}
	id, err := repo.Upsert(ctx, p)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := repo.UpdateStatus(ctx, id, entityproposal.StatusRejected, now); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}

	second := p
	second.ID = "p2"
	id2, err := repo.Upsert(ctx, second)
	if err != nil {
		t.Fatalf("second Upsert() error = %v", err)
	}
	if id2 == id {
		t.Fatalf("second Upsert() merged into a rejected proposal — a rejected candidate must not silently reappear as approved/pending via merge")
	}
}

func TestEntityProposalRepository_GetReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	_, err := repo.Get(context.Background(), "does-not-exist")
	if !errors.Is(err, entityproposal.ErrNotFound) {
		t.Fatalf("Get() error = %v, want errors.Is(err, entityproposal.ErrNotFound)", err)
	}
}

func TestEntityProposalRepository_ListFiltersByTypeAndStatus(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(id string, et entityproposal.EntityType, status entityproposal.Status) entityproposal.Proposal {
		return entityproposal.Proposal{
			ID: id, CanonicalName: id, NormalizedName: id, EntityType: et, Kind: entityproposal.KindNewEntity,
			Status: status, FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
		}
	}
	for _, p := range []entityproposal.Proposal{
		mk("player-pending", entityproposal.EntityTypePlayer, entityproposal.StatusPending),
		mk("coach-pending", entityproposal.EntityTypeCoach, entityproposal.StatusPending),
	} {
		if _, err := repo.Upsert(ctx, p); err != nil {
			t.Fatalf("Upsert(%s) error = %v", p.ID, err)
		}
	}
	approved := mk("player-approved", entityproposal.EntityTypePlayer, entityproposal.StatusPending)
	id, err := repo.Upsert(ctx, approved)
	if err != nil {
		t.Fatalf("Upsert(player-approved) error = %v", err)
	}
	if err := repo.UpdateStatus(ctx, id, entityproposal.StatusApproved, now); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}

	players, err := repo.List(ctx, "player", "pending")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(players) != 1 || players[0].CanonicalName != "player-pending" {
		t.Fatalf("List(player, pending) = %+v, want exactly [player-pending]", players)
	}

	allPending, err := repo.List(ctx, "", "pending")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(allPending) != 2 {
		t.Fatalf("List(\"\", pending) returned %d, want 2", len(allPending))
	}
}

func TestEntityProposalRepository_FilterAndMarkProcessedArticles(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	unprocessed, err := repo.FilterUnprocessed(ctx, "v1", []string{"a1", "a2", "a3"})
	if err != nil {
		t.Fatalf("FilterUnprocessed() error = %v", err)
	}
	sort.Strings(unprocessed)
	if len(unprocessed) != 3 {
		t.Fatalf("FilterUnprocessed() = %v, want all 3 unprocessed", unprocessed)
	}

	if err := repo.MarkArticlesProcessed(ctx, "v1", []string{"a1", "a2"}, now); err != nil {
		t.Fatalf("MarkArticlesProcessed() error = %v", err)
	}

	unprocessed, err = repo.FilterUnprocessed(ctx, "v1", []string{"a1", "a2", "a3"})
	if err != nil {
		t.Fatalf("FilterUnprocessed() error = %v", err)
	}
	if len(unprocessed) != 1 || unprocessed[0] != "a3" {
		t.Fatalf("FilterUnprocessed() = %v, want only [a3]", unprocessed)
	}

	// A different extractor_version has never processed anything.
	unprocessedOtherVersion, err := repo.FilterUnprocessed(ctx, "v2", []string{"a1"})
	if err != nil {
		t.Fatalf("FilterUnprocessed() error = %v", err)
	}
	if len(unprocessedOtherVersion) != 1 {
		t.Fatalf("FilterUnprocessed() for a different version = %v, want [a1] (versions are independent)", unprocessedOtherVersion)
	}

	// Idempotent: marking an already-processed id again does not error.
	if err := repo.MarkArticlesProcessed(ctx, "v1", []string{"a1"}, now); err != nil {
		t.Fatalf("MarkArticlesProcessed() re-mark error = %v", err)
	}
}

func TestEntityProposalRepository_FindByNormalizedName(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	if _, found, err := repo.FindByNormalizedName(ctx, "nobody here", entityproposal.EntityTypePlayer); err != nil || found {
		t.Fatalf("FindByNormalizedName() on empty table = (found=%v, err=%v), want (false, nil)", found, err)
	}

	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Random Newcomer", NormalizedName: "random newcomer",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{}, Confidence: 0.8, Occurrences: 1,
		FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
		Source: entityproposal.ProposalSourceOnlineFallback,
	}
	if _, err := repo.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	found, ok, err := repo.FindByNormalizedName(ctx, "random newcomer", entityproposal.EntityTypePlayer)
	if err != nil || !ok {
		t.Fatalf("FindByNormalizedName() = (found=%v, err=%v), want (true, nil)", ok, err)
	}
	if found.CanonicalName != "Random Newcomer" || found.Source != entityproposal.ProposalSourceOnlineFallback {
		t.Fatalf("FindByNormalizedName() = %+v, want CanonicalName=Random Newcomer Source=online_fallback", found)
	}

	if _, ok, err := repo.FindByNormalizedName(ctx, "random newcomer", entityproposal.EntityTypeCoach); err != nil || ok {
		t.Fatalf("FindByNormalizedName() wrong entity_type = (found=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestEntityProposalRepository_FindByNormalizedName_PrefersApprovedOverOtherStatuses(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	older := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	// Insert distinct rows directly (Upsert's pending-only unique index
	// would otherwise merge same-identity pending rows) with different
	// statuses sharing normalized_name+entity_type, to exercise the
	// deterministic tie-break rather than SQLite's arbitrary row order.
	insertProposalRow(t, db, "rejected-1", "Random Newcomer", "random newcomer", entityproposal.EntityTypePlayer, entityproposal.StatusRejected, newer)
	insertProposalRow(t, db, "approved-1", "Random Newcomer", "random newcomer", entityproposal.EntityTypePlayer, entityproposal.StatusApproved, older)

	got, ok, err := repo.FindByNormalizedName(ctx, "random newcomer", entityproposal.EntityTypePlayer)
	if err != nil || !ok {
		t.Fatalf("FindByNormalizedName() = (found=%v, err=%v), want (true, nil)", ok, err)
	}
	if got.ID != "approved-1" || got.Status != entityproposal.StatusApproved {
		t.Fatalf("FindByNormalizedName() = %+v, want the approved row even though the rejected row is newer", got)
	}
}

func TestEntityProposalRepository_FindByAlias(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	p := entityproposal.Proposal{
		ID: "p1", CanonicalName: "Random Newcomer", NormalizedName: "random newcomer",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{}, Confidence: 0.8, Occurrences: 1,
		Aliases:   []string{"sign random newcomer"},
		FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
		Source: entityproposal.ProposalSourceOnlineFallback,
	}
	if _, err := repo.Upsert(ctx, p); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	found, ok, err := repo.FindByAlias(ctx, "sign random newcomer", entityproposal.EntityTypePlayer)
	if err != nil || !ok {
		t.Fatalf("FindByAlias() = (found=%v, err=%v), want (true, nil)", ok, err)
	}
	if found.CanonicalName != "Random Newcomer" {
		t.Fatalf("FindByAlias() = %+v, want CanonicalName=Random Newcomer", found)
	}

	if _, ok, err := repo.FindByAlias(ctx, "totally different span", entityproposal.EntityTypePlayer); err != nil || ok {
		t.Fatalf("FindByAlias() non-matching alias = (found=%v, err=%v), want (false, nil)", ok, err)
	}

	if _, ok, err := repo.FindByAlias(ctx, "sign random newcomer", entityproposal.EntityTypeCoach); err != nil || ok {
		t.Fatalf("FindByAlias() wrong entity_type = (found=%v, err=%v), want (false, nil)", ok, err)
	}
}

// insertProposalRow inserts a minimal entity_proposals row directly,
// bypassing Upsert's pending-only merge/unique-index logic, so tests can
// set up two rows sharing normalized_name+entity_type with different
// statuses (which Upsert alone cannot produce for 'pending' rows, and
// has no path to create a 'rejected'/'approved' row from at all).
func insertProposalRow(t testing.TB, db *sql.DB, id, canonicalName, normalizedName string, entityType entityproposal.EntityType, status entityproposal.Status, createdAt time.Time) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO entity_proposals (
			id, canonical_name, normalized_name, entity_type, kind, target_entity_id,
			metadata_json, confidence, occurrences, status, first_seen, last_seen,
			extractor_version, created_at, source
		) VALUES (?,?,?,?,?,?, ?,?,?,?,?,?, ?,?,?)
	`, id, canonicalName, normalizedName, string(entityType), string(entityproposal.KindNewEntity), "",
		"{}", 0.8, 1, string(status), createdAt.UTC().Format(time.RFC3339Nano), createdAt.UTC().Format(time.RFC3339Nano),
		"v1", createdAt.UTC().Format(time.RFC3339Nano), string(entityproposal.ProposalSourceOnlineFallback))
	if err != nil {
		t.Fatalf("insert proposal row directly: %v", err)
	}
}

func TestUpsert_PersistsAndMergesWebSources(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	first := entityproposal.Proposal{
		CanonicalName: "Jai'Rene' Mossbank", NormalizedName: "jairene mossbank",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{}, Confidence: 0.9, Occurrences: 1,
		Sources:    []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "t1"}},
		WebSources: []entityproposal.WebSource{{Domain: "isotopes.example.com", URL: "https://isotopes.example.com/news/1", Title: "Isotopes Add CB Jai'Rene' Mossbank"}},
		FirstSeen:  now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
	id, err := repo.Upsert(ctx, first)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	second := first
	second.ID = ""
	second.WebSources = []entityproposal.WebSource{{Domain: "league.example.com", URL: "https://league.example.com/players/2", Title: "Player Profile"}}
	if _, err := repo.Upsert(ctx, second); err != nil {
		t.Fatalf("second Upsert() error = %v", err)
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(got.WebSources) != 2 {
		t.Fatalf("WebSources = %+v, want 2 entries merged across both Upsert calls", got.WebSources)
	}
	domains := map[string]bool{}
	for _, ws := range got.WebSources {
		domains[ws.Domain] = true
	}
	if !domains["isotopes.example.com"] || !domains["league.example.com"] {
		t.Fatalf("WebSources domains = %+v, want both isotopes.example.com and league.example.com", domains)
	}
}

func newVerifyProposal(now time.Time) entityproposal.Proposal {
	return entityproposal.Proposal{
		ID: "v1", CanonicalName: "Jane Doe", NormalizedName: "jane doe",
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{"position": "QB"}, Confidence: 0.5, Occurrences: 1,
		Aliases:   []string{"Doe"},
		Sources:   []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "t"}},
		Status:    entityproposal.StatusPending,
		FirstSeen: now, LastSeen: now, ExtractorVersion: "v1", CreatedAt: now,
	}
}

func TestEntityProposalRepository_UpdateVerificationRewritesFields(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, err := repo.Upsert(ctx, newVerifyProposal(now))
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	upd := newVerifyProposal(now)
	upd.ID = id
	upd.CanonicalName = "Jane Q. Doe"
	upd.NormalizedName = "jane q doe"
	upd.EntityType = entityproposal.EntityTypeCoach
	upd.Metadata = map[string]string{"role": "OC", "team_id": "spr"}
	upd.Confidence = 0.95
	upd.Aliases = []string{"Coach Doe"}
	upd.WebSources = []entityproposal.WebSource{{Domain: "league.example.com", URL: "https://league.example.com/x", Title: "X"}}
	for i := 0; i < 2; i++ {
		if err := repo.UpdateVerification(ctx, upd); err != nil {
			t.Fatalf("UpdateVerification() #%d error = %v", i, err)
		}
	}
	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.EntityType != entityproposal.EntityTypeCoach || got.CanonicalName != "Jane Q. Doe" || got.NormalizedName != "jane q doe" ||
		got.Metadata["role"] != "OC" || got.Metadata["position"] != "" || got.Confidence != 0.95 || got.Status != entityproposal.StatusPending {
		t.Fatalf("Get() = %+v", got)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "Coach Doe" || len(got.WebSources) != 1 {
		t.Fatalf("aliases=%v web=%v, want [Coach Doe] and 1 web source (replaced, not duplicated)", got.Aliases, got.WebSources)
	}
	if !got.DecidedAt.IsZero() {
		t.Fatalf("DecidedAt = %v, want zero for pending", got.DecidedAt)
	}
}

func TestEntityProposalRepository_UpdateVerificationRejectedRoundTrips(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, _ := repo.Upsert(ctx, newVerifyProposal(now))

	upd := newVerifyProposal(now)
	upd.ID = id
	upd.Status = entityproposal.StatusRejected
	upd.DecisionReason = entityproposal.ReasonAutoRejectedNonNFL
	if err := repo.UpdateVerification(ctx, upd); err != nil {
		t.Fatalf("UpdateVerification() error = %v", err)
	}
	pending, _ := repo.List(ctx, "", "pending")
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want none", pending)
	}
	rejected, _ := repo.List(ctx, "", "rejected")
	if len(rejected) != 1 || rejected[0].DecisionReason != entityproposal.ReasonAutoRejectedNonNFL || rejected[0].DecidedAt.IsZero() {
		t.Fatalf("rejected = %+v, want one with reason and decided_at", rejected)
	}
}

func TestEntityProposalRepository_UpdateVerificationCollisionReturnsError(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	a := newVerifyProposal(now)
	id, _ := repo.Upsert(ctx, a)
	b := newVerifyProposal(now)
	b.ID, b.EntityType = "v2", entityproposal.EntityTypeCoach
	if _, err := repo.Upsert(ctx, b); err != nil {
		t.Fatalf("Upsert(b) error = %v", err)
	}

	upd := a
	upd.ID = id
	upd.EntityType = entityproposal.EntityTypeCoach
	upd.Aliases = []string{"Changed"}
	err := repo.UpdateVerification(ctx, upd)
	if !errors.Is(err, entityproposal.ErrConflict) {
		t.Fatalf("UpdateVerification() error = %v, want ErrConflict", err)
	}
	got, _ := repo.Get(ctx, id)
	if got.EntityType != entityproposal.EntityTypePlayer || len(got.Aliases) != 1 || got.Aliases[0] != "Doe" {
		t.Fatalf("row corrupted after failed update: %+v", got)
	}
}

func TestEntityProposalRepository_UpdateVerificationNotFound(t *testing.T) {
	repo := sqliteentityproposal.NewEntityProposalRepository(newTestDB(t))
	p := newVerifyProposal(time.Now())
	if err := repo.UpdateVerification(context.Background(), p); !errors.Is(err, entityproposal.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestEntityProposalRepository_UpsertDoesNotResurrectAutoRejectedNonNFL(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, _ := repo.Upsert(ctx, newVerifyProposal(now))
	upd := newVerifyProposal(now)
	upd.ID = id
	upd.Status = entityproposal.StatusRejected
	upd.DecisionReason = entityproposal.ReasonAutoRejectedNonNFL
	if err := repo.UpdateVerification(ctx, upd); err != nil {
		t.Fatal(err)
	}

	again := newVerifyProposal(now)
	again.ID = "other"
	gotID, err := repo.Upsert(ctx, again)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if gotID != id {
		t.Fatalf("Upsert() id = %q, want rejected row %q", gotID, id)
	}
	pending, _ := repo.List(ctx, "", "pending")
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want none (not resurrected)", pending)
	}
}

func TestEntityProposalRepository_UpsertStillRequeuesManuallyRejected(t *testing.T) {
	db := newTestDB(t)
	repo := sqliteentityproposal.NewEntityProposalRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, _ := repo.Upsert(ctx, newVerifyProposal(now))
	if err := repo.UpdateStatus(ctx, id, entityproposal.StatusRejected, now); err != nil {
		t.Fatal(err)
	}
	again := newVerifyProposal(now)
	again.ID = "other"
	if _, err := repo.Upsert(ctx, again); err != nil {
		t.Fatal(err)
	}
	pending, _ := repo.List(ctx, "", "pending")
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1 (existing behavior for manual rejects)", len(pending))
	}
}

func TestEntityProposalRepository_UpdateVerificationCannotResurrectAutoRejected(t *testing.T) {
	repo := sqliteentityproposal.NewEntityProposalRepository(newTestDB(t))
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, _ := repo.Upsert(ctx, newVerifyProposal(now))
	rej := newVerifyProposal(now)
	rej.ID, rej.Status, rej.DecisionReason = id, entityproposal.StatusRejected, entityproposal.ReasonAutoRejectedNonNFL
	if err := repo.UpdateVerification(ctx, rej); err != nil {
		t.Fatal(err)
	}
	keep := newVerifyProposal(now)
	keep.ID = id
	if err := repo.UpdateVerification(ctx, keep); !errors.Is(err, entityproposal.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	got, _ := repo.Get(ctx, id)
	if got.Status != entityproposal.StatusRejected || got.DecisionReason != entityproposal.ReasonAutoRejectedNonNFL {
		t.Fatalf("row = %+v, want still auto-rejected", got)
	}
}

// Upsert -> Get -> ApplyVerification -> UpdateVerification against the real
// repo must keep what an earlier run accumulated.
func TestEntityProposalRepository_VerificationSequencePreservesMergedState(t *testing.T) {
	repo := sqliteentityproposal.NewEntityProposalRepository(newTestDB(t))
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	first := newVerifyProposal(now)
	first.Aliases = []string{"Doe", "J. Doe"}
	first.Metadata = map[string]string{"position": "QB", "extra": "keep-me"}
	first.Confidence = 0.9
	first.WebSources = []entityproposal.WebSource{{Domain: "old.com", URL: "https://old.com/1", Title: "o"}}
	if _, err := repo.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := newVerifyProposal(now)
	second.Aliases = []string{"Jane"}
	second.Metadata = map[string]string{}
	second.Confidence = 0.4
	second.Sources = []entityproposal.Source{{ArticleID: "a2", ArticleTitle: "t2"}}
	id, err := repo.Upsert(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Aliases) != 3 || len(merged.Sources) != 2 || len(merged.WebSources) != 1 || merged.Metadata["extra"] != "keep-me" || merged.Confidence != 0.9 {
		t.Fatalf("merged Get() = %+v, want aliases/sources/web sources/metadata/confidence populated", merged)
	}
	merged.Status, merged.DecisionReason = entityproposal.StatusRejected, entityproposal.ReasonAutoRejectedNonNFL
	if err := repo.UpdateVerification(ctx, merged); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, id)
	if len(got.Aliases) != 3 || got.Metadata["extra"] != "keep-me" || got.Confidence != 0.9 || len(got.Sources) != 2 {
		t.Fatalf("after update = %+v", got)
	}
}

// A verification run must never overwrite a decision a human made while
// the verifier was running.
func TestEntityProposalRepository_UpdateVerificationRefusesHumanDecidedRows(t *testing.T) {
	for _, status := range []entityproposal.Status{entityproposal.StatusApproved, entityproposal.StatusRejected} {
		t.Run(string(status), func(t *testing.T) {
			repo := sqliteentityproposal.NewEntityProposalRepository(newTestDB(t))
			ctx := context.Background()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			id, _ := repo.Upsert(ctx, newVerifyProposal(now))
			if err := repo.UpdateStatus(ctx, id, status, now); err != nil {
				t.Fatal(err)
			}
			before, _ := repo.Get(ctx, id)
			for _, upd := range []entityproposal.Status{entityproposal.StatusPending, entityproposal.StatusRejected} {
				p := newVerifyProposal(now)
				p.ID, p.Status, p.CanonicalName = id, upd, "Overwritten"
				if upd == entityproposal.StatusRejected {
					p.DecisionReason = entityproposal.ReasonAutoRejectedNonNFL
				}
				if err := repo.UpdateVerification(ctx, p); !errors.Is(err, entityproposal.ErrConflict) {
					t.Fatalf("UpdateVerification(%s) error = %v, want ErrConflict", upd, err)
				}
			}
			got, _ := repo.Get(ctx, id)
			if got.Status != status || got.CanonicalName != before.CanonicalName || got.DecisionReason != "" {
				t.Fatalf("row = %+v, want unchanged %s row", got, status)
			}
		})
	}
}

// A row rejected as duplicate_of_existing absorbs a rediscovery of the
// same surface form instead of re-queuing it (finding 3).
func TestEntityProposalRepository_UpsertDoesNotResurrectDuplicateOfExisting(t *testing.T) {
	repo := sqliteentityproposal.NewEntityProposalRepository(newTestDB(t))
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, _ := repo.Upsert(ctx, newVerifyProposal(now))
	dup := newVerifyProposal(now)
	dup.ID, dup.Status, dup.DecisionReason = id, entityproposal.StatusRejected, entityproposal.ReasonDuplicateOfExisting
	if err := repo.UpdateVerification(ctx, dup); err != nil {
		t.Fatal(err)
	}
	again := newVerifyProposal(now)
	again.ID = "other"
	gotID, err := repo.Upsert(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := repo.List(ctx, "", "pending")
	if gotID != id || len(pending) != 0 {
		t.Fatalf("id = %q pending = %d, want absorbed into %q and none pending", gotID, len(pending), id)
	}
}

func TestEntityProposalRepository_UpdateVerificationPersistsProvenance(t *testing.T) {
	repo := sqliteentityproposal.NewEntityProposalRepository(newTestDB(t))
	ctx := context.Background()
	now := time.Now().UTC()
	id, err := repo.Upsert(ctx, entityproposal.Proposal{
		CanonicalName: "Springfield Mackerels", NormalizedName: "springfield mackerels",
		EntityType: entityproposal.EntityTypeTeam, Kind: entityproposal.KindNewEntity,
		Metadata: map[string]string{}, Status: entityproposal.StatusPending,
		FirstSeen: now, LastSeen: now, CreatedAt: now, ExtractorVersion: "v1", Occurrences: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	p.Status = entityproposal.StatusRejected
	p.DecisionReason = entityproposal.ReasonAutoRejectedNonNFL
	p.Verification = entityproposal.Verification{Provider: "wikidata", ProviderID: "Q910000011", Version: "wikidata-v1"}
	if err := repo.UpdateVerification(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verification != p.Verification {
		t.Fatalf("verification = %+v, want %+v", got.Verification, p.Verification)
	}
	if got.ExtractorVersion != "v1" {
		t.Errorf("extractor version clobbered: %q", got.ExtractorVersion)
	}
	list, err := repo.List(ctx, "", "rejected")
	if err != nil || len(list) != 1 || list[0].Verification.ProviderID != "Q910000011" {
		t.Fatalf("List lost provenance: %v %+v", err, list)
	}
}

func TestEntityProposalRepository_UpsertDoesNotRequeueNewAutoRejectReasons(t *testing.T) {
	for _, reason := range []string{entityproposal.ReasonNotAnNFLTeam, entityproposal.ReasonAutoRejectedNoise} {
		t.Run(reason, func(t *testing.T) {
			repo := sqliteentityproposal.NewEntityProposalRepository(newTestDB(t))
			ctx := context.Background()
			now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
			id, err := repo.Upsert(ctx, newVerifyProposal(now))
			if err != nil {
				t.Fatal(err)
			}
			rej := newVerifyProposal(now)
			rej.ID, rej.Status, rej.DecisionReason = id, entityproposal.StatusRejected, reason
			if err := repo.UpdateVerification(ctx, rej); err != nil {
				t.Fatal(err)
			}
			again := newVerifyProposal(now)
			again.ID = "rediscovered"
			got, err := repo.Upsert(ctx, again)
			if err != nil || got != id {
				t.Fatalf("Upsert() = %q, %v; want the rejected row's id %q", got, err, id)
			}
			if pending, _ := repo.List(ctx, "", "pending"); len(pending) != 0 {
				t.Fatalf("pending = %d, want 0: a %s row must not be re-queued", len(pending), reason)
			}
		})
	}
}
