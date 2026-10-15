package usecase_test

// Task 11: behavioral tests proving the source-policy cooldown
// reservation lifecycle end-to-end against a REAL, migrated SQLite database
// — running ProcessCycle and PublishOutbox together, across multiple Run()
// calls with a controlled fake clock, plus a genuine multi-goroutine race
// between two independent *sql.DB connections against the same file (the
// actual proof that SQLite's CAS UPSERT — not application-level locking —
// is what prevents two concurrent candidates from both evading the cooldown
// window). Task 10 (process_cycle_cooldown_test.go) already proved Gate B's
// wiring against fakes; this file proves the same contract against the real
// adapters.
//
// Deviation from the task-11-brief.md Step 1 pseudocode, noted up front so
// it isn't mistaken for a corner cut: the brief's sketch names three
// articles (A founder/exempt, B blocked, C boundary-success) and says B
// "must be rejected" 5 minutes after A's PublishOutbox confirm. Under the
// corrected Task 8 Gate B wiring (see process_cycle.go's Gate B section),
// this cannot be literal: MemberRoleFirstReport resolves to
// sourcepolicy.CodeStandard, which sourcepolicy.IsCooldownRelevant reports
// false for — so A, being first_report, never calls Reserve/ForceReserve at
// all (confirmed by reading process_cycle.go directly, and by Task 10's own
// TestProcessCycle_Cooldown_ExactDuplicateNeverAttemptsAReservation-style
// reasoning). A's outbox confirmation therefore cannot be what blocks a
// following confirmation candidate — nothing ever reserved the window on
// A's behalf. The fixture actually needed to test "a confirmed publication
// blocks exactly the configured window" needs a FOURTH article: A (founder,
// exempt, establishes the cluster only), B (first MemberRoleConfirmation
// candidate — this is the one that reserves the cooldown and is the one
// PublishOutbox actually confirms — this is the "confirmed publication" the
// test name refers to), C (second confirmation candidate, 5 minutes after
// B's confirm, well inside the window — must be rejected), and D (third
// confirmation candidate, run exactly at B's cooldown_until boundary — must
// not be blocked). This also lines up with this task's own self-review
// checklist ("prove a THIRD candidate succeeds exactly at/after the
// boundary") once "candidate" is read as "cooldown-relevant candidate" (B,
// C, D — A is exempt and never becomes one): D is that third candidate.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
)

// newIntegrationPublishOutbox wires a PublishOutbox against real SQLite
// repositories bound to db, mirroring integration_test.go's inline
// PublishOutbox construction (that file has no reusable helper for it,
// unlike process_cycle_integration_test.go's newIntegrationProcessCycle*
// helpers for ProcessCycle) — factored out here since every test in this
// file needs one, sometimes more than once per test.
func newIntegrationPublishOutbox(db *sql.DB, pub *fakePublisher, clock fakeClock) *usecase.PublishOutbox {
	articles := sqliteadapter.NewArticleRepository(db)
	outbox := sqliteadapter.NewOutboxRepository(db)
	decisions := sqliteadapter.NewDecisionRepository(db)
	uow := sqliteadapter.NewUnitOfWork(db)
	clusterPublications := sqliteadapter.NewClusterPublicationRepository(db)
	sourcePolicyCooldowns := sqliteadapter.NewSourcePolicyCooldownRepository(db)
	return usecase.NewPublishOutbox(articles, outbox, decisions, uow, pub, testBackoff, publishing.PolicyConfig{}, "worker-1", 10, time.Minute, clock, fakeLogger{}, clusterPublications, sourcePolicyCooldowns,
		usecase.ReconciliationConfig{BatchSize: 10, ChannelRef: "@testchannel", HistoryLimit: 200, ClockSkew: 2 * time.Minute}, nil)
}

// cooldownFounderArticle builds the MemberRoleFirstReport article that
// founds a fresh cluster — same "Jane Doe" signing shape this package's
// other cooldown/cluster-gate fixtures already use (see
// process_cycle_cooldown_test.go's confirmationFoundingCandidate and
// process_cycle_integration_test.go's ClusterGate fixtures), but run
// through the real pipeline so the cluster, its event keys, and this
// article's aliases are all genuinely persisted (not hand-seeded).
func cooldownFounderArticle(t *testing.T, sourceRaw, url string, now time.Time) article.Article {
	t.Helper()
	return article.Article{
		Title:       "Isotopes Sign Jane Doe To Bolster Secondary",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, sourceRaw), Type: source.TypeRSS},
		OriginalURL: url, ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-2 * time.Hour),
	}
}

