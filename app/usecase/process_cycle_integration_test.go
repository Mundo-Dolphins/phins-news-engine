package usecase_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

// newIntegrationDB opens a real, migrated, temp-file SQLite database with
// the project's standard connection settings (foreign_keys=ON,
// SetMaxOpenConns(1) — see infrastructure/sqlite.Open). Unlike the fakes in
// fakes_test.go, this database enforces every foreign key immediately, so
// it can catch write-ordering bugs the fakes cannot.
func newIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "process_cycle_integration.db")
	copyMigratedTestDB(t, path)

	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return db
}

// newIntegrationProcessCycle wires a ProcessCycle against real SQLite
// repositories (the same adapters used in production) instead of the
// in-memory fakes used by the rest of this package's tests. Those fakes
// don't enforce foreign keys and can't catch ordering bugs where a
// dependent row is written before the article row it references — this
// helper closes that gap.
func newIntegrationProcessCycle(db *sql.DB, fetcher fakeFetcher, clock fakeClock) (*usecase.ProcessCycle, *sqliteadapter.ArticleRepository) {
	return newIntegrationProcessCycleWithResolver(db, fetcher, clock, &fakeSourcePolicyResolver{})
}

// newIntegrationProcessCycleWithResolver is newIntegrationProcessCycle with
// a caller-supplied sourcepolicy.Resolver, for reproducing Gate A/Gate B
// scenarios (shadow mode, retained near-duplicates, ...) against real
// SQLite instead of the in-memory fakes.
func newIntegrationProcessCycleWithResolver(db *sql.DB, fetcher fakeFetcher, clock fakeClock, resolver *fakeSourcePolicyResolver) (*usecase.ProcessCycle, *sqliteadapter.ArticleRepository) {
	articles := sqliteadapter.NewArticleRepository(db)
	dedup := sqliteadapter.NewDeduplicationRepository(db)
	aliases := sqliteadapter.NewAliasRepository(db)
	similarityIndex := sqliteadapter.NewSimilarityIndexRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	sourcePolicies := sqliteadapter.NewSourcePolicyRepository(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	seenItems := sqliteadapter.NewSeenItemsRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	unknownEntities := sqliteadapter.NewUnknownEntityRepository(db)

	uc := usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, nil, nil, defaultCascade(), clock, fakeLogger{}, sourcePolicies, resolver, clusterPublications, seenItems, sourcePolicyCooldowns, unknownEntities, nil)
	return uc, articles
}

// newIntegrationProcessCycleWithEditorial is newIntegrationProcessCycle
// extended with an editorial model + mode + ambiguous-zone policy, for
// exercising the enforce-mode editorial gate (score-reject and
// ambiguous-reject) against real SQLite instead of the in-memory fakes.
func newIntegrationProcessCycleWithEditorial(db *sql.DB, fetcher fakeFetcher, clock fakeClock, model *editorial.Model, mode, ambiguousPolicy string) (*usecase.ProcessCycle, *sqliteadapter.ArticleRepository) {
	articles := sqliteadapter.NewArticleRepository(db)
	dedup := sqliteadapter.NewDeduplicationRepository(db)
	aliases := sqliteadapter.NewAliasRepository(db)
	similarityIndex := sqliteadapter.NewSimilarityIndexRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	sourcePolicies := sqliteadapter.NewSourcePolicyRepository(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	seenItems := sqliteadapter.NewSeenItemsRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	unknownEntities := sqliteadapter.NewUnknownEntityRepository(db)

	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{Mode: mode, AmbiguousPolicy: ambiguousPolicy}

	uc := usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, model, nil, cascade, clock, fakeLogger{}, sourcePolicies, &fakeSourcePolicyResolver{}, clusterPublications, seenItems, sourcePolicyCooldowns, unknownEntities, nil)
	return uc, articles
}

// newIntegrationProcessCycleWithEditorialAndVeto is
// newIntegrationProcessCycleWithEditorial extended with an
// editorialveto.Config, for proving veto-vs-editorial-score ordering
// (Task 8's veto layer) against real SQLite instead of the in-memory
// fakes process_cycle_test.go's newProcessCycleWithEditorialAndVeto uses.
func newIntegrationProcessCycleWithEditorialAndVeto(db *sql.DB, fetcher fakeFetcher, clock fakeClock, model *editorial.Model, vetoCfg *editorialveto.Config, mode, ambiguousPolicy string) (*usecase.ProcessCycle, *sqliteadapter.ArticleRepository) {
	articles := sqliteadapter.NewArticleRepository(db)
	dedup := sqliteadapter.NewDeduplicationRepository(db)
	aliases := sqliteadapter.NewAliasRepository(db)
	similarityIndex := sqliteadapter.NewSimilarityIndexRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	sourcePolicies := sqliteadapter.NewSourcePolicyRepository(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	seenItems := sqliteadapter.NewSeenItemsRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	unknownEntities := sqliteadapter.NewUnknownEntityRepository(db)

	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{Mode: mode, AmbiguousPolicy: ambiguousPolicy}

	uc := usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, model, vetoCfg, cascade, clock, fakeLogger{}, sourcePolicies, &fakeSourcePolicyResolver{}, clusterPublications, seenItems, sourcePolicyCooldowns, unknownEntities, nil)
	return uc, articles
}

// newIntegrationProcessCycleWithCanary is
// newIntegrationProcessCycleWithEditorial extended with a canary_percent,
// for exercising editorial.mode=canary's per-article enforced/shadow
// traffic split (usecase.CanaryEnforced) against real SQLite.
func newIntegrationProcessCycleWithCanary(db *sql.DB, fetcher fakeFetcher, clock fakeClock, model *editorial.Model, ambiguousPolicy string, canaryPercent int) (*usecase.ProcessCycle, *sqliteadapter.ArticleRepository) {
	articles := sqliteadapter.NewArticleRepository(db)
	dedup := sqliteadapter.NewDeduplicationRepository(db)
	aliases := sqliteadapter.NewAliasRepository(db)
	similarityIndex := sqliteadapter.NewSimilarityIndexRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	sourcePolicies := sqliteadapter.NewSourcePolicyRepository(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	seenItems := sqliteadapter.NewSeenItemsRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	unknownEntities := sqliteadapter.NewUnknownEntityRepository(db)

	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{Mode: "canary", AmbiguousPolicy: ambiguousPolicy, CanaryPercent: canaryPercent}

	uc := usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, model, nil, cascade, clock, fakeLogger{}, sourcePolicies, &fakeSourcePolicyResolver{}, clusterPublications, seenItems, sourcePolicyCooldowns, unknownEntities, nil)
	return uc, articles
}

