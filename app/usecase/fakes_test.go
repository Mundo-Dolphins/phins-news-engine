package usecase_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/ingestidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// mustID builds a source.ID for test fixtures, failing the test immediately
// if raw isn't a valid identifier for platform — shared by every _test.go
// file in this package (all package usecase_test).
func mustID(t *testing.T, platform source.Type, raw string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, raw)
	if err != nil {
		t.Fatalf("NewID(%q, %q): %v", platform, raw, err)
	}
	return id
}

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

type fakeLogger struct{}

func (fakeLogger) Debug(string, ...any)        {}
func (fakeLogger) Info(string, ...any)         {}
func (fakeLogger) Warn(string, ...any)         {}
func (fakeLogger) Error(string, error, ...any) {}

type fakeFetcher struct {
	articles []article.Article
	err      error
}

func (f fakeFetcher) Fetch(context.Context) ([]article.Article, error) {
	return f.articles, f.err
}

type fakeArticleRepo struct {
	saved          []article.Article
	byID           map[string]article.Article
	saveErr        error
	findErr        error
	deleteByStatus func(statuses []article.Status, cutoff time.Time) (int, error)
}

func (r *fakeArticleRepo) Save(_ context.Context, a article.Article) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, a)
	if r.byID == nil {
		r.byID = map[string]article.Article{}
	}
	r.byID[a.ID] = a
	return nil
}

func (r *fakeArticleRepo) FindByID(_ context.Context, id string) (article.Article, error) {
	if r.findErr != nil {
		return article.Article{}, r.findErr
	}
	a, ok := r.byID[id]
	if !ok {
		return article.Article{}, fmt.Errorf("article %s: %w", id, sql.ErrNoRows)
	}
	return a, nil
}

func (r *fakeArticleRepo) DeleteByStatusOlderThan(_ context.Context, statuses []article.Status, cutoff time.Time) (int, error) {
	if r.deleteByStatus != nil {
		return r.deleteByStatus(statuses, cutoff)
	}
	return 0, nil
}

func (r *fakeArticleRepo) Count(context.Context) (int, error) {
	return len(r.saved), nil
}

func (r *fakeArticleRepo) ListCandidatesForReview(context.Context, time.Time, int) ([]article.Article, error) {
	return nil, nil
}

func (r *fakeArticleRepo) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (r *fakeArticleRepo) FindByCanonicalURL(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

func (r *fakeArticleRepo) lastSaved() article.Article {
	return r.saved[len(r.saved)-1]
}

type fakeDeduplicationRepo struct {
	knownFingerprint string
	existsErr        error
	saveErr          error
	saved            map[string]string
}

func (r *fakeDeduplicationRepo) Exists(_ context.Context, fingerprint string) (bool, error) {
	if r.existsErr != nil {
		return false, r.existsErr
	}
	return fingerprint == r.knownFingerprint, nil
}

func (r *fakeDeduplicationRepo) Save(_ context.Context, fingerprint, articleID string) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	if r.saved == nil {
		r.saved = map[string]string{}
	}
	r.saved[fingerprint] = articleID
	return nil
}

type clusterMember struct {
	clusterID, articleID string
	role                 clustering.MemberRole
	score                float64
}

type savedEventKeys struct {
	articleID, clusterID string
	keys                 []string
}

type savedDecision struct {
	articleID, chosenClusterID string
	decision                   clustering.Decision
}

type representativeUpdate struct {
	clusterID, articleID string
	score                float64
	evidence             []clustering.Evidence
	rulesVersion         string
}