// cooldownConfirmationArticle builds a MemberRoleConfirmation candidate for
// the same cluster: same entity (Jane Doe) and action (sign) as the
// founder, worded distinctly enough (title varies per caller) to clear
// this package's dedup near-duplicate threshold — proven safe by
// process_cycle_integration_test.go's
// TestProcessCycle_Integration_ClusterGate_ParaphraseFromUnconfiguredSourceIsDenied,
// which established that a "Isotopes Sign Jane Doe..." founder plus a
// differently-worded "officially confirmed" paraphrase clears dedup but
// still clusters together — and includes an explicit "officially"/
// "confirm" content cue (clusterTestSettings' ContentCues) so
// clustering.selectMemberRole's Confirmation branch fires deterministically
// rather than relying on the (also-correct, but less obviously so) default
// same-action fallback.
func cooldownConfirmationArticle(t *testing.T, sourceRaw, title, url string, now time.Time) article.Article {
	t.Helper()
	return article.Article{
		Title:       title,
		Source:      source.Source{ID: mustID(t, source.TypeRSS, sourceRaw), Type: source.TypeRSS},
		OriginalURL: url, ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}
}

// queriedArticle is the subset of an articles row this file's tests need to
// assert on, fetched by original_url (unique per fixture article) since
// real ArticleRepository exposes no ListAll/FindByURL for tests to use
// directly — mirroring process_cycle_integration_test.go's
// "capture the ID via a targeted query" style.
type queriedArticle struct {
	id, status, clusterID, memberRole string
}

func queryArticleByURL(t *testing.T, db *sql.DB, url string) queriedArticle {
	t.Helper()
	var q queriedArticle
	var clusterID sql.NullString
	if err := db.QueryRowContext(context.Background(), `
		SELECT id, status, current_cluster_id, cluster_member_role FROM articles WHERE original_url = ?
	`, url).Scan(&q.id, &q.status, &clusterID, &q.memberRole); err != nil {
		t.Fatalf("query article by url %s: %v", url, err)
	}
	q.clusterID = clusterID.String
	return q
}

// cooldownTableRow is one source_policy_cooldowns row, queried directly —
// the definitive ground truth this whole file exists to exercise, since
// process_cycle.go/publish_outbox.go's ProcessCycleResult/PublishOutboxResult
// counters are a proxy for it, not a replacement.
type cooldownTableRow struct {
	state         string
	articleID     string
	cooldownUntil time.Time
}

func queryCooldownRow(t *testing.T, db *sql.DB, sourceKey, clusterID string) (cooldownTableRow, bool) {
	t.Helper()
	var row cooldownTableRow
	err := db.QueryRowContext(context.Background(), `
		SELECT state, article_id, cooldown_until FROM source_policy_cooldowns WHERE source_id = ? AND cluster_id = ?
	`, sourceKey, clusterID).Scan(&row.state, &row.articleID, &row.cooldownUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return cooldownTableRow{}, false
	}
	if err != nil {
		t.Fatalf("query source_policy_cooldowns for %s/%s: %v", sourceKey, clusterID, err)
	}
	return row, true
}

// enforceCooldownPolicy builds the standard "official confirmation allowed,
// enforce mode, cooldown-configured" policy this file's tests share —
// mirroring process_cycle_cooldown_test.go's confirmationPolicy, but as a
// resolver-ready map entry since every test here needs to plug its own
// cooldown duration in.
func enforceCooldownResolver(sourceKey string, cooldown time.Duration) *fakeSourcePolicyResolver {
	return &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{
			sourceKey: {
				SourceID: sourceKey, Authority: sourcepolicy.AuthorityOfficialTeam,
				AllowOfficialConfirmation:                true,
				PublishConfirmationWithoutNewInformation: true,
				EventCooldown:                            cooldown,
			},
		},
	}
}

// --- Step 1: confirmed publication blocks exactly the configured window ---

