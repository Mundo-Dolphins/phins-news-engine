package cycle

import (
	"context"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/events"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/eventrules"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// eventRuleLLM is the event rules discovery LLM; nil is the
// offline-training client. Tests substitute it.
var eventRuleLLM out.EventRuleDiscoveryLLM

// eventRulesDataset is the gold clustering dataset the apply gate scores,
// as the CLI's --event-rules-dataset default. Tests substitute it.
var eventRulesDataset = eventrules.DefaultDatasetPath

func eventRulesDisabled(reason string) error {
	return action.Conflict(fmt.Errorf("event rules: step disabled (%s)", reason))
}

// runEventRulesDiscover runs the event rule discovery of the step
// (events.rules_discover's work) and records the CLI's counters: sampled,
// proposed and discovery_failed (every LLM call failed).
func runEventRulesDiscover(ctx context.Context, env action.Env, _ any) (_ action.Result, err error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	var r eventRulesResult
	run, err := beginStep(ctx, env, StepEventRules, func(s *StepState) error {
		if reason := eventrules.CycleStepDisabledReason(env.Config); reason != "" {
			return eventRulesDisabled(reason)
		}
		decodeInto(s, &r)
		return nil
	})
	if err != nil {
		return action.Result{}, err
	}
	defer func() { run.finish(ctx, recover(), &err) }()
	d, err := events.DiscoverRules(ctx, env, eventRuleLLM)
	if err != nil {
		return action.Result{}, run.fail(ctx, r, fmt.Errorf("event rules: %w", err))
	}
	r.Sampled, r.Proposed, r.DiscoveryFailed = d.Sampled, d.Proposed, d.Unavailable
	if err := run.end(ctx, StatusPending, r, ""); err != nil {
		return action.Result{}, err
	}
	summary := fmt.Sprintf("%d sampled, %d calls (%d failed), %d proposals created or updated", d.Sampled, d.Calls, d.Failed, d.Proposed)
	if d.Failed > 0 {
		summary = "WARNING: " + summary
	}
	return action.Result{Render: "object", Object: r, Summary: summary}, nil
}

// runEventRulesApply applies the approved phrases when the clustering gate
// passes (events.rules_apply's work) and accumulates the CLI's counters:
// applied, gate_blocked and gate_not_exercised (the gate ran and changed no
// benchmark article).
func runEventRulesApply(ctx context.Context, env action.Env, _ any) (_ action.Result, err error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	var r eventRulesResult
	run, err := beginStep(ctx, env, StepEventRules, func(s *StepState) error {
		if reason := eventrules.ReviewDisabledReason(env.Config); reason != "" {
			return eventRulesDisabled(reason)
		}
		decodeInto(s, &r)
		return nil
	})
	if err != nil {
		return action.Result{}, err
	}
	defer func() { run.finish(ctx, recover(), &err) }()
	// The state lock is not held here: beginStep released it, and end takes
	// it again after the config files lock is released.
	res, err := func() (eventrulecycle.ApplyResult, error) {
		action.ConfigFilesMu.Lock()
		defer action.ConfigFilesMu.Unlock()
		return events.ApplyRules(context.WithoutCancel(ctx), env, eventRulesDataset)
	}()
	if err != nil {
		return action.Result{}, run.fail(ctx, r, fmt.Errorf("event rules: %w", err))
	}
	r.Applied += len(res.Applied)
	if res.Candidate != nil {
		r.GateBlocked, r.GateNotExercised = res.Blocked, res.Gate.Affected == 0
	}
	if err := run.end(ctx, StatusPending, r, ""); err != nil {
		return action.Result{}, err
	}
	summary := fmt.Sprintf("PASS: %d rule(s) applied to %s", len(res.Applied), eventrules.GeneratedPath(env.Config))
	switch {
	case res.Nothing:
		summary = "Nothing to apply."
	case res.Blocked:
		summary = "BLOCKED: " + res.Reason
	}
	return action.Result{Render: "object", Object: r, Summary: summary}, nil
}

// runEventRulesFinish finishes the step with the counters recorded by the
// discover and apply actions; approved is recomputed (applied plus the
// approved phrases not yet applied). Nothing is decided or rejected here.
func runEventRulesFinish(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	mu.Lock()
	defer mu.Unlock()
	w, err := loadOpenStep(ctx, env, StepEventRules)
	if err != nil {
		return action.Result{}, err
	}
	// Only discovery needs the LLM: the review can finish without it.
	if reason := eventrules.ReviewDisabledReason(cfg); reason != "" {
		return action.Result{}, eventRulesDisabled(reason)
	}
	var r eventRulesResult
	decodeInto(w.step(StepEventRules), &r)
	d := eventrules.CycleDeps(cfg, env.DB, eventRulesDataset, nil, func(m string) { env.Log.Warn(m) }, env.Now)
	approved, err := eventrulecycle.ApprovedNotApplied(ctx, d)
	if err != nil {
		return action.Result{}, err
	}
	r.Approved = r.Applied + len(approved)
	if err := w.set(StepEventRules, StatusDone, r, "", env.Now()); err != nil {
		return action.Result{}, err
	}
	if err := save(context.WithoutCancel(ctx), env, w); err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: r, Summary: fmt.Sprintf(
		"event rules: %d sampled, %d proposed, %d approved, %d applied", r.Sampled, r.Proposed, r.Approved, r.Applied)}, nil
}