// seedModelRegistry inserts a model_registry row for the given version, so
// articles.classifier_version (which the migration defines as
// `REFERENCES model_registry(version)`) can be set to a test fixture
// model's Version without tripping the real database's foreign-key
// enforcement (unlike the in-memory fakes used elsewhere in this package).
func seedModelRegistry(t *testing.T, db *sql.DB, version string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO model_registry (version, kind) VALUES (?, ?)`, version, "test"); err != nil {
		t.Fatalf("seed model_registry: %v", err)
	}
}

// ambiguousScoringArticle is a fixture article with a fixed ID (so the test
// can look it up by ID after Run(), the same way other ProcessCycle
// fixtures with a pre-set article.Article.ID do) for use against
// alwaysAmbiguousModel() (defined in process_cycle_test.go), whose Bias=0
// and empty Weights place every article's score exactly at the model's
// midpoint — strictly inside (Thresholds.Reject, Thresholds.Accept).
func ambiguousScoringArticle(t *testing.T) article.Article {
	t.Helper()
	return article.Article{
		ID:          "ambiguous-zone-test-article",
		Title:       "Ambiguous zone article",
		OriginalURL: "https://example.com/ambiguous-zone-story",
		ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		PublishedAt: time.Date(2026, 7, 20, 11, 0, 0, 0, time.UTC),
	}
}

// rejectScoringArticleWithID is a fixture article with a caller-supplied ID
// (so a canary test can pick IDs landing on each side of
// usecase.CanaryEnforced's split) for use against alwaysRejectModel()
// (defined in process_cycle_test.go), which scores every article into
// editorial.ZoneReject regardless of content.
// title carries an unrelated sentence per call so two fixture articles
// built from different IDs don't trip near-duplicate/similarity detection
// (Step 2/5 of ProcessCycle, which runs before the editorial gate this
// test exercises) by sharing a near-identical title/summary shape.
func rejectScoringArticleWithID(t *testing.T, id, title string) article.Article {
	t.Helper()
	return article.Article{
		ID:          id,
		Title:       title,
		Summary:     title,
		OriginalURL: "https://example.com/reject-zone-story-" + id,
		ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		PublishedAt: time.Date(2026, 7, 20, 11, 0, 0, 0, time.UTC),
	}
}

// pickCanaryFixtureIDs finds two deterministic candidate article IDs — one
// landing in usecase.CanaryEnforced's enforced arm at percent, one in its
// shadow arm — by probing candidate IDs directly rather than guessing
// blindly. Fails the test if either arm can't be found within a generous
// search budget (which would indicate CanaryEnforced itself is broken,
// not a fixture-picking problem).
func pickCanaryFixtureIDs(t *testing.T, percent int) (enforcedID, shadowID string) {
	t.Helper()
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("canary-fixture-%d", i)
		if usecase.CanaryEnforced(id, percent) {
			if enforcedID == "" {
				enforcedID = id
			}
		} else if shadowID == "" {
			shadowID = id
		}
		if enforcedID != "" && shadowID != "" {
			return enforcedID, shadowID
		}
	}
	t.Fatalf("could not find both a canary-enforced and a canary-shadow fixture ID within budget (percent=%d)", percent)
	return "", ""
}

// TestProcessCycle_Integration_EnforceMode_AmbiguousZone_RejectsByDefault
// proves the enforce-mode contract fix: with AmbiguousPolicy="reject", an
// article whose editorial score lands in ZoneAmbiguous must be a terminal
// rejection — never falling through to accept/publish — against real
// SQLite (so a write-ordering bug or an accidental outbox row would be
// caught the same way the file's other integration tests catch them).
func TestProcessCycle_Integration_EnforceMode_AmbiguousZone_RejectsByDefault(t *testing.T) {
	db := newIntegrationDB(t)
	seedModelRegistry(t, db, alwaysAmbiguousModel().Version)
	fetcher := fakeFetcher{articles: []article.Article{ambiguousScoringArticle(t)}}
	clock := fakeClock{now: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)}
	pc, articleRepo := newIntegrationProcessCycleWithEditorial(db, fetcher, clock, alwaysAmbiguousModel(), "enforce", "reject")

	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Rejected != 1 {
		t.Errorf("Rejected = %d, want 1", result.Rejected)
	}
	saved, err := articleRepo.FindByID(context.Background(), ambiguousScoringArticle(t).ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if saved.Status != article.StatusRejectedQuality {
		t.Errorf("Status = %v, want %v", saved.Status, article.StatusRejectedQuality)
	}
	var outboxCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM publication_outbox WHERE article_id = ?`, saved.ID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if outboxCount != 0 {
		t.Errorf("outbox rows = %d, want 0 (ambiguous must not reach outbox under reject policy)", outboxCount)
	}
}