func TestProcessCycle_Cooldown_Integration_ConfirmedPublicationBlocksExactlyTheConfiguredWindow(t *testing.T) {
	db := newIntegrationDB(t)
	ctx := context.Background()
	baseNow := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cooldown := 20 * time.Minute
	const sourceRaw = "source-1"
	sourceKey := "rss:" + sourceRaw
	resolver := enforceCooldownResolver(sourceKey, cooldown)

	// --- Article A: first_report, founds cluster c1, publishes immediately.
	// MemberRoleFirstReport resolves to sourcepolicy.CodeStandard, which is
	// never cooldown-relevant (see process_cycle.go's Gate B section) — A
	// never calls Reserve/ForceReserve at all.
	urlA := "https://example.com/window-a-founder"
	tA := baseNow
	fetcherA := fakeFetcher{articles: []article.Article{cooldownFounderArticle(t, sourceRaw, urlA, tA)}}
	ucA, _ := newIntegrationProcessCycleWithResolver(db, fetcherA, fakeClock{now: tA}, resolver)
	resultA, err := ucA.Run(ctx)
	if err != nil {
		t.Fatalf("Run() [A] error = %v", err)
	}
	if resultA.Accepted != 1 {
		t.Fatalf("resultA = %+v, want accepted=1", resultA)
	}
	qA := queryArticleByURL(t, db, urlA)
	if qA.memberRole != string(clustering.MemberRoleFirstReport) {
		t.Fatalf("test fixture bug: A's cluster_member_role = %q, want %q", qA.memberRole, clustering.MemberRoleFirstReport)
	}
	clusterID := qA.clusterID
	if clusterID == "" {
		t.Fatalf("test fixture bug: A has no current_cluster_id")
	}

	pubResultA, err := newIntegrationPublishOutbox(db, &fakePublisher{}, fakeClock{now: tA}).Run(ctx)
	if err != nil {
		t.Fatalf("PublishOutbox.Run() [A] error = %v", err)
	}
	if pubResultA.Published != 1 {
		t.Fatalf("pubResultA = %+v, want published=1", pubResultA)
	}
	if _, ok := queryCooldownRow(t, db, sourceKey, clusterID); ok {
		t.Error("source_policy_cooldowns has a row after A's confirm, want none: A is exempt from the cooldown gate")
	}

	// --- Article B: first MemberRoleConfirmation candidate for this
	// cluster+source — nothing else holds the window yet, so Gate B's
	// Reserve succeeds and B is accepted. Running PublishOutbox for B is
	// what this test's name refers to as "a confirmed publication": the
	// cooldown row transitions reserved -> confirmed, and the window it
	// established (cooldown_until = tB + cooldown) is what the rest of this
	// test proves blocks exactly.
	urlB := "https://example.com/window-b-establishes"
	tB := baseNow.Add(1 * time.Minute)
	fetcherB := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Springfield Officially Signs Veteran Defensive Back Jane Doe To One-Year Deal", urlB, tB)}}
	ucB, _ := newIntegrationProcessCycleWithResolver(db, fetcherB, fakeClock{now: tB}, resolver)
	resultB, err := ucB.Run(ctx)
	if err != nil {
		t.Fatalf("Run() [B] error = %v", err)
	}
	qB := queryArticleByURL(t, db, urlB)
	if qB.memberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: B's cluster_member_role = %q, want %q", qB.memberRole, clustering.MemberRoleConfirmation)
	}
	if qB.clusterID != clusterID {
		t.Fatalf("test fixture bug: B joined cluster %q, want the founder's cluster %q", qB.clusterID, clusterID)
	}
	if resultB.Accepted != 1 || resultB.CooldownReserved != 1 {
		t.Fatalf("resultB = %+v, want accepted=1 cooldownReserved=1 (nothing held the window before B)", resultB)
	}

	pubResultB, err := newIntegrationPublishOutbox(db, &fakePublisher{}, fakeClock{now: tB}).Run(ctx)
	if err != nil {
		t.Fatalf("PublishOutbox.Run() [B] error = %v", err)
	}
	if pubResultB.Published != 1 || pubResultB.CooldownConfirmed != 1 {
		t.Fatalf("pubResultB = %+v, want published=1 cooldownConfirmed=1", pubResultB)
	}
	rowAfterB, ok := queryCooldownRow(t, db, sourceKey, clusterID)
	if !ok || rowAfterB.state != "confirmed" || rowAfterB.articleID != qB.id {
		t.Fatalf("cooldown row after B's confirm = %+v (ok=%v), want state=confirmed article_id=%s", rowAfterB, ok, qB.id)
	}
	wantCooldownUntil := tB.Add(cooldown)
	if !rowAfterB.cooldownUntil.Equal(wantCooldownUntil) {
		t.Fatalf("cooldown_until = %v, want %v (reserved_at=tB + cooldown, independent of when PublishOutbox later confirms)", rowAfterB.cooldownUntil, wantCooldownUntil)
	}

	// --- Article C: second confirmation candidate, 5 minutes after B's
	// confirm — well inside the 20-minute window. Must be rejected with
	// CodeRejectCooldown, and the cooldown row must remain untouched
	// (still held by B).
	urlC := "https://example.com/window-c-blocked"
	tC := tB.Add(5 * time.Minute)
	fetcherC := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Isotopes Make It Official: Jane Doe Signs With Springfield's Secondary", urlC, tC)}}
	ucC, _ := newIntegrationProcessCycleWithResolver(db, fetcherC, fakeClock{now: tC}, resolver)
	resultC, err := ucC.Run(ctx)
	if err != nil {
		t.Fatalf("Run() [C] error = %v", err)
	}
	qC := queryArticleByURL(t, db, urlC)
	if qC.memberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: C's cluster_member_role = %q, want %q", qC.memberRole, clustering.MemberRoleConfirmation)
	}
	if resultC.Rejected != 1 || resultC.CooldownBlocked != 1 || qC.status != string(article.StatusRetainedNoPublish) {
		t.Fatalf("resultC = %+v, C.status = %q, want rejected=1 cooldownBlocked=1 status=%q — C must not evade the window B's confirmed publication established", resultC, qC.status, article.StatusRetainedNoPublish)
	}
	rowAfterC, ok := queryCooldownRow(t, db, sourceKey, clusterID)
	if !ok || rowAfterC.state != "confirmed" || rowAfterC.articleID != qB.id {
		t.Fatalf("cooldown row after C = %+v (ok=%v), want unchanged: still state=confirmed article_id=%s (B)", rowAfterC, ok, qB.id)
	}

	// --- Article D: third confirmation candidate, run exactly at B's
	// cooldown_until boundary. Must NOT be blocked: cooldown_until <= now
	// is the Reserve CAS's own re-acquisition condition (see
	// sqlite/source_policy_cooldown_repository.go's Reserve), so D
	// re-acquires the window right at the boundary, not merely "eventually
	// after" it.
	urlD := "https://example.com/window-d-boundary"
	tD := wantCooldownUntil
	fetcherD := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Team Sources Confirm Jane Doe Has Officially Signed With The Isotopes", urlD, tD)}}
	ucD, _ := newIntegrationProcessCycleWithResolver(db, fetcherD, fakeClock{now: tD}, resolver)
	resultD, err := ucD.Run(ctx)
	if err != nil {
		t.Fatalf("Run() [D] error = %v", err)
	}
	qD := queryArticleByURL(t, db, urlD)
	if qD.memberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: D's cluster_member_role = %q, want %q", qD.memberRole, clustering.MemberRoleConfirmation)
	}
	if resultD.Accepted != 1 || resultD.CooldownReserved != 1 || resultD.CooldownBlocked != 0 {
		t.Fatalf("resultD = %+v, want accepted=1 cooldownReserved=1 cooldownBlocked=0 exactly at the boundary", resultD)
	}
	rowAfterD, ok := queryCooldownRow(t, db, sourceKey, clusterID)
	if !ok || rowAfterD.state != "reserved" || rowAfterD.articleID != qD.id {
		t.Fatalf("cooldown row after D = %+v (ok=%v), want state=reserved article_id=%s (D re-acquired at the boundary)", rowAfterD, ok, qD.id)
	}
	if !rowAfterD.cooldownUntil.Equal(tD.Add(cooldown)) {
		t.Errorf("cooldown_until after D = %v, want %v", rowAfterD.cooldownUntil, tD.Add(cooldown))
	}
}

