package usecase_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewdecision"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var migratedTestDBTemplate string

func TestMain(m *testing.M) {
	templateDir, err := os.MkdirTemp("", "engine-v2-usecase-test-template-")
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

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	copyMigratedTestDB(t, path)
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestFullCycle_RealSQLite_FetchThroughPublish exercises fetch -> accept ->
// outbox -> (fake) publish against a real, migrated, temp-file SQLite
// database — the "ciclo completo con SQLite real" case from the phase-8
// test list, complementing the fakes-only ProcessCycle/PublishOutbox tests
// elsewhere in this package.
func TestFullCycle_RealSQLite_FetchThroughPublish(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

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

	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/story", ContentType: article.ContentTypeArticle,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clock := fakeClock{now: now}

	processCycle := usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, nil, nil, defaultCascade(), clock, fakeLogger{}, sourcePolicies, &fakeSourcePolicyResolver{}, clusterPublications, seenItems, sourcePolicyCooldowns, unknownEntities, nil)
	result, err := processCycle.Run(ctx)
	if err != nil {
		t.Fatalf("ProcessCycle.Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1", result)
	}

	pub := &fakePublisher{}
	publishOutbox := usecase.NewPublishOutbox(articles, outbox, decisions, uow, pub, testBackoff, publishing.PolicyConfig{}, "worker-1", 10, time.Minute, clock, fakeLogger{}, clusterPublications, sourcePolicyCooldowns,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)
	pubResult, err := publishOutbox.Run(ctx)
	if err != nil {
		t.Fatalf("PublishOutbox.Run() error = %v", err)
	}
	if pubResult.Published != 1 {
		t.Fatalf("pubResult = %+v, want published=1", pubResult)
	}
	if len(pub.published) != 1 {
		t.Fatalf("pub.published = %v, want 1 article actually reaching the publisher", pub.published)
	}

	final, err := articles.FindByID(ctx, pub.published[0].ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if final.Status != article.StatusPublished {
		t.Errorf("final.Status = %v, want %v", final.Status, article.StatusPublished)
	}
}

// TestFullCycle_RestartBetweenClaimAndPublish_ReclaimsAndPublishes exercises
// the "reinicio entre outbox y envío" case: an outbox item stuck Claimed
// past staleAfter (simulating a crash before the first PublishOutbox
// finished) must be reclaimed by a later call and still get published —
// no pending article is lost across a restart.
func TestFullCycle_RestartBetweenClaimAndPublish_ReclaimsAndPublishes(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)

	if err := articles.Save(ctx, article.Article{ID: "a1", CanonicalURL: "https://example.com/a1", Source: source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}, PublishedAt: now, FetchedAt: now, Status: article.StatusPendingPublication}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if err := outbox.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	// Simulate the "first process" crashing right after claiming: claim it,
	// but never call PublishOutbox to completion.
	staleAfter := time.Minute
	claimed, err := outbox.ClaimBatch(ctx, "crashed-worker", 10, staleAfter, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("initial ClaimBatch() = %v, %v, want 1 claimed item", claimed, err)
	}

	// "Restart": a fresh PublishOutbox, well past staleAfter, reclaims and
	// publishes it.
	pub := &fakePublisher{}
	clock := fakeClock{now: now.Add(2 * staleAfter)}
	publishOutbox := usecase.NewPublishOutbox(articles, outbox, decisions, uow, pub, testBackoff, publishing.PolicyConfig{}, "restarted-worker", 10, staleAfter, clock, fakeLogger{}, clusterPublications, sourcePolicyCooldowns,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)
	result, err := publishOutbox.Run(ctx)
	if err != nil {
		t.Fatalf("PublishOutbox.Run() error = %v", err)
	}
	if result.Published != 1 {
		t.Fatalf("result = %+v, want published=1 (the stale claim must be reclaimed)", result)
	}
}

