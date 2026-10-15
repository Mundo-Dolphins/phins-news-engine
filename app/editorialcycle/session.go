// Package editorialcycle orchestrates the `editorial cycle` command's
// resumable session: creating or resuming a session record, freezing the
// candidate set that session reviews, applying each human review
// decision transactionally, and closing the session out through exactly
// one of the guarded status transitions.
package editorialcycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/activelearning"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewdecision"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// StepReview is the only step this plan implements; later Phase 9 plans
// add more (StepDictionary, StepTrain, StepDeploy, ...) to the same
// session record's CurrentStep field.
const StepReview = "review"

// The five persisted session statuses. They are unexported
// because nothing outside this package may write a status: every
// transition goes through StartOrResume/Complete/Pause/Fail below, which
// is the whole point of the state machine.
const (
	statusRunning   = "running"
	statusPaused    = "paused"
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusCancelled = "cancelled"
)

// legacyBackfillSampleSize is the sample size backfillLegacyFrozenSet
// falls back to when a legacy session's stored flags carry none (a
// pre-flags-JSON row, whose flags_json defaulted to '{}' under migration
// 0012). Selecting with SampleSize 0 would return zero candidates, which
// would let a session that was genuinely interrupted mid-review resume
// with nothing pending and complete immediately — precisely the false
// "completed" state this repair exists to remove. A conservative non-zero
// default keeps the repair meaningful; the warning the CLI prints for
// every backfill already tells the operator the set is a reconstruction.
const legacyBackfillSampleSize = 25

// legacyBackfillWindowDays bounds the corpus the legacy repair reselects
// from, mirroring the 30-day window cmd/editorialcycle.go passes to
// activelearning.Select for a real run.
const legacyBackfillWindowDays = 30

// Flags is the subset of `editorial cycle`'s CLI flags a session
// remembers, so --resume reconstructs what the interrupted run was
// doing and the legacy frozen-set backfill (see backfillLegacyFrozenSet)
// can rerun selection with the same SampleSize.
type Flags struct {
	SampleSize int  `json:"sample_size"`
	DryRun     bool `json:"dry_run"`
}

// Deps are the ports this package depends on. Articles/Labels/Events/
// ReviewDecisions/Clock are also activelearning.Deps' fields, reused
// as-is (not duplicated) so the legacy-frozen-set-backfill path can call
// activelearning.Select directly — see activeLearningDeps.
type Deps struct {
	Sessions          out.EditorialCycleSessionRepository
	Candidates        out.EditorialCycleSessionCandidateRepository
	ReviewDecisions   out.ReviewDecisionRepository
	Labels            out.HumanLabelRepository
	Articles          out.ArticleRepository
	Events            out.DecisionRepository
	UOW               out.UnitOfWork
	Clock             out.Clock
	ExcludedSourceIDs map[string]bool
}

// activeLearningDeps projects Deps onto activelearning.Deps for the one
// place this package selects candidates itself (the legacy backfill);
// every other selection happens in the caller and arrives through
// StartOrResume's freshCandidates argument.
func (d Deps) activeLearningDeps() activelearning.Deps {
	return activelearning.Deps{
		Articles: d.Articles, Labels: d.Labels, Events: d.Events,
		ReviewDecisions: d.ReviewDecisions, Clock: d.Clock, ExcludedSourceIDs: d.ExcludedSourceIDs,
	}
}