// --- Step 3: permanent outbox failure releases the reservation -----------

func TestProcessCycle_Cooldown_Integration_PermanentOutboxFailureReleasesReservation(t *testing.T) {
	db := newIntegrationDB(t)
	ctx := context.Background()
	baseNow := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cooldown := 20 * time.Minute
	const sourceRaw = "source-1"
	sourceKey := "rss:" + sourceRaw
	resolver := enforceCooldownResolver(sourceKey, cooldown)

	urlFounder := "https://example.com/permanent-founder"
	tFounder := baseNow
	fetcherFounder := fakeFetcher{articles: []article.Article{cooldownFounderArticle(t, sourceRaw, urlFounder, tFounder)}}
	ucFounder, _ := newIntegrationProcessCycleWithResolver(db, fetcherFounder, fakeClock{now: tFounder}, resolver)
	if resultFounder, err := ucFounder.Run(ctx); err != nil || resultFounder.Accepted != 1 {
		t.Fatalf("Run() [founder] = %+v, %v, want accepted=1 nil", resultFounder, err)
	}
	clusterID := queryArticleByURL(t, db, urlFounder).clusterID
	// Publish the founder's own outbox item now (with a successful
	// publisher) so it isn't still sitting claimable when the failing
	// publisher below runs — otherwise ClaimBatch would also grab it,
	// muddying the Discarded/Retried/CooldownReleased counts this test
	// asserts on for A specifically.
	if pubResultFounder, err := newIntegrationPublishOutbox(db, &fakePublisher{}, fakeClock{now: tFounder}).Run(ctx); err != nil || pubResultFounder.Published != 1 {
		t.Fatalf("PublishOutbox.Run() [founder] = %+v, %v, want published=1 nil", pubResultFounder, err)
	}

	// A: confirmation candidate, Gate B reserves.
	urlA := "https://example.com/permanent-a"
	tA := baseNow.Add(1 * time.Minute)
	fetcherA := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Springfield Officially Signs Veteran Defensive Back Jane Doe To One-Year Deal", urlA, tA)}}
	ucA, _ := newIntegrationProcessCycleWithResolver(db, fetcherA, fakeClock{now: tA}, resolver)
	resultA, err := ucA.Run(ctx)
	if err != nil {
		t.Fatalf("Run() [A] error = %v", err)
	}
	qA := queryArticleByURL(t, db, urlA)
	if qA.memberRole != string(clustering.MemberRoleConfirmation) || qA.clusterID != clusterID {
		t.Fatalf("test fixture bug: A's cluster_member_role=%q cluster_id=%q, want %q/%q", qA.memberRole, qA.clusterID, clustering.MemberRoleConfirmation, clusterID)
	}
	if resultA.Accepted != 1 || resultA.CooldownReserved != 1 {
		t.Fatalf("resultA = %+v, want accepted=1 cooldownReserved=1", resultA)
	}
	rowAfterReserve, ok := queryCooldownRow(t, db, sourceKey, clusterID)
	if !ok || rowAfterReserve.state != "reserved" || rowAfterReserve.articleID != qA.id {
		t.Fatalf("cooldown row after A's reserve = %+v (ok=%v), want state=reserved article_id=%s", rowAfterReserve, ok, qA.id)
	}

	// PublishOutbox.Run with a fakePublisher returning a genuine
	// *out.PermanentError (via fakePublisher.permanent — see fakes_test.go):
	// recordFailure's dead branch fires, releasing the cooldown.
	permanentPub := &fakePublisher{err: errors.New("recipient permanently rejected"), permanent: true}
	pubResultA, err := newIntegrationPublishOutbox(db, permanentPub, fakeClock{now: tA}).Run(ctx)
	if err != nil {
		t.Fatalf("PublishOutbox.Run() [A] error = %v", err)
	}
	if pubResultA.Discarded != 1 || pubResultA.CooldownReleased != 1 {
		t.Fatalf("pubResultA = %+v, want discarded=1 cooldownReleased=1", pubResultA)
	}
	rowAfterRelease, ok := queryCooldownRow(t, db, sourceKey, clusterID)
	if !ok || rowAfterRelease.state != "released" {
		t.Fatalf("cooldown row after permanent failure = %+v (ok=%v), want state=released", rowAfterRelease, ok)
	}

	// B, same source+cluster, 5 minutes later (inside the window): must NOT
	// be blocked, since A's reservation was released, not confirmed.
	urlB := "https://example.com/permanent-b"
	tB := tA.Add(5 * time.Minute)
	fetcherB := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Isotopes Make It Official: Jane Doe Signs With Springfield's Secondary", urlB, tB)}}
	ucB, _ := newIntegrationProcessCycleWithResolver(db, fetcherB, fakeClock{now: tB}, resolver)
	resultB, err := ucB.Run(ctx)
	if err != nil {
		t.Fatalf("Run() [B] error = %v", err)
	}
	qB := queryArticleByURL(t, db, urlB)
	if qB.memberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: B's cluster_member_role = %q, want %q", qB.memberRole, clustering.MemberRoleConfirmation)
	}
	if resultB.Accepted != 1 || resultB.CooldownBlocked != 0 || resultB.CooldownReserved != 1 {
		t.Fatalf("resultB = %+v, want accepted=1 cooldownBlocked=0 cooldownReserved=1: a released reservation must not block a later candidate", resultB)
	}
	rowAfterB, ok := queryCooldownRow(t, db, sourceKey, clusterID)
	if !ok || rowAfterB.state != "reserved" || rowAfterB.articleID != qB.id {
		t.Fatalf("cooldown row after B = %+v (ok=%v), want state=reserved article_id=%s (B)", rowAfterB, ok, qB.id)
	}
}

