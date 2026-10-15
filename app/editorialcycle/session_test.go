package editorialcycle_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/activelearning"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewdecision"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

type fakeSessions struct {
	byID      map[string]out.EditorialCycleSession
	running   *out.EditorialCycleSession
	completed *out.EditorialCycleSession
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{byID: map[string]out.EditorialCycleSession{}}
}

func (f *fakeSessions) Create(_ context.Context, s out.EditorialCycleSession) error {
	f.byID[s.ID] = s
	running := s
	f.running = &running
	return nil
}

func (f *fakeSessions) Update(_ context.Context, s out.EditorialCycleSession) error {
	f.byID[s.ID] = s
	if s.Status == "running" {
		f.running = &s
	} else {
		f.running = nil
	}
	if s.Status == "completed" {
		f.completed = &s
	}
	return nil
}

func (f *fakeSessions) GetLatestRunning(context.Context) (out.EditorialCycleSession, error) {
	if f.running == nil {
		return out.EditorialCycleSession{}, fmt.Errorf("no running session: %w", sql.ErrNoRows)
	}
	return *f.running, nil
}

func (f *fakeSessions) GetLatestCompleted(context.Context) (out.EditorialCycleSession, error) {
	if f.completed == nil {
		return out.EditorialCycleSession{}, fmt.Errorf("no completed session: %w", sql.ErrNoRows)
	}
	return *f.completed, nil
}

// GetLatestResumable mirrors the real SQL — status IN
// ('running','paused','failed') ORDER BY updated_at DESC LIMIT 1 — by
// scanning every stored session rather than tracking yet another
// single-session pointer the way running/completed above do: three more
// statuses would mean three more pointers to keep in sync. Ties on
// UpdatedAt (common here, since tests share one frozen clock) are broken
// by ID, so the result never depends on map iteration order.
func (f *fakeSessions) GetLatestResumable(context.Context) (out.EditorialCycleSession, error) {
	var latest out.EditorialCycleSession
	var found bool
	for _, s := range f.byID {
		switch s.Status {
		case "running", "paused", "failed":
		default:
			continue
		}
		newer := s.UpdatedAt.After(latest.UpdatedAt) ||
			(s.UpdatedAt.Equal(latest.UpdatedAt) && s.ID > latest.ID)
		if !found || newer {
			latest, found = s, true
		}
	}
	if !found {
		return out.EditorialCycleSession{}, fmt.Errorf("no resumable session: %w", sql.ErrNoRows)
	}
	return latest, nil
}

// fakeArticles is the slice of out.ArticleRepository these tests need: a
// corpus fakeCandidates checks membership against (so purging an article
// behaves like editorial_cycle_session_candidates' ON DELETE CASCADE),
// plus enough of ListCandidatesForReview to drive the legacy
// frozen-set backfill's activelearning.Select call.
type fakeArticles struct{ byID map[string]article.Article }

func newFakeArticles(ids ...string) *fakeArticles {
	f := &fakeArticles{byID: map[string]article.Article{}}
	for _, id := range ids {
		f.byID[id] = article.Article{ID: id, Title: "article " + id}
	}
	return f
}

// purge removes an article from the corpus, as retention would.
func (f *fakeArticles) purge(id string) { delete(f.byID, id) }

func (f *fakeArticles) Save(_ context.Context, a article.Article) error {
	f.byID[a.ID] = a
	return nil
}

func (f *fakeArticles) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f.byID[id]
	if !ok {
		return article.Article{}, fmt.Errorf("fakeArticles: %s: %w", id, sql.ErrNoRows)
	}
	return a, nil
}

func (f *fakeArticles) DeleteByStatusOlderThan(context.Context, []article.Status, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeArticles) Count(context.Context) (int, error) { return len(f.byID), nil }

func (f *fakeArticles) ListCandidatesForReview(_ context.Context, _ time.Time, limit int) ([]article.Article, error) {
	ids := make([]string, 0, len(f.byID))
	for id := range f.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids) // the real query orders by fetched_at; any stable order will do here
	pool := make([]article.Article, 0, len(ids))
	for _, id := range ids {
		if limit > 0 && len(pool) == limit {
			break
		}
		pool = append(pool, f.byID[id])
	}
	return pool, nil
}

func (f *fakeArticles) ListByClusterID(context.Context, string, string, int) ([]article.Article, error) {
	return nil, nil
}

func (f *fakeArticles) FindByCanonicalURL(context.Context, string) (article.Article, error) {
	return article.Article{}, nil
}

// fakeEvents is an empty out.DecisionRepository: activelearning.Select
// asks it for each candidate's prior editorial-classifier event, and
// "no prior event" is a perfectly good answer for these tests, which
// care about which candidates get frozen, not how they score.
type fakeEvents struct{}

func (fakeEvents) Append(context.Context, out.Event) error { return nil }
func (fakeEvents) ListByArticle(context.Context, string) ([]out.Event, error) {
	return nil, nil
}

func (fakeEvents) ListByEventTypesSince(context.Context, []string, time.Time, int) ([]out.Event, error) {
	return nil, nil
}
func (fakeEvents) DeleteOlderThan(context.Context, time.Time) (int, error) { return 0, nil }