// TestProcessCycle_Integration_EnforceMode_AmbiguousZone_AcceptPolicyOptsOut
// proves the explicit AmbiguousPolicy="accept" opt-out still works after
// the contract fix: the same ambiguous-zone score that rejects by default
// must publish when an operator has explicitly configured the fall-through.
func TestProcessCycle_Integration_EnforceMode_AmbiguousZone_AcceptPolicyOptsOut(t *testing.T) {
	db := newIntegrationDB(t)
	seedModelRegistry(t, db, alwaysAmbiguousModel().Version)
	fetcher := fakeFetcher{articles: []article.Article{ambiguousScoringArticle(t)}}
	clock := fakeClock{now: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)}
	pc, articleRepo := newIntegrationProcessCycleWithEditorial(db, fetcher, clock, alwaysAmbiguousModel(), "enforce", "accept")

	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Errorf("result = %+v, want accepted=1 rejected=0 (explicit accept policy must still publish)", result)
	}
	saved, err := articleRepo.FindByID(context.Background(), ambiguousScoringArticle(t).ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	var outboxCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM publication_outbox WHERE article_id = ?`, saved.ID).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if outboxCount != 1 {
		t.Errorf("outbox rows = %d, want 1 (explicit accept policy must still reach the outbox)", outboxCount)
	}
}

// TestProcessCycle_Integration_CanaryMode_SplitsEnforcedAndShadowArms proves
// editorial.mode=canary's per-article deterministic traffic split: two
// articles that both score into ZoneReject under the same model must
// diverge in outcome based solely on usecase.CanaryEnforced(articleID,
// canaryPercent) — the enforced-arm article is a terminal rejection (same
// as enforce mode), the shadow-arm article is log-only (same as shadow
// mode) — against real SQLite so a write-ordering or outbox-leak bug in
// either canary arm is caught the same way the file's other integration
// tests catch them.
func TestProcessCycle_Integration_CanaryMode_SplitsEnforcedAndShadowArms(t *testing.T) {
	db := newIntegrationDB(t)
	seedModelRegistry(t, db, alwaysRejectModel().Version)
	// Two fixture articles with IDs chosen so one is in the enforced arm
	// and one is in the shadow arm at canary_percent=50 (verified via
	// usecase.CanaryEnforced directly when picking fixture IDs, rather
	// than guessing) — both scored into the reject zone by the same wired
	// editorial model fixture this file's other editorial tests use.
	enforcedID, shadowID := pickCanaryFixtureIDs(t, 50)
	fetcher := fakeFetcher{articles: []article.Article{
		rejectScoringArticleWithID(t, enforcedID, "Isotopes sign veteran offensive lineman ahead of training camp"),
		rejectScoringArticleWithID(t, shadowID, "Local weather forecast predicts thunderstorms across the county this weekend"),
	}}
	clock := fakeClock{now: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)}
	pc, articleRepo := newIntegrationProcessCycleWithCanary(db, fetcher, clock, alwaysRejectModel(), "reject", 50)

	if _, err := pc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	enforced, err := articleRepo.FindByID(context.Background(), enforcedID)
	if err != nil {
		t.Fatalf("FindByID(enforced) error = %v", err)
	}
	if enforced.Status != article.StatusRejectedQuality {
		t.Errorf("enforced-arm article Status = %v, want %v (canary must block it)", enforced.Status, article.StatusRejectedQuality)
	}

	shadow, err := articleRepo.FindByID(context.Background(), shadowID)
	if err != nil {
		t.Fatalf("FindByID(shadow) error = %v", err)
	}
	if shadow.Status == article.StatusRejectedQuality {
		t.Errorf("shadow-arm article Status = %v, want NOT rejected-by-quality (canary shadow arm must not block)", shadow.Status)
	}

	var enforcedOutbox, shadowOutbox int
	if err := db.QueryRow(`SELECT COUNT(*) FROM publication_outbox WHERE article_id = ?`, enforcedID).Scan(&enforcedOutbox); err != nil {
		t.Fatalf("count enforced outbox rows: %v", err)
	}
	if enforcedOutbox != 0 {
		t.Errorf("enforced-arm outbox rows = %d, want 0 (canary enforced arm must not reach outbox on reject)", enforcedOutbox)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM publication_outbox WHERE article_id = ?`, shadowID).Scan(&shadowOutbox); err != nil {
		t.Fatalf("count shadow outbox rows: %v", err)
	}
	if shadowOutbox != 1 {
		t.Errorf("shadow-arm outbox rows = %d, want 1 (canary shadow arm is log-only and must still publish)", shadowOutbox)
	}
}

func TestProcessCycle_Integration_AcceptsFreshArticleAgainstRealSQLite(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		Title:       "Isotopes Sign Random Newcomer",
		OriginalURL: "https://example.com/real-db-story",
		ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	uc, articles := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v (this is the error a foreign-key-ordering regression would produce)", err)
	}
	if result.Fetched != 1 || result.Accepted != 1 || result.Rejected != 0 || result.Duplicate != 0 {
		t.Fatalf("result = %+v, want fetched=1 accepted=1", result)
	}

	// Prove an unknown-entity occurrence actually lands in the database
	// through the real pipeline — not just that the adapter compiles
	// against the port. The title's "Random Newcomer" is a two-word
	// Title-Case name unmatched by defaultCascade()'s catalog (which
	// knows "Isotopes" but nothing about a "Random Newcomer").
	var candidateCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM unknown_entity_occurrences`).Scan(&candidateCount); err != nil {
		t.Fatalf("count unknown_entity_occurrences: %v", err)
	}
	if candidateCount != 1 {
		t.Fatalf("unknown_entity_occurrences row count = %d, want 1", candidateCount)
	}
	var candidateText string
	if err := db.QueryRowContext(context.Background(), `SELECT candidate_text FROM unknown_entity_occurrences LIMIT 1`).Scan(&candidateText); err != nil {
		t.Fatalf("select unknown_entity_occurrences.candidate_text: %v", err)
	}
	if candidateText != "Sign Random Newcomer" {
		t.Errorf("unknown_entity_occurrences.candidate_text = %q, want %q", candidateText, "Sign Random Newcomer")
	}

	// Find the persisted article by scanning: we don't know its generated
	// ID up front, so query via Count + a direct lookup isn't available;
	// instead assert indirectly that exactly one article exists and fetch
	// it by re-deriving the ID is unnecessary — ArticleRepository doesn't
	// expose ListAll, so we capture the ID from the outbox instead.
	rows, err := db.QueryContext(context.Background(), `SELECT article_id FROM publication_outbox`)
	if err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 {
		t.Fatalf("outbox article ids = %v, want exactly 1", ids)
	}

	got, err := articles.FindByID(context.Background(), ids[0])
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.Status != article.StatusPendingPublication {
		t.Errorf("Status = %v, want %v", got.Status, article.StatusPendingPublication)
	}
}

func TestProcessCycle_Integration_RejectsStaleArticleAgainstRealSQLite(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/real-db-stale-story",
		ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		PublishedAt: now.Add(-200 * time.Hour),
	}}}
	uc, articles := newIntegrationProcessCycle(db, fetcher, fakeClock{now: now})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Rejected != 1 || result.Accepted != 0 {
		t.Fatalf("result = %+v, want rejected=1 accepted=0", result)
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM articles`).Scan(&count); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if count != 1 {
		t.Fatalf("articles table has %d rows, want 1", count)
	}

	var id string
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM articles LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("select article id: %v", err)
	}

	got, err := articles.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if got.Status != article.StatusRejectedStale {
		t.Errorf("Status = %v, want %v", got.Status, article.StatusRejectedStale)
	}
}