// StartOrResume returns the latest resumable session (running, paused,
// or failed) if one exists, flipped to "running"
// (resumed: true), or creates a new one seeded with flags and
// freshCandidates (resumed: false).
//
// backfilled is true exactly when this call took the legacy-frozen-set
// repair path (backfillLegacyFrozenSet): a session created before
// migration 0024's candidate table existed, whose frozen set could only
// be reconstructed, not recovered. The caller (cmd/editorialcycle.go) is
// responsible for warning the operator when this happens (design spec
// §11.3) — this package only signals it, matching the "return data, let
// the caller print" convention backfillLegacyFrozenSet's own doc comment
// already documents for its own return value.
//
// freshCandidates becomes the new session's frozen candidate set (design
// spec §6.1) and is ignored entirely when resuming: the whole point of
// freezing is that a resumed session reviews the set it began with, not
// a recomputed one. Callers that only intend to resume may pass nil.
// Creating a session from an empty freshCandidates is legal, not an
// error — it is the "nothing to review" case the spec's §8 matrix routes
// straight to `completed`, since Complete's guard is satisfied
// vacuously.
//
// Two repairs happen on the resume path, both keyed off how many of the
// session's frozen candidates still resolve against the corpus
// (Candidates.CountResolvable):
//
//   - Nothing resolvable left, but the session did freeze candidates
//     (TotalCandidates > 0): every one of them was purged from the corpus
//     while the session was interrupted, so it can never be pending nor
//     complete again. It is transitioned to "cancelled" (transition 9)
//     and a fresh session is started from freshCandidates instead.
//   - Nothing resolvable left and no frozen rows at all recorded: a
//     session from before migration 0024's table existed. Its frozen set
//     is repaired once, best-effort — see backfillLegacyFrozenSet.
//
// The two are distinguished by TotalCandidates, not by comparing
// ListFrozen against CountResolvable: editorial_cycle_session_candidates
// .article_id is ON DELETE CASCADE, so a purged article's row is deleted
// outright rather than orphaned, and those two numbers can therefore
// never disagree. TotalCandidates (migration 0025) is the only surviving
// record of how large the set was at creation.
func StartOrResume(ctx context.Context, deps Deps, flags Flags, freshCandidates []activelearning.Candidate) (out.EditorialCycleSession, bool, bool, error) {
	existing, err := deps.Sessions.GetLatestResumable(ctx)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return out.EditorialCycleSession{}, false, false, fmt.Errorf("editorialcycle: get latest resumable session: %w", err)
		}
		session, resumed, err := startFresh(ctx, deps, flags, freshCandidates)
		return session, resumed, false, err
	}

	resolvable, err := deps.Candidates.CountResolvable(ctx, existing.ID)
	if err != nil {
		return out.EditorialCycleSession{}, false, false, fmt.Errorf("editorialcycle: count resolvable candidates for %s: %w", existing.ID, err)
	}
	backfilled := false
	if resolvable == 0 {
		if existing.TotalCandidates > 0 {
			if err := transition(ctx, deps, existing, statusCancelled); err != nil {
				return out.EditorialCycleSession{}, false, false, err
			}
			session, resumed, err := startFresh(ctx, deps, flags, freshCandidates)
			return session, resumed, false, err
		}
		// TotalCandidates == 0: no baseline was ever recorded. If the
		// candidate table has no row for this session either, nothing
		// about its set was ever persisted — repair it. (Rows present
		// with a zero baseline can only mean rows that outlived their
		// article with foreign keys off; leave them alone rather than
		// insert over them.)
		frozen, listErr := deps.Candidates.ListFrozen(ctx, existing.ID)
		if listErr != nil {
			return out.EditorialCycleSession{}, false, false, fmt.Errorf("editorialcycle: list frozen candidates for %s: %w", existing.ID, listErr)
		}
		if len(frozen) == 0 {
			repaired, backfillErr := backfillLegacyFrozenSet(ctx, deps, existing)
			if backfillErr != nil {
				return out.EditorialCycleSession{}, false, false, backfillErr
			}
			existing = repaired
			backfilled = true
		}
	}

	if err := transition(ctx, deps, existing, statusRunning); err != nil {
		return out.EditorialCycleSession{}, false, false, err
	}
	existing.Status = statusRunning
	return existing, true, backfilled, nil
}