// fakeCandidates is an in-memory out.EditorialCycleSessionCandidateRepository.
// It models the two SQL joins the real adapter does rather than storing
// their results: a row is visible only while its article is still in the
// corpus (article_id's ON DELETE CASCADE, which deletes the row outright
// — which is exactly why ListFrozen and CountResolvable can never
// disagree, and why the all-purged guard has to key off
// EditorialCycleSession.TotalCandidates instead), and it is pending only
// while no decision names it.
type fakeCandidates struct {
	bySession map[string][]out.EditorialCycleSessionCandidate
	articles  *fakeArticles
	decisions *fakeReviewDecisions
}

func newFakeCandidates(articles *fakeArticles, decisions *fakeReviewDecisions) *fakeCandidates {
	return &fakeCandidates{
		bySession: map[string][]out.EditorialCycleSessionCandidate{},
		articles:  articles, decisions: decisions,
	}
}

func (f *fakeCandidates) SaveAll(_ context.Context, sessionID string, candidates []out.EditorialCycleSessionCandidate) error {
	f.bySession[sessionID] = append(f.bySession[sessionID], candidates...)
	return nil
}

// resolvable is the frozen set as storage would still hold it: rows
// whose article survives.
func (f *fakeCandidates) resolvable(sessionID string) []out.EditorialCycleSessionCandidate {
	var live []out.EditorialCycleSessionCandidate
	for _, c := range f.bySession[sessionID] {
		if _, ok := f.articles.byID[c.ArticleID]; ok {
			live = append(live, c)
		}
	}
	return live
}

func (f *fakeCandidates) ListFrozen(_ context.Context, sessionID string) ([]out.EditorialCycleSessionCandidate, error) {
	return f.resolvable(sessionID), nil
}

func (f *fakeCandidates) CountResolvable(_ context.Context, sessionID string) (int, error) {
	return len(f.resolvable(sessionID)), nil
}

func (f *fakeCandidates) ListPending(_ context.Context, sessionID string) ([]out.EditorialCycleSessionCandidate, error) {
	var pending []out.EditorialCycleSessionCandidate
	for _, c := range f.resolvable(sessionID) {
		if !f.decided(sessionID, c.ArticleID) {
			pending = append(pending, c)
		}
	}
	return pending, nil
}

func (f *fakeCandidates) decided(sessionID, articleID string) bool {
	for _, d := range f.decisions.saved {
		if d.SessionID == sessionID && d.ArticleID == articleID {
			return true
		}
	}
	return false
}

// testStart is the instant every testEnv's clock starts at.
var testStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// testEnv bundles the fakes behind an editorialcycle.Deps so a test can
// both drive the package and inspect what it actually wrote.
type testEnv struct {
	deps       editorialcycle.Deps
	sessions   *fakeSessions
	candidates *fakeCandidates
	articles   *fakeArticles
	decisions  *fakeReviewDecisions
	now        time.Time
}

// newTestEnv wires a full Deps over an in-memory corpus containing
// corpusIDs.
func newTestEnv(t *testing.T, corpusIDs ...string) *testEnv {
	t.Helper()
	articles := newFakeArticles(corpusIDs...)
	decisions := &fakeReviewDecisions{}
	sessions := newFakeSessions()
	env := &testEnv{
		sessions: sessions, candidates: newFakeCandidates(articles, decisions),
		articles: articles, decisions: decisions, now: testStart,
	}
	env.deps = editorialcycle.Deps{
		Sessions: sessions, Candidates: env.candidates, ReviewDecisions: decisions,
		Labels: &fakeLabels{}, Articles: articles, Events: fakeEvents{},
		UOW: fakeUOW{}, Clock: fakeClock{now: env.now},
	}
	return env
}

// advance moves the clock forward. Session IDs are derived from it, so
// any test that creates two sessions must advance between them, exactly
// as wall-clock time would.
func (e *testEnv) advance(d time.Duration) {
	e.now = e.now.Add(d)
	e.deps.Clock = fakeClock{now: e.now}
}

// candidatesFor builds a descending-score selection over articleIDs, the
// shape activelearning.Select returns and StartOrResume freezes.
func (e *testEnv) candidatesFor(articleIDs ...string) []activelearning.Candidate {
	candidates := make([]activelearning.Candidate, len(articleIDs))
	for i, id := range articleIDs {
		candidates[i] = activelearning.Candidate{
			Article: article.Article{ID: id}, Score: float64(len(articleIDs) - i),
		}
	}
	return candidates
}

// start creates a fresh running session whose frozen set is articleIDs.
func (e *testEnv) start(t *testing.T, articleIDs ...string) out.EditorialCycleSession {
	t.Helper()
	session, resumed, _, err := editorialcycle.StartOrResume(
		context.Background(), e.deps, editorialcycle.Flags{SampleSize: 5}, e.candidatesFor(articleIDs...))
	if err != nil {
		t.Fatalf("StartOrResume() error = %v", err)
	}
	if resumed {
		t.Fatalf("StartOrResume() resumed = true, want a fresh session")
	}
	return session
}

// decide journals a decision for articleID, which is what removes it
// from the session's pending set.
func (e *testEnv) decide(t *testing.T, sessionID, articleID string) {
	t.Helper()
	if err := editorialcycle.ApplyDecision(
		context.Background(), e.deps, sessionID, articleID, reviewdecision.ActionPublish, ""); err != nil {
		t.Fatalf("ApplyDecision(%s) error = %v", articleID, err)
	}
}