func TestProcessCycle_Integration_TwoArticlesShareAClusterAcrossCyclesAgainstRealSQLite(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	first := fakeFetcher{articles: []article.Article{{
		Title:       "Springfield Isotopes Sign Jane Doe To Bolster Injury-Depleted Front Seven",
		OriginalURL: "https://example.com/signing-original",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-2 * time.Hour),
	}}}
	uc1, _ := newIntegrationProcessCycle(db, first, fakeClock{now: now.Add(-2 * time.Hour)})
	if _, err := uc1.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}

	second := fakeFetcher{articles: []article.Article{{
		Title:       "Isotopes Sign Jane Doe To Bolster Injury-Depleted Front Seven In Free Agency",
		OriginalURL: "https://example.com/signing-followup",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	uc2, articles := newIntegrationProcessCycle(db, second, fakeClock{now: now})
	result, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}

	var clusterCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM event_clusters`).Scan(&clusterCount); err != nil {
		t.Fatalf("count clusters: %v", err)
	}

	// Whichever band the two similar-but-reworded titles land in (near
	// duplicate rejected, or same event joined), there must never be two
	// separate clusters holding coverage of the same named-player signing.
	if result.Duplicate == 0 && clusterCount != 1 {
		t.Errorf("clusterCount = %d, want 1 when the second article was accepted as the same event", clusterCount)
	}

	var articleCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM articles`).Scan(&articleCount); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if articleCount != 2 {
		t.Fatalf("articleCount = %d, want 2 (both articles persisted regardless of dedup/cluster outcome)", articleCount)
	}
	_ = articles
}

