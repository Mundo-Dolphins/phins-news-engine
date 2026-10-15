package usecase_test

// Task 10: behavioral, fake-based tests proving Gate B's
// cooldown-reservation wiring in process_cycle.go — the corrected version
// from Task 8's fix round, where wouldPublishSpecial is captured once
// (immediately after sourcepolicy.Decide) and never reassigned, while
// policyDecision/reservationOutcome ARE reassigned by
// sourcepolicy.ResolveCooldownReservation. No new production code is added
// here; every fixture below reuses this package's existing fakes
// (fakes_test.go) and existing construction helpers/patterns
// (process_cycle_test.go) — see confirmationFoundingCandidate/
// confirmationCandidateArticle/newCooldownProcessCycle below for the small
// amount of new-but-not-reinvented wiring this file needed, since none of
// the existing NewProcessCycle-wrapping helpers exposed a caller-supplied
// *fakeSourcePolicyCooldownRepository (needed here to assert on its
// state/rows/released fields after Run).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/deduplication"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// confirmationFoundingCandidate is the "existing cluster member" half of the
// MemberRoleConfirmation fixture this file reuses throughout — the exact
// same shape TestProcessCycle_OfficialConfirmation_NoNewInfo_RetainedNotPublished
// and TestProcessCycle_ClusterJoinsExistingClusterOnMatchingEventSignals
// (process_cycle_test.go) already established: a rival source's "Jane Doe"
// signing report, 2 hours before now, inside the 48h transaction window
// clusterTestSettings() configures. Parametrized by clusterID/now so callers
// can share one fakeClusterRepo across sequential Run() calls at different
// clock values (see the Step 1 reproduction test).
func confirmationFoundingCandidate(clusterID string, now time.Time) clustering.Candidate {
	return clustering.Candidate{
		ArticleID: "existing-1", ClusterID: clusterID,
		Representation: eventsignals.Representation{
			ArticleID: "existing-1", SourceID: "rival-source", Domain: "rival.com",
			PrimaryEntity: "Jane Doe", Action: eventsignals.ActionSign, EventType: eventsignals.EventTypeTransaction,
			EventDate: now.Add(-2 * time.Hour), EventWindow: 48 * time.Hour,
		},
	}
}

// confirmationCandidateArticle builds the incoming half of the same fixture:
// same entity/action as confirmationFoundingCandidate, from a distinct
// source, so it joins as MemberRoleConfirmation via clustering.selectMemberRole's
// default case (same action as the winning candidate, no explicit
// correction/confirmation/analysis/reaction framing, no dedup relation).
func confirmationCandidateArticle(t *testing.T, sourceRaw, url string, now time.Time) article.Article {
	t.Helper()
	return article.Article{
		Title:       "Isotopes Sign Jane Doe To Bolster Secondary",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, sourceRaw), Type: source.TypeRSS},
		OriginalURL: url, ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}
}

// newCooldownProcessCycle is this file's counterpart to process_cycle_test.go's
// newProcessCycleWithSourcePolicy/newProcessCycleWithSourcePolicyRepo: none of
// those expose a caller-supplied *fakeSourcePolicyCooldownRepository or
// *fakeClusterPublicationRepository or editorial model, all of which this
// file's tests need to either pre-seed or inspect afterward. Every other
// dependency (dedup/aliases/similarityIndex/uow/sourcePolicies/seenItems)
// is a fresh, inert fake per call, matching this package's existing
// "share only what the test actually inspects" convention.
func newCooldownProcessCycle(
	fetcher fakeFetcher, articles *fakeArticleRepo, clusters *fakeClusterRepo, decisions *fakeDecisionRepo,
	outbox *fakeOutboxRepo, clock fakeClock, resolver *fakeSourcePolicyResolver,
	cooldowns *fakeSourcePolicyCooldownRepository, clusterPubs *fakeClusterPublicationRepository,
	cascade in.DedupCascadeConfig, model *editorial.Model, unknownEntities out.UnknownEntityRepository,
) *usecase.ProcessCycle {
	return usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, decisions, outbox, &fakeUnitOfWork{}, freshness, dictionary.Matcher{}, model, nil, cascade, clock, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, clusterPubs, &fakeSeenItemsRepository{}, cooldowns, unknownEntities,
		nil,
	)
}

