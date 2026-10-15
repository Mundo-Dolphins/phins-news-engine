package usecase_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	telegramadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/telegram"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// newRealPublishOutbox wires a PublishOutbox against real SQLite
// (db, from openIntegrationDB) and a real telegram.Publisher pointed at
// srv — the "fake Telegram, real everything else" combination the issue
// asks for. workerID lets tests simulate distinct dispatchers/restarts.
func newRealPublishOutbox(db *sql.DB, srv *FakeTelegramServer, workerID string, now time.Time) (*usecase.PublishOutbox, appout.OutboxRepository) {
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	publisher := telegramadapter.New(telegramadapter.Config{ChannelID: "@test", BotToken: "test-token", BaseURL: srv.URL, Timeout: 100 * time.Millisecond})

	uc := usecase.NewPublishOutbox(
		articles, outbox, decisions, uow, publisher, testBackoff, allowAllPolicy,
		workerID, 10, time.Minute, fixedClock{now}, fakeLogger{}, clusterPublications, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10}, nil, // no channel importer in this test file — see the dedicated reconciliation tests below
	)
	return uc, outbox
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func seedRealPendingArticle(t *testing.T, db *sql.DB, id, canonicalURL string, now time.Time) {
	t.Helper()
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	// A Source with a valid ID is required here even though PublishOutbox
	// itself never inspects it: ArticleRepository persists Source.ID as
	// its "platform:identifier" string form and rejects a zero-value ID
	// on the way back out (source.ID{}.String() == ":", which
	// source.ParseID rejects) — so any article a real Run() will
	// FindByID (every fault-injection test in this file does) needs one,
	// exactly like TestFullCycle_RestartBetweenClaimAndPublish_ReclaimsAndPublishes
	// in integration_test.go already does.
	if err := articles.Save(context.Background(), article.Article{
		ID: id, CanonicalURL: canonicalURL, Status: article.StatusPendingPublication, FetchedAt: now, PublishedAt: now,
		Source: source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if err := outbox.Enqueue(context.Background(), id, "{}", now); err != nil {
		t.Fatalf("enqueue outbox item: %v", err)
	}
}

func TestFaultInjection_P1_CrashBeforeCallingTelegram_LeasedItemSafelyReclaimed(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedRealPendingArticle(t, db, "a1", "https://example.com/a1", now)

	// Simulate crash point 1 directly: claim the item (this is the only
	// state a real process could have reached before ever calling
	// Telegram) and go no further — no Publish call happens at all.
	outbox := sqliteadapter.NewOutboxRepository(db)
	items, err := outbox.ClaimBatch(context.Background(), "worker-1", 10, time.Minute, now)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v", items, err)
	}
	if srv.Calls() != 0 {
		t.Fatalf("srv.Calls() = %d, want 0 — Telegram must never be contacted before this point", srv.Calls())
	}

	// "Restart": a fresh PublishOutbox, after the lease has expired.
	uc, _ := newRealPublishOutbox(db, srv, "worker-2", now.Add(2*time.Minute))
	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Published != 1 {
		t.Fatalf("result = %+v, want published=1 (safe to reclaim and publish: nothing was ever sent)", result)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d, want exactly 1", srv.Calls())
	}
}

func TestFaultInjection_P2_TimeoutDuringSend_NoImmediateRetryNoBlindResend(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	srv.SetMode("hang") // the send is in flight when "the process" gives up (client timeout)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedRealPendingArticle(t, db, "a1", "https://example.com/a1", now)

	uc, outbox := newRealPublishOutbox(db, srv, "worker-1", now)
	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.AmbiguousDetected != 1 || result.Published != 0 || result.Retried != 0 {
		t.Fatalf("result = %+v, want AmbiguousDetected=1 and nothing else", result)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d, want 1", srv.Calls())
	}

	// A second Run tick — simulating the scheduler's normal next
	// interval, still with no channel importer configured — must not
	// call Telegram again: it must quarantine, not blindly retry.
	result2, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if result2.Quarantined != 1 {
		t.Fatalf("result2 = %+v, want Quarantined=1", result2)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d after a second tick, want still 1 — no blind resend of an unresolved ambiguity", srv.Calls())
	}

	list, err := outbox.ListQuarantined(context.Background(), 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListQuarantined() = %+v, %v, want 1 item", list, err)
	}
}

// TestFaultInjection_P3_CrashAfterMessageIDBeforeCommit_NoBlindResendOnRestart
// is the literal scenario from ADR 0007/0016's "known
// risk": Telegram accepts sendMessage, but the process dies before any
// local record of that success exists. Before this feature, that left
// the item 'claimed' with zero evidence — the next stale-claim reclaim
// sent it again, producing a real duplicate channel post. This test
// hand-replicates that crash point for a second article (claim it, call
// the real publisher once, record only the sent-unconfirmed evidence,
// then stop — no finalize), then demonstrates the guarantee this feature
// actually delivers: given that durable evidence, a fresh PublishOutbox
// instance ("restart") reconciles from it instead of resending. It does
// not, on its own, prove that production's recordSentThenFinalize always
// writes that evidence before attempting to finalize — that ordering
// invariant is covered elsewhere in this package's test suite.
func TestFaultInjection_P3_CrashAfterMessageIDBeforeCommit_NoBlindResendOnRestart(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedRealPendingArticle(t, db, "a1", "https://example.com/a1", now)

	uc, outbox := newRealPublishOutbox(db, srv, "worker-1", now)
	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Published != 1 {
		t.Fatalf("result = %+v, want published=1", result)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d, want 1", srv.Calls())
	}

	// Now simulate the crash directly at fault-injection point 3 for a
	// SECOND article: claim it, call the real publisher once (Telegram
	// accepts it — message_id known), but do NOT let the use case
	// proceed any further — this is what a process death immediately
	// after Publish() returns looks like at the SQLite level, before
	// this feature existed. This hand-replicates the sequence
	// production's recordSentThenFinalize follows (Publish, then
	// RecordSentUnconfirmed, then stop before finalize) rather than
	// driving that unexported method itself, so it does not by itself
	// prove production always writes evidence before finalizing — see
	// this test's doc comment. What it does prove is the part below: once
	// that evidence is durably recorded, a restart reconciles from it
	// instead of resending.
	seedRealPendingArticle(t, db, "a2", "https://example.com/a2", now)
	items, err := outbox.ClaimBatch(context.Background(), "worker-1", 10, time.Minute, now)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch() = %v, %v", items, err)
	}
	item := items[0]
	publisher := telegramadapter.New(telegramadapter.Config{ChannelID: "@test", BotToken: "test-token", BaseURL: srv.URL, Timeout: 100 * time.Millisecond})
	articles := sqliteadapter.NewArticleRepository(db)
	a2, err := articles.FindByID(context.Background(), "a2")
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	messageID, err := publisher.Publish(context.Background(), a2)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if err := outbox.RecordSentUnconfirmed(context.Background(), item.ID, messageID, "", now); err != nil {
		t.Fatalf("RecordSentUnconfirmed() error = %v", err)
	}
	// "Crash" here: the finalize transaction (MarkDelivered/RecordAttempt/
	// article save/decision event) never runs — that's the exact window
	// ADR 0007/0016 flagged. srv.Calls() is now 2 (one per article).
	if srv.Calls() != 2 {
		t.Fatalf("srv.Calls() = %d, want 2", srv.Calls())
	}

	// "Restart": a fresh PublishOutbox instance, well past staleAfter.
	uc2, _ := newRealPublishOutbox(db, srv, "worker-2", now.Add(2*time.Minute))
	result2, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() (restart) error = %v", err)
	}
	if result2.ReconciledPositive != 1 {
		t.Fatalf("result2 = %+v, want ReconciledPositive=1 (Reconcile finalizes from the recorded message_id — no network call needed)", result2)
	}
	if srv.Calls() != 2 {
		t.Fatalf("srv.Calls() = %d after restart, want STILL 2 — this is the regression this feature closes: no duplicate Telegram post", srv.Calls())
	}

	list, err := outbox.ListSentUnconfirmed(context.Background(), 10, now.Add(time.Hour))
	if err != nil || len(list) != 0 {
		t.Fatalf("ListSentUnconfirmed() = %+v, %v, want empty (a2 must be fully finalized now)", list, err)
	}
}

