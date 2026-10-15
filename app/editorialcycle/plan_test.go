package editorialcycle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// countingSessions wraps an out.EditorialCycleSessionRepository (the same
// *fakeSessions session_test.go's testEnv already builds) and counts calls
// to its two write methods, so TestBuildPlan_NeverWrites has a mechanical
// proof of BuildPlan's read-only contract rather than an assertion on the
// value it returns.
type countingSessions struct {
	out.EditorialCycleSessionRepository
	creates, updates int
}

func (c *countingSessions) Create(ctx context.Context, s out.EditorialCycleSession) error {
	c.creates++
	return c.EditorialCycleSessionRepository.Create(ctx, s)
}

func (c *countingSessions) Update(ctx context.Context, s out.EditorialCycleSession) error {
	c.updates++
	return c.EditorialCycleSessionRepository.Update(ctx, s)
}

// countingCandidates does the same for
// out.EditorialCycleSessionCandidateRepository's one write method,
// SaveAll.
type countingCandidates struct {
	out.EditorialCycleSessionCandidateRepository
	saveAllCalls int
}

func (c *countingCandidates) SaveAll(ctx context.Context, sessionID string, candidates []out.EditorialCycleSessionCandidate) error {
	c.saveAllCalls++
	return c.EditorialCycleSessionCandidateRepository.SaveAll(ctx, sessionID, candidates)
}

func TestBuildPlan_NoResumableSession_PreviewsFreshSelectionWithHydratedTitles(t *testing.T) {
	te := newTestEnv(t, "a1", "a2")

	plan, err := editorialcycle.BuildPlan(context.Background(), te.deps, editorialcycle.Flags{SampleSize: 5}, nil)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if plan.ResumingSessionID != "" || !plan.IsPreview {
		t.Fatalf("plan = %+v, want a preview (no resumable session)", plan)
	}
	if len(plan.Pending) == 0 {
		t.Fatal("plan.Pending is empty, want the preview candidates")
	}
	for _, c := range plan.Pending {
		if c.Article.Title == "" {
			t.Errorf("plan.Pending candidate %+v has no hydrated title", c)
		}
	}
	if plan.DriftWarning != "" {
		t.Errorf("plan.DriftWarning = %q, want empty for a preview", plan.DriftWarning)
	}
}

func TestBuildPlan_ResumableSessionExists_ReportsFrozenPendingOnlyWithHydratedTitles(t *testing.T) {
	te := newTestEnv(t, "f1", "f2")
	session := te.start(t, "f1", "f2")
	te.decide(t, session.ID, "f1")
	if err := editorialcycle.Pause(context.Background(), te.deps, session, "w"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}

	plan, err := editorialcycle.BuildPlan(context.Background(), te.deps, editorialcycle.Flags{SampleSize: 5}, nil)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if plan.ResumingSessionID != session.ID || plan.IsPreview {
		t.Fatalf("plan = %+v, want it to report resuming %s, not a preview", plan, session.ID)
	}
	if len(plan.Pending) != 1 || plan.Pending[0].Article.ID != "f2" {
		t.Fatalf("plan.Pending = %+v, want only f2 (f1 already decided)", plan.Pending)
	}
	if plan.Pending[0].Article.Title == "" {
		t.Error("plan.Pending[0].Article.Title is empty, want the hydrated article title")
	}
	if plan.DriftWarning != "" {
		t.Errorf("plan.DriftWarning = %q, want empty: nothing was purged", plan.DriftWarning)
	}
}