// TestProcessCycle_Integration_ShadowModeRetainNearDuplicate_ReproducesGateAForeignKeyViolation
// reproduces the original bug against a real, migrated SQLite database with
// PRAGMA foreign_keys=ON: in shadow mode, when the resolved policy has
// RetainNearDuplicates=true, ProcessCycle records a source_policy_decisions
// row (article_id = a.ID) before articles.Save has ever persisted that
// article — source_policy_decisions.article_id REFERENCES articles(id)
// (migration 0016), so the insert violates the foreign key and the whole
// cycle errors out, exactly mirroring the in-memory-fake test
// TestProcessCycle_NearDuplicate_ShadowModeRecordsWouldRetainDecision (which
// cannot catch this because fakeSourcePolicyRepository does not enforce
// foreign keys).
func TestProcessCycle_Integration_ShadowModeRetainNearDuplicate_ReproducesGateAForeignKeyViolation(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	officialSource := source.Source{ID: mustID(t, source.TypeRSS, "official-source"), Type: source.TypeRSS}

	// First cycle: persist a baseline article through the real pipeline
	// (disabled resolver, i.e. Gate A/B are no-ops) so its dedup shingles
	// are indexed by the production code path itself, not hand-computed —
	// exactly like TestProcessCycle_Integration_TwoArticlesShareAClusterAcrossCyclesAgainstRealSQLite
	// above.
	first := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In An Overtime Thriller", Source: officialSource,
		OriginalURL: "https://example.com/existing-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-2 * time.Hour),
	}}}
	uc1, _ := newIntegrationProcessCycle(db, first, fakeClock{now: now.Add(-2 * time.Hour)})
	if _, err := uc1.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}

	// Second cycle: a near-duplicate of the first, from a source whose
	// shadow-mode policy has RetainNearDuplicates=true — the exact
	// condition TestProcessCycle_NearDuplicate_ShadowModeRecordsWouldRetainDecision
	// exercises against fakes, reproduced here against real SQLite.
	second := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In Overtime Thriller", Source: officialSource,
		OriginalURL: "https://example.com/official-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeShadow,
		policies: map[string]sourcepolicy.Policy{
			"rss:official-source": {SourceID: "rss:official-source", Authority: sourcepolicy.AuthorityOfficialTeam, RetainNearDuplicates: true},
		},
	}
	uc2, _ := newIntegrationProcessCycleWithResolver(db, second, fakeClock{now: now}, resolver)

	result, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v (Gate A must never leave the cycle failing on a real database once the write order is fixed)", err)
	}
	if result.Duplicate != 1 {
		t.Fatalf("result = %+v, want duplicate=1: this is a precondition of the repro, not the bug itself (test fixture must land in the near-duplicate band)", result)
	}

	var decisionCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM source_policy_decisions WHERE decision_code = ?`, string(sourcepolicy.CodeWouldRetainNearDuplicate)).Scan(&decisionCount); err != nil {
		t.Fatalf("count source_policy_decisions: %v", err)
	}
	if decisionCount != 1 {
		t.Errorf("source_policy_decisions rows with would_retain_near_duplicate = %d, want 1", decisionCount)
	}
}

// TestProcessCycle_Integration_ClusterGate_ParaphraseFromUnconfiguredSourceIsDenied
// is the characterization scenario, proven against real SQLite:
// two articles semantically describing the same event (below the
// near-duplicate text-similarity threshold, so exact/near dedup lets the
// second one through), from two different sources, neither with a
// configured source-policy entry, resolver mode shadow (today's production
// default). clustering.DecideJoin correctly joins the second into the
// first's cluster as MemberRoleConfirmation. Before this task, the second
// article still reached article.StatusPendingPublication (a second
// publication_outbox row for the same event); after this task, it must be
// denied (StatusRetainedNoPublish) and the cluster must end up with
// exactly one publication_outbox row.
func TestProcessCycle_Integration_ClusterGate_ParaphraseFromUnconfiguredSourceIsDenied(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	first := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Sign Jane Doe To Bolster Secondary", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/jane-doe-original", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-2 * time.Hour),
	}}}
	resolver := &fakeSourcePolicyResolver{mode: sourcepolicy.ModeShadow} // no policies configured for either source
	uc1, articles := newIntegrationProcessCycleWithResolver(db, first, fakeClock{now: now.Add(-2 * time.Hour)}, resolver)
	result1, err := uc1.Run(context.Background())
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if result1.Accepted != 1 {
		t.Fatalf("first result = %+v, want accepted=1", result1)
	}

	// A paraphrase: different wording (so near-dedup, using the shingle
	// threshold from defaultCascade, does not catch it), same event signals
	// (same entity Jane Doe, same action "sign"), different source.
	second := fakeFetcher{articles: []article.Article{{
		Title: "Springfield Officially Adds Defensive Back Jane Doe In Free Agency Move", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-2"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/jane-doe-confirmed-elsewhere", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	uc2, _ := newIntegrationProcessCycleWithResolver(db, second, fakeClock{now: now}, resolver)
	result2, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}

	var clusterCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM event_clusters`).Scan(&clusterCount); err != nil {
		t.Fatalf("count clusters: %v", err)
	}
	if clusterCount != 1 {
		t.Fatalf("clusterCount = %d, want 1 (test precondition: clustering must have joined the paraphrase, not created a second cluster — if this fails, adjust the fixture's title/entity wording, not the gate logic)", clusterCount)
	}

	if result2.Accepted != 0 || result2.ClusterGateDenied != 1 {
		t.Errorf("second result = %+v, want accepted=0 clusterGateDenied=1: an unconfigured source's confirmation of an already-published event must be denied", result2)
	}

	var publishedOutboxCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM publication_outbox`).Scan(&publishedOutboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if publishedOutboxCount != 1 {
		t.Errorf("publication_outbox rows = %d, want exactly 1: the second article must never reach the outbox", publishedOutboxCount)
	}

	var secondStatus string
	if err := db.QueryRowContext(context.Background(), `
		SELECT status FROM articles WHERE id NOT IN (SELECT id FROM articles ORDER BY fetched_at ASC LIMIT 1)
	`).Scan(&secondStatus); err != nil {
		t.Fatalf("query second article status: %v", err)
	}
	if secondStatus != string(article.StatusRetainedNoPublish) {
		t.Errorf("second article status = %q, want %q", secondStatus, article.StatusRetainedNoPublish)
	}
	_ = articles
}

// TestProcessCycle_Integration_ClusterGate_ModeIndependence proves the
// deny-a-second-publication-for-the-same-cluster outcome holds identically
// in sourcepolicy.ModeShadow and sourcepolicy.ModeEnforce — the
// mode-independence half of the fix.
//
// The two modes reach that outcome via different mechanisms, and this test
// asserts that difference explicitly rather than papering over it: in
// ModeShadow, Gate B (process_cycle.go's pre-existing, enforce-only
// early-return) never fires, so the cluster publication gate added by this
// task is what denies the second article (ClusterGateDenied == 1). In
// ModeEnforce, Gate B's own condition (`!ShouldPublish && Code !=
// CodeStandard`) already covers this exact fixture — an unconfigured
// source's MemberRoleConfirmation always resolves to
// sourcepolicy.CodeRetainClusterMember/ShouldPublish=false (see
// TestProcessCycle_OfficialConfirmation_NoNewInfo_RetainedNotPublished for
// the same pre-existing mechanism), so Gate B intercepts before Step 8 and
// the cluster gate's own Reserve is never attempted for this article
// (ClusterGateDenied stays 0 — correct, since ports/in/process_cycle.go's
// ClusterGateDenied doc comment defines it as "a subset of Rejected...
// specifically by the cluster publication gate", not by Gate B). What must
// hold regardless of mode — and is asserted unconditionally below — is the
// actual cluster-publication-gate guarantee: the second article is never accepted and the
// cluster never accumulates a second publication_outbox row.
func TestProcessCycle_Integration_ClusterGate_ModeIndependence(t *testing.T) {
	for _, mode := range []sourcepolicy.Mode{sourcepolicy.ModeShadow, sourcepolicy.ModeEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			db := newIntegrationDB(t)
			now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
			resolver := &fakeSourcePolicyResolver{mode: mode}

			first := fakeFetcher{articles: []article.Article{{
				Title: "Isotopes Sign Jane Doe To Bolster Secondary", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
				OriginalURL: "https://example.com/mode-" + string(mode) + "-original", ContentType: article.ContentTypeArticle,
				PublishedAt: now.Add(-2 * time.Hour),
			}}}
			uc1, _ := newIntegrationProcessCycleWithResolver(db, first, fakeClock{now: now.Add(-2 * time.Hour)}, resolver)
			if _, err := uc1.Run(context.Background()); err != nil {
				t.Fatalf("first Run() error = %v", err)
			}

			second := fakeFetcher{articles: []article.Article{{
				Title: "Springfield Officially Adds Defensive Back Jane Doe In Free Agency Move", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-2"), Type: source.TypeRSS},
				OriginalURL: "https://example.com/mode-" + string(mode) + "-confirm", ContentType: article.ContentTypeArticle,
				PublishedAt: now.Add(-1 * time.Hour),
			}}}
			uc2, _ := newIntegrationProcessCycleWithResolver(db, second, fakeClock{now: now}, resolver)
			result2, err := uc2.Run(context.Background())
			if err != nil {
				t.Fatalf("second Run() error = %v", err)
			}

			if result2.Accepted != 0 {
				t.Errorf("mode %s: result = %+v, want accepted=0: a second publication for the same cluster must be denied regardless of mode", mode, result2)
			}
			var outboxCount int
			if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM publication_outbox`).Scan(&outboxCount); err != nil {
				t.Fatalf("count outbox: %v", err)
			}
			if outboxCount != 1 {
				t.Errorf("mode %s: publication_outbox rows = %d, want exactly 1 regardless of mode", mode, outboxCount)
			}

			wantClusterGateDenied := 1
			if mode == sourcepolicy.ModeEnforce {
				// Gate B already denies this exact fixture in enforce mode
				// (see doc comment above) before the cluster gate's Reserve
				// is ever attempted.
				wantClusterGateDenied = 0
			}
			if result2.ClusterGateDenied != wantClusterGateDenied {
				t.Errorf("mode %s: result = %+v, want clusterGateDenied=%d", mode, result2, wantClusterGateDenied)
			}
		})
	}
}

// TestProcessCycle_Integration_ClusterGate_ConfiguredPolicyStillAllowsMaterialUpdate
// proves the gate does not regress the legitimate case: a source
// explicitly configured with allow_substantial_update publishes its update
// even though the cluster already has a confirmed initial publication.
func TestProcessCycle_Integration_ClusterGate_ConfiguredPolicyStillAllowsMaterialUpdate(t *testing.T) {
	db := newIntegrationDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{
			"rss:sprisotopes": {SourceID: "rss:sprisotopes", Authority: sourcepolicy.AuthorityOfficialTeam, AllowSubstantialUpdate: true, MinimumUpdateNovelty: 0.1},
		},
	}

	first := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Sign Jane Doe To Bolster Secondary", Source: source.Source{ID: mustID(t, source.TypeRSS, "source-1"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/update-original", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-2 * time.Hour),
	}}}
	uc1, _ := newIntegrationProcessCycleWithResolver(db, first, fakeClock{now: now.Add(-2 * time.Hour)}, resolver)
	if _, err := uc1.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}

	second := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Release Jane Doe Ahead Of Matchup With Sharks", Source: source.Source{ID: mustID(t, source.TypeRSS, "sprisotopes"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/update-release", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	uc2, _ := newIntegrationProcessCycleWithResolver(db, second, fakeClock{now: now}, resolver)
	result2, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}

	var clusterCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM event_clusters`).Scan(&clusterCount); err != nil {
		t.Fatalf("count clusters: %v", err)
	}
	if clusterCount != 1 {
		t.Fatalf("clusterCount = %d, want 1 (test precondition: clustering must have joined the update, not created a second cluster — if this fails, adjust the fixture's title/entity wording, not the gate logic)", clusterCount)
	}

	if result2.Accepted != 1 || result2.ClusterGateDenied != 0 {
		t.Errorf("result = %+v, want accepted=1 clusterGateDenied=0: an explicitly-authorized substantial update must still publish", result2)
	}

	var outboxCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM publication_outbox`).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 2 {
		t.Errorf("publication_outbox rows = %d, want 2 (the initial publication plus the authorized update)", outboxCount)
	}
}

// acceptScoringArticle is a fixture article with a fixed ID (so the restart
// test can look it up by ID both before and after simulating a restart) —
// PublishedAt is computed relative to wall-clock time.Now() (rather than a
// fixed 2026 date, as this file's other fixtures use) since this test's own
// fakeClock is also wall-clock-based, and freshness.HardNewsHours (72h)
// would otherwise reject an article published against a stale fixed date.
func acceptScoringArticle(t *testing.T) article.Article {
	t.Helper()
	return article.Article{
		ID:          "restart-consistency-test-article",
		Title:       "Restart consistency test article",
		Summary:     "Restart consistency test article",
		OriginalURL: "https://example.com/restart-consistency-story",
		ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		PublishedAt: time.Now().UTC().Add(-1 * time.Hour),
	}
}

// vetoTriggeringButHighScoringArticle is a fixture article whose title
// triggers vetoingConfig()'s "free bets" gambling rule (process_cycle_test.go)
// — same phrase as TestProcessCycle_EnforceModeVetoRejectsRegardlessOfModelScore's
// in-memory-fake fixture — for exercising the same veto-overrides-a-high-score
// scenario against real SQLite.
func vetoTriggeringButHighScoringArticle(t *testing.T) article.Article {
	t.Helper()
	return article.Article{
		ID:          "veto-overrides-high-score-test-article",
		Title:       "Free bets available for the big game",
		Summary:     "Sign up now.",
		OriginalURL: "https://example.com/veto-overrides-high-score-story",
		ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		PublishedAt: time.Now().UTC().Add(-1 * time.Hour),
	}
}

// checksummedModel is alwaysRejectModel with a non-empty Checksum, the way
// the production composition root wires it (engine.Run sets
// Model.Checksum from the verified manifest's artifact_hash). Tests that
// assert on the persisted article_processing_events.model_checksum column
// need a fixture that actually carries one — otherwise "checksum is
// populated" and "checksum is empty" are indistinguishable.
func checksummedModel() *editorial.Model {
	m := alwaysRejectModel()
	m.Checksum = "sha256:integration-fixture-checksum"
	return m
}

// editorialEventAttribution reads the mode/model_checksum/reject_threshold
// actually persisted for articleID's editorial event of eventType, straight
// out of SQLite. Deliberately a raw query rather than a repository call:
// the point is to prove the columns are written, not to re-exercise the
// repository's own mapping.
func editorialEventAttribution(t *testing.T, db *sql.DB, articleID, eventType string) (mode, checksum string, rejectThreshold float64) {
	t.Helper()
	err := db.QueryRow(
		`SELECT mode, model_checksum, applied_reject_threshold FROM article_processing_events WHERE article_id = ? AND event_type = ?`,
		articleID, eventType,
	).Scan(&mode, &checksum, &rejectThreshold)
	if err != nil {
		t.Fatalf("read %s event for %s: %v", eventType, articleID, err)
	}
	return mode, checksum, rejectThreshold
}

// TestProcessCycle_Integration_Restart_ModeAndModelIdentityConsistent proves
// a persisted article's ClassifierVersion/Status — and the mode/model
// identity persisted alongside its editorial event — survive a process
// restart: a brand-new ProcessCycle, wired against the same
// already-migrated, already-written real SQLite database (no fetcher
// activity, no in-memory state carried over — everything must come from
// what was actually persisted), must read back the exact same identity a
// first run wrote.
//
// Uses shadow mode (never gates) with a checksummed alwaysRejectModel so
// the article is accepted with a real, non-empty ClassifierVersion — a
// stronger fixture than editorial-disabled, whose ClassifierVersion is
// always the constant "placeholder-v0" regardless of persistence. Shadow
// mode is also the mode production actually runs today, and it takes the
// appendEditorialEvent path (not rejectEditorialScored) — so this is the
// test that would have caught appendEditorialEvent silently omitting
// Mode/ModelChecksum/RejectThreshold, leaving 100% of real traffic
// unattributed.
func TestProcessCycle_Integration_Restart_ModeAndModelIdentityConsistent(t *testing.T) {
	db := newIntegrationDB(t)
	model := checksummedModel()
	seedModelRegistry(t, db, model.Version)
	fetcher := fakeFetcher{articles: []article.Article{acceptScoringArticle(t)}}
	clock := fakeClock{now: time.Now().UTC()}
	pc, articleRepo := newIntegrationProcessCycleWithEditorial(db, fetcher, clock, model, "shadow", "reject")

	if _, err := pc.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	first, err := articleRepo.FindByID(context.Background(), acceptScoringArticle(t).ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if first.ClassifierVersion == "" {
		t.Fatalf("precondition failed: first.ClassifierVersion is empty, want %q (fixture must actually exercise the editorial model)", model.Version)
	}

	mode, checksum, rejectThreshold := editorialEventAttribution(t, db, first.ID, "editorial_shadow")
	if mode != "shadow" {
		t.Errorf("persisted editorial_shadow event mode = %q, want %q", mode, "shadow")
	}
	if checksum != model.Checksum {
		t.Errorf("persisted editorial_shadow event model_checksum = %q, want %q", checksum, model.Checksum)
	}
	if rejectThreshold != model.Thresholds.Reject {
		t.Errorf("persisted editorial_shadow event reject_threshold = %v, want %v", rejectThreshold, model.Thresholds.Reject)
	}

	// Simulate a restart: build a brand-new ProcessCycle over the same
	// (already-migrated, already-written) db, same config-derived mode — no
	// in-memory state carries over except what's persisted. pc2 is not run
	// again for this fixture; this test only asserts persisted identity
	// survives a fresh wiring.
	pc2, articleRepo2 := newIntegrationProcessCycleWithEditorial(db, fakeFetcher{}, clock, model, "shadow", "reject")
	_ = pc2
	second, err := articleRepo2.FindByID(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("FindByID() after restart error = %v", err)
	}
	if second.ClassifierVersion != first.ClassifierVersion || second.Status != first.Status {
		t.Errorf("post-restart article = %+v, want ClassifierVersion/Status matching pre-restart %+v", second, first)
	}

	modeAfter, checksumAfter, _ := editorialEventAttribution(t, db, first.ID, "editorial_shadow")
	if modeAfter != mode || checksumAfter != checksum {
		t.Errorf("post-restart event attribution = (%q, %q), want pre-restart (%q, %q)", modeAfter, checksumAfter, mode, checksum)
	}
}

// TestProcessCycle_Integration_CanaryShadowArm_EventCarriesModeAndChecksum
// covers appendEditorialEvent's other live call site: canary's shadow arm.
// It also pins the would-reject classification — the shadow-arm fixture
// scores into ZoneReject, so its event type must record the counterfactual
// rejection rather than the neutral "editorial_canary_shadow".
func TestProcessCycle_Integration_CanaryShadowArm_EventCarriesModeAndChecksum(t *testing.T) {
	db := newIntegrationDB(t)
	model := checksummedModel()
	seedModelRegistry(t, db, model.Version)
	_, shadowID := pickCanaryFixtureIDs(t, 50)
	fetcher := fakeFetcher{articles: []article.Article{
		rejectScoringArticleWithID(t, shadowID, "Local weather forecast predicts thunderstorms across the county this weekend"),
	}}
	clock := fakeClock{now: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)}
	pc, _ := newIntegrationProcessCycleWithCanary(db, fetcher, clock, model, "reject", 50)

	if _, err := pc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mode, checksum, rejectThreshold := editorialEventAttribution(t, db, shadowID, "editorial_canary_shadow_would_reject")
	if mode != "canary" {
		t.Errorf("persisted canary shadow-arm event mode = %q, want %q", mode, "canary")
	}
	if checksum != model.Checksum {
		t.Errorf("persisted canary shadow-arm event model_checksum = %q, want %q", checksum, model.Checksum)
	}
	if rejectThreshold != model.Thresholds.Reject {
		t.Errorf("persisted canary shadow-arm event reject_threshold = %v, want %v", rejectThreshold, model.Thresholds.Reject)
	}
}

// TestProcessCycle_Integration_CanaryShadowArm_VetoTriggeredCountsAsWouldReject
// guards that deterministic vetoes are terminal before canary/model
// evaluation. Canary remains exclusively a model rollout mechanism.
func TestProcessCycle_Integration_CanaryDeterministicVetoIsTerminal(t *testing.T) {
	db := newIntegrationDB(t)
	model := &editorial.Model{
		Version: "test-accept-v1", Bias: 5, Weights: map[string]float64{},
		Thresholds: editorial.Thresholds{Reject: 10, Accept: 90},
		Checksum:   "sha256:integration-fixture-checksum",
	}
	seedModelRegistry(t, db, model.Version)
	_, shadowID := pickCanaryFixtureIDs(t, 50)
	a := rejectScoringArticleWithID(t, shadowID, "Isotopes rumor mill churns on a wild unconfirmed trade whisper")
	fetcher := fakeFetcher{articles: []article.Article{a}}
	clock := fakeClock{now: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)}
	vetoCfg := &editorialveto.Config{Rules: []editorialveto.Rule{{
		Category: editorialveto.CategoryNoise, ReasonCode: "veto:noise", Patterns: []string{"rumor mill"},
	}}}

	articles := sqliteadapter.NewArticleRepository(db)
	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{Mode: "canary", AmbiguousPolicy: "reject", CanaryPercent: 50}
	pc := usecase.NewProcessCycle(fetcher, articles, sqliteadapter.NewDeduplicationRepository(db), sqliteadapter.NewAliasRepository(db),
		sqliteadapter.NewSimilarityIndexRepository(db), sqliteadapter.NewClusterRepository(db), sqliteadapter.NewDecisionRepository(db),
		sqliteadapter.NewOutboxRepository(db), sqliteadapter.NewUnitOfWork(db), freshness, dictionary.Matcher{}, model, vetoCfg, cascade,
		clock, fakeLogger{}, sqliteadapter.NewSourcePolicyRepository(db), &fakeSourcePolicyResolver{},
		sqliteadapter.NewClusterPublicationRepository(db), sqliteadapter.NewSeenItemsRepository(db), sqliteadapter.NewSourcePolicyCooldownRepository(db), sqliteadapter.NewUnknownEntityRepository(db), nil)

	if _, err := pc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var vetoEvents, shadowEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM article_processing_events WHERE article_id = ? AND event_type = ?`,
		shadowID, "editorial_veto").Scan(&vetoEvents); err != nil {
		t.Fatalf("count veto events: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM article_processing_events WHERE article_id = ? AND event_type = ?`,
		shadowID, "editorial_canary_shadow").Scan(&shadowEvents); err != nil {
		t.Fatalf("count shadow events: %v", err)
	}
	if vetoEvents != 1 || shadowEvents != 0 {
		t.Errorf("veto=%d shadow=%d, want 1/0", vetoEvents, shadowEvents)
	}
}