// startFresh creates a brand-new running session and freezes
// freshCandidates as its candidate set. TotalCandidates is stamped here,
// once, and never written again for this session (see
// out.EditorialCycleSession.TotalCandidates).
//
// The two writes share one transaction (design spec §6.1). They have to:
// a session row whose frozen set failed to land would carry a non-zero
// TotalCandidates with nothing resolvable, which is exactly the shape
// StartOrResume reads as "every candidate was purged" and cancels on.
// This is the one place this package opens a transaction spanning more
// than a single decision, and it is bounded by session creation — never
// held across the review loop (§7).
func startFresh(ctx context.Context, deps Deps, flags Flags, freshCandidates []activelearning.Candidate) (out.EditorialCycleSession, bool, error) {
	flagsJSON, err := json.Marshal(flags)
	if err != nil {
		return out.EditorialCycleSession{}, false, fmt.Errorf("editorialcycle: marshal flags: %w", err)
	}
	now := deps.Clock.Now()
	session := out.EditorialCycleSession{
		ID: fmt.Sprintf("cycle-%d", now.UnixNano()), StartedAt: now, UpdatedAt: now,
		Status: statusRunning, CurrentStep: StepReview, FlagsJSON: string(flagsJSON),
		TotalCandidates: len(freshCandidates),
	}
	err = deps.UOW.Do(ctx, func(ctx context.Context) error {
		if err := deps.Sessions.Create(ctx, session); err != nil {
			return fmt.Errorf("editorialcycle: create session: %w", err)
		}
		if err := deps.Candidates.SaveAll(ctx, session.ID, frozenRows(session.ID, freshCandidates, now)); err != nil {
			return fmt.Errorf("editorialcycle: freeze candidate set for %s: %w", session.ID, err)
		}
		return nil
	})
	if err != nil {
		return out.EditorialCycleSession{}, false, err
	}
	return session, false, nil
}

// backfillLegacyFrozenSet re-derives a frozen set for a session created
// before the candidate table existed: a
// best-effort repair, not a guarantee of reproducing the original
// selection, since the schema never recorded it — the session's own
// stored flags are all there is to reselect from, and the corpus itself
// has moved on. It also stamps the TotalCandidates baseline migration
// 0025 added, which such a session necessarily lacks, so drift
// detection works for the rest of its life.
//
// It returns the repaired session so the caller's subsequent status
// transition writes the new baseline rather than the stale zero it read.
// The warning an operator sees for this is printed by the CLI layer
// (cmd/editorialcycle.go), not here, matching this package's existing
// "return data, let the caller print" convention (see AuditReport).
func backfillLegacyFrozenSet(ctx context.Context, deps Deps, session out.EditorialCycleSession) (out.EditorialCycleSession, error) {
	var flags Flags
	if err := json.Unmarshal([]byte(session.FlagsJSON), &flags); err != nil {
		return out.EditorialCycleSession{}, fmt.Errorf("editorialcycle: backfill legacy session %s: unmarshal flags: %w", session.ID, err)
	}
	sampleSize := flags.SampleSize
	if sampleSize <= 0 {
		sampleSize = legacyBackfillSampleSize
	}
	now := deps.Clock.Now()
	candidates, err := activelearning.Select(ctx, deps.activeLearningDeps(), activelearning.Options{
		Since:      now.AddDate(0, 0, -legacyBackfillWindowDays),
		PoolLimit:  sampleSize * 10,
		SampleSize: sampleSize,
	})
	if err != nil {
		return out.EditorialCycleSession{}, fmt.Errorf("editorialcycle: backfill legacy session %s: select candidates: %w", session.ID, err)
	}
	session.TotalCandidates = len(candidates)
	session.UpdatedAt = now
	// One transaction, for the same reason startFresh uses one: a frozen
	// set without its baseline (or a baseline without its set) is a state
	// StartOrResume would misread on the next resume.
	if err := deps.UOW.Do(ctx, func(ctx context.Context) error {
		if err := deps.Candidates.SaveAll(ctx, session.ID, frozenRows(session.ID, candidates, now)); err != nil {
			return fmt.Errorf("editorialcycle: backfill legacy session %s: save frozen set: %w", session.ID, err)
		}
		if err := deps.Sessions.Update(ctx, session); err != nil {
			return fmt.Errorf("editorialcycle: backfill legacy session %s: record candidate total: %w", session.ID, err)
		}
		return nil
	}); err != nil {
		return out.EditorialCycleSession{}, err
	}
	return session, nil
}