// TestBuildPlan_ResumableSessionWithDrift_ReportsDriftWarningUsingTotalCandidates
// pins the corrected computation: DriftWarning must compare CountResolvable
// against session.TotalCandidates, not against len(ListFrozen(...)) — the
// latter can never differ from CountResolvable, because a purged article's
// frozen-set row cascades away outright rather than becoming orphaned.
func TestBuildPlan_ResumableSessionWithDrift_ReportsDriftWarningUsingTotalCandidates(t *testing.T) {
	te := newTestEnv(t, "d1", "d2", "d3")
	session := te.start(t, "d1", "d2", "d3")
	if session.TotalCandidates != 3 {
		t.Fatalf("session.TotalCandidates = %d, want 3", session.TotalCandidates)
	}
	if err := editorialcycle.Pause(context.Background(), te.deps, session, "w"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	te.articles.purge("d2")

	plan, err := editorialcycle.BuildPlan(context.Background(), te.deps, editorialcycle.Flags{SampleSize: 5}, nil)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if plan.ResumingSessionID != session.ID {
		t.Fatalf("plan.ResumingSessionID = %q, want %q", plan.ResumingSessionID, session.ID)
	}
	if plan.DriftWarning == "" {
		t.Fatal("plan.DriftWarning is empty, want a warning: 1 of 3 originally-frozen candidates was purged")
	}
	if !strings.Contains(plan.DriftWarning, "1 of 3") {
		t.Errorf("plan.DriftWarning = %q, want it to name 1 of 3 originally-frozen candidates", plan.DriftWarning)
	}
	// The purged candidate must not appear in Pending, but the two
	// surviving ones must.
	if len(plan.Pending) != 2 {
		t.Fatalf("plan.Pending = %+v, want 2 surviving candidates (d1, d3)", plan.Pending)
	}
	for _, c := range plan.Pending {
		if c.Article.ID == "d2" {
			t.Errorf("plan.Pending contains purged article d2: %+v", plan.Pending)
		}
	}
}

func TestBuildPlan_PassesThroughPendingMigrations(t *testing.T) {
	te := newTestEnv(t, "a1")

	plan, err := editorialcycle.BuildPlan(context.Background(), te.deps, editorialcycle.Flags{SampleSize: 5}, []string{"0099_future.sql"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.PendingMigrations) != 1 || plan.PendingMigrations[0] != "0099_future.sql" {
		t.Errorf("plan.PendingMigrations = %v, want [0099_future.sql]", plan.PendingMigrations)
	}
}

// TestBuildPlan_NeverWrites is the mechanical proof of BuildPlan's
// read-only contract: it wraps the session and candidate repositories
// with call counters (rather than only inspecting the returned Plan
// value) and runs BuildPlan once with a resumable session in play — the
// branch with the most write-shaped calls available to accidentally
// invoke (GetLatestResumable, ListFrozen, CountResolvable, ListPending,
// Articles.FindByID) — plus once more with no resumable session, to cover
// the preview branch's activelearning.Select path too.
func TestBuildPlan_NeverWrites(t *testing.T) {
	te := newTestEnv(t, "w1", "w2")
	session := te.start(t, "w1", "w2")
	te.decide(t, session.ID, "w1")
	if err := editorialcycle.Pause(context.Background(), te.deps, session, "w"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}

	cs := &countingSessions{EditorialCycleSessionRepository: te.deps.Sessions}
	cc := &countingCandidates{EditorialCycleSessionCandidateRepository: te.deps.Candidates}
	deps := te.deps
	deps.Sessions = cs
	deps.Candidates = cc

	sessionRowsBefore := len(te.sessions.byID)
	frozenBefore, err := te.candidates.ListFrozen(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("ListFrozen() (baseline) error = %v", err)
	}
	candidateRowsBefore := len(frozenBefore)

	if _, err := editorialcycle.BuildPlan(context.Background(), deps, editorialcycle.Flags{SampleSize: 5}, []string{"0099_future.sql"}); err != nil {
		t.Fatalf("BuildPlan() (resuming branch) error = %v", err)
	}

	// Also exercise the preview branch (no resumable session) with its own
	// counters, since it calls a disjoint set of methods.
	te2 := newTestEnv(t, "p1", "p2")
	cs2 := &countingSessions{EditorialCycleSessionRepository: te2.deps.Sessions}
	cc2 := &countingCandidates{EditorialCycleSessionCandidateRepository: te2.deps.Candidates}
	deps2 := te2.deps
	deps2.Sessions = cs2
	deps2.Candidates = cc2
	if _, err := editorialcycle.BuildPlan(context.Background(), deps2, editorialcycle.Flags{SampleSize: 5}, nil); err != nil {
		t.Fatalf("BuildPlan() (preview branch) error = %v", err)
	}

	if cs.creates != 0 || cs.updates != 0 {
		t.Errorf("resuming branch: session repository Create/Update calls = %d/%d, want 0/0", cs.creates, cs.updates)
	}
	if cc.saveAllCalls != 0 {
		t.Errorf("resuming branch: candidate repository SaveAll calls = %d, want 0", cc.saveAllCalls)
	}
	if cs2.creates != 0 || cs2.updates != 0 {
		t.Errorf("preview branch: session repository Create/Update calls = %d/%d, want 0/0", cs2.creates, cs2.updates)
	}
	if cc2.saveAllCalls != 0 {
		t.Errorf("preview branch: candidate repository SaveAll calls = %d, want 0", cc2.saveAllCalls)
	}

	if got := len(te.sessions.byID); got != sessionRowsBefore {
		t.Errorf("session repository row count = %d, want unchanged %d", got, sessionRowsBefore)
	}
	frozenAfter, err := te.candidates.ListFrozen(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("ListFrozen() (after) error = %v", err)
	}
	if got := len(frozenAfter); got != candidateRowsBefore {
		t.Errorf("candidate repository frozen row count for %s = %d, want unchanged %d", session.ID, got, candidateRowsBefore)
	}
}