// confirmationPolicy builds the standard "official confirmation, allowed to
// publish without new information, 20-minute cooldown" policy this file's
// Confirmation-role fixtures share.
func confirmationPolicy(sourceKey string, allowConfirmation bool) sourcepolicy.Policy {
	return sourcepolicy.Policy{
		SourceID: sourceKey, Authority: sourcepolicy.AuthorityOfficialTeam,
		AllowOfficialConfirmation:                allowConfirmation,
		PublishConfirmationWithoutNewInformation: allowConfirmation,
		EventCooldown:                            20 * time.Minute,
	}
}

// releasedContains reports whether key appears in released — the shared
// assertion helper every "must have released" test below uses against
// fakeSourcePolicyCooldownRepository.released.
func releasedContains(released []string, key string) bool {
	for _, k := range released {
		if k == key {
			return true
		}
	}
	return false
}

// --- Step 1-2: the issue's own reproduction scenario -----------------------

// TestProcessCycle_Cooldown_PreliminaryWouldPublishDoesNotBlockAFollowingCandidate
// reproduces the design doc's motivating bug end-to-end at the ProcessCycle
// unit level: candidate A joins cluster "cluster-existing-1" as
// MemberRoleConfirmation under a policy allowing official confirmation
// (cooldown 20m), Gate B reserves the cooldown window for A, then Step 7's
// forced-ZoneReject editorial gate rejects A before it ever reaches
// accept() — and rejectEditorial releases A's reservation. Candidate B, same
// source+cluster, 5 minutes later (well inside the 20-minute window by
// wall-clock), must NOT be blocked, because A never actually published.
func TestProcessCycle_Cooldown_PreliminaryWouldPublishDoesNotBlockAFollowingCandidate(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	const clusterID = "cluster-existing-1"
	const sourceRaw = "sprisotopes"
	sourceKey := "rss:" + sourceRaw

	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, true)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{confirmationFoundingCandidate(clusterID, now)}}

	// --- Run 1: A joins as MemberRoleConfirmation, Gate B reserves the
	// cooldown, then a forced ZoneReject editorial gate rejects it.
	cascadeA := defaultCascade()
	cascadeA.Editorial = in.EditorialSettings{Mode: "enforce"}
	articlesA := &fakeArticleRepo{}
	fetcherA := fakeFetcher{articles: []article.Article{confirmationCandidateArticle(t, sourceRaw, "https://example.com/a-confirms", now)}}
	uc1 := usecase.NewProcessCycle(
		fetcherA, articlesA, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{},
		alwaysRejectModel(), nil, cascadeA, fakeClock{now: now}, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, cooldowns, &fakeUnknownEntityRepository{},
		nil,
	)

	result1, err := uc1.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() [A] error = %v", err)
	}
	finalA := articlesA.lastSaved()
	if finalA.ClusterMemberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: A's ClusterMemberRole = %q, want %q (this test requires Gate B's cooldown-relevant Confirmation path)", finalA.ClusterMemberRole, clustering.MemberRoleConfirmation)
	}
	if result1.Rejected != 1 || result1.Accepted != 0 {
		t.Fatalf("result1 = %+v, want rejected=1 accepted=0 (A rejected by editorial after Gate B reserved its window)", result1)
	}
	if finalA.Status != article.StatusRejectedQuality {
		t.Errorf("A's Status = %q, want %q", finalA.Status, article.StatusRejectedQuality)
	}
	// Gate B's counter increments right after ResolveCooldownReservation,
	// before Step 7 ever runs — so A's later editorial rejection does not
	// undo this count. This is the crux of the issue: a preliminary
	// ShouldPublish=true decision DID reserve the window.
	if result1.CooldownReserved != 1 {
		t.Errorf("result1.CooldownReserved = %d, want 1", result1.CooldownReserved)
	}

	key := cooldownKey(sourceKey, clusterID)
	if !releasedContains(cooldowns.released, key) {
		t.Fatalf("cooldowns.released = %v, want it to contain %q — A's editorial rejection must release the reservation it took", cooldowns.released, key)
	}
	if cooldowns.state[key] != "released" {
		t.Errorf("cooldowns.state[%q] = %q, want %q", key, cooldowns.state[key], "released")
	}

	// --- Run 2: B, same source+cluster, 5 minutes later (inside the
	// 20-minute window) — must publish, not be blocked.
	now2 := now.Add(5 * time.Minute)
	cascadeB := defaultCascade()
	articlesB := &fakeArticleRepo{}
	fetcherB := fakeFetcher{articles: []article.Article{confirmationCandidateArticle(t, sourceRaw, "https://example.com/b-confirms", now2)}}
	uc2 := usecase.NewProcessCycle(
		fetcherB, articlesB, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{},
		nil, nil, cascadeB, fakeClock{now: now2}, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, cooldowns, &fakeUnknownEntityRepository{},
		nil,
	)

	result2, err := uc2.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() [B] error = %v", err)
	}
	finalB := articlesB.lastSaved()
	if finalB.ClusterMemberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: B's ClusterMemberRole = %q, want %q", finalB.ClusterMemberRole, clustering.MemberRoleConfirmation)
	}
	if result2.Accepted != 1 || result2.Rejected != 0 {
		t.Fatalf("result2 = %+v, want accepted=1 rejected=0: B must publish, not be blocked by A's already-released reservation", result2)
	}
	if result2.CooldownBlocked != 0 {
		t.Errorf("result2.CooldownBlocked = %d, want 0 — the bug this issue fixes would have made this 1", result2.CooldownBlocked)
	}
}

