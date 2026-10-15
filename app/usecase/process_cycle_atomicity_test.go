package usecase_test

// This file covers the Gate A bug (shadow mode, RetainNearDuplicates)
// could insert a source_policy_decisions row before the article row it
// references existed, violating that table's foreign key — and, more
// generally, no write ProcessCycle performs for a single article was ever
// atomic with any other. TestProcessCycle_Integration_ShadowModeRetainNearDuplicate_ReproducesGateAForeignKeyViolation
// (process_cycle_integration_test.go) proves the original bug is fixed;
// the tests here prove the fix's actual mechanism — wrapping every write
// for one article in a single transaction, with the article row saved
// first — by injecting a failure at each write in turn (real SQLite, not
// fakes: fakeUnitOfWork does not roll back, so only a real transaction can
// demonstrate this) and asserting that failure leaves zero rows behind
// anywhere, then that a clean retry afterward leaves exactly one coherent
// set of rows with no duplicates.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// errInjected is the sentinel every decorator below returns in place of
// delegating to the real adapter, standing in for whatever transient error
// (disk full, busy timeout, driver error) could hit a real write.
var errInjected = errors.New("injected write failure")

// failingArticleRepo wraps the real ArticleRepository and fails its Save
// method when armed, delegating everything else (including reads) to the
// real adapter — so the rest of ProcessCycle still observes a consistent
// database.
type failingArticleRepo struct {
	*sqliteadapter.ArticleRepository
	failSave bool
}

func (r *failingArticleRepo) Save(ctx context.Context, a article.Article) error {
	if r.failSave {
		return errInjected
	}
	return r.ArticleRepository.Save(ctx, a)
}

type failingSourcePolicyRepo struct {
	*sqliteadapter.SourcePolicyRepository
	failSave bool
}

func (r *failingSourcePolicyRepo) Save(ctx context.Context, rec out.SourcePolicyDecisionRecord) error {
	if r.failSave {
		return errInjected
	}
	return r.SourcePolicyRepository.Save(ctx, rec)
}

type failingDecisionRepo struct {
	*sqliteadapter.DecisionRepository
	failAppend bool
}

func (r *failingDecisionRepo) Append(ctx context.Context, e out.Event) error {
	if r.failAppend {
		return errInjected
	}
	return r.DecisionRepository.Append(ctx, e)
}

type failingClusterRepo struct {
	*sqliteadapter.ClusterRepository
	failCreateCluster bool
}

func (r *failingClusterRepo) CreateCluster(ctx context.Context, id, articleID string, status clustering.Status, now time.Time) error {
	if r.failCreateCluster {
		return errInjected
	}
	return r.ClusterRepository.CreateCluster(ctx, id, articleID, status, now)
}

type failingOutboxRepo struct {
	*sqliteadapter.OutboxRepository
	failEnqueue bool
}

func (r *failingOutboxRepo) EnqueueLane(ctx context.Context, articleID, payload string, lane publishing.PublicationLane, now time.Time) error {
	if r.failEnqueue {
		return errInjected
	}
	return r.OutboxRepository.EnqueueLane(ctx, articleID, payload, lane, now)
}

func (r *failingOutboxRepo) Enqueue(ctx context.Context, articleID, payload string, now time.Time) error {
	if r.failEnqueue {
		return errInjected
	}
	return r.OutboxRepository.Enqueue(ctx, articleID, payload, now)
}

type failingClusterPublicationRepo struct {
	*sqliteadapter.ClusterPublicationRepository
	failReserve bool
}

func (r *failingClusterPublicationRepo) Reserve(ctx context.Context, res out.ClusterPublicationReservation, now time.Time) (bool, error) {
	if r.failReserve {
		return false, errInjected
	}
	return r.ClusterPublicationRepository.Reserve(ctx, res, now)
}

type failingSeenItemsRepo struct {
	*sqliteadapter.SeenItemsRepository
	failFinalize bool
}

func (r *failingSeenItemsRepo) Finalize(ctx context.Context, identity ingestidentity.Identity, outcome, contentHash, canonicalURL string, now time.Time) error {
	if r.failFinalize {
		return errInjected
	}
	return r.SeenItemsRepository.Finalize(ctx, identity, outcome, contentHash, canonicalURL, now)
}