type fakeClusterRepo struct {
	createErr   error
	created     []string
	addErr      error
	addedMember []clusterMember

	// candidates is returned verbatim by FindCandidatesByEventKeys,
	// regardless of the requested keys/window/limit — mirroring
	// fakeSimilarityIndexRepository.FindCandidates' existing "ignore the
	// filter, return what the test configured" style.
	candidates        []clustering.Candidate
	findCandidatesErr error

	representations []eventsignals.Representation
	saveRepErr      error

	savedEventKeys []savedEventKeys
	saveKeysErr    error

	decisions       []savedDecision
	saveDecisionErr error

	representativeUpdates []representativeUpdate
	updateRepErr          error

	// closeInactiveBatchFunc, when set, is called by CloseInactiveBatch
	// directly — lets tests script return values/errors across a
	// multi-call loop (batch-size cap, max-batches cap, time-budget cap,
	// error propagation) without a shared results queue. nil means "close
	// nothing, no error," matching every other fake method's safe
	// zero-value default in this file.
	closeInactiveBatchFunc func(cutoff time.Time, closeReason string, batchSize int, now time.Time) (int, error)
}

func (r *fakeClusterRepo) CreateCluster(_ context.Context, id, _ string, _ clustering.Status, _ time.Time) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, id)
	return nil
}

func (r *fakeClusterRepo) AddMember(_ context.Context, clusterID, articleID string, role clustering.MemberRole, score float64, _ time.Time) error {
	if r.addErr != nil {
		return r.addErr
	}
	r.addedMember = append(r.addedMember, clusterMember{clusterID, articleID, role, score})
	return nil
}