func TestFaultInjection_P4_CrashAfterCommit_NeverReclaimed(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedRealPendingArticle(t, db, "a1", "https://example.com/a1", now)

	uc, _ := newRealPublishOutbox(db, srv, "worker-1", now)
	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d, want 1", srv.Calls())
	}

	// "Restart" long after — a delivered item is never reclaimable.
	uc2, _ := newRealPublishOutbox(db, srv, "worker-2", now.Add(24*time.Hour))
	result, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() (restart) error = %v", err)
	}
	if result.Claimed != 0 {
		t.Fatalf("result = %+v, want claimed=0", result)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d, want still 1", srv.Calls())
	}
}

func TestFaultInjection_ConfirmedRejectionNeverGoesThroughAmbiguousPath(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	srv.SetMode("reject")
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedRealPendingArticle(t, db, "a1", "https://example.com/a1", now)

	uc, _ := newRealPublishOutbox(db, srv, "worker-1", now)
	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Discarded != 1 || result.AmbiguousDetected != 0 {
		t.Fatalf("result = %+v, want discarded=1, ambiguous=0 (a confirmed 400 is never ambiguous)", result)
	}
}

func TestFaultInjection_ReconcilePositiveViaFakeChannelHistory(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	srv.SetMode("hang")
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedRealPendingArticle(t, db, "a1", "https://example.com/a1", now)

	// NOTE: the message text below is plain text with a bare URL, not the
	// HTML-anchor form (`<a href="...">Leer más</a>`) originally sketched
	// for this test — telegramimport.ExtractURL uses a bare `https?://\S+`
	// regex with no HTML awareness, so an anchor tag's href gets captured
	// together with the trailing `">Leer` and fails to normalize as a
	// URL at all (verified: MatchInHistory returns matched=false,
	// covered=false on that input, which would quarantine, not reconcile
	// positive, defeating this test's purpose). Every other reconciliation
	// test in this package (see TestPublishOutbox_ReconcilePositiveViaChannelHistory
	// in publish_outbox_test.go) already uses this same plain-text style.
	uc := buildPublishOutboxWithImporter(db, srv, "worker-1", now, &fakeChannelImporter{messages: []telegramimport.Message{
		{ID: 55, PostedAt: now.Add(time.Second), Text: "Leer más: https://example.com/a1"},
	}})
	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d, want 1 (the one ambiguous send attempt)", srv.Calls())
	}
	result, err := uc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.ReconciledPositive != 1 {
		t.Fatalf("result = %+v, want ReconciledPositive=1", result)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d after reconciling via channel history, want still 1 — a channel-history match must finalize without contacting Telegram again", srv.Calls())
	}
}