// atomicityRepos holds every real-SQLite repository ProcessCycle needs,
// each already the concrete adapter type so a test can swap exactly one
// field for a failing decorator before building the use case.
type atomicityRepos struct {
	articles       *failingArticleRepo
	dedup          out.DeduplicationRepository
	aliases        out.AliasRepository
	similarity     out.SimilarityIndexRepository
	clusters       *failingClusterRepo
	decisions      *failingDecisionRepo
	outbox         *failingOutboxRepo
	sourcePolicies *failingSourcePolicyRepo

	clusterPublications *failingClusterPublicationRepo
	seenItems           *failingSeenItemsRepo

	sourcePolicyCooldowns out.SourcePolicyCooldownRepository

	unknownEntities out.UnknownEntityRepository
}

func newAtomicityRepos(db *sql.DB) *atomicityRepos {
	return &atomicityRepos{
		articles:       &failingArticleRepo{ArticleRepository: sqliteadapter.NewArticleRepository(db)},
		dedup:          sqliteadapter.NewDeduplicationRepository(db),
		aliases:        sqliteadapter.NewAliasRepository(db),
		similarity:     sqliteadapter.NewSimilarityIndexRepository(db),
		clusters:       &failingClusterRepo{ClusterRepository: sqliteadapter.NewClusterRepository(db)},
		decisions:      &failingDecisionRepo{DecisionRepository: sqliteadapter.NewDecisionRepository(db)},
		outbox:         &failingOutboxRepo{OutboxRepository: sqliteadapter.NewOutboxRepository(db)},
		sourcePolicies: &failingSourcePolicyRepo{SourcePolicyRepository: sqliteadapter.NewSourcePolicyRepository(db)},

		clusterPublications: &failingClusterPublicationRepo{ClusterPublicationRepository: sqliteadapter.NewClusterPublicationRepository(db)},
		seenItems:           &failingSeenItemsRepo{SeenItemsRepository: sqliteadapter.NewSeenItemsRepository(db)},

		sourcePolicyCooldowns: sqliteadapter.NewSourcePolicyCooldownRepository(db),

		unknownEntities: sqliteadapter.NewUnknownEntityRepository(db),
	}
}

func (r *atomicityRepos) build(db *sql.DB, fetcher fakeFetcher, clock fakeClock, resolver *fakeSourcePolicyResolver) *usecase.ProcessCycle {
	uow := sqliteadapter.NewUnitOfWork(db)
	return usecase.NewProcessCycle(
		fetcher, r.articles, r.dedup, r.aliases, r.similarity, r.clusters, r.decisions, r.outbox, uow,
		freshness, dictionary.Matcher{}, nil, nil, defaultCascade(), clock, fakeLogger{}, r.sourcePolicies, resolver, r.clusterPublications, r.seenItems, r.sourcePolicyCooldowns, r.unknownEntities, nil,
	)
}

// tableCounts snapshots every table a single article's processing can
// write to, so a test can assert "nothing at all was left behind" without
// needing to know the article's generated ID (each run mints a fresh
// uuid.NewString(), unobservable from the test).
type tableCounts struct {
	articles, sourcePolicyDecisions, events, aliases, fingerprints, clusters, clusterMembers, outbox, clusterPublications, seenItems, unknownEntityOccurrences int
}

func countTables(t *testing.T, db *sql.DB) tableCounts {
	t.Helper()
	q := func(table string) int {
		var n int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	return tableCounts{
		articles:                 q("articles"),
		sourcePolicyDecisions:    q("source_policy_decisions"),
		events:                   q("article_processing_events"),
		aliases:                  q("article_aliases"),
		fingerprints:             q("exact_fingerprints"),
		clusters:                 q("event_clusters"),
		clusterMembers:           q("event_cluster_members"),
		outbox:                   q("publication_outbox"),
		clusterPublications:      q("event_cluster_publications"),
		seenItems:                q("seen_items"),
		unknownEntityOccurrences: q("unknown_entity_occurrences"),
	}
}

func newAtomicityDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "process_cycle_atomicity.db")
	copyMigratedTestDB(t, path)
	db, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestProcessCycle_Integration_GateAShadowRetain_FailureAtEachWriteRollsBackEverything