// TestProcessCycle_Integration_VetoRunsBeforeEditorialScore_DedupBeforeFreshness
// is a regression guard for the Step-ordering documented in
// process_cycle.go's comments: a veto-triggering article must be rejected
// by the veto (EventType/ReasonCode "editorial_veto"/"veto:...", never
// "editorial"/"editorial:reject_score"), even though its editorial score —
// from a model that would otherwise ACCEPT everything (Bias=5, no weights,
// same fixture as TestProcessCycle_EnforceModeVetoRejectsRegardlessOfModelScore)
// — independently lands in the accept zone. This proves veto evaluation
// precedes/overrides editorial score's own verdict, not merely that the
// article got rejected somehow (which a low-scoring fixture would not
// distinguish from ordinary score-based rejection). Runs against real
// SQLite so a write-ordering bug (e.g. the veto's audit event or article
// save happening out of FK order relative to dedup/cluster writes) would
// be caught the same way this file's other integration tests catch them —
// exercising the dedup-then-editorial-gate step ordering this file's
// #152/#153 tests already cover for the non-veto path.
func TestProcessCycle_Integration_VetoRunsBeforeEditorialScore_DedupBeforeFreshness(t *testing.T) {
	alwaysAcceptModel := &editorial.Model{Version: "test-accept-v1", Bias: 5, Weights: map[string]float64{}, Thresholds: editorial.Thresholds{Reject: 10, Accept: 90}}
	db := newIntegrationDB(t)
	seedModelRegistry(t, db, alwaysAcceptModel.Version)
	fetcher := fakeFetcher{articles: []article.Article{vetoTriggeringButHighScoringArticle(t)}}
	clock := fakeClock{now: time.Now().UTC()}
	pc, articleRepo := newIntegrationProcessCycleWithEditorialAndVeto(db, fetcher, clock, alwaysAcceptModel, vetoingConfig(), "enforce", "reject")

	result, err := pc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Rejected != 1 || result.Accepted != 0 {
		t.Fatalf("result = %+v, want rejected=1 accepted=0 (veto must override an accepting model)", result)
	}

	saved, err := articleRepo.FindByID(context.Background(), vetoTriggeringButHighScoringArticle(t).ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if saved.Status != article.StatusRejectedQuality {
		t.Errorf("Status = %v, want %v (veto must reject regardless of a high editorial score)", saved.Status, article.StatusRejectedQuality)
	}

	// The deterministic veto is evaluated once, before candidate generation,
	// and its terminal event carries the category-specific reason code.
	var vetoEventCount, scoreRejectEventCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM article_processing_events WHERE article_id = ? AND event_type = 'editorial_veto' AND reason_code = ?`,
		saved.ID, "veto:gambling:sportsbook_promo").Scan(&vetoEventCount); err != nil {
		t.Fatalf("count editorial_veto events: %v", err)
	}
	if vetoEventCount != 1 {
		t.Errorf("editorial_veto events with the expected reason_code = %d, want 1", vetoEventCount)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM article_processing_events WHERE article_id = ? AND event_type = 'editorial' AND to_status = ?`,
		saved.ID, string(article.StatusRejectedQuality)).Scan(&scoreRejectEventCount); err != nil {
		t.Fatalf("count editorial score-reject events: %v", err)
	}
	if scoreRejectEventCount != 0 {
		t.Errorf("editorial (score-driven) reject events = %d, want 0 — the veto, not the model's own accept score, must have driven this rejection", scoreRejectEventCount)
	}
}