// status reads sessionID's currently persisted status straight out of
// the fake repository, so a test can assert on a session the package's
// own getters would no longer return (e.g. a cancelled one).
func (e *testEnv) status(t *testing.T, sessionID string) string {
	t.Helper()
	s, ok := e.sessions.byID[sessionID]
	if !ok {
		t.Fatalf("no session %s in the fake repository", sessionID)
	}
	return s.Status
}

func TestStartOrResume_NoRunningSession_StartsNew(t *testing.T) {
	env := newTestEnv(t)

	session, resumed, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{SampleSize: 25}, nil)
	if err != nil {
		t.Fatalf("StartOrResume() error = %v", err)
	}
	if resumed {
		t.Error("resumed = true, want false when no session is running")
	}
	if session.ID == "" || session.Status != "running" || session.CurrentStep != "review" {
		t.Errorf("session = %+v, unexpected", session)
	}
}

func TestStartOrResume_RunningSessionExists_Resumes(t *testing.T) {
	env := newTestEnv(t, "a1")

	first, _, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{SampleSize: 25}, env.candidatesFor("a1"))
	if err != nil {
		t.Fatalf("StartOrResume() first call error = %v", err)
	}

	second, resumed, backfilled, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{SampleSize: 999}, nil)
	if err != nil {
		t.Fatalf("StartOrResume() second call error = %v", err)
	}
	if !resumed {
		t.Error("resumed = false, want true when a running session already exists")
	}
	if backfilled {
		t.Error("backfilled = true, want false — this session already has a real frozen set, nothing to repair")
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %q, want %q (same session)", second.ID, first.ID)
	}
}

func TestStartOrResume_RepositoryError_Propagates(t *testing.T) {
	deps := editorialcycle.Deps{Sessions: errSessions{}, Clock: fakeClock{now: time.Now()}}
	_, _, _, err := editorialcycle.StartOrResume(context.Background(), deps, editorialcycle.Flags{}, nil)
	if err == nil {
		t.Fatal("StartOrResume() error = nil, want a propagated repository error")
	}
}

// errSessions.GetLatestRunning returns a real (non-sql.ErrNoRows) error, to
// verify StartOrResume distinguishes "no running session" from a genuine
// repository failure and does not paper over the latter by creating a new
// session.
type errSessions struct{}

func (errSessions) Create(context.Context, out.EditorialCycleSession) error { return nil }
func (errSessions) Update(context.Context, out.EditorialCycleSession) error { return nil }
func (errSessions) GetLatestRunning(context.Context) (out.EditorialCycleSession, error) {
	return out.EditorialCycleSession{}, fmt.Errorf("db connection lost")
}

func (errSessions) GetLatestCompleted(context.Context) (out.EditorialCycleSession, error) {
	return out.EditorialCycleSession{}, fmt.Errorf("db connection lost")
}

func (errSessions) GetLatestResumable(context.Context) (out.EditorialCycleSession, error) {
	return out.EditorialCycleSession{}, fmt.Errorf("db connection lost")
}

type fakeReviewDecisions struct{ saved []out.ReviewDecision }

func (f *fakeReviewDecisions) Save(_ context.Context, d out.ReviewDecision) error {
	f.saved = append(f.saved, d)
	return nil
}

func (f *fakeReviewDecisions) ListBySession(_ context.Context, sessionID string) ([]out.ReviewDecision, error) {
	var got []out.ReviewDecision
	for _, d := range f.saved {
		if d.SessionID == sessionID {
			got = append(got, d)
		}
	}
	return got, nil
}

func (f *fakeReviewDecisions) ListRecentByAction(context.Context, string, time.Time) ([]string, error) {
	return nil, nil
}

// fakeLabels is an in-memory stand-in for out.HumanLabelRepository.
// saved holds only the currently *active* example per article_id (mirrors
// ListAll's active-only contract); history holds every revision ever
// written, including superseded ones, for tests that want to assert
// nothing was deleted.
type fakeLabels struct {
	saved   []training.Example
	history []training.Example
}

func (f *fakeLabels) Label(_ context.Context, e training.Example) error {
	for _, s := range f.saved {
		if s.ArticleID == e.ArticleID {
			return fmt.Errorf("fakeLabels: %s already has an active label: %w", e.ArticleID, training.ErrAlreadyLabeled)
		}
	}
	e.Revision = 1
	f.saved = append(f.saved, e)
	f.history = append(f.history, e)
	return nil
}

func (f *fakeLabels) Relabel(_ context.Context, e training.Example, expectedRevision int) error {
	for i, s := range f.saved {
		if s.ArticleID == e.ArticleID {
			if s.Revision != expectedRevision {
				return fmt.Errorf("fakeLabels: %s: %w", e.ArticleID, training.ErrRevisionConflict)
			}
			e.Revision = s.Revision + 1
			e.Supersedes = s.Revision
			f.saved[i] = e
			f.history = append(f.history, e)
			return nil
		}
	}
	return fmt.Errorf("fakeLabels: %s: %w", e.ArticleID, training.ErrNotLabeled)
}