// --- Step 3: release/block/shadow/force scenarios --------------------------

// TestProcessCycle_Cooldown_ReleasedOnFreshnessStale mirrors the Step 1
// reproduction's release path but through Step 8's freshness gate instead of
// Step 7's editorial gate: Gate B reserves the cooldown for a Confirmation
// candidate whose PublishedAt is still close enough to the founding
// candidate's EventDate to join the cluster, but a deliberately tight
// FreshnessPolicy (max age 1h, vs. the fixture's 3h-old PublishedAt) rejects
// it as stale — reject() must release the reservation reject() would
// otherwise orphan.
func TestProcessCycle_Cooldown_ReleasedOnFreshnessStale(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	const clusterID = "cluster-existing-1"
	const sourceRaw = "sprisotopes"
	sourceKey := "rss:" + sourceRaw

	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, true)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{confirmationFoundingCandidate(clusterID, now)}}
	articles := &fakeArticleRepo{}

	a := confirmationCandidateArticle(t, sourceRaw, "https://example.com/stale-confirms", now)
	// 3 hours old: still within the founding candidate's 48h transaction
	// window (diff from its EventDate, now-2h, is only 1h) so clustering
	// still joins, but outside a deliberately tight freshness policy.
	a.PublishedAt = now.Add(-3 * time.Hour)
	fetcher := fakeFetcher{articles: []article.Article{a}}
	tightFreshness := publishing.FreshnessPolicy{HardNewsHours: 1, SocialHours: 1, AnalysisHours: 1, DefaultHours: 1}

	uc := usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, tightFreshness, dictionary.Matcher{},
		nil, nil, defaultCascade(), fakeClock{now: now}, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, cooldowns, &fakeUnknownEntityRepository{},
		nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := articles.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q", final.ClusterMemberRole, clustering.MemberRoleConfirmation)
	}
	if result.Rejected != 1 || final.Status != article.StatusRejectedStale {
		t.Fatalf("result = %+v, final.Status = %q, want rejected=1 status=%q", result, final.Status, article.StatusRejectedStale)
	}
	if result.CooldownReserved != 1 {
		t.Errorf("result.CooldownReserved = %d, want 1 (Gate B reserved before freshness rejected)", result.CooldownReserved)
	}
	key := cooldownKey(sourceKey, clusterID)
	if !releasedContains(cooldowns.released, key) {
		t.Fatalf("cooldowns.released = %v, want it to contain %q", cooldowns.released, key)
	}
	if cooldowns.state[key] != "released" {
		t.Errorf("cooldowns.state[%q] = %q, want %q", key, cooldowns.state[key], "released")
	}
}