// frozenRows adapts selected candidates into the persisted frozen-set
// shape. Only the article ID and the score survive; the rest of
// activelearning.Signals is deliberately not persisted (see
// CandidatesFromFrozen for the reverse direction and what it costs).
func frozenRows(sessionID string, candidates []activelearning.Candidate, now time.Time) []out.EditorialCycleSessionCandidate {
	rows := make([]out.EditorialCycleSessionCandidate, len(candidates))
	for i, c := range candidates {
		rows[i] = out.EditorialCycleSessionCandidate{
			SessionID: sessionID, ArticleID: c.Article.ID, Score: c.Score, SelectedAt: now,
		}
	}
	return rows
}

// CandidatesFromFrozen adapts persisted frozen-set rows back into
// activelearning.Candidate shape, which is what the review screen and
// the dry-run plan both display.
//
// Only Article.ID and Score survive the round trip through storage — the
// frozen set never persisted the article body or the rest of
// activelearning.Signals. Callers that need anything else (a title, a
// URL) must hydrate each Article via Articles.FindByID themselves;
// nothing here silently invents it.
func CandidatesFromFrozen(rows []out.EditorialCycleSessionCandidate) []activelearning.Candidate {
	candidates := make([]activelearning.Candidate, len(rows))
	for i, r := range rows {
		candidates[i] = activelearning.Candidate{Article: article.Article{ID: r.ArticleID}, Score: r.Score}
	}
	return candidates
}

// ApplyDecision resolves action via domain/reviewdecision and persists it:
// always a ReviewDecision journal row, and — depending on the resolved
// Outcome.Persist and articleID's current active label (read via
// GetActive) — one of Label/Relabel/Retract, all in one transaction via
// deps.UOW. Deliberately never bundled with a session status transition
// (design spec §7): a failed status flip must not be able to lose or
// duplicate an already-committed decision.
//
// A reviewer may "back" to an already-decided article (still in the
// pending slice for this session) and re-decide it. Unlike the prior
// unconditional-delete implementation, this reads exactly what is
// currently active for articleID and supersedes exactly that — never a
// stale row from an unrelated session or one that outlived a retention
// purge — and never deletes it: Relabel/Retract mark it superseded,
// preserving it as history.
func ApplyDecision(ctx context.Context, deps Deps, sessionID, articleID string, action reviewdecision.Action, notes string) error {
	outcome, err := reviewdecision.Resolve(action)
	if err != nil {
		return fmt.Errorf("editorialcycle: apply decision for %s: %w", articleID, err)
	}

	now := deps.Clock.Now()
	return deps.UOW.Do(ctx, func(ctx context.Context) error {
		if err := deps.ReviewDecisions.Save(ctx, out.ReviewDecision{
			SessionID: sessionID, ArticleID: articleID, Action: string(action),
			Label: outcome.Label, Reason: outcome.Reason, Notes: notes, DecidedAt: now,
		}); err != nil {
			return fmt.Errorf("editorialcycle: save review decision: %w", err)
		}

		active, hasActive, err := deps.Labels.GetActive(ctx, articleID)
		if err != nil {
			return fmt.Errorf("editorialcycle: get active label for %s: %w", articleID, err)
		}

		newExample := training.Example{
			ArticleID: articleID, Label: outcome.Label, Reason: outcome.Reason,
			LabeledAt: now, Notes: notes, Source: "editorial_cycle", SessionID: sessionID,
		}

		switch {
		case outcome.Persist && hasActive:
			if err := deps.Labels.Relabel(ctx, newExample, active.Revision); err != nil {
				return fmt.Errorf("editorialcycle: relabel %s: %w", articleID, err)
			}
		case outcome.Persist && !hasActive:
			if err := deps.Labels.Label(ctx, newExample); err != nil {
				return fmt.Errorf("editorialcycle: save human label: %w", err)
			}
		case !outcome.Persist && hasActive:
			if err := deps.Labels.Retract(ctx, articleID, active.Revision); err != nil {
				return fmt.Errorf("editorialcycle: retract label for %s: %w", articleID, err)
			}
		}
		// !outcome.Persist && !hasActive: nothing to do.
		return nil
	})
}