func (f *fakeLabels) Retract(_ context.Context, articleID string, expectedRevision int) error {
	for i, s := range f.saved {
		if s.ArticleID == articleID {
			if s.Revision != expectedRevision {
				return fmt.Errorf("fakeLabels: %s: %w", articleID, training.ErrRevisionConflict)
			}
			f.saved = append(f.saved[:i], f.saved[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("fakeLabels: %s: %w", articleID, training.ErrNotLabeled)
}

func (f *fakeLabels) GetActive(_ context.Context, articleID string) (training.Example, bool, error) {
	for _, s := range f.saved {
		if s.ArticleID == articleID {
			return s, true, nil
		}
	}
	return training.Example{}, false, nil
}

func (f *fakeLabels) ListByArticle(context.Context, string) ([]training.Example, error) {
	return nil, nil
}
func (f *fakeLabels) ListAll(context.Context) ([]training.Example, error) { return f.saved, nil }

// ListEditorialExamples, CountEditorialExamples, and the conflict methods
// are no-ops: this package's session_test.go tests exercise
// ApplyDecision/StartOrResume, none of which read them (only
// RunTraining's fakeLabelRepo, in training_test.go, needs
// ListEditorialExamples/CountEditorialExamples to do anything).
func (f *fakeLabels) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return nil, nil
}
func (f *fakeLabels) CountEditorialExamples(context.Context) (int, error) { return 0, nil }
func (f *fakeLabels) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}

func (f *fakeLabels) CountUnresolvedConflicts(context.Context) (int, error)           { return 0, nil }
func (f *fakeLabels) ResolveConflict(context.Context, string, training.Example) error { return nil }

type fakeUOW struct{}

func (fakeUOW) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

// failingUOW simulates a transaction whose fn returns an error partway
// through — used to verify ApplyDecision propagates the failure rather
// than swallowing it.
type failingUOW struct{}

func (failingUOW) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// failingLabels always errors on the write path, to verify a failure in
// the human_labels write inside the transaction propagates as an error
// from ApplyDecision.
type failingLabels struct{}

func (failingLabels) Label(context.Context, training.Example) error { return errors.New("boom") }

func (failingLabels) Relabel(context.Context, training.Example, int) error { return errors.New("boom") }

func (failingLabels) Retract(context.Context, string, int) error { return errors.New("boom") }

func (failingLabels) GetActive(context.Context, string) (training.Example, bool, error) {
	return training.Example{}, false, nil
}

func (failingLabels) ListByArticle(context.Context, string) ([]training.Example, error) {
	return nil, nil
}
func (failingLabels) ListAll(context.Context) ([]training.Example, error) { return nil, nil }
func (failingLabels) ListEditorialExamples(context.Context) ([]training.EditorialExample, error) {
	return nil, nil
}
func (failingLabels) CountEditorialExamples(context.Context) (int, error) { return 0, nil }
func (failingLabels) ListUnresolvedConflicts(context.Context) ([]training.Conflict, error) {
	return nil, nil
}

func (failingLabels) CountUnresolvedConflicts(context.Context) (int, error)           { return 0, nil }
func (failingLabels) ResolveConflict(context.Context, string, training.Example) error { return nil }

func TestApplyDecision_PublishWritesThroughToHumanLabels(t *testing.T) {
	reviewDecisions := &fakeReviewDecisions{}
	labels := &fakeLabels{}
	deps := editorialcycle.Deps{
		ReviewDecisions: reviewDecisions, Labels: labels, UOW: fakeUOW{},
		Clock: fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionPublish, "looks good")
	if err != nil {
		t.Fatalf("ApplyDecision() error = %v", err)
	}
	if len(reviewDecisions.saved) != 1 || reviewDecisions.saved[0].Action != "publish" {
		t.Fatalf("reviewDecisions.saved = %+v, want one \"publish\" decision", reviewDecisions.saved)
	}
	if len(labels.saved) != 1 || labels.saved[0].Label != "publish" || labels.saved[0].ArticleID != "a1" {
		t.Fatalf("labels.saved = %+v, want one publish label for a1", labels.saved)
	}
}

func TestApplyDecision_AmbiguousDoesNotWriteThroughToHumanLabels(t *testing.T) {
	reviewDecisions := &fakeReviewDecisions{}
	labels := &fakeLabels{}
	deps := editorialcycle.Deps{
		ReviewDecisions: reviewDecisions, Labels: labels, UOW: fakeUOW{},
		Clock: fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionAmbiguous, "")
	if err != nil {
		t.Fatalf("ApplyDecision() error = %v", err)
	}
	if len(reviewDecisions.saved) != 1 {
		t.Fatalf("reviewDecisions.saved = %+v, want one journal entry even for ambiguous", reviewDecisions.saved)
	}
	if len(labels.saved) != 0 {
		t.Fatalf("labels.saved = %+v, want no human_labels write for ambiguous", labels.saved)
	}
}

// TestApplyDecision_RedecidingSameArticle_ReplacesPriorHumanLabel reproduces
// the "back" + re-decide bug: a reviewer decides article a1 (publish), then
// — because "back" leaves a1 in the in-memory pending slice for the rest of
// the session — decides it again with a different action (reject). Only
// one human_labels row must survive, holding the latest decision, not two
// contradictory rows.
func TestApplyDecision_RedecidingSameArticle_ReplacesPriorHumanLabel(t *testing.T) {
	reviewDecisions := &fakeReviewDecisions{}
	labels := &fakeLabels{}
	deps := editorialcycle.Deps{
		ReviewDecisions: reviewDecisions, Labels: labels, UOW: fakeUOW{},
		Clock: fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	if err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionPublish, ""); err != nil {
		t.Fatalf("ApplyDecision(publish) error = %v", err)
	}
	// Simulate "back" then re-deciding the same article differently.
	if err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionReject, ""); err != nil {
		t.Fatalf("ApplyDecision(reject) error = %v", err)
	}

	if len(labels.saved) != 1 {
		t.Fatalf("labels.saved = %+v, want exactly one human_labels row after re-deciding the same article", labels.saved)
	}
	if labels.saved[0].Label != "reject" {
		t.Errorf("labels.saved[0].Label = %q, want %q (the latest decision)", labels.saved[0].Label, "reject")
	}
}

// TestApplyDecision_RedecidingSameArticle_NonPersistingActionClearsPriorHumanLabel
// reproduces the residual bug found in re-review: a reviewer decides
// article a1 (publish, which persists a human_labels row), then — via
// "back" — re-decides the same article "ambiguous" (a non-persisting
// outcome). "ambiguous" is meant to be a temporary cooldown,
// not a permanent exclusion, but a stale "publish" row left behind here
// would permanently exclude a1 from ListCandidatesForReview even after the
// ambiguous cooldown lapses. The prior human_labels row must be cleared, and
// since ambiguous itself persists nothing, zero rows must remain.
func TestApplyDecision_RedecidingSameArticle_NonPersistingActionClearsPriorHumanLabel(t *testing.T) {
	reviewDecisions := &fakeReviewDecisions{}
	labels := &fakeLabels{}
	deps := editorialcycle.Deps{
		ReviewDecisions: reviewDecisions, Labels: labels, UOW: fakeUOW{},
		Clock: fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	if err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionPublish, ""); err != nil {
		t.Fatalf("ApplyDecision(publish) error = %v", err)
	}
	// Simulate "back" then re-deciding the same article as ambiguous.
	if err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionAmbiguous, ""); err != nil {
		t.Fatalf("ApplyDecision(ambiguous) error = %v", err)
	}

	if len(labels.saved) != 0 {
		t.Fatalf("labels.saved = %+v, want zero human_labels rows after re-deciding a1 as ambiguous (the stale publish row must be cleared)", labels.saved)
	}
}