// TestProcessCycle_Cooldown_ReleasedOnClusterGateDenial proves
// denyClusterDuplicate's unconditional Release call is safe cleanup even on
// the one path that can actually reach a cluster-gate denial with a
// cooldown-configured policy. By construction (see sourcepolicy.Decide and
// clustergate.Decide), the two cooldown-relevant codes
// (CodePublishOfficialConfirmation/CodePublishSubstantialUpdate) are always
// exempt from denial at the cluster gate — a lost cluster-publication CAS
// for either of those codes yields OutcomeAllowedUpdate, never
// OutcomeDeniedDuplicate — so an article that ever reserved a cooldown can
// never end up denied here. Reaching OutcomeDeniedDuplicate therefore
// requires a decision that never attempted a reservation in the first
// place: mode=ModeShadow so Gate B's enforce-only reject-check does not
// stop a ShouldPublish=false CodeRetainClusterMember decision
// (AllowOfficialConfirmation=false) before Step 8, where a pre-seeded
// cluster-publication reservation held by a different article denies it.
func TestProcessCycle_Cooldown_ReleasedOnClusterGateDenial(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	const clusterID = "cluster-existing-1"
	const sourceRaw = "sprisotopes"
	sourceKey := "rss:" + sourceRaw

	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeShadow,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, false)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{confirmationFoundingCandidate(clusterID, now)}}
	clusterPubs := &fakeClusterPublicationRepository{
		state: map[string]string{clusterID: "reserved"},
		rows:  map[string]out.ClusterPublicationReservation{clusterID: {ClusterID: clusterID, ArticleID: "someone-else-article"}},
	}
	articles := &fakeArticleRepo{}
	fetcher := fakeFetcher{articles: []article.Article{confirmationCandidateArticle(t, sourceRaw, "https://example.com/denied-confirms", now)}}

	uc := usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{},
		nil, nil, defaultCascade(), fakeClock{now: now}, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, clusterPubs, &fakeSeenItemsRepository{}, cooldowns, &fakeUnknownEntityRepository{},
		nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := articles.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q", final.ClusterMemberRole, clustering.MemberRoleConfirmation)
	}
	if result.ClusterGateDenied != 1 || result.Rejected != 1 || final.Status != article.StatusRetainedNoPublish {
		t.Fatalf("result = %+v, final.Status = %q, want ClusterGateDenied=1 rejected=1 status=%q", result, final.Status, article.StatusRetainedNoPublish)
	}
	if result.CooldownReserved != 0 {
		t.Errorf("result.CooldownReserved = %d, want 0: CodeRetainClusterMember is never cooldown-relevant, so no reservation was ever attempted", result.CooldownReserved)
	}
	key := cooldownKey(sourceKey, clusterID)
	if !releasedContains(cooldowns.released, key) {
		t.Errorf("cooldowns.released = %v, want it to contain %q: denyClusterDuplicate calls Release unconditionally", cooldowns.released, key)
	}
	if _, exists := cooldowns.state[key]; exists {
		t.Errorf("cooldowns.state[%q] = %q, want no entry: nothing was ever reserved for this key", key, cooldowns.state[key])
	}
}

// TestProcessCycle_Cooldown_ShadowModeNeverReserves proves mode=ModeShadow
// never calls Reserve/ForceReserve at all — the reservation layer is purely
// simulated (OutcomeShadow) — while still publishing (shadow mode never
// gates) and recording the "would have reserved" telemetry counter.
func TestProcessCycle_Cooldown_ShadowModeNeverReserves(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	const clusterID = "cluster-existing-1"
	const sourceRaw = "sprisotopes"
	sourceKey := "rss:" + sourceRaw

	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeShadow,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, true)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{confirmationFoundingCandidate(clusterID, now)}}
	articles := &fakeArticleRepo{}
	fetcher := fakeFetcher{articles: []article.Article{confirmationCandidateArticle(t, sourceRaw, "https://example.com/shadow-confirms", now)}}

	uc := newCooldownProcessCycle(fetcher, articles, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, fakeClock{now: now}, resolver, cooldowns, &fakeClusterPublicationRepository{}, defaultCascade(), nil, &fakeUnknownEntityRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := articles.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q", final.ClusterMemberRole, clustering.MemberRoleConfirmation)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 rejected=0: shadow mode never gates publication", result)
	}
	if result.CooldownShadowWouldPublish != 1 {
		t.Errorf("result.CooldownShadowWouldPublish = %d, want 1", result.CooldownShadowWouldPublish)
	}
	if result.CooldownReserved != 0 {
		t.Errorf("result.CooldownReserved = %d, want 0: shadow mode must never actually reserve", result.CooldownReserved)
	}
	if len(cooldowns.state) != 0 || len(cooldowns.rows) != 0 {
		t.Errorf("cooldowns.state = %v, rows = %v, want both empty: shadow mode must never touch the reservation table", cooldowns.state, cooldowns.rows)
	}
}