// --- Step 4: retryable outbox failure keeps blocking ----------------------

func TestProcessCycle_Cooldown_Integration_RetryableOutboxFailureKeepsBlocking(t *testing.T) {
	db := newIntegrationDB(t)
	ctx := context.Background()
	baseNow := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cooldown := 20 * time.Minute
	const sourceRaw = "source-1"
	sourceKey := "rss:" + sourceRaw
	resolver := enforceCooldownResolver(sourceKey, cooldown)

	urlFounder := "https://example.com/retry-founder"
	tFounder := baseNow
	fetcherFounder := fakeFetcher{articles: []article.Article{cooldownFounderArticle(t, sourceRaw, urlFounder, tFounder)}}
	ucFounder, _ := newIntegrationProcessCycleWithResolver(db, fetcherFounder, fakeClock{now: tFounder}, resolver)
	if resultFounder, err := ucFounder.Run(ctx); err != nil || resultFounder.Accepted != 1 {
		t.Fatalf("Run() [founder] = %+v, %v, want accepted=1 nil", resultFounder, err)
	}
	clusterID := queryArticleByURL(t, db, urlFounder).clusterID
	// Publish the founder's own outbox item now (with a successful
	// publisher) so it isn't still sitting claimable when the failing
	// publisher below runs — otherwise ClaimBatch would also grab it,
	// muddying the Retried/CooldownReleased counts this test asserts on
	// for A specifically.
	if pubResultFounder, err := newIntegrationPublishOutbox(db, &fakePublisher{}, fakeClock{now: tFounder}).Run(ctx); err != nil || pubResultFounder.Published != 1 {
		t.Fatalf("PublishOutbox.Run() [founder] = %+v, %v, want published=1 nil", pubResultFounder, err)
	}

	// A: confirmation candidate, Gate B reserves.
	urlA := "https://example.com/retry-a"
	tA := baseNow.Add(1 * time.Minute)
	fetcherA := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Springfield Officially Signs Veteran Defensive Back Jane Doe To One-Year Deal", urlA, tA)}}
	ucA, _ := newIntegrationProcessCycleWithResolver(db, fetcherA, fakeClock{now: tA}, resolver)
	resultA, err := ucA.Run(ctx)
	if err != nil {
		t.Fatalf("Run() [A] error = %v", err)
	}
	qA := queryArticleByURL(t, db, urlA)
	if qA.memberRole != string(clustering.MemberRoleConfirmation) || qA.clusterID != clusterID {
		t.Fatalf("test fixture bug: A's cluster_member_role=%q cluster_id=%q, want %q/%q", qA.memberRole, qA.clusterID, clustering.MemberRoleConfirmation, clusterID)
	}
	if resultA.Accepted != 1 || resultA.CooldownReserved != 1 {
		t.Fatalf("resultA = %+v, want accepted=1 cooldownReserved=1", resultA)
	}

	// PublishOutbox.Run with a plain (non-permanent, non-exhausted-retry)
	// error: recordFailure's non-dead branch — the reservation stays
	// 'reserved'. testBackoff.MaxAttempts is 6, so a first failure is never
	// exhausted.
	retryablePub := &fakePublisher{err: errors.New("telegram temporarily unreachable")}
	pubResultA, err := newIntegrationPublishOutbox(db, retryablePub, fakeClock{now: tA}).Run(ctx)
	if err != nil {
		t.Fatalf("PublishOutbox.Run() [A] error = %v", err)
	}
	if pubResultA.Retried != 1 || pubResultA.Discarded != 0 || pubResultA.CooldownReleased != 0 {
		t.Fatalf("pubResultA = %+v, want retried=1 discarded=0 cooldownReleased=0", pubResultA)
	}
	rowAfterRetry, ok := queryCooldownRow(t, db, sourceKey, clusterID)
	if !ok || rowAfterRetry.state != "reserved" || rowAfterRetry.articleID != qA.id {
		t.Fatalf("cooldown row after retryable failure = %+v (ok=%v), want unchanged: state=reserved article_id=%s (A)", rowAfterRetry, ok, qA.id)
	}

	// B, same source+cluster, 5 minutes later: must still be blocked
	// (CodeRejectCooldown) — a merely-retrying delivery still owns the
	// window, exactly like the cluster-publication gate's own
	// pending_retry behavior (TestFullCycle_RetryThenSuccess_...,
	// integration_test.go).
	urlB := "https://example.com/retry-b"
	tB := tA.Add(5 * time.Minute)
	fetcherB := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Team Sources Confirm Jane Doe Has Officially Signed With The Isotopes", urlB, tB)}}
	ucB, _ := newIntegrationProcessCycleWithResolver(db, fetcherB, fakeClock{now: tB}, resolver)
	resultB, err := ucB.Run(ctx)
	if err != nil {
		t.Fatalf("Run() [B] error = %v", err)
	}
	qB := queryArticleByURL(t, db, urlB)
	if qB.memberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: B's cluster_member_role = %q, want %q", qB.memberRole, clustering.MemberRoleConfirmation)
	}
	if resultB.Rejected != 1 || resultB.CooldownBlocked != 1 || qB.status != string(article.StatusRetainedNoPublish) {
		t.Fatalf("resultB = %+v, B.status = %q, want rejected=1 cooldownBlocked=1 status=%q — B (A's own retry outcome is irrelevant here) must still be blocked by A's still-active reservation", resultB, qB.status, article.StatusRetainedNoPublish)
	}
	rowAfterB, ok := queryCooldownRow(t, db, sourceKey, clusterID)
	if !ok || rowAfterB.state != "reserved" || rowAfterB.articleID != qA.id {
		t.Fatalf("cooldown row after B = %+v (ok=%v), want unchanged: still state=reserved article_id=%s (A)", rowAfterB, ok, qA.id)
	}
}