func (r *fakeClusterRepo) DeleteClosedOlderThan(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (r *fakeClusterRepo) SaveRepresentation(_ context.Context, rep eventsignals.Representation, _ string, _ time.Time) error {
	if r.saveRepErr != nil {
		return r.saveRepErr
	}
	r.representations = append(r.representations, rep)
	return nil
}

func (r *fakeClusterRepo) SaveEventKeys(_ context.Context, articleID, clusterID string, keys []string, _ time.Time) error {
	if r.saveKeysErr != nil {
		return r.saveKeysErr
	}
	r.savedEventKeys = append(r.savedEventKeys, savedEventKeys{articleID, clusterID, keys})
	return nil
}

func (r *fakeClusterRepo) FindCandidatesByEventKeys(_ context.Context, _ []string, _ time.Time, _ string, _ int) ([]clustering.Candidate, error) {
	if r.findCandidatesErr != nil {
		return nil, r.findCandidatesErr
	}
	return r.candidates, nil
}

func (r *fakeClusterRepo) SaveDecision(_ context.Context, articleID, chosenClusterID string, d clustering.Decision) error {
	if r.saveDecisionErr != nil {
		return r.saveDecisionErr
	}
	r.decisions = append(r.decisions, savedDecision{articleID, chosenClusterID, d})
	return nil
}

func (r *fakeClusterRepo) UpdateRepresentative(_ context.Context, clusterID, articleID string, score float64, evidence []clustering.Evidence, rulesVersion string, _ time.Time) error {
	if r.updateRepErr != nil {
		return r.updateRepErr
	}
	r.representativeUpdates = append(r.representativeUpdates, representativeUpdate{clusterID, articleID, score, evidence, rulesVersion})
	return nil
}

func (r *fakeClusterRepo) CloseInactiveBatch(_ context.Context, cutoff time.Time, closeReason string, batchSize int, now time.Time) (int, error) {
	if r.closeInactiveBatchFunc != nil {
		return r.closeInactiveBatchFunc(cutoff, closeReason, batchSize, now)
	}
	return 0, nil
}

func (r *fakeClusterRepo) Stats(context.Context, time.Time) (out.ClusterLifecycleStats, error) {
	return out.ClusterLifecycleStats{}, nil
}

type fakeDecisionRepo struct {
	events    []out.Event
	appendErr error
}

func (r *fakeDecisionRepo) Append(_ context.Context, e out.Event) error {
	if r.appendErr != nil {
		return r.appendErr
	}
	r.events = append(r.events, e)
	return nil
}

func (r *fakeDecisionRepo) ListByArticle(_ context.Context, articleID string) ([]out.Event, error) {
	var result []out.Event
	for _, e := range r.events {
		if e.ArticleID == articleID {
			result = append(result, e)
		}
	}
	return result, nil
}

func (r *fakeDecisionRepo) DeleteOlderThan(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (r *fakeDecisionRepo) ListByEventTypesSince(_ context.Context, eventTypes []string, since time.Time, limit int) ([]out.Event, error) {
	wanted := make(map[string]bool, len(eventTypes))
	for _, et := range eventTypes {
		wanted[et] = true
	}
	var result []out.Event
	for i := len(r.events) - 1; i >= 0 && len(result) < limit; i-- {
		e := r.events[i]
		if wanted[e.EventType] && !e.OccurredAt.Before(since) {
			result = append(result, e)
		}
	}
	return result, nil
}

type fakeOutboxRepo struct {
	enqueued             []string
	enqueueErr           error
	claimItems           []out.OutboxItem
	claimErr             error
	claimExpiredItems    []out.OutboxItem
	claimExpiredErr      error
	delivered            []int64
	retryable            []int64
	retryableAvailableAt []time.Time
	discarded            []int64 // MarkRetryable(dead=true) calls — mirrors the old fakeOutboxRepo.failed for permanent/exhausted outcomes
	expired              []int64
	quarantined          []int64
	repaired             []int64
	repairErr            error
	sentUnconfirmed      map[int64]out.OutboxItem // by outbox ID, latest RecordSentUnconfirmed call
	deferred             []int64
	deferredAvailableAt  []time.Time
	deferErr             error
	attempts             []out.Attempt
	deleteOldAttemptsErr error
	sentUnconfirmedList  []out.OutboxItem // what ListSentUnconfirmed returns, test-controlled
	quarantinedList      []out.OutboxItem // what ListQuarantined returns, test-controlled
	succeededSince       int
	succeededSinceErr    error
	succeededByEntity    map[string]int
	succeededByEntityErr error

	// calls records, in order, every ClaimBatch ("claim") and
	// ListSentUnconfirmed ("reconcile") invocation — the minimal call-order
	// signal tests need to prove Run calls Reconcile before ClaimBatch,
	// without building a general call-order framework.
	calls []string
}

func (r *fakeOutboxRepo) Enqueue(_ context.Context, articleID, _ string, _ time.Time) error {
	if r.enqueueErr != nil {
		return r.enqueueErr
	}
	r.enqueued = append(r.enqueued, articleID)
	return nil
}

func (r *fakeOutboxRepo) ClaimBatch(context.Context, string, int, time.Duration, time.Time) ([]out.OutboxItem, error) {
	r.calls = append(r.calls, "claim")
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	return r.claimItems, nil
}

func (r *fakeOutboxRepo) ClaimExpired(context.Context, string, int, time.Time) ([]out.OutboxItem, error) {
	r.calls = append(r.calls, "claim_expired")
	if r.claimExpiredErr != nil {
		return nil, r.claimExpiredErr
	}
	return r.claimExpiredItems, nil
}

func (r *fakeOutboxRepo) RecordSentUnconfirmed(_ context.Context, id int64, messageID, ambiguousReason string, now time.Time) error {
	if r.sentUnconfirmed == nil {
		r.sentUnconfirmed = make(map[int64]out.OutboxItem)
	}
	r.sentUnconfirmed[id] = out.OutboxItem{ID: id, SentMessageID: messageID, AmbiguousReason: ambiguousReason, SentAt: now}
	return nil
}

func (r *fakeOutboxRepo) MarkDelivered(_ context.Context, id int64, _ time.Time) error {
	r.delivered = append(r.delivered, id)
	return nil
}

func (r *fakeOutboxRepo) ExpireLeased(_ context.Context, id int64, _ string, _ time.Time) error {
	r.expired = append(r.expired, id)
	return nil
}

func (r *fakeOutboxRepo) MarkRetryable(_ context.Context, id int64, _ string, availableAt time.Time, dead bool, _ time.Time) error {
	if dead {
		r.discarded = append(r.discarded, id)
		return nil
	}
	r.retryable = append(r.retryable, id)
	r.retryableAvailableAt = append(r.retryableAvailableAt, availableAt)
	return nil
}

func (r *fakeOutboxRepo) Quarantine(_ context.Context, id int64, _ string, _ time.Time) error {
	r.quarantined = append(r.quarantined, id)
	return nil
}

func (r *fakeOutboxRepo) Defer(_ context.Context, id int64, availableAt time.Time, _ time.Time) error {
	if r.deferErr != nil {
		return r.deferErr
	}
	r.deferred = append(r.deferred, id)
	r.deferredAvailableAt = append(r.deferredAvailableAt, availableAt)
	return nil
}

func (r *fakeOutboxRepo) RecordAttempt(_ context.Context, a out.Attempt) error {
	r.attempts = append(r.attempts, a)
	return nil
}

func (r *fakeOutboxRepo) DeleteOldAttempts(context.Context, time.Time) (int, error) {
	if r.deleteOldAttemptsErr != nil {
		return 0, r.deleteOldAttemptsErr
	}
	return 0, nil
}

func (r *fakeOutboxRepo) CountSucceededSince(context.Context, time.Time) (int, error) {
	if r.succeededSinceErr != nil {
		return 0, r.succeededSinceErr
	}
	return r.succeededSince, nil
}

func (r *fakeOutboxRepo) CountSucceededByPrimaryEntitySince(_ context.Context, entity string, _ time.Time) (int, error) {
	if r.succeededByEntityErr != nil {
		return 0, r.succeededByEntityErr
	}
	return r.succeededByEntity[entity], nil
}

func (r *fakeOutboxRepo) ListSentUnconfirmed(context.Context, int, time.Time) ([]out.OutboxItem, error) {
	r.calls = append(r.calls, "reconcile")
	return r.sentUnconfirmedList, nil
}

func (r *fakeOutboxRepo) ListQuarantined(context.Context, int) ([]out.OutboxItem, error) {
	return r.quarantinedList, nil
}

func (r *fakeOutboxRepo) Repair(_ context.Context, id int64, _ string, _ publishing.RepairAction, _ string, _ time.Time) error {
	if r.repairErr != nil {
		return r.repairErr
	}
	r.repaired = append(r.repaired, id)
	return nil
}

func (r *fakeOutboxRepo) StatusCounts(context.Context, time.Time) (map[publishing.OutboxStatus]out.StatusCount, error) {
	return nil, nil
}

type fakeChannelImporter struct {
	messages []telegramimport.Message
	err      error
	calls    int // how many times ExportChannelMessages was invoked — proves Reconcile fetches at most once per pass, not once per item
}

func (f *fakeChannelImporter) ExportChannelMessages(context.Context, out.TelegramImportRequest) ([]telegramimport.Message, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.messages, nil
}

type fakeUnitOfWork struct {
	err error
}

func (u *fakeUnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if u.err != nil {
		return u.err
	}
	return fn(ctx)
}

type fakePublisher struct {
	published []article.Article
	messageID string
	err       error
	permanent bool
}

func (p *fakePublisher) Publish(_ context.Context, a article.Article) (string, error) {
	if p.err != nil {
		if p.permanent {
			return "", &out.PermanentError{Err: p.err}
		}
		return "", p.err
	}
	p.published = append(p.published, a)
	if p.messageID != "" {
		return p.messageID, nil
	}
	return "fake-message-id", nil
}

type fakeAliasRepository struct {
	byKey     map[string]string // "type|value" -> articleID
	findErr   error
	saveErr   error
	savedKeys []string
}

func aliasKey(t out.AliasType, value string) string { return string(t) + "|" + value }

func (r *fakeAliasRepository) FindArticleID(_ context.Context, aliasType out.AliasType, aliasValue string) (string, error) {
	if r.findErr != nil {
		return "", r.findErr
	}
	id, ok := r.byKey[aliasKey(aliasType, aliasValue)]
	if !ok {
		return "", fmt.Errorf("alias %s: %w", aliasKey(aliasType, aliasValue), sql.ErrNoRows)
	}
	return id, nil
}

func (r *fakeAliasRepository) Save(_ context.Context, aliasType out.AliasType, aliasValue, articleID string) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	if r.byKey == nil {
		r.byKey = map[string]string{}
	}
	r.byKey[aliasKey(aliasType, aliasValue)] = articleID
	r.savedKeys = append(r.savedKeys, aliasKey(aliasType, aliasValue))
	return nil
}

type fakeSimilarityIndexRepository struct {
	candidates []out.Candidate
	findErr    error
	indexErr   error
	indexedFor []string
}

func (r *fakeSimilarityIndexRepository) IndexTerms(_ context.Context, articleID string, _ []string) error {
	if r.indexErr != nil {
		return r.indexErr
	}
	r.indexedFor = append(r.indexedFor, articleID)
	return nil
}

func (r *fakeSimilarityIndexRepository) FindCandidates(_ context.Context, _ out.CandidateFilter) ([]out.Candidate, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.candidates, nil
}

// fakeSourcePolicyRepository is an in-memory out.SourcePolicyDecisionRepository,
// mirroring fakeDecisionRepo's append-only-slice-plus-filter style (the two
// ports are shaped alike: Save/ByArticle/ListByRange here vs.
// Append/ListByArticle/ListByEventTypesSince there). Gate A (this task) never
// calls any of these methods — persistence is wired by Gate B (a later
// task) — so this fake exists only so every NewProcessCycle call site has a
// safe, do-nothing implementation to pass.
type fakeSourcePolicyRepository struct {
	saved          []out.SourcePolicyDecisionRecord
	saveErr        error
	byArticleErr   error
	listByRangeErr error
}

func (r *fakeSourcePolicyRepository) Save(_ context.Context, rec out.SourcePolicyDecisionRecord) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, rec)
	return nil
}