// buildPublishOutboxWithImporter is newRealPublishOutbox plus a
// channel-history importer — factored out because only the reconciliation
// tests in this file need one.
func buildPublishOutboxWithImporter(db *sql.DB, srv *FakeTelegramServer, workerID string, now time.Time, importer appout.TelegramChannelImporter) *usecase.PublishOutbox {
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	publisher := telegramadapter.New(telegramadapter.Config{ChannelID: "@test", BotToken: "test-token", BaseURL: srv.URL, Timeout: 100 * time.Millisecond})
	return usecase.NewPublishOutbox(
		articles, outbox, decisions, uow, publisher, testBackoff, allowAllPolicy,
		workerID, 10, time.Minute, fixedClock{now}, fakeLogger{}, clusterPublications, &fakeSourcePolicyCooldownRepository{},
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@test", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, importer,
	)
}

func TestFaultInjection_ManualRepairResolvesWhatReconcileCannot(t *testing.T) {
	db := openIntegrationDB(t)
	srv := NewFakeTelegramServer()
	defer srv.Close()
	srv.SetMode("hang")
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	seedRealPendingArticle(t, db, "a1", "https://example.com/a1", now)

	uc, outbox := newRealPublishOutbox(db, srv, "worker-1", now) // no importer: will quarantine
	if _, err := uc.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := uc.Run(context.Background()); err != nil { // second tick: quarantines
		t.Fatalf("second Run() error = %v", err)
	}
	if srv.Calls() != 1 {
		t.Fatalf("srv.Calls() = %d, want 1 — quarantining must not contact Telegram again", srv.Calls())
	}
	list, err := outbox.ListQuarantined(context.Background(), 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListQuarantined() = %+v, %v", list, err)
	}

	decisions := sqliteadapter.NewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	articles := sqliteadapter.NewArticleRepository(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	repair := usecase.NewRepairOutbox(outbox, articles, clusterPublications, decisions, uow, fixedClock{now}, fakeLogger{})
	if err := repair.Run(context.Background(), list[0].ID, publishing.RepairRetry, "a1", "operator confirmed via channel: not delivered"); err != nil {
		t.Fatalf("repair.Run() error = %v", err)
	}

	srv.SetMode("ok")
	uc3, _ := newRealPublishOutbox(db, srv, "worker-3", now.Add(time.Hour))
	result, err := uc3.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() after repair error = %v", err)
	}
	if result.Published != 1 {
		t.Fatalf("result = %+v, want published=1 — the operator's retry decision must return the item to the claimable pool", result)
	}
	if srv.Calls() != 2 {
		t.Fatalf("srv.Calls() = %d, want 2 (the original ambiguous attempt, plus exactly one repair-driven resend) — manual repair must resend at most once, on the operator's explicit say-so", srv.Calls())
	}
}