// --- Step 5: the two-worker concurrent race — the cooldown's own concurrency
// requirement ----------------------------------------------------------

// TestProcessCycle_Cooldown_Integration_ConcurrentCandidatesOnlyOneWins is
// the pipeline-level version of
// TestSourcePolicyCooldownRepository_ConcurrentReserveOnlyOneWinner
// (adapters/outbound/sqlite/source_policy_cooldown_repository_test.go): two
// independent *sql.DB connections (simulating two ProcessCycle workers),
// each running a full Run() for a MemberRoleConfirmation candidate on the
// same source+cluster, at the same instant, inside the cooldown window.
// Proven under go test -race: exactly one must Accept, the other must
// Reject with CodeRejectCooldown — never both, never neither.
func TestProcessCycle_Cooldown_Integration_ConcurrentCandidatesOnlyOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cooldown-process-cycle-race.db")
	copyMigratedTestDB(t, path)
	db1, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() db1 error = %v", err)
	}
	defer db1.Close()
	db2, err := infrasqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("Open() db2 error = %v", err)
	}
	defer db2.Close()

	ctx := context.Background()
	baseNow := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cooldown := 20 * time.Minute
	const sourceRaw = "source-1"
	sourceKey := "rss:" + sourceRaw
	resolver := enforceCooldownResolver(sourceKey, cooldown)

	// Pre-seed a cluster via a first, single-threaded ProcessCycle.Run
	// (over db1) establishing the founding first_report article and cluster
	// c1, so both racing candidates below join the SAME existing cluster as
	// MemberRoleConfirmation rather than each independently creating their
	// own (which would never contend for the same cooldown key at all).
	urlFounder := "https://example.com/race-founder"
	fetcherFounder := fakeFetcher{articles: []article.Article{cooldownFounderArticle(t, sourceRaw, urlFounder, baseNow)}}
	ucFounder, _ := newIntegrationProcessCycleWithResolver(db1, fetcherFounder, fakeClock{now: baseNow}, resolver)
	if resultFounder, err := ucFounder.Run(ctx); err != nil || resultFounder.Accepted != 1 {
		t.Fatalf("Run() [founder] = %+v, %v, want accepted=1 nil", resultFounder, err)
	}
	clusterID := queryArticleByURL(t, db1, urlFounder).clusterID
	if clusterID == "" {
		t.Fatalf("test fixture bug: founder has no current_cluster_id")
	}

	raceNow := baseNow.Add(1 * time.Minute)
	url1 := "https://example.com/race-candidate-1"
	url2 := "https://example.com/race-candidate-2"
	fetcher1 := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Springfield Officially Signs Veteran Defensive Back Jane Doe To One-Year Deal", url1, raceNow)}}
	fetcher2 := fakeFetcher{articles: []article.Article{cooldownConfirmationArticle(t, sourceRaw, "Isotopes Make It Official: Jane Doe Signs With Springfield's Secondary", url2, raceNow)}}

	uc1, _ := newIntegrationProcessCycleWithResolver(db1, fetcher1, fakeClock{now: raceNow}, resolver)
	uc2, _ := newIntegrationProcessCycleWithResolver(db2, fetcher2, fakeClock{now: raceNow}, resolver)

	var mu sync.Mutex
	results := make([]in.ProcessCycleResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r, e := uc1.Run(ctx)
		mu.Lock()
		results[0], errs[0] = r, e
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		r, e := uc2.Run(ctx)
		mu.Lock()
		results[1], errs[1] = r, e
		mu.Unlock()
	}()
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("Run() errors = %v, %v, want both nil", errs[0], errs[1])
	}

	q1 := queryArticleByURL(t, db1, url1)
	q2 := queryArticleByURL(t, db1, url2)
	if q1.memberRole != string(clustering.MemberRoleConfirmation) || q2.memberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: memberRoles = %q, %q, want both %q", q1.memberRole, q2.memberRole, clustering.MemberRoleConfirmation)
	}
	if q1.clusterID != clusterID || q2.clusterID != clusterID {
		t.Fatalf("test fixture bug: cluster ids = %q, %q, want both %q (both candidates must join the SAME cluster to actually contend)", q1.clusterID, q2.clusterID, clusterID)
	}

	acceptedCount := results[0].Accepted + results[1].Accepted
	blockedCount := results[0].CooldownBlocked + results[1].CooldownBlocked
	if acceptedCount != 1 || blockedCount != 1 {
		t.Fatalf("results = %+v, %+v, want exactly one Accepted=1 and the other CooldownBlocked=1 (never both, never neither)", results[0], results[1])
	}
	// The two outcomes must land on different candidates: whichever result
	// accepted must not also be the one reporting CooldownBlocked, and vice
	// versa — ruling out a degenerate "both counters happen to sum right
	// but on the same side" false pass.
	if (results[0].Accepted == 1) == (results[0].CooldownBlocked == 1) {
		t.Fatalf("results[0] = %+v, want exactly one of Accepted/CooldownBlocked set, not both/neither on the same result", results[0])
	}

	row, ok := queryCooldownRow(t, db1, sourceKey, clusterID)
	if !ok || row.state != "reserved" {
		t.Fatalf("cooldown row after the race = %+v (ok=%v), want exactly one state=reserved row", row, ok)
	}
	winnerURL := url1
	if results[1].Accepted == 1 {
		winnerURL = url2
	}
	winnerID := queryArticleByURL(t, db1, winnerURL).id
	if row.articleID != winnerID {
		t.Fatalf("cooldown row holder = %q, want the accepted candidate's article id %q", row.articleID, winnerID)
	}

	var decisionCount int
	if err := db1.QueryRowContext(ctx, `SELECT COUNT(1) FROM source_policy_decisions WHERE decision_code = ?`, string(sourcepolicy.CodeRejectCooldown)).Scan(&decisionCount); err != nil {
		t.Fatalf("count source_policy_decisions: %v", err)
	}
	if decisionCount != 1 {
		t.Errorf("source_policy_decisions rows with decision_code=reject_cooldown = %d, want exactly 1 (the loser's audit row)", decisionCount)
	}
}