// TestFullCycle_EnqueueIsIdempotent asserts a second Enqueue for an
// already-outboxed article is a no-op, not an error or a duplicate row —
// the "publicación idempotente" case.
func TestFullCycle_EnqueueIsIdempotent(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)

	if err := articles.Save(ctx, article.Article{ID: "a1", CanonicalURL: "https://example.com/a1", PublishedAt: now, FetchedAt: now, Status: article.StatusPendingPublication}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if err := outbox.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	if err := outbox.Enqueue(ctx, "a1", `{"changed":"payload"}`, now); err != nil {
		t.Fatalf("second Enqueue() error = %v", err)
	}

	claimed, err := outbox.ClaimBatch(ctx, "worker-1", 10, time.Minute, now)
	if err != nil {
		t.Fatalf("ClaimBatch() error = %v", err)
	}
	if len(claimed) != 1 {
		t.Errorf("claimed = %v, want exactly 1 row despite two Enqueue calls", claimed)
	}
}

// TestFullCycle_ClusterWithoutMatchingRepresentativeCandidate covers the
// "cluster sin representante" edge case: a join where no same-cluster
// candidate is found among the scored candidates (winningClusterCandidate
// returns ok=false inside ProcessCycle.maybeUpdateRepresentative) must not
// error or panic — it simply skips the representative-switch check.
func TestFullCycle_ClusterWithoutMatchingRepresentativeCandidate(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

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

	// No prior clusters/candidates exist in a fresh DB, so the first
	// article's clustering step always takes the ActionCreate path — there
	// is no matching representative candidate by construction.
	fetcher := fakeFetcher{articles: []article.Article{{
		OriginalURL: "https://example.com/first", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	clock := fakeClock{now: now}
	processCycle := usecase.NewProcessCycle(fetcher, articles, dedup, aliases, similarityIndex, clusters, decisions, outbox, uow, freshness, dictionary.Matcher{}, nil, nil, defaultCascade(), clock, fakeLogger{}, sourcePolicies, &fakeSourcePolicyResolver{}, clusterPublications, seenItems, sourcePolicyCooldowns, unknownEntities, nil)

	result, err := processCycle.Run(ctx)
	if err != nil {
		t.Fatalf("ProcessCycle.Run() error = %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want accepted=1 even with no representative candidate to compare against", result)
	}
}

// TestFullCycle_RetryThenSuccess_ConfirmsClusterGateOnlyOnEventualSuccess is
// the "retry" scenario: a temporary failure followed by an eventual success
// must leave the gate reserved throughout every retry and confirmed only
// once delivery actually succeeds — never released mid-retry (which would
// open a duplicate path for a different article).
func TestFullCycle_RetryThenSuccess_ConfirmsClusterGateOnlyOnEventualSuccess(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	gate := sqliteadapter.NewClusterPublicationRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)

	if _, err := db.ExecContext(ctx, `INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', ?, ?, 'open')`, now, now); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if err := articles.Save(ctx, article.Article{ID: "a1", CanonicalURL: "https://example.com/a1", Source: source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}, PublishedAt: now, FetchedAt: now, Status: article.StatusPendingPublication, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if err := outbox.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if acquired, err := gate.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a1", MemberRole: "first_report"}, now); err != nil || !acquired {
		t.Fatalf("Reserve() = %v, %v, want true, nil", acquired, err)
	}

	// First attempt: temporary failure.
	failing := &fakePublisher{err: errors.New("telegram unreachable")}
	pub1 := usecase.NewPublishOutbox(articles, outbox, decisions, uow, failing, testBackoff, publishing.PolicyConfig{}, "worker-1", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, gate, sourcePolicyCooldowns,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)
	if _, err := pub1.Run(ctx); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&state); err != nil {
		t.Fatalf("query gate state: %v", err)
	}
	if state != "reserved" {
		t.Fatalf("state after temporary failure = %q, want reserved", state)
	}

	// Second attempt, well past backoff: succeeds.
	succeeding := &fakePublisher{}
	pub2 := usecase.NewPublishOutbox(articles, outbox, decisions, uow, succeeding, testBackoff, publishing.PolicyConfig{}, "worker-1", 10, time.Minute, fakeClock{now: now.Add(time.Hour)}, fakeLogger{}, gate, sourcePolicyCooldowns,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)
	result, err := pub2.Run(ctx)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result.Published != 1 {
		t.Fatalf("second result = %+v, want published=1", result)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&state); err != nil {
		t.Fatalf("query gate state: %v", err)
	}
	if state != "confirmed" {
		t.Errorf("final state = %q, want confirmed", state)
	}
}

// TestFullCycle_PermanentFailureReleasesGateForALaterArticle is the "fallo
// después de crear outbox" scenario: delivery permanently fails, the gate
// releases, and a later, different article for the same cluster can then
// legitimately acquire it.
func TestFullCycle_PermanentFailureReleasesGateForALaterArticle(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	gate := sqliteadapter.NewClusterPublicationRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)

	if _, err := db.ExecContext(ctx, `INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES ('c1', ?, ?, 'open')`, now, now); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if err := articles.Save(ctx, article.Article{ID: "a1", CanonicalURL: "https://example.com/a1", Source: source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}, PublishedAt: now, FetchedAt: now, Status: article.StatusPendingPublication, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article a1: %v", err)
	}
	if err := outbox.Enqueue(ctx, "a1", "{}", now); err != nil {
		t.Fatalf("Enqueue() a1 error = %v", err)
	}
	if acquired, err := gate.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a1", MemberRole: "first_report"}, now); err != nil || !acquired {
		t.Fatalf("Reserve() = %v, %v, want true, nil", acquired, err)
	}

	pub := usecase.NewPublishOutbox(articles, outbox, decisions, uow, &fakePublisher{err: errors.New("rejected"), permanent: true}, testBackoff, publishing.PolicyConfig{}, "worker-1", 10, time.Minute, fakeClock{now: now}, fakeLogger{}, gate, sourcePolicyCooldowns,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)
	if _, err := pub.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM event_cluster_publications WHERE cluster_id = 'c1'`).Scan(&state); err != nil {
		t.Fatalf("query gate state: %v", err)
	}
	if state != "released" {
		t.Fatalf("state after permanent failure = %q, want released", state)
	}

	// A later, different article for the same cluster must now be able to
	// acquire the gate.
	if err := articles.Save(ctx, article.Article{ID: "a2", CanonicalURL: "https://example.com/a2", Source: source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS}, PublishedAt: now, FetchedAt: now, Status: article.StatusClustered, ClusterID: "c1"}); err != nil {
		t.Fatalf("seed article a2: %v", err)
	}
	acquired, err := gate.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a2", MemberRole: "confirmation"}, now)
	if err != nil {
		t.Fatalf("Reserve() a2 error = %v", err)
	}
	if !acquired {
		t.Error("acquired = false, want true: a released gate must be re-claimable by a later article")
	}
}

// TestRunRetention_PreservesEditorialExamplesAcrossPurge is the core
// regression test: label two articles (one publish, one
// reject), export the training dataset, purge both underlying articles
// via RunRetention (one from each of the three terminal-rejected
// statuses that feed rejected_articles_days, plus the separate
// duplicates_days window), and prove the export produced after retention
// is identical to the one produced before — even though the articles
// themselves are gone. Runs retention twice to prove idempotency: a
// second run finds nothing left to purge and still reports the same
// EditorialExamplesRetained count.
func TestRunRetention_PreservesEditorialExamplesAcrossPurge(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	old := now.Add(-365 * 24 * time.Hour)

	articles := sqliteadapter.NewArticleRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	clusters := sqliteadapter.NewClusterRepository(db)
	checkpointer := sqliteadapter.NewCheckpointer(db)

	seedRejected := func(id string, status article.Status) {
		t.Helper()
		if err := articles.Save(ctx, article.Article{
			ID: id, OriginalURL: "https://example.com/" + id, CanonicalURL: "https://example.com/" + id,
			Domain: "example.com", Title: "Title " + id, Summary: "Body text for " + id,
			Author: "Reporter", ContentType: article.ContentTypeArticle,
			FetchedAt: old, PublishedAt: old, Status: status,
		}); err != nil {
			t.Fatalf("save article %s: %v", id, err)
		}
	}

	seedRejected("publish-1", article.StatusRejectedQuality)
	seedRejected("reject-1", article.StatusRejectedNotRelated)
	seedRejected("dup-1", article.StatusRejectedDuplicate)
	seedRejected("discard-1", article.StatusDiscarded)

	if err := labels.Label(ctx, training.Example{ArticleID: "publish-1", Label: "publish", LabeledAt: now}); err != nil {
		t.Fatalf("label publish-1: %v", err)
	}
	if err := labels.Label(ctx, training.Example{ArticleID: "reject-1", Label: "reject", Reason: "not_related", LabeledAt: now}); err != nil {
		t.Fatalf("label reject-1: %v", err)
	}
	if err := labels.Label(ctx, training.Example{ArticleID: "dup-1", Label: "reject", Reason: "duplicate", LabeledAt: now}); err != nil {
		t.Fatalf("label dup-1: %v", err)
	}
	if err := labels.Label(ctx, training.Example{ArticleID: "discard-1", Label: "reject", Reason: "quality", LabeledAt: now}); err != nil {
		t.Fatalf("label discard-1: %v", err)
	}

	before, err := editorialtrain.ExportDataset(ctx, labels, nil)
	if err != nil {
		t.Fatalf("ExportDataset() before retention error = %v", err)
	}
	if len(before) != 4 {
		t.Fatalf("len(before) = %d, want 4", len(before))
	}

	windows := in.RetentionWindows{RejectedArticles: 24 * time.Hour, Duplicates: 24 * time.Hour, AuditEvents: 24 * time.Hour, PublicationAttempts: 24 * time.Hour, Clusters: 24 * time.Hour}
	retention := usecase.NewRunRetention(articles, decisions, outbox, clusters, labels, checkpointer, windows, fakeClock{now: now}, fakeLogger{})

	result, err := retention.Run(ctx)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.RejectedArticlesDeleted != 3 {
		t.Errorf("RejectedArticlesDeleted = %d, want 3 (publish-1: rejected_quality, reject-1: rejected_not_related, discard-1: discarded)", result.RejectedArticlesDeleted)
	}
	if result.DuplicatesDeleted != 1 {
		t.Errorf("DuplicatesDeleted = %d, want 1", result.DuplicatesDeleted)
	}
	if result.EditorialExamplesRetained != 4 {
		t.Errorf("EditorialExamplesRetained = %d, want 4 (retention must never shrink the corpus)", result.EditorialExamplesRetained)
	}

	for _, id := range []string{"publish-1", "reject-1", "dup-1", "discard-1"} {
		if _, err := articles.FindByID(ctx, id); err == nil {
			t.Errorf("FindByID(%s) succeeded after retention, want the article purged", id)
		}
	}

	after, err := editorialtrain.ExportDataset(ctx, labels, nil)
	if err != nil {
		t.Fatalf("ExportDataset() after retention error = %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("len(after) = %d, want %d (same as before retention)", len(after), len(before))
	}
	byID := func(examples []editorialtrain.TrainingExample) map[string]editorialtrain.TrainingExample {
		m := make(map[string]editorialtrain.TrainingExample, len(examples))
		for _, e := range examples {
			m[e.ArticleID] = e
		}
		return m
	}
	beforeByID, afterByID := byID(before), byID(after)
	for id, b := range beforeByID {
		a, ok := afterByID[id]
		if !ok {
			t.Errorf("article %s present before retention, missing after", id)
			continue
		}
		if a.Title != b.Title || a.Body != b.Body || a.Label != b.Label || a.Reason != b.Reason || a.SourceID != b.SourceID || a.Domain != b.Domain {
			t.Errorf("example for %s changed across retention:\nbefore=%+v\nafter=%+v", id, b, a)
		}
	}

	result2, err := retention.Run(ctx)
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result2.RejectedArticlesDeleted != 0 || result2.DuplicatesDeleted != 0 {
		t.Errorf("second Run() deleted %+v, want nothing left to delete", result2)
	}
	if result2.EditorialExamplesRetained != 4 {
		t.Errorf("second Run() EditorialExamplesRetained = %d, want 4 (idempotent — no further loss)", result2.EditorialExamplesRetained)
	}
}

// TestApplyDecision_NonPersistingDecisionPreservesSurvivingEditorialExample
// pins a fix delivered by the human-label-history rework. The OLD
// ApplyDecision called Labels.DeleteByArticle unconditionally before every
// review decision, persisting or not, so a reviewer could "back" and
// re-decide an article within the same session without leaving a stale,
// contradictory human_labels row behind. That call had no way to
// distinguish "this article_id's human_labels row already outlived its
// editorial_examples snapshot (e.g. a past RunRetention purge already
// cascade-deleted it)" from "this article is being decided for the first
// time" — so a later, unrelated non-persisting decision (e.g. ambiguous)
// on the same article_id would still clear the surviving durable
// snapshot, even though nothing replaced it.
//
// The new ApplyDecision (app/editorialcycle/session.go)
// closes this gap: it reads the article's actual active label via
// Labels.GetActive first, and only acts (Relabel/Retract) when there is
// an active label to supersede. For a non-persisting decision on an
// article with no active label — exactly this test's scenario, a
// human_labels row manually deleted to simulate an already-completed
// retention purge — it now correctly does nothing, leaving any surviving
// durable editorial_examples snapshot untouched. This test pins that
// corrected behavior so it is never silently regressed.
func TestApplyDecision_NonPersistingDecisionPreservesSurvivingEditorialExample(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	articles := sqliteadapter.NewArticleRepository(db)
	labels := sqliteadapter.NewHumanLabelRepository(db)
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	reviewDecisions := sqliteadapter.NewReviewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)

	articleID := "a1"
	if err := articles.Save(ctx, article.Article{
		ID: articleID, OriginalURL: "https://example.com/a1", CanonicalURL: "https://example.com/a1",
		Domain: "example.com", Title: "Title", Summary: "Body",
		Author: "Reporter", ContentType: article.ContentTypeArticle,
		FetchedAt: now, PublishedAt: now, Status: article.StatusRejectedQuality,
	}); err != nil {
		t.Fatalf("save article: %v", err)
	}

	if err := labels.Label(ctx, training.Example{ArticleID: articleID, Label: "reject", Reason: "quality", LabeledAt: now}); err != nil {
		t.Fatalf("label article: %v", err)
	}

	// Simulate the human_labels row having already outlived — i.e. having
	// already been cascade-deleted out from under — its editorial_examples
	// counterpart, by a past RunRetention purge, while the durable
	// snapshot survived (the whole point of this branch's fix). Deleting
	// human_labels directly, without touching the article or the
	// editorial_examples row, reproduces exactly that state.
	if _, err := db.ExecContext(ctx, `DELETE FROM human_labels WHERE article_id = ?`, articleID); err != nil {
		t.Fatalf("delete human_labels row: %v", err)
	}

	countBefore, err := labels.CountEditorialExamples(ctx)
	if err != nil {
		t.Fatalf("CountEditorialExamples() before error = %v", err)
	}
	if countBefore != 1 {
		t.Fatalf("CountEditorialExamples() before = %d, want 1 (the surviving snapshot)", countBefore)
	}

	deps := editorialcycle.Deps{
		Sessions: sessions, Candidates: sqliteadapter.NewEditorialCycleSessionCandidateRepository(db),
		ReviewDecisions: reviewDecisions, Labels: labels, UOW: uow,
		Clock: fakeClock{now: now},
	}
	// editorial_review_decisions.session_id has a foreign key to
	// editorial_cycle_sessions(id) (migration 0012), so ApplyDecision needs
	// a real session row to reference. This test is about ApplyDecision's
	// effect on editorial_examples, not about the review queue, so the
	// session freezes no candidates.
	session, _, _, err := editorialcycle.StartOrResume(ctx, deps, editorialcycle.Flags{}, nil)
	if err != nil {
		t.Fatalf("StartOrResume() error = %v", err)
	}
	if err := editorialcycle.ApplyDecision(ctx, deps, session.ID, articleID, reviewdecision.ActionAmbiguous, ""); err != nil {
		t.Fatalf("ApplyDecision() error = %v", err)
	}

	countAfter, err := labels.CountEditorialExamples(ctx)
	if err != nil {
		t.Fatalf("CountEditorialExamples() after error = %v", err)
	}
	if countAfter != 1 {
		t.Errorf("CountEditorialExamples() after = %d, want 1 — ApplyDecision now reads the actual active label via GetActive before deciding what to do, so a non-persisting decision on an article with no active label correctly leaves the surviving editorial_examples snapshot untouched", countAfter)
	}
}