func (r *fakeSourcePolicyRepository) ByArticle(_ context.Context, articleID string) ([]out.SourcePolicyDecisionRecord, error) {
	if r.byArticleErr != nil {
		return nil, r.byArticleErr
	}
	var result []out.SourcePolicyDecisionRecord
	for _, rec := range r.saved {
		if rec.ArticleID == articleID {
			result = append(result, rec)
		}
	}
	return result, nil
}

func (r *fakeSourcePolicyRepository) ListByRange(_ context.Context, from, to time.Time, sourceID, mode string) ([]out.SourcePolicyDecisionRecord, error) {
	if r.listByRangeErr != nil {
		return nil, r.listByRangeErr
	}
	var result []out.SourcePolicyDecisionRecord
	for _, rec := range r.saved {
		if rec.DecidedAt.Before(from) || !rec.DecidedAt.Before(to) {
			continue
		}
		if sourceID != "" && rec.SourceID != sourceID {
			continue
		}
		if mode != "" && rec.Mode != mode {
			continue
		}
		result = append(result, rec)
	}
	return result, nil
}

// fakeSourcePolicyResolver is a test double for sourcepolicy.Resolver. Its
// zero value (mode "" and a nil policies map) resolves every source to
// sourcepolicy.DefaultPolicy and reports sourcepolicy.ModeDisabled —
// reproducing pre-Phase-12 behavior exactly — so every existing
// NewProcessCycle call site can pass &fakeSourcePolicyResolver{} unchanged
// and stay a no-op for Gate A.
type fakeSourcePolicyResolver struct {
	policies map[string]sourcepolicy.Policy
	mode     sourcepolicy.Mode
}