// correctionCascade extends defaultCascade()'s clustering rules with a
// content cue for eventsignals.ContentSignalCorrection — clusterTestSettings
// (process_cycle_test.go) only configures Confirmation/Rumor cues, and this
// file needs a real "correction" framing detected from article text (not
// hand-built via a domain-level Representation literal, the way
// domain/clustering's own tests do it) to drive MemberRoleCorrection through
// the full ProcessCycle pipeline. defaultCascade() builds a fresh
// eventsignals.Rules map on every call, so mutating the returned cascade's
// ContentCues here cannot leak into any other test.
func correctionCascade() in.DedupCascadeConfig {
	cascade := defaultCascade()
	cascade.Cluster.Rules.ContentCues[eventsignals.ContentSignalCorrection] = []string{"correction"}
	return cascade
}

// TestProcessCycle_Cooldown_CorrectionForceReservesEvenOverAnActiveWindow
// pre-seeds the fake with an active, un-expired reservation held by a
// different article, then runs a MemberRoleCorrection candidate for the
// same source+cluster. sourcepolicy.Decide's Correction branch bypasses the
// cooldown check entirely by design ("factual correctness is not something
// a pacing control should delay"), and Gate B's wiring uses ForceReserve
// (not the CAS'd Reserve) for MemberRoleCorrection — so this must still
// publish, and the reservation's holder must become the correction's own
// article, overwriting the pre-seeded one.
func TestProcessCycle_Cooldown_CorrectionForceReservesEvenOverAnActiveWindow(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	const clusterID = "cluster-existing-1"
	const sourceRaw = "sprisotopes"
	sourceKey := "rss:" + sourceRaw
	key := cooldownKey(sourceKey, clusterID)

	resolver := &fakeSourcePolicyResolver{
		mode: sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{
			sourceKey: {SourceID: sourceKey, Authority: sourcepolicy.AuthorityOfficialTeam, AllowSubstantialUpdate: true, EventCooldown: 20 * time.Minute},
		},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{
		state:         map[string]string{key: "reserved"},
		rows:          map[string]out.SourcePolicyCooldownReservation{key: {SourceID: sourceKey, ClusterID: clusterID, ArticleID: "prior-confirmation-article", DecisionCode: string(sourcepolicy.CodePublishOfficialConfirmation)}},
		cooldownUntil: map[string]time.Time{key: now.Add(15 * time.Minute)},
	}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{confirmationFoundingCandidate(clusterID, now)}}
	articles := &fakeArticleRepo{}
	fetcher := fakeFetcher{articles: []article.Article{{
		Title:       "Correction: Isotopes Did Not Sign Jane Doe",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, sourceRaw), Type: source.TypeRSS},
		OriginalURL: "https://example.com/correction", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}

	uc := newCooldownProcessCycle(fetcher, articles, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, fakeClock{now: now}, resolver, cooldowns, &fakeClusterPublicationRepository{}, correctionCascade(), nil, &fakeUnknownEntityRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := articles.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleCorrection) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q", final.ClusterMemberRole, clustering.MemberRoleCorrection)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 rejected=0: a correction always publishes, bypassing the cooldown check", result)
	}
	if result.CooldownReserved != 1 {
		t.Errorf("result.CooldownReserved = %d, want 1 (OutcomeForced counts toward CooldownReserved)", result.CooldownReserved)
	}
	if cooldowns.state[key] != "reserved" {
		t.Errorf("cooldowns.state[%q] = %q, want %q", key, cooldowns.state[key], "reserved")
	}
	if cooldowns.rows[key].ArticleID != final.ID {
		t.Errorf("cooldowns.rows[%q].ArticleID = %q, want the correction's own article %q — ForceReserve must overwrite the prior holder", key, cooldowns.rows[key].ArticleID, final.ID)
	}
}