// TestApplyDecision_RedecidingSameArticle_PreservesPriorRevisionAsHistory
// proves the fix's actual improvement over the old DeleteByArticle-based
// implementation: re-deciding an article no longer destroys the prior
// decision, it supersedes it.
func TestApplyDecision_RedecidingSameArticle_PreservesPriorRevisionAsHistory(t *testing.T) {
	reviewDecisions := &fakeReviewDecisions{}
	labels := &fakeLabels{}
	deps := editorialcycle.Deps{
		ReviewDecisions: reviewDecisions, Labels: labels, UOW: fakeUOW{},
		Clock: fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	if err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionPublish, ""); err != nil {
		t.Fatalf("ApplyDecision(publish) error = %v", err)
	}
	if err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionReject, ""); err != nil {
		t.Fatalf("ApplyDecision(reject) error = %v", err)
	}

	if len(labels.history) != 2 {
		t.Fatalf("len(labels.history) = %d, want 2 -- the prior \"publish\" decision must survive as superseded history, not be deleted", len(labels.history))
	}
	if labels.history[0].Label != "publish" || labels.history[1].Label != "reject" {
		t.Errorf("labels.history = %+v, want [publish, reject] in order", labels.history)
	}
}

func TestApplyDecision_RejectsUnknownAction(t *testing.T) {
	deps := editorialcycle.Deps{
		ReviewDecisions: &fakeReviewDecisions{}, Labels: &fakeLabels{}, UOW: fakeUOW{},
		Clock: fakeClock{now: time.Now()},
	}
	err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.Action("bogus"), "")
	if err == nil {
		t.Error("ApplyDecision() error = nil, want an error for an unknown action")
	}
}

// TestApplyDecision_TransactionFailure_Propagates verifies both writes
// really happen inside one deps.UOW.Do call: when the second write
// (human_labels) fails, ApplyDecision must surface that error rather than
// reporting success — proving the two Save calls are not just invoked in
// sequence outside a transaction that would otherwise mask a failure.
func TestApplyDecision_TransactionFailure_Propagates(t *testing.T) {
	reviewDecisions := &fakeReviewDecisions{}
	deps := editorialcycle.Deps{
		ReviewDecisions: reviewDecisions, Labels: failingLabels{}, UOW: failingUOW{},
		Clock: fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	err := editorialcycle.ApplyDecision(context.Background(), deps, "sess-1", "a1", reviewdecision.ActionPublish, "")
	if err == nil {
		t.Fatal("ApplyDecision() error = nil, want the human_labels write failure to propagate")
	}
}

func TestComplete_MarksSessionCompleted(t *testing.T) {
	env := newTestEnv(t)
	session, _, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{}, nil)
	if err != nil {
		t.Fatalf("StartOrResume() error = %v", err)
	}

	if err := editorialcycle.Complete(context.Background(), env.deps, session); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if env.sessions.byID[session.ID].Status != "completed" {
		t.Errorf("session status = %q, want %q", env.sessions.byID[session.ID].Status, "completed")
	}
	env.advance(time.Hour)
	if _, _, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{}, nil); err != nil {
		t.Fatalf("StartOrResume() after Complete() error = %v", err)
	}
}