func (r *fakeSourcePolicyResolver) Resolve(sourceID string) sourcepolicy.Policy {
	if p, ok := r.policies[sourceID]; ok {
		return p
	}
	return sourcepolicy.DefaultPolicy(sourceID)
}

func (r *fakeSourcePolicyResolver) Mode() sourcepolicy.Mode {
	if r.mode == "" {
		return sourcepolicy.ModeDisabled
	}
	return r.mode
}

// fakeClusterPublicationRepository is a stateful in-memory simulation of
// ports/out.ClusterPublicationRepository's compare-and-swap semantics — a
// map keyed by cluster_id, not a rubber-stamp "always succeeds" double, so
// unit tests exercise the same reserve/confirm/release state machine the
// real SQLite adapter implements (see adapters/outbound/sqlite/
// cluster_publication_repository.go).
type fakeClusterPublicationRepository struct {
	state map[string]string                            // cluster_id -> "reserved" | "confirmed" | "released"
	rows  map[string]out.ClusterPublicationReservation // cluster_id -> current holder

	reserveErr error
	confirmErr error
	releaseErr error

	confirmed []string // cluster_ids Confirm was called for
	released  []string // cluster_ids Release was called for
}

func (r *fakeClusterPublicationRepository) Reserve(_ context.Context, res out.ClusterPublicationReservation, _ time.Time) (bool, error) {
	if r.reserveErr != nil {
		return false, r.reserveErr
	}
	if r.state == nil {
		r.state = map[string]string{}
		r.rows = map[string]out.ClusterPublicationReservation{}
	}
	switch r.state[res.ClusterID] {
	case "", "released":
		r.state[res.ClusterID] = "reserved"
		r.rows[res.ClusterID] = res
		return true, nil
	default: // "reserved" or "confirmed"
		return false, nil
	}
}