// TestProcessCycle_Cooldown_BlockedByCooldownWhenReservationCASFails
// pre-seeds an active, un-expired reservation held by a different article
// for the same source+cluster, then runs a non-correction Confirmation
// candidate. Whichever internal path produces it (see the note below), the
// externally visible behavior must be: the candidate is retained, not
// published, and result.CooldownBlocked counts it.
//
// NOTE on this fake's fidelity to the "CAS race" case specifically: with
// fakeSourcePolicyCooldownRepository (fakes_test.go), Decide's own
// cooldownRemaining read (via ActiveCooldownUntil) and Reserve's CAS check
// are both derived from the exact same state/cooldownUntil map entries,
// using an equivalent "now before cooldownUntil" boundary test — so a
// pre-seeded active reservation is caught by Decide's own stale-read
// short-circuit (Code already CodeRejectCooldown, sourcepolicy.
// OutcomeNotApplicable) before Reserve is ever attempted, not by
// ResolveCooldownReservation's OutcomeBlocked branch (the genuine
// lost-CAS-race case). A true "Decide's read said clear, but the CAS lost
// anyway" race requires the window to become active in the gap between
// those two calls, which a single-threaded, single-map fake sharing one
// `now` per article cannot reproduce. This is flagged explicitly in this
// task's report as a test-infrastructure limitation, not a production bug:
// OutcomeBlocked itself is already covered directly at the pure-function
// level by domain/sourcepolicy/cooldown_test.go (which calls
// ResolveCooldownReservation with acquired=false directly). The
// process_cycle.go-visible contract this test guards — CooldownBlocked
// increments and the article is retained — holds identically either way.
func TestProcessCycle_Cooldown_BlockedByCooldownWhenReservationCASFails(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	const clusterID = "cluster-existing-1"
	const sourceRaw = "sprisotopes"
	sourceKey := "rss:" + sourceRaw
	key := cooldownKey(sourceKey, clusterID)

	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, true)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{
		state:         map[string]string{key: "reserved"},
		rows:          map[string]out.SourcePolicyCooldownReservation{key: {SourceID: sourceKey, ClusterID: clusterID, ArticleID: "other-article"}},
		cooldownUntil: map[string]time.Time{key: now.Add(15 * time.Minute)},
	}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{confirmationFoundingCandidate(clusterID, now)}}
	articles := &fakeArticleRepo{}
	fetcher := fakeFetcher{articles: []article.Article{confirmationCandidateArticle(t, sourceRaw, "https://example.com/blocked-confirms", now)}}

	uc := newCooldownProcessCycle(fetcher, articles, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, fakeClock{now: now}, resolver, cooldowns, &fakeClusterPublicationRepository{}, defaultCascade(), nil, &fakeUnknownEntityRepository{})

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := articles.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q", final.ClusterMemberRole, clustering.MemberRoleConfirmation)
	}
	if result.Rejected != 1 || final.Status != article.StatusRetainedNoPublish {
		t.Fatalf("result = %+v, final.Status = %q, want rejected=1 status=%q", result, final.Status, article.StatusRetainedNoPublish)
	}
	if result.CooldownBlocked != 1 {
		t.Errorf("result.CooldownBlocked = %d, want 1", result.CooldownBlocked)
	}
	if result.CooldownReserved != 0 {
		t.Errorf("result.CooldownReserved = %d, want 0: this candidate never acquired the active window", result.CooldownReserved)
	}
	if cooldowns.rows[key].ArticleID != "other-article" {
		t.Errorf("cooldowns.rows[%q].ArticleID = %q, want unchanged %q: this candidate never became the holder", key, cooldowns.rows[key].ArticleID, "other-article")
	}
}

// --- Step 3: "never touches the reservation" scenarios ---------------------