// drives the exact failure scenario (shadow mode, RetainNearDuplicates
// true — a near-duplicate that gets rejected after Gate A records its
// shadow-mode "would retain" preview) and, in each subtest, fails a
// different one of that path's three writes (parent article Save, the
// source_policy_decisions Save that used to run before the article
// existed, and the terminal reject's article_processing_events Append).
// Every subtest must see zero rows in every table afterward: the whole
// per-article write sequence is one transaction.
func TestProcessCycle_Integration_GateAShadowRetain_FailureAtEachWriteRollsBackEverything(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	officialSource := source.Source{ID: mustID(t, source.TypeRSS, "official-source"), Type: source.TypeRSS}
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeShadow,
		policies: map[string]sourcepolicy.Policy{
			"rss:official-source": {SourceID: "rss:official-source", Authority: sourcepolicy.AuthorityOfficialTeam, RetainNearDuplicates: true},
		},
	}

	tests := []struct {
		name string
		arm  func(r *atomicityRepos)
	}{
		{"article_save", func(r *atomicityRepos) { r.articles.failSave = true }},
		{"source_policy_decision_save", func(r *atomicityRepos) { r.sourcePolicies.failSave = true }},
		{"reject_event_append", func(r *atomicityRepos) { r.decisions.failAppend = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newAtomicityDB(t)

			// Seed a baseline article through an unarmed cycle so the
			// second (armed) cycle has a near-duplicate to match against —
			// mirrors the repro test in process_cycle_integration_test.go.
			seedRepos := newAtomicityRepos(db)
			seedFetcher := fakeFetcher{articles: []article.Article{{
				Title: "Isotopes Beat Sharks In An Overtime Thriller", Source: officialSource,
				OriginalURL: "https://example.com/existing-copy", ContentType: article.ContentTypeArticle,
				PublishedAt: now.Add(-2 * time.Hour),
			}}}
			seedUC := seedRepos.build(db, seedFetcher, fakeClock{now: now.Add(-2 * time.Hour)}, &fakeSourcePolicyResolver{})
			if _, err := seedUC.Run(context.Background()); err != nil {
				t.Fatalf("seed Run() error = %v", err)
			}
			before := countTables(t, db)

			repos := newAtomicityRepos(db)
			tt.arm(repos)
			fetcher := fakeFetcher{articles: []article.Article{{
				Title: "Isotopes Beat Sharks In Overtime Thriller", Source: officialSource,
				OriginalURL: "https://example.com/official-copy", ContentType: article.ContentTypeArticle,
				PublishedAt: now.Add(-1 * time.Hour),
			}}}
			uc := repos.build(db, fetcher, fakeClock{now: now}, resolver)

			if _, err := uc.Run(context.Background()); !errors.Is(err, errInjected) {
				t.Fatalf("Run() error = %v, want wrapped errInjected", err)
			}

			after := countTables(t, db)
			if after != before {
				t.Errorf("table counts changed despite injected failure: before=%+v after=%+v (a partial write survived the rollback)", before, after)
			}
		})
	}
}

// TestProcessCycle_Integration_AcceptPath_FailureAtEachWriteRollsBackEverything
// exercises the accept path (clustering enabled, no near-duplicate, no
// source policy) and fails one write per subtest, covering the full
// breadth the issue asked for: the article's own Save, cluster creation
// (event_clusters/event_cluster_members), the clustering audit event, and
// — the strongest case — the very last write in the whole function
// (accept's own article_processing_events Append), proving even a
// last-statement failure undoes everything that came before it in the same
// transaction, including the article row itself.
func TestProcessCycle_Integration_AcceptPath_FailureAtEachWriteRollsBackEverything(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		arm  func(r *atomicityRepos)
	}{
		{"article_save", func(r *atomicityRepos) { r.articles.failSave = true }},
		{"cluster_create", func(r *atomicityRepos) { r.clusters.failCreateCluster = true }},
		{"cluster_gate_reserve", func(r *atomicityRepos) { r.clusterPublications.failReserve = true }},
		{"outbox_enqueue", func(r *atomicityRepos) { r.outbox.failEnqueue = true }},
		{"event_append", func(r *atomicityRepos) { r.decisions.failAppend = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newAtomicityDB(t)
			before := countTables(t, db)

			repos := newAtomicityRepos(db)
			tt.arm(repos)
			fetcher := fakeFetcher{articles: []article.Article{{
				OriginalURL: "https://example.com/atomicity-accept-story",
				ContentType: article.ContentTypeArticle,
				PublishedAt: now.Add(-1 * time.Hour),
			}}}
			uc := repos.build(db, fetcher, fakeClock{now: now}, &fakeSourcePolicyResolver{})

			if _, err := uc.Run(context.Background()); !errors.Is(err, errInjected) {
				t.Fatalf("Run() error = %v, want wrapped errInjected", err)
			}

			after := countTables(t, db)
			if after != before {
				t.Errorf("table counts changed despite injected failure: before=%+v after=%+v (a partial write survived the rollback)", before, after)
			}
		})
	}
}