func (r *fakeClusterPublicationRepository) PublishedSourceIDs(_ context.Context, clusterID, excludeArticleID string) ([]string, error) {
	row, ok := r.rows[clusterID]
	if !ok || r.state[clusterID] == "released" || row.ArticleID == excludeArticleID {
		return nil, nil
	}
	return []string{row.SourceID}, nil
}

func (r *fakeClusterPublicationRepository) Confirm(_ context.Context, clusterID, articleID string, _ int64, _ time.Time) error {
	if r.confirmErr != nil {
		return r.confirmErr
	}
	r.confirmed = append(r.confirmed, clusterID)
	if row, ok := r.rows[clusterID]; ok && row.ArticleID == articleID {
		r.state[clusterID] = "confirmed"
	}
	return nil
}

func (r *fakeClusterPublicationRepository) Release(_ context.Context, clusterID, articleID string, _ time.Time) error {
	if r.releaseErr != nil {
		return r.releaseErr
	}
	r.released = append(r.released, clusterID)
	if row, ok := r.rows[clusterID]; ok && row.ArticleID == articleID {
		r.state[clusterID] = "released"
	}
	return nil
}

// fakeSourcePolicyCooldownRepository is a stateful in-memory simulation
// of ports/out.SourcePolicyCooldownRepository's compare-and-swap
// semantics — a map keyed by "sourceID|clusterID", not a rubber-stamp
// "always succeeds" double, so unit tests exercise the same
// reserve/confirm/release state machine the real SQLite adapter
// implements (see adapters/outbound/sqlite/
// source_policy_cooldown_repository.go).
type fakeSourcePolicyCooldownRepository struct {
	state         map[string]string                              // key -> "reserved" | "confirmed" | "released"
	rows          map[string]out.SourcePolicyCooldownReservation // key -> current holder
	cooldownUntil map[string]time.Time                           // key -> current window end

	reserveErr error
	confirmErr error
	releaseErr error

	confirmed []string // keys Confirm was called for
	released  []string // keys Release was called for
}

func cooldownKey(sourceID, clusterID string) string { return sourceID + "|" + clusterID }

