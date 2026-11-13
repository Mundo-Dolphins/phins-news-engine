package cycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

// discoveryLLM builds the entity step's discovery LLM; nil is the default
// client. Tests substitute it.
var discoveryLLM entitywiring.DiscoveryLLMFactory

const msgNoReviewedArticles = "entities: the session has no reviewed articles"

func runEntitiesDiscover(ctx context.Context, env action.Env, _ any) (_ action.Result, err error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	var prior entitiesResult
	run, err := beginStep(ctx, env, StepEntities, func(s *StepState) error {
		if reason := entitywiring.CycleStepDisabledReason(cfg); reason != "" {
			return action.Conflict(fmt.Errorf("entities: step disabled (%s)", reason))
		}
		decodeInto(s, &prior)
		return nil
	})
	if err != nil {
		return action.Result{}, err
	}
	defer func() { run.finish(ctx, recover(), &err) }()
	if run.w.SessionID == "" {
		return action.Result{}, run.fail(ctx, nil, errNoSession)
	}
	logs := newLogWriter(env.Log)
	deps, in, err := entitywiring.CycleDeps(ctx, cfg, env.DB, run.w.SessionID, run.w.EntitiesVerification, logs, discoveryLLM)
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, err)
	}
	if len(in.SessionArticleIDs) == 0 {
		// As the CLI: nothing to discover or review.
		if err := run.end(ctx, StatusDone, entitiesResult{}, ""); err != nil {
			return action.Result{}, err
		}
		return action.Result{Render: "object", Object: entitiesResult{}, Summary: msgNoReviewedArticles}, nil
	}
	in.Out = logs
	res, err := entitycycle.Discover(ctx, deps, in)
	warnings := logs.flush()
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, fmt.Errorf("entities: %w", err))
	}
	result := entitiesResult{
		Articles: res.Articles, Proposed: res.Proposed, DiscoveryFailed: res.DiscoveryFailed,
		Warnings: warnings, Discovered: true,
	}
	// The step waits for the review of the session's proposals.
	if err := run.end(ctx, StatusPending, result, ""); err != nil {
		return action.Result{}, err
	}
	summary := fmt.Sprintf("entities: %d articles, %d batches (%d failed), %d proposals created or updated",
		res.Articles, res.Batches, res.BatchesFailed, res.Proposed)
	return action.Result{Render: "object", Object: result, Summary: summary}, nil
}

// stepEnteredAt is when the wizard moved onto step: its entered_at, or for a
// state recorded before entered_at existed, when the previous step last
// changed.
func stepEnteredAt(w *Wizard, step string) time.Time {
	if at := w.step(step).EnteredAt; at != nil {
		return *at
	}
	if i := slices.Index(Steps, step); i > 0 {
		return w.step(Steps[i-1]).UpdatedAt
	}
	return w.StartedAt
}

func runEntitiesFinish(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	mu.Lock()
	defer mu.Unlock()
	w, err := loadOpenStep(ctx, env, StepEntities)
	if err != nil {
		return action.Result{}, err
	}
	if w.SessionID == "" {
		return action.Result{}, errNoSession
	}
	var r entitiesResult
	decodeInto(w.step(StepEntities), &r)
	if !r.Discovered {
		return action.Result{}, action.Conflict(errors.New("run the entity discovery first"))
	}
	ids, err := entitywiring.SessionArticleIDs(ctx, env.DB, w.SessionID)
	if err != nil {
		return action.Result{}, err
	}
	all, err := sqliteentityproposal.NewEntityProposalRepository(env.DB).List(ctx, "", "")
	if err != nil {
		return action.Result{}, err
	}
	// The operator's decisions on the session's proposals since the step was
	// entered (automatic rejections carry a reason).
	entered := stepEnteredAt(w, StepEntities)
	r.Approved, r.Rejected = 0, 0
	for _, p := range entitycycle.SourcedFrom(all, ids) {
		if p.DecisionReason != "" || p.DecidedAt.Before(entered) {
			continue
		}
		switch p.Status {
		case entityproposal.StatusApproved:
			r.Approved++
		case entityproposal.StatusRejected:
			r.Rejected++
		}
	}
	ctx = context.WithoutCancel(ctx)
	if r.Approved > 0 {
		// Regeneration is idempotent: an approval of the session regenerates.
		r.Regenerated, err = func() (bool, error) {
			action.ConfigFilesMu.Lock()
			defer action.ConfigFilesMu.Unlock()
			return entitycycle.FinishReview(ctx, entitycycle.Deps{Regenerate: func() error {
				_, err := entitywiring.GenerateCatalog(cfg.Dictionary.Path, cfg.Clustering.EntityCatalog.GeneratedPath)
				return err
			}}, true)
		}()
		if err != nil {
			return action.Result{}, err
		}
	}
	if err := w.set(StepEntities, StatusDone, r, "", env.Now()); err != nil {
		return action.Result{}, err
	}
	if err := save(ctx, env, w); err != nil {
		return action.Result{}, err
	}
	summary := fmt.Sprintf("Entity review complete. approved: %d, rejected: %d", r.Approved, r.Rejected)
	if r.Regenerated {
		summary += "; entity catalog regenerated"
	}
	return action.Result{Render: "object", Object: r, Summary: summary}, nil
}
