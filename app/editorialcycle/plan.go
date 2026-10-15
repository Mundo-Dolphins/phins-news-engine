package editorialcycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/app/activelearning"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// previewWindowDays bounds the corpus a preview (no resumable session)
// selects from, mirroring the window a real run and the legacy backfill
// both use (see legacyBackfillWindowDays).
const previewWindowDays = 30

// Plan is editorial cycle's --dry-run output — computed with zero writes.
// Every field is derived from
// read-only queries: BuildPlan never calls Sessions.Create/Update,
// Candidates.SaveAll, ReviewDecisions.Save, or Labels.Label/Relabel/Retract.
type Plan struct {
	// ResumingSessionID is the session BuildPlan found via
	// GetLatestResumable, or "" if a real (non-dry-run) invocation would
	// start a fresh one instead.
	ResumingSessionID string
	// Pending is the real frozen-set pending list (ListPending, hydrated
	// with each article's current data) when ResumingSessionID != "", or a
	// live activelearning.Select preview otherwise.
	Pending []activelearning.Candidate
	// IsPreview is true only when ResumingSessionID == "" — nothing is
	// frozen yet, so Pending is a preview of what a fresh session would
	// freeze, not an actual frozen set (see the design spec's §6.1).
	IsPreview bool
	// DriftWarning is non-empty when resuming and some of the resuming
	// session's originally-frozen candidates are no longer resolvable
	// against the corpus (purged by retention or otherwise deleted).
	//
	// This is computed as CountResolvable < session.TotalCandidates, not
	// as CountResolvable < len(ListFrozen(...)) — those two would always
	// agree, because editorial_cycle_session_candidates.article_id is ON
	// DELETE CASCADE: a purged article's frozen-set row is deleted
	// outright, not orphaned, so ListFrozen already only ever returns
	// resolvable rows. TotalCandidates (stamped once, at session creation
	// or legacy backfill — see out.EditorialCycleSession.TotalCandidates)
	// is the only durable record of how large the frozen set originally
	// was, which is what a drift warning needs to compare against.
	DriftWarning string
	// PendingMigrations is passed through unchanged from the caller
	// (infrasqlite.PendingMigrations) — BuildPlan itself has no database
	// handle of its own, only the repository ports, matching this
	// package's existing dependency shape.
	PendingMigrations []string
}

// BuildPlan computes what a real (non-dry-run) invocation of `editorial
// cycle` would do, without writing anything. It is purely a read: it
// calls Sessions.GetLatestResumable and, if a session is found,
// Candidates.ListFrozen/CountResolvable/ListPending plus Articles.FindByID
// to hydrate each pending row's article; if no resumable session exists,
// it falls back to a live activelearning.Select preview (itself
// read-only). BuildPlan must never call Sessions.Create/Update,
// Candidates.SaveAll, ReviewDecisions.Save, or Labels.Label/Relabel/Retract — none of
// StartOrResume's cancel/backfill repair logic runs here; see
// StartOrResume's doc comment for that.
func BuildPlan(ctx context.Context, deps Deps, flags Flags, pendingMigrations []string) (Plan, error) {
	plan := Plan{PendingMigrations: pendingMigrations}

	existing, err := deps.Sessions.GetLatestResumable(ctx)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return Plan{}, fmt.Errorf("editorialcycle: plan: get latest resumable session: %w", err)
		}
		return buildPreviewPlan(ctx, deps, flags, plan)
	}

	plan.ResumingSessionID = existing.ID

	resolvable, err := deps.Candidates.CountResolvable(ctx, existing.ID)
	if err != nil {
		return Plan{}, fmt.Errorf("editorialcycle: plan: count resolvable candidates for %s: %w", existing.ID, err)
	}
	if resolvable < existing.TotalCandidates {
		plan.DriftWarning = fmt.Sprintf(
			"%d of %d originally-frozen candidate(s) are no longer in the corpus",
			existing.TotalCandidates-resolvable, existing.TotalCandidates)
	}

	pending, err := deps.Candidates.ListPending(ctx, existing.ID)
	if err != nil {
		return Plan{}, fmt.Errorf("editorialcycle: plan: list pending candidates for %s: %w", existing.ID, err)
	}
	hydrated, err := hydrateCandidates(ctx, deps, pending)
	if err != nil {
		return Plan{}, err
	}
	plan.Pending = manualReviewCandidates(hydrated, deps.ExcludedSourceIDs)
	return plan, nil
}

// buildPreviewPlan fills in the no-resumable-session branch of BuildPlan:
// a live, read-only activelearning.Select preview of what a fresh
// session would freeze. Split out only to keep BuildPlan's two branches
// readable — this is not called anywhere else.
func buildPreviewPlan(ctx context.Context, deps Deps, flags Flags, plan Plan) (Plan, error) {
	plan.IsPreview = true
	preview, err := activelearning.Select(ctx, deps.activeLearningDeps(), activelearning.Options{
		Since:      deps.Clock.Now().AddDate(0, 0, -previewWindowDays),
		PoolLimit:  flags.SampleSize * 10,
		SampleSize: flags.SampleSize,
	})
	if err != nil {
		return Plan{}, fmt.Errorf("editorialcycle: plan: preview selection: %w", err)
	}
	plan.Pending = preview
	return plan, nil
}

// hydrateCandidates adapts persisted frozen-set rows into
// activelearning.Candidate shape, then fills in each Article beyond the
// ID/Score that CandidatesFromFrozen alone carries (see its doc comment)
// by fetching it via Articles.FindByID — one lookup per row, bounded by
// however many rows ListPending returned for one session, which is cheap
// (at most the session's original SampleSize). This is what lets
// BuildPlan's caller show a real title instead of a bare article ID; it
// belongs here rather than in CandidatesFromFrozen because that function
// stays a pure struct conversion with no I/O.
func hydrateCandidates(ctx context.Context, deps Deps, rows []out.EditorialCycleSessionCandidate) ([]activelearning.Candidate, error) {
	candidates := CandidatesFromFrozen(rows)
	for i, row := range rows {
		a, err := deps.Articles.FindByID(ctx, row.ArticleID)
		if err != nil {
			return nil, fmt.Errorf("editorialcycle: plan: hydrate article %s: %w", row.ArticleID, err)
		}
		candidates[i].Article = a
	}
	return candidates, nil
}