// TestProcessCycle_Integration_RetryAfterFailureIsIdempotent proves the
// "retries must not create duplicate decisions" requirement directly: a
// first attempt at the scenario fails (armed to fail exactly
// where the original bug wrote first, the source_policy_decisions Save)
// and leaves zero rows; a second, unarmed attempt at the very same
// scenario against the same database then succeeds and leaves exactly one
// of everything — never two — because the failed attempt left no trace to
// duplicate.
func TestProcessCycle_Integration_RetryAfterFailureIsIdempotent(t *testing.T) {
	db := newAtomicityDB(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	officialSource := source.Source{ID: mustID(t, source.TypeRSS, "official-source"), Type: source.TypeRSS}
	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeShadow,
		policies: map[string]sourcepolicy.Policy{
			"rss:official-source": {SourceID: "rss:official-source", Authority: sourcepolicy.AuthorityOfficialTeam, RetainNearDuplicates: true},
		},
	}

	seedRepos := newAtomicityRepos(db)
	seedFetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In An Overtime Thriller", Source: officialSource,
		OriginalURL: "https://example.com/existing-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-2 * time.Hour),
	}}}
	seedUC := seedRepos.build(db, seedFetcher, fakeClock{now: now.Add(-2 * time.Hour)}, &fakeSourcePolicyResolver{})
	if _, err := seedUC.Run(context.Background()); err != nil {
		t.Fatalf("seed Run() error = %v", err)
	}

	fetcher := fakeFetcher{articles: []article.Article{{
		Title: "Isotopes Beat Sharks In Overtime Thriller", Source: officialSource,
		OriginalURL: "https://example.com/official-copy", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}

	failingRepos := newAtomicityRepos(db)
	failingRepos.sourcePolicies.failSave = true
	failingUC := failingRepos.build(db, fetcher, fakeClock{now: now}, resolver)
	if _, err := failingUC.Run(context.Background()); !errors.Is(err, errInjected) {
		t.Fatalf("first (armed) Run() error = %v, want wrapped errInjected", err)
	}

	var afterFailureDecisions int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM source_policy_decisions WHERE decision_code = ?`, string(sourcepolicy.CodeWouldRetainNearDuplicate)).Scan(&afterFailureDecisions); err != nil {
		t.Fatalf("count source_policy_decisions after failure: %v", err)
	}
	if afterFailureDecisions != 0 {
		t.Fatalf("source_policy_decisions rows after the failed attempt = %d, want 0", afterFailureDecisions)
	}

	retryRepos := newAtomicityRepos(db)
	retryUC := retryRepos.build(db, fetcher, fakeClock{now: now}, resolver)
	result, err := retryUC.Run(context.Background())
	if err != nil {
		t.Fatalf("retry Run() error = %v", err)
	}
	if result.Duplicate != 1 {
		t.Fatalf("retry result = %+v, want duplicate=1", result)
	}

	var afterRetryDecisions int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM source_policy_decisions WHERE decision_code = ?`, string(sourcepolicy.CodeWouldRetainNearDuplicate)).Scan(&afterRetryDecisions); err != nil {
		t.Fatalf("count source_policy_decisions after retry: %v", err)
	}
	if afterRetryDecisions != 1 {
		t.Errorf("source_policy_decisions rows after the successful retry = %d, want exactly 1 (no duplicate from the failed attempt)", afterRetryDecisions)
	}

	var articleCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM articles`).Scan(&articleCount); err != nil {
		t.Fatalf("count articles: %v", err)
	}
	if articleCount != 2 {
		t.Errorf("articles = %d, want 2 (the seed article plus exactly one article from the retried cycle, not two)", articleCount)
	}
}

// TestProcessCycle_Integration_GateANearDuplicateMatrix drives Gate A's
// full mode x policy matrix — shadow/enforce crossed with
// RetainNearDuplicates true/false — against real SQLite with
// PRAGMA foreign_keys=ON, so any future regression that reintroduces a
// write-ordering bug on any one of these four combinations (not just the
// shadow+retain=true case the original bug was filed against) fails loudly with
// a foreign key error instead of silently passing under the in-memory
// fakes used by process_cycle_test.go's equivalent (non-table-driven)
// coverage of this same matrix.
func TestProcessCycle_Integration_GateANearDuplicateMatrix(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	officialSource := source.Source{ID: mustID(t, source.TypeRSS, "official-source"), Type: source.TypeRSS}

	tests := []struct {
		name                 string
		mode                 sourcepolicy.Mode
		retainNearDuplicates bool
		wantDuplicate        int
		wantShadowDecision   bool
	}{
		{"shadow_retain_true", sourcepolicy.ModeShadow, true, 1, true},
		{"shadow_retain_false", sourcepolicy.ModeShadow, false, 1, false},
		{"enforce_retain_true", sourcepolicy.ModeEnforce, true, 0, false},
		{"enforce_retain_false", sourcepolicy.ModeEnforce, false, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newAtomicityDB(t)

			seedRepos := newAtomicityRepos(db)
			seedFetcher := fakeFetcher{articles: []article.Article{{
				Title: "Isotopes Beat Sharks In An Overtime Thriller", Source: officialSource,
				OriginalURL: "https://example.com/existing-copy", ContentType: article.ContentTypeArticle,
				PublishedAt: now.Add(-2 * time.Hour),
			}}}
			seedUC := seedRepos.build(db, seedFetcher, fakeClock{now: now.Add(-2 * time.Hour)}, &fakeSourcePolicyResolver{})
			if _, err := seedUC.Run(context.Background()); err != nil {
				t.Fatalf("seed Run() error = %v", err)
			}

			resolver := &fakeSourcePolicyResolver{
				mode: tt.mode,
				policies: map[string]sourcepolicy.Policy{
					"rss:official-source": {SourceID: "rss:official-source", Authority: sourcepolicy.AuthorityOfficialTeam, RetainNearDuplicates: tt.retainNearDuplicates},
				},
			}
			repos := newAtomicityRepos(db)
			fetcher := fakeFetcher{articles: []article.Article{{
				Title: "Isotopes Beat Sharks In Overtime Thriller", Source: officialSource,
				OriginalURL: "https://example.com/official-copy", ContentType: article.ContentTypeArticle,
				PublishedAt: now.Add(-1 * time.Hour),
			}}}
			uc := repos.build(db, fetcher, fakeClock{now: now}, resolver)

			result, err := uc.Run(context.Background())
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if result.Duplicate != tt.wantDuplicate {
				t.Errorf("Duplicate = %d, want %d", result.Duplicate, tt.wantDuplicate)
			}

			var shadowDecisions int
			if err := db.QueryRowContext(context.Background(), `SELECT COUNT(1) FROM source_policy_decisions WHERE decision_code = ?`, string(sourcepolicy.CodeWouldRetainNearDuplicate)).Scan(&shadowDecisions); err != nil {
				t.Fatalf("count source_policy_decisions: %v", err)
			}
			gotShadowDecision := shadowDecisions == 1
			if gotShadowDecision != tt.wantShadowDecision {
				t.Errorf("shadow would-retain decision recorded = %v, want %v", gotShadowDecision, tt.wantShadowDecision)
			}
		})
	}
}

// TestProcessCycle_Integration_SeenItemsFinalizeFailure_RollsBackEverything
// covers the seen_items.Finalize call: it runs inside the same
// per-article transaction as the terminal article Save/event Append, so a
// failure there must roll back the whole thing, and a clean retry
// afterward must process from scratch (no orphaned "claimed but never
// finalized" row blocking it).
func TestProcessCycle_Integration_SeenItemsFinalizeFailure_RollsBackEverything(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	db := newAtomicityDB(t)
	repos := newAtomicityRepos(db)
	repos.seenItems.failFinalize = true

	fetcher := fakeFetcher{articles: []article.Article{{
		ExternalID:  "guid-rollback",
		OriginalURL: "https://example.com/rollback-story",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "feed-a"), Type: source.TypeRSS},
		Title:       "A story that will fail to finalize",
		ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1000 * time.Hour), // rejected as stale — exercises reject()'s Finalize call
	}}}
	uc := repos.build(db, fetcher, fakeClock{now: now}, &fakeSourcePolicyResolver{})

	if _, err := uc.Run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want the injected seen_items finalize failure to propagate")
	}

	before := countTables(t, db)
	if before.articles != 0 || before.events != 0 || before.seenItems != 0 {
		t.Fatalf("counts = %+v, want all zero (finalize failure must roll back the article/event/seen_items writes together)", before)
	}

	repos.seenItems.failFinalize = false
	uc2 := repos.build(db, fetcher, fakeClock{now: now.Add(time.Hour)}, &fakeSourcePolicyResolver{})
	result, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("retry Run() error = %v", err)
	}
	if result.Rejected != 1 {
		t.Fatalf("retry result = %+v, want Rejected=1 (a clean retry after rollback must process from scratch, not see a stale claim)", result)
	}
	after := countTables(t, db)
	if after.articles != 1 || after.events != 2 || after.seenItems != 1 {
		t.Fatalf("counts after retry = %+v, want articles=1 events=2 seen_items=1", after)
	}
}