// TestProcessCycle_Cooldown_ExactDuplicateNeverAttemptsAReservation proves
// an exact-duplicate reject (Step 2, canonical-URL fingerprint match) never
// reaches clustering/Gate B at all, so the cooldown repository's state/rows
// maps stay entirely empty — not merely that a reservation happened not to
// succeed.
func TestProcessCycle_Cooldown_ExactDuplicateNeverAttemptsAReservation(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	sourceKey := "rss:sprisotopes"
	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, true)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	a := article.Article{
		Title:       "Isotopes Sign Jane Doe To Bolster Secondary",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "sprisotopes"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/exact-dup", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}
	fp := deduplication.Fingerprint(article.Normalize(a))
	articles := &fakeArticleRepo{}
	fetcher := fakeFetcher{articles: []article.Article{a}}

	uc := usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{knownFingerprint: fp}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		&fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{},
		nil, nil, defaultCascade(), fakeClock{now: now}, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, cooldowns, &fakeUnknownEntityRepository{},
		nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := articles.lastSaved()
	if final.Status != article.StatusRejectedDuplicate {
		t.Fatalf("test fixture bug: Status = %q, want %q", final.Status, article.StatusRejectedDuplicate)
	}
	if result.Duplicate != 1 || result.CooldownReserved != 0 || result.CooldownBlocked != 0 {
		t.Errorf("result = %+v, want duplicate=1 cooldownReserved=0 cooldownBlocked=0", result)
	}
	if len(cooldowns.state) != 0 || len(cooldowns.rows) != 0 {
		t.Errorf("cooldowns.state = %v, rows = %v, want both empty: an exact duplicate rejects before clustering/Gate B ever run", cooldowns.state, cooldowns.rows)
	}
}

// TestProcessCycle_Cooldown_ClassificationRejectNeverAttemptsAReservation is
// ExactDuplicateNeverAttemptsAReservation's Step 3 (dictionary
// classification Related:false) counterpart — also entirely before Gate B.
func TestProcessCycle_Cooldown_ClassificationRejectNeverAttemptsAReservation(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	sourceKey := "rss:sprisotopes"
	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, true)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	articles := &fakeArticleRepo{}
	// Same not-related fixture text TestProcessCycle_RejectsNotRelatedArticleWithoutClustering
	// (process_cycle_test.go) already proved rejects under classificationMatcher.
	fetcher := fakeFetcher{articles: []article.Article{{
		Title:       "Weather forecast for the weekend",
		Source:      source.Source{ID: mustID(t, source.TypeRSS, "sprisotopes"), Type: source.TypeRSS},
		OriginalURL: "https://example.com/not-related", ContentType: article.ContentTypeArticle,
		PublishedAt: now.Add(-1 * time.Hour),
	}}}
	cascade := defaultCascade()
	cascade.Classification = in.ClassificationSettings{Enabled: true, TrustedSourceIDs: map[string]bool{}}

	uc := usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		&fakeClusterRepo{}, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, classificationMatcher(t),
		nil, nil, cascade, fakeClock{now: now}, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, cooldowns, &fakeUnknownEntityRepository{},
		nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := articles.lastSaved()
	if final.Status != article.StatusRejectedNotRelated {
		t.Fatalf("test fixture bug: Status = %q, want %q", final.Status, article.StatusRejectedNotRelated)
	}
	if result.Rejected != 1 || result.CooldownReserved != 0 || result.CooldownBlocked != 0 {
		t.Errorf("result = %+v, want rejected=1 cooldownReserved=0 cooldownBlocked=0", result)
	}
	if len(cooldowns.state) != 0 || len(cooldowns.rows) != 0 {
		t.Errorf("cooldowns.state = %v, rows = %v, want both empty: a classification reject happens before clustering/Gate B ever run", cooldowns.state, cooldowns.rows)
	}
}