func (r *fakeSourcePolicyCooldownRepository) Reserve(_ context.Context, res out.SourcePolicyCooldownReservation, cooldown time.Duration, now time.Time) (bool, error) {
	if r.reserveErr != nil {
		return false, r.reserveErr
	}
	if r.state == nil {
		r.state = map[string]string{}
		r.rows = map[string]out.SourcePolicyCooldownReservation{}
		r.cooldownUntil = map[string]time.Time{}
	}
	key := cooldownKey(res.SourceID, res.ClusterID)
	switch r.state[key] {
	case "", "released":
	default: // "reserved" or "confirmed"
		if !now.After(r.cooldownUntil[key]) && !now.Equal(r.cooldownUntil[key]) {
			return false, nil
		}
	}
	r.state[key] = "reserved"
	r.rows[key] = res
	r.cooldownUntil[key] = now.Add(cooldown)
	return true, nil
}

func (r *fakeSourcePolicyCooldownRepository) ForceReserve(_ context.Context, res out.SourcePolicyCooldownReservation, cooldown time.Duration, now time.Time) error {
	if r.state == nil {
		r.state = map[string]string{}
		r.rows = map[string]out.SourcePolicyCooldownReservation{}
		r.cooldownUntil = map[string]time.Time{}
	}
	key := cooldownKey(res.SourceID, res.ClusterID)
	r.state[key] = "reserved"
	r.rows[key] = res
	r.cooldownUntil[key] = now.Add(cooldown)
	return nil
}

func (r *fakeSourcePolicyCooldownRepository) ActiveCooldownUntil(_ context.Context, sourceID, clusterID string) (time.Time, bool, error) {
	key := cooldownKey(sourceID, clusterID)
	switch r.state[key] {
	case "reserved", "confirmed":
		return r.cooldownUntil[key], true, nil
	default:
		return time.Time{}, false, nil
	}
}

func (r *fakeSourcePolicyCooldownRepository) Confirm(_ context.Context, sourceID, clusterID, articleID string, _ int64, _ time.Time) (bool, error) {
	if r.confirmErr != nil {
		return false, r.confirmErr
	}
	key := cooldownKey(sourceID, clusterID)
	r.confirmed = append(r.confirmed, key)
	if row, ok := r.rows[key]; ok && row.ArticleID == articleID {
		r.state[key] = "confirmed"
		return true, nil
	}
	return false, nil
}

func (r *fakeSourcePolicyCooldownRepository) Release(_ context.Context, sourceID, clusterID, articleID string, _ time.Time) (bool, error) {
	if r.releaseErr != nil {
		return false, r.releaseErr
	}
	key := cooldownKey(sourceID, clusterID)
	r.released = append(r.released, key)
	if row, ok := r.rows[key]; ok && row.ArticleID == articleID {
		r.state[key] = "released"
		return true, nil
	}
	return false, nil
}

// fakeSeenItemsRepository is a stateful in-memory simulation of
// ports/out.SeenItemsRepository's claim/finalize semantics (New/Seen/
// Revised/InFlight) — mirroring fakeClusterPublicationRepository's
// state-machine style so unit tests exercise the same decision logic the
// real SQLite adapter implements, not a rubber-stamp double.
type fakeSeenItemsRepository struct {
	rows map[string]*fakeSeenItemRow // identity key -> row

	claimErr    error
	finalizeErr error
}

type fakeSeenItemRow struct {
	articleID       string
	terminalOutcome string
	contentHash     string
	canonicalURL    string
	seenCount       int
	revision        int
	firstSeenAt     time.Time
	lastSeenAt      time.Time
}