// TestComplete_ThenStartOrResume_StartsANewSession proves
// GetLatestResumable really filters by status: after Complete flips the
// session to "completed", a subsequent StartOrResume must not resume it
// — it must start a brand-new session with a different ID.
func TestComplete_ThenStartOrResume_StartsANewSession(t *testing.T) {
	env := newTestEnv(t)
	first, _, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{}, nil)
	if err != nil {
		t.Fatalf("StartOrResume() error = %v", err)
	}
	if err := editorialcycle.Complete(context.Background(), env.deps, first); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	env.advance(24 * time.Hour)
	second, resumed, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{}, nil)
	if err != nil {
		t.Fatalf("StartOrResume() after Complete() error = %v", err)
	}
	if resumed {
		t.Error("resumed = true, want false: the prior session is completed, not running")
	}
	if second.ID == first.ID {
		t.Errorf("second.ID = %q, want a different ID from the completed session %q", second.ID, first.ID)
	}
}

// --- guarded transitions ---------------------------------
//
// The tests below cover every transition this package owns: creation
// (1), resume of a running/paused/failed session (2, 8), the guarded
// Complete (3), Pause (4-6), Fail (7), and the all-purged cancellation
// (9). Each asserts on what was actually persisted through the fake
// repository, not only on the returned value, since the point of the
// state machine is what the next process finds in the database.

func TestComplete_RefusesWhenCandidatesArePending(t *testing.T) {
	env := newTestEnv(t, "a1", "a2")
	session := env.start(t, "a1", "a2")
	env.decide(t, session.ID, "a1") // a2 left undecided

	err := editorialcycle.Complete(context.Background(), env.deps, session)
	if err == nil {
		t.Fatal("Complete() error = nil, want an error because a2 is still pending")
	}
	if !strings.Contains(err.Error(), "1 candidate(s) still pending") {
		t.Errorf("error = %q, want it to name the pending count", err.Error())
	}
	if got := env.status(t, session.ID); got != "running" {
		t.Errorf("session.Status = %q, want it to remain %q after a refused Complete()", got, "running")
	}
}

func TestComplete_SucceedsWhenNothingPending(t *testing.T) {
	env := newTestEnv(t, "a1")
	session := env.start(t, "a1")
	env.decide(t, session.ID, "a1")

	if err := editorialcycle.Complete(context.Background(), env.deps, session); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	got, err := env.deps.Sessions.GetLatestCompleted(context.Background())
	if err != nil || got.Status != "completed" {
		t.Fatalf("session not marked completed: %+v, err=%v", got, err)
	}
}

// TestComplete_PurgedCandidateDoesNotBlockCompletion pins the design
// spec's §6.3 tradeoff: corpus drift shrinks the frozen set rather than
// deadlocking the session. A candidate whose article was purged while
// the session was open can never be decided, so it must not count as
// pending — otherwise the session could never complete at all.
func TestComplete_PurgedCandidateDoesNotBlockCompletion(t *testing.T) {
	env := newTestEnv(t, "a1", "a2")
	session := env.start(t, "a1", "a2")
	env.decide(t, session.ID, "a1")
	env.articles.purge("a2")

	if err := editorialcycle.Complete(context.Background(), env.deps, session); err != nil {
		t.Fatalf("Complete() error = %v, want a purged candidate not to block completion", err)
	}
	if got := env.status(t, session.ID); got != "completed" {
		t.Errorf("session.Status = %q, want %q", got, "completed")
	}
}