// TestProcessCycle_Cooldown_AmbiguousEditorialZoneProceedsWithoutReleasing
// proves editorial.ZoneAmbiguous is genuinely different from a reject:
// processOne's switch only appends an "editorial_ambiguous" audit event for
// that zone, the article still proceeds to Step 8 and accept() — so the
// cooldown reservation Gate B already took must remain "reserved", never
// "released".
func TestProcessCycle_Cooldown_AmbiguousEditorialZoneProceedsWithoutReleasing(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	const clusterID = "cluster-existing-1"
	const sourceRaw = "sprisotopes"
	sourceKey := "rss:" + sourceRaw

	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, true)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{confirmationFoundingCandidate(clusterID, now)}}
	articles := &fakeArticleRepo{}
	fetcher := fakeFetcher{articles: []article.Article{confirmationCandidateArticle(t, sourceRaw, "https://example.com/ambiguous-confirms", now)}}
	cascade := defaultCascade()
	cascade.Editorial = in.EditorialSettings{Mode: "enforce"}

	uc := usecase.NewProcessCycle(
		fetcher, articles, &fakeDeduplicationRepo{}, &fakeAliasRepository{}, &fakeSimilarityIndexRepository{},
		clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, &fakeUnitOfWork{}, freshness, dictionary.Matcher{},
		alwaysAmbiguousModel(), nil, cascade, fakeClock{now: now}, fakeLogger{},
		&fakeSourcePolicyRepository{}, resolver, &fakeClusterPublicationRepository{}, &fakeSeenItemsRepository{}, cooldowns, &fakeUnknownEntityRepository{},
		nil,
	)

	result, err := uc.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	final := articles.lastSaved()
	if final.ClusterMemberRole != string(clustering.MemberRoleConfirmation) {
		t.Fatalf("test fixture bug: ClusterMemberRole = %q, want %q", final.ClusterMemberRole, clustering.MemberRoleConfirmation)
	}
	if result.Accepted != 1 || result.Rejected != 0 {
		t.Fatalf("result = %+v, want accepted=1 rejected=0: the ambiguous zone flags, it never blocks", result)
	}
	key := cooldownKey(sourceKey, clusterID)
	if cooldowns.state[key] != "reserved" {
		t.Errorf("cooldowns.state[%q] = %q, want %q: still headed for the outbox, must not be released", key, cooldowns.state[key], "reserved")
	}
	if releasedContains(cooldowns.released, key) {
		t.Errorf("cooldowns.released = %v, want it to NOT contain %q: ZoneAmbiguous must never trigger a release", cooldowns.released, key)
	}
}

// TestProcessCycle_Cooldown_CancellationLeavesNoReservation mirrors
// TestProcessCycle_ContextCancellationStopsProcessing's exact mechanism (a
// context already cancelled before Run is called — this package has no
// mid-loop cancellation hook) to prove the design doc's Consequences-section
// claim: p.uow.Do wraps the reservation attempt and everything downstream of
// it, so a crash/cancellation before commit can never leave an orphaned
// reservation. Run's per-article ctx.Err() check fires before processOne is
// ever called for this fixture's one candidate, so its would-be
// reservation is never attempted at all.
func TestProcessCycle_Cooldown_CancellationLeavesNoReservation(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	const clusterID = "cluster-existing-1"
	const sourceRaw = "sprisotopes"
	sourceKey := "rss:" + sourceRaw

	resolver := &fakeSourcePolicyResolver{
		mode:     sourcepolicy.ModeEnforce,
		policies: map[string]sourcepolicy.Policy{sourceKey: confirmationPolicy(sourceKey, true)},
	}
	cooldowns := &fakeSourcePolicyCooldownRepository{}
	clusters := &fakeClusterRepo{candidates: []clustering.Candidate{confirmationFoundingCandidate(clusterID, now)}}
	fetcher := fakeFetcher{articles: []article.Article{confirmationCandidateArticle(t, sourceRaw, "https://example.com/cancelled-confirms", now)}}

	uc := newCooldownProcessCycle(fetcher, &fakeArticleRepo{}, clusters, &fakeDecisionRepo{}, &fakeOutboxRepo{}, fakeClock{now: now}, resolver, cooldowns, &fakeClusterPublicationRepository{}, defaultCascade(), nil, &fakeUnknownEntityRepository{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := uc.Run(ctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if len(cooldowns.state) != 0 || len(cooldowns.rows) != 0 {
		t.Errorf("cooldowns.state = %v, rows = %v, want both empty: a cancelled cycle must never commit a reservation attempt", cooldowns.state, cooldowns.rows)
	}
}