func (r *fakeSeenItemsRepository) Claim(_ context.Context, identity ingestidentity.Identity, articleID, contentHash, canonicalURL string, now time.Time, reclaimAfter time.Duration) (out.SeenClaim, error) {
	if r.claimErr != nil {
		return out.SeenClaim{}, r.claimErr
	}
	if r.rows == nil {
		r.rows = map[string]*fakeSeenItemRow{}
	}
	existing, ok := r.rows[identity.Key]
	if !ok {
		r.rows[identity.Key] = &fakeSeenItemRow{
			articleID: articleID, contentHash: contentHash, canonicalURL: canonicalURL,
			seenCount: 1, revision: 1, firstSeenAt: now, lastSeenAt: now,
		}
		return out.SeenClaim{Status: out.SeenClaimNew, ArticleID: articleID, SeenCount: 1, Revision: 1, FirstSeenAt: now}, nil
	}

	switch {
	case existing.terminalOutcome == "" && now.Sub(existing.lastSeenAt) < reclaimAfter:
		return out.SeenClaim{Status: out.SeenClaimInFlight, ArticleID: existing.articleID, SeenCount: existing.seenCount, Revision: existing.revision, FirstSeenAt: existing.firstSeenAt}, nil
	case existing.terminalOutcome == "":
		existing.lastSeenAt = now
		existing.seenCount++
		return out.SeenClaim{Status: out.SeenClaimNew, ArticleID: existing.articleID, SeenCount: existing.seenCount, Revision: existing.revision, FirstSeenAt: existing.firstSeenAt}, nil
	case existing.contentHash == contentHash:
		existing.lastSeenAt = now
		existing.seenCount++
		return out.SeenClaim{
			Status: out.SeenClaimSeen, ArticleID: existing.articleID, TerminalOutcome: existing.terminalOutcome,
			PreviousContentHash: existing.contentHash, PreviousCanonicalURL: existing.canonicalURL,
			SeenCount: existing.seenCount, Revision: existing.revision, FirstSeenAt: existing.firstSeenAt,
		}, nil
	default:
		return out.SeenClaim{
			Status: out.SeenClaimRevised, ArticleID: existing.articleID, TerminalOutcome: existing.terminalOutcome,
			PreviousContentHash: existing.contentHash, PreviousCanonicalURL: existing.canonicalURL,
			SeenCount: existing.seenCount, Revision: existing.revision, FirstSeenAt: existing.firstSeenAt,
		}, nil
	}
}

func (r *fakeSeenItemsRepository) Finalize(_ context.Context, identity ingestidentity.Identity, outcome, contentHash, canonicalURL string, now time.Time) error {
	if r.finalizeErr != nil {
		return r.finalizeErr
	}
	row, ok := r.rows[identity.Key]
	if !ok {
		return fmt.Errorf("fake seen item %s: finalize before claim", identity.Key)
	}
	if row.contentHash != contentHash {
		row.revision++
	}
	row.terminalOutcome = outcome
	row.contentHash = contentHash
	row.canonicalURL = canonicalURL
	row.lastSeenAt = now
	return nil
}

type fakeLabelRepo struct {
	saved          []training.Example
	editorialCount int
	countErr       error
}

func (r *fakeLabelRepo) Label(_ context.Context, e training.Example) error {
	r.saved = append(r.saved, e)
	return nil
}

func (r *fakeLabelRepo) Relabel(context.Context, training.Example, int) error { return nil }
func (r *fakeLabelRepo) Retract(context.Context, string, int) error           { return nil }
func (r *fakeLabelRepo) GetActive(context.Context, string) (training.Example, bool, error) {
	return training.Example{}, false, nil
}

func (r *fakeLabelRepo) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}
func (r *fakeLabelRepo) CountUnresolvedConflicts(context.Context) (int, error) { return 0, nil }
func (r *fakeLabelRepo) ResolveConflict(context.Context, string, training.Example) error {
	return nil
}

func (r *fakeLabelRepo) ListByArticle(context.Context, string) ([]training.Example, error) {
	return nil, nil
}

func (r *fakeLabelRepo) ListAll(context.Context) ([]training.Example, error) {
	return r.saved, nil
}

func (r *fakeLabelRepo) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return nil, nil
}

func (r *fakeLabelRepo) CountEditorialExamples(context.Context) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.editorialCount, nil
}