func TestPause_SetsStatusPaused(t *testing.T) {
	env := newTestEnv(t, "a1")
	session := env.start(t, "a1")

	if err := editorialcycle.Pause(context.Background(), env.deps, session, "eof with pending"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	got, err := env.deps.Sessions.GetLatestResumable(context.Background())
	if err != nil || got.Status != "paused" {
		t.Fatalf("session not paused: %+v, err=%v", got, err)
	}
	if got.ID != session.ID {
		t.Errorf("resumable session ID = %q, want the paused session %q", got.ID, session.ID)
	}
}

func TestFail_SetsStatusFailed(t *testing.T) {
	env := newTestEnv(t, "a1")
	session := env.start(t, "a1")

	if err := editorialcycle.Fail(context.Background(), env.deps, session, fmt.Errorf("boom")); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	got, err := env.deps.Sessions.GetLatestResumable(context.Background())
	if err != nil || got.Status != "failed" {
		t.Fatalf("session not failed: %+v, err=%v", got, err)
	}
}

func TestStartOrResume_CreatesFrozenSetOnlyOnce(t *testing.T) {
	env := newTestEnv(t, "a1", "a2", "a3")
	session, resumed, _, err := editorialcycle.StartOrResume(
		context.Background(), env.deps, editorialcycle.Flags{SampleSize: 5}, env.candidatesFor("a1", "a2"))
	if err != nil || resumed {
		t.Fatalf("StartOrResume() = %+v, %v, %v, want a fresh (not resumed) session", session, resumed, err)
	}
	frozen, err := env.candidates.ListFrozen(context.Background(), session.ID)
	if err != nil || len(frozen) != 2 {
		t.Fatalf("ListFrozen() = %+v, err=%v, want 2 frozen candidates", frozen, err)
	}
	if session.TotalCandidates != 2 {
		t.Errorf("session.TotalCandidates = %d, want 2 (the drift baseline, stamped once at creation)", session.TotalCandidates)
	}

	// Resume must NOT touch the frozen set even if different candidates
	// are passed in — that is what "frozen" means.
	session2, resumed2, _, err := editorialcycle.StartOrResume(
		context.Background(), env.deps, editorialcycle.Flags{SampleSize: 5}, env.candidatesFor("a3"))
	if err != nil || !resumed2 || session2.ID != session.ID {
		t.Fatalf("StartOrResume() (resume) = %+v, %v, %v, want the same session resumed", session2, resumed2, err)
	}
	frozen2, _ := env.candidates.ListFrozen(context.Background(), session.ID)
	if len(frozen2) != 2 {
		t.Fatalf("ListFrozen() after resume = %+v, want still 2 (the frozen set must not change on resume)", frozen2)
	}
	for _, c := range frozen2 {
		if c.ArticleID == "a3" {
			t.Errorf("ListFrozen() after resume contains %q, want only the originally frozen a1/a2", c.ArticleID)
		}
	}
	if session2.TotalCandidates != 2 {
		t.Errorf("resumed session.TotalCandidates = %d, want the original 2", session2.TotalCandidates)
	}
}

func TestStartOrResume_ResumingPausedOrFailed_FlipsToRunning(t *testing.T) {
	stops := []struct {
		name string
		stop func(*testEnv, out.EditorialCycleSession) error
	}{
		{"paused", func(e *testEnv, s out.EditorialCycleSession) error {
			return editorialcycle.Pause(context.Background(), e.deps, s, "w")
		}},
		{"failed", func(e *testEnv, s out.EditorialCycleSession) error {
			return editorialcycle.Fail(context.Background(), e.deps, s, fmt.Errorf("boom"))
		}},
	}
	for _, tc := range stops {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, "a1")
			session := env.start(t, "a1")
			if err := tc.stop(env, session); err != nil {
				t.Fatalf("stopping the session error = %v", err)
			}

			resumedSession, resumed, _, err := editorialcycle.StartOrResume(
				context.Background(), env.deps, editorialcycle.Flags{}, nil)
			if err != nil || !resumed || resumedSession.Status != "running" {
				t.Fatalf("StartOrResume() = %+v, %v, %v, want the %s session picked up and flipped to running",
					resumedSession, resumed, err, tc.name)
			}
			if resumedSession.ID != session.ID {
				t.Errorf("resumed ID = %q, want the interrupted session %q", resumedSession.ID, session.ID)
			}
			if got := env.status(t, session.ID); got != "running" {
				t.Errorf("persisted status = %q, want %q — the flip must be durable, not merely returned", got, "running")
			}
		})
	}
}

// TestStartOrResume_AllFrozenCandidatesPurged_Cancels covers transition
// 9. The trigger is TotalCandidates > 0 with nothing resolvable left,
// not a ListFrozen/CountResolvable disagreement: article_id cascades, so
// a purge deletes the frozen row outright and those two counts always
// agree. TotalCandidates is the only surviving record that there was
// ever anything to review.
func TestStartOrResume_AllFrozenCandidatesPurged_Cancels(t *testing.T) {
	env := newTestEnv(t, "gone1")
	session := env.start(t, "gone1")
	if err := editorialcycle.Pause(context.Background(), env.deps, session, "w"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	env.articles.purge("gone1")
	env.advance(time.Hour) // session IDs are clock-derived; the fresh one needs its own

	fresh, resumed, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{}, nil)
	if err != nil {
		t.Fatalf("StartOrResume() error = %v", err)
	}
	if resumed {
		t.Error("resumed = true, want a fresh session to have been started instead, since the old one is now cancelled")
	}
	if fresh.ID == session.ID {
		t.Errorf("fresh.ID = %q, want a different session from the cancelled %q", fresh.ID, session.ID)
	}
	if got := env.status(t, session.ID); got != "cancelled" {
		t.Errorf("old session status = %q, want %q", got, "cancelled")
	}
}

// TestStartOrResume_PartiallyPurgedFrozenSet_StillResumes is transition
// 9's negative case: drift that leaves at least one reviewable candidate
// is a warning (Task 6's Plan reports it by comparing the surviving
// count against TotalCandidates), never a cancellation.
func TestStartOrResume_PartiallyPurgedFrozenSet_StillResumes(t *testing.T) {
	env := newTestEnv(t, "a1", "a2")
	session := env.start(t, "a1", "a2")
	if err := editorialcycle.Pause(context.Background(), env.deps, session, "w"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	env.articles.purge("a1")

	resumedSession, resumed, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{}, nil)
	if err != nil || !resumed || resumedSession.ID != session.ID {
		t.Fatalf("StartOrResume() = %+v, %v, %v, want the partially-drifted session resumed", resumedSession, resumed, err)
	}
	if got := env.status(t, session.ID); got != "running" {
		t.Errorf("session status = %q, want %q — one surviving candidate is enough to resume", got, "running")
	}
	if resumedSession.TotalCandidates != 2 {
		t.Errorf("TotalCandidates = %d, want the original 2 preserved as the drift baseline", resumedSession.TotalCandidates)
	}
	resolvable, err := env.candidates.CountResolvable(context.Background(), session.ID)
	if err != nil || resolvable != 1 {
		t.Errorf("CountResolvable() = %d, err=%v, want 1 (a2 survived, a1 cascaded away)", resolvable, err)
	}
}