// Complete transitions session to "completed" — refusing, with the
// status left untouched, unless every frozen-set candidate has been
// decided. The guard lives here, not in the
// CLI caller, so no future call site can reintroduce the
// early-completion bug by forgetting it.
//
// "Pending" is Candidates.ListPending's SQL projection: frozen-set rows
// with no decision for this session that still resolve to a live article
// (design spec §6.2). A candidate purged from the corpus while the
// session was paused therefore does not block completion, and a session
// that froze nothing at all completes vacuously.
//
// session must be the freshest known value for that session, never a
// stale copy — the status write overwrites the whole row. See
// transition's CALLER INVARIANT for what that costs if ignored.
func Complete(ctx context.Context, deps Deps, session out.EditorialCycleSession) error {
	pending, err := deps.Candidates.ListPending(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("editorialcycle: complete session %s: list pending: %w", session.ID, err)
	}
	if len(pending) > 0 {
		return fmt.Errorf("editorialcycle: cannot complete session %s: %d candidate(s) still pending", session.ID, len(pending))
	}
	return transition(ctx, deps, session, statusCompleted)
}

// Pause transitions session to "paused" — the benign-interruption status
// (save-and-exit, EOF with candidates remaining, SIGINT, context
// cancellation). reason is not persisted (there is no
// column for it) — callers log it themselves; it exists on this
// signature so every call site documents its intent at the call, matching
// this package's existing self-documenting-parameter style (e.g.
// ApplyDecision's notes).
//
// session must be the freshest known value for that session, never a
// stale copy — the status write overwrites the whole row. This matters
// most here: Pause is the transition a long-running CLI review loop
// reaches for at EOF/SIGINT, i.e. exactly where a session variable
// captured before the loop is easiest to reach for. See transition's
// CALLER INVARIANT.
func Pause(ctx context.Context, deps Deps, session out.EditorialCycleSession, reason string) error {
	_ = reason
	return transition(ctx, deps, session, statusPaused)
}

// Fail transitions session to "failed" — the unexpected-error status
// (stdin I/O error, recovered panic, a decision or transition write
// itself erroring). cause is not persisted — callers fold
// it into their own AuditReport.Error field, mirroring how the CLI
// already threads errors into the audit report today.
//
// session must be the freshest known value for that session, never a
// stale copy — the status write overwrites the whole row. Fail is often
// called from a deferred recover() or a top-level error path, where the
// only session value in scope may be an early one; re-read it or carry
// the current value forward. See transition's CALLER INVARIANT.
func Fail(ctx context.Context, deps Deps, session out.EditorialCycleSession, cause error) error {
	_ = cause
	return transition(ctx, deps, session, statusFailed)
}

// transition is the single writer of editorial_cycle_sessions.status:
// one UPDATE, never inside a decision's transaction (design spec §7).
//
// CALLER INVARIANT — session must be the freshest known value for that
// session. EditorialCycleSessionRepository.Update overwrites the WHOLE
// row (status, current_step, flags_json AND total_candidates) from the
// struct handed to it, not just status/updated_at, so passing a stale
// copy silently reverts every one of those columns to whatever it was
// captured with. The most damaging case is total_candidates: it is the
// write-once drift baseline StartOrResume's "every candidate was purged
// → cancelled" guard reads, so a stale 0 written over a real baseline
// permanently disables that guard (and Plan's drift warning) for the
// session. Pass what StartOrResume just returned, or a value re-read
// immediately before the call — never a long-lived variable captured
// earlier in a run that may have been updated since (e.g. by the legacy
// frozen-set backfill, which stamps total_candidates during resume).
func transition(ctx context.Context, deps Deps, session out.EditorialCycleSession, status string) error {
	session.Status = status
	session.UpdatedAt = deps.Clock.Now()
	if err := deps.Sessions.Update(ctx, session); err != nil {
		return fmt.Errorf("editorialcycle: transition session %s to %s: %w", session.ID, status, err)
	}
	return nil
}