// TestFaultInjection_ManualRepairResolvesClusterPublicationGate is the
// real-SQLite proof for the wedge the whole-branch review found: a
// quarantined item whose article belongs to an event cluster leaves that
// cluster's event_cluster_publications row 'reserved' — and Reserve only
// ever succeeds on a 'released' row, so without a terminal repair
// resolving the gate, NO other article for that event could ever publish
// again. discard must release it (freeing the cluster), deliver must
// confirm it, and retry must leave it reserved (the item is going back
// into the normal flow, which resolves the gate through the existing
// paths).
func TestFaultInjection_ManualRepairResolvesClusterPublicationGate(t *testing.T) {
	for _, tc := range []struct {
		action    publishing.RepairAction
		wantState string
	}{
		{publishing.RepairDiscard, "released"},
		{publishing.RepairDeliver, "confirmed"},
		{publishing.RepairRetry, "reserved"},
	} {
		t.Run(string(tc.action), func(t *testing.T) {
			ctx := context.Background()
			db := openIntegrationDB(t)
			srv := NewFakeTelegramServer()
			defer srv.Close()
			srv.SetMode("hang")
			now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
			seedRealClusteredPendingArticle(t, db, "a1", "c1", "https://example.com/a1", now)

			uc, outbox := newRealPublishOutbox(db, srv, "worker-1", now) // no importer: will quarantine
			if _, err := uc.Run(ctx); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if _, err := uc.Run(ctx); err != nil { // second tick: quarantines
				t.Fatalf("second Run() error = %v", err)
			}
			list, err := outbox.ListQuarantined(ctx, 10)
			if err != nil || len(list) != 1 {
				t.Fatalf("ListQuarantined() = %+v, %v, want 1 item", list, err)
			}
			// Quarantine deliberately leaves the gate reserved: until an
			// operator resolves it, the cluster is blocked — a bounded,
			// deliberate cost, not "nothing" (see ADR 0030's Residual risk).
			if got := clusterGateState(t, db, "c1"); got != "reserved" {
				t.Fatalf("gate state while quarantined = %q, want reserved", got)
			}

			repair := usecase.NewRepairOutbox(
				outbox, sqliteadapter.NewArticleRepository(db), sqliteadapter.NewClusterPublicationRepository(db),
				sqliteadapter.NewDecisionRepository(db), sqliteadapter.NewUnitOfWork(db), fixedClock{now}, fakeLogger{},
			)
			if err := repair.Run(ctx, list[0].ID, tc.action, "a1", "operator decision"); err != nil {
				t.Fatalf("repair.Run() error = %v", err)
			}
			if got := clusterGateState(t, db, "c1"); got != tc.wantState {
				t.Fatalf("gate state after %s repair = %q, want %q", tc.action, got, tc.wantState)
			}

			// A released gate must actually be re-reservable by a different
			// article — the wedge is only really closed if this succeeds.
			if tc.wantState == "released" {
				gate := sqliteadapter.NewClusterPublicationRepository(db)
				if err := sqliteadapter.NewArticleRepository(db).Save(ctx, article.Article{
					ID: "a2", CanonicalURL: "https://example.com/a2", Status: article.StatusClustered,
					FetchedAt: now, PublishedAt: now, ClusterID: "c1",
					Source: source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
				}); err != nil {
					t.Fatalf("seed article a2: %v", err)
				}
				acquired, err := gate.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: "c1", ArticleID: "a2"}, now)
				if err != nil || !acquired {
					t.Fatalf("Reserve() for a later article = %v, %v, want true, nil", acquired, err)
				}
			}
		})
	}
}

// seedRealClusteredPendingArticle is seedRealPendingArticle plus the
// event_clusters row and the 'reserved' event_cluster_publications gate
// row that ProcessCycle's accept path would have created — the state a
// real clustered article is in by the time PublishOutbox sees it.
func seedRealClusteredPendingArticle(t *testing.T, db *sql.DB, id, clusterID, canonicalURL string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO event_clusters (id, first_seen_at, last_updated_at, status) VALUES (?, ?, ?, 'open')`, clusterID, now, now); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	if err := articles.Save(ctx, article.Article{
		ID: id, CanonicalURL: canonicalURL, Status: article.StatusPendingPublication, FetchedAt: now, PublishedAt: now,
		ClusterID: clusterID,
		Source:    source.Source{ID: mustID(t, source.TypeRSS, "test-source"), Type: source.TypeRSS},
	}); err != nil {
		t.Fatalf("seed article: %v", err)
	}
	if err := outbox.Enqueue(ctx, id, "{}", now); err != nil {
		t.Fatalf("enqueue outbox item: %v", err)
	}
	gate := sqliteadapter.NewClusterPublicationRepository(db)
	if acquired, err := gate.Reserve(ctx, appout.ClusterPublicationReservation{ClusterID: clusterID, ArticleID: id}, now); err != nil || !acquired {
		t.Fatalf("Reserve() = %v, %v, want true, nil", acquired, err)
	}
}

func clusterGateState(t *testing.T, db *sql.DB, clusterID string) string {
	t.Helper()
	var state string
	if err := db.QueryRowContext(context.Background(), `SELECT state FROM event_cluster_publications WHERE cluster_id = ?`, clusterID).Scan(&state); err != nil {
		t.Fatalf("query gate state: %v", err)
	}
	return state
}