// TestStartOrResume_LegacySessionWithoutFrozenSet_BackfillsIt covers the
// design spec's §11 repair: a session predating migration 0024's
// candidate table has no frozen set and no TotalCandidates baseline, so
// resuming it reselects one best-effort from the session's own stored
// flags and stamps the baseline at the same time.
func TestStartOrResume_LegacySessionWithoutFrozenSet_BackfillsIt(t *testing.T) {
	tests := []struct {
		name       string
		flagsJSON  string
		wantFrozen int
	}{
		{"stored sample size", `{"sample_size":2}`, 2},
		// A pre-flags row (flags_json defaulted to '{}' under migration
		// 0012) reselects with the package's fallback sample size, which
		// exceeds this corpus — so the whole corpus comes back. What
		// matters is that it is not zero: a legacy session must not be
		// repaired into an empty set and then completed as though it had
		// been reviewed.
		{"no stored sample size", `{}`, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, "c1", "c2", "c3")
			legacy := out.EditorialCycleSession{
				ID: "legacy-1", StartedAt: testStart, UpdatedAt: testStart,
				Status: "paused", CurrentStep: "review", FlagsJSON: tc.flagsJSON,
			}
			env.sessions.byID[legacy.ID] = legacy

			resumedSession, resumed, backfilled, err := editorialcycle.StartOrResume(
				context.Background(), env.deps, editorialcycle.Flags{}, nil)
			if err != nil || !resumed || resumedSession.ID != legacy.ID {
				t.Fatalf("StartOrResume() = %+v, %v, %v, want the legacy session resumed", resumedSession, resumed, err)
			}
			if !backfilled {
				// The CLI layer (cmd/editorialcycle.go) prints the design
				// spec §11.3 legacy-backfill warning only when this is
				// true — a false negative here would silently drop that
				// warning for a real operator.
				t.Error("backfilled = false, want true — this resume took the legacy-frozen-set repair path")
			}
			frozen, err := env.candidates.ListFrozen(context.Background(), legacy.ID)
			if err != nil || len(frozen) != tc.wantFrozen {
				t.Fatalf("ListFrozen() = %+v, err=%v, want %d backfilled candidates", frozen, err, tc.wantFrozen)
			}
			if resumedSession.TotalCandidates != tc.wantFrozen {
				t.Errorf("TotalCandidates = %d, want %d", resumedSession.TotalCandidates, tc.wantFrozen)
			}
			if got := env.sessions.byID[legacy.ID].TotalCandidates; got != tc.wantFrozen {
				t.Errorf("persisted TotalCandidates = %d, want %d — the repair must survive the status flip", got, tc.wantFrozen)
			}
			if got := env.status(t, legacy.ID); got != "running" {
				t.Errorf("legacy session status = %q, want %q", got, "running")
			}
		})
	}
}

// TestStartOrResume_LegacyBackfill_RunsOnlyOnce proves the repair is not
// re-applied on every resume: once a frozen set exists it is frozen,
// exactly like one written at creation.
func TestStartOrResume_LegacyBackfill_RunsOnlyOnce(t *testing.T) {
	env := newTestEnv(t, "c1", "c2", "c3")
	env.sessions.byID["legacy-1"] = out.EditorialCycleSession{
		ID: "legacy-1", StartedAt: testStart, UpdatedAt: testStart,
		Status: "paused", CurrentStep: "review", FlagsJSON: `{"sample_size":2}`,
	}
	if _, _, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{}, nil); err != nil {
		t.Fatalf("StartOrResume() first resume error = %v", err)
	}
	if err := editorialcycle.Pause(context.Background(), env.deps, env.sessions.byID["legacy-1"], "w"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}

	env.advance(time.Hour)
	if _, _, _, err := editorialcycle.StartOrResume(context.Background(), env.deps, editorialcycle.Flags{}, nil); err != nil {
		t.Fatalf("StartOrResume() second resume error = %v", err)
	}
	frozen, _ := env.candidates.ListFrozen(context.Background(), "legacy-1")
	if len(frozen) != 2 {
		t.Errorf("ListFrozen() = %+v (%d rows), want still 2 — the backfill must not run again", frozen, len(frozen))
	}
}

func TestCandidatesFromFrozen_KeepsIDAndScoreOnly(t *testing.T) {
	got := editorialcycle.CandidatesFromFrozen([]out.EditorialCycleSessionCandidate{
		{SessionID: "s1", ArticleID: "a1", Score: 2.5},
		{SessionID: "s1", ArticleID: "a2", Score: 1.5},
	})
	if len(got) != 2 {
		t.Fatalf("CandidatesFromFrozen() = %+v, want 2 candidates", got)
	}
	if got[0].Article.ID != "a1" || got[0].Score != 2.5 || got[1].Article.ID != "a2" {
		t.Errorf("CandidatesFromFrozen() = %+v, want the article IDs and scores carried across in order", got)
	}
}
