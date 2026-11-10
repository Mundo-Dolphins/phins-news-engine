package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/eventrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// newDiscoveryLLM builds the discovery LLM from offline_training.llm; a
// variable so tests never reach a real model.
var newDiscoveryLLM = func(cfg config.OfflineTrainingLLMConfig) appout.EventRuleDiscoveryLLM {
	return llmclient.New(trainwiring.LLMClientConfig(cfg))
}

// gateDatasetPath is the gold dataset the apply gate scores, as the CLI's
// `events review`; a variable so tests can point it at a fixture.
var gateDatasetPath = eventrules.DefaultDatasetPath

type ruleDecideParams struct {
	ID       string `json:"id" help:"event rule phrase proposal id" required:"true"`
	Decision string `json:"decision" help:"accept, reject or edit" enum:"accept,reject,edit" required:"true"`
	Phrase   string `json:"phrase" help:"replacement phrase (edit only)"`
	Preview  bool   `json:"preview" help:"edit only: return the impact and verdict of the phrase without saving"`
}

func ruleActions() []action.Action {
	return []action.Action{
		{
			Name: "events.rules_discover", Group: group, Title: "Discover event rule phrases",
			Description: "Samples recent generic articles, asks the offline-training LLM for event rule phrases and stores them as pending proposals. Failed LLM calls are reported in the result, not as an error.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true, Params: noParams{}, Run: runRulesDiscover,
		},
		{
			Name: "events.rules_queue", Group: group, Title: "Event rule review queue",
			Description: "Lists the pending event rule phrase proposals with their checks, impact and evidence, those without evidence in the sample, and the approved ones not yet applied. Nothing is decided.",
			Kind:        action.KindRead, Modes: localOnly, Params: noParams{}, Run: runRulesQueue,
		},
		{
			Name: "events.rules_decide", Group: group, Title: "Decide an event rule phrase",
			Description: "Accepts, rejects or edits a pending event rule phrase proposal. A proposal failing the checks cannot be accepted. Edit replaces the phrase (preview shows its impact without saving).",
			Kind:        action.KindWrite, Modes: localOnly, Params: ruleDecideParams{}, Run: runRulesDecide,
		},
		{
			Name: "events.rules_reject_failing", Group: group, Title: "Reject failing event rule phrases",
			Description: "Rejects the pending event rule phrase proposals that fail the checks now and returns them. Proposals without evidence stay pending.",
			Kind:        action.KindWrite, Modes: localOnly, Params: noParams{}, Run: runRulesRejectFailing,
		},
		{
			Name: "events.rules_apply", Group: group, Title: "Apply approved event rule phrases",
			Description: "Gates the generated rules plus the approved phrases against the clustering benchmark and, when it passes, writes the generated rules file and marks them applied. A blocked gate is reported in the result and leaves them approved.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true, Params: noParams{}, Run: runRulesApply,
		},
	}
}

// ruleError maps the rule cycle errors to their action codes.
func ruleError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return action.NotFound(err)
	case errors.Is(err, eventrulecycle.ErrPhraseUnchanged), errors.Is(err, eventrulecycle.ErrNoCitedEvidence):
		return &action.Error{Code: action.CodeInvalidParams, Message: err.Error()}
	case errors.Is(err, eventrulecycle.ErrEditFailsChecks), errors.Is(err, eventrulecycle.ErrAlreadyDecided),
		errors.Is(err, eventrules.ErrAlreadyDecided), errors.Is(err, eventrules.ErrGateDatasetMissing):
		return action.Conflict(err)
	}
	return err
}

// ruleDeps wires the cycle to the environment. Only discovery needs the LLM
// (llm is nil elsewhere).
func ruleDeps(env action.Env, llm appout.EventRuleDiscoveryLLM) (eventrulecycle.Deps, error) {
	if env.Config == nil {
		return eventrulecycle.Deps{}, errors.New("events: no configuration loaded")
	}
	if env.Config.Clustering.RulesPath == "" {
		return eventrulecycle.Deps{}, action.Conflict(errors.New("clustering.rules_path is empty"))
	}
	return eventrules.CycleDeps(env.Config, env.DB, gateDatasetPath, llm, func(w string) { env.Log.Warn(w) }, env.Now), nil
}

func runRulesDiscover(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errors.New("events: no configuration loaded")
	}
	if reason := eventrules.CycleStepDisabledReason(env.Config); reason != "" {
		return action.Result{}, action.Conflict(errors.New(reason))
	}
	d, err := ruleDeps(env, newDiscoveryLLM(env.Config.OfflineTraining.LLM))
	if err != nil {
		return action.Result{}, err
	}
	c, err := eventrulecycle.Prepare(ctx, d)
	if err != nil {
		return action.Result{}, err
	}
	r, err := eventrulecycle.Discover(ctx, d, c, env.Config.OfflineTraining.LLM.BatchSize)
	if err != nil {
		return action.Result{}, err
	}
	obj := map[string]any{"sampled": r.Sampled, "calls": r.Calls, "failed": r.Failed, "proposed": r.Proposed, "error": ""}
	if r.FirstErr != nil {
		obj["error"] = r.FirstErr.Error()
	}
	summary := fmt.Sprintf("%d sampled, %d calls (%d failed), %d proposals created or updated", r.Sampled, r.Calls, r.Failed, r.Proposed)
	if r.Failed > 0 {
		summary = "WARNING: " + summary
	}
	env.Log.Info("event rule discovery", "sampled", r.Sampled, "calls", r.Calls, "failed", r.Failed, "proposed", r.Proposed, "by", env.Actor)
	return action.Result{Render: "object", Object: obj, Summary: summary}, nil
}

// failing reports whether the proposal fails the checks for a reason other
// than having no evidence in the sample.
func failing(a eventrulecycle.Assessment) bool { return !a.Verdict.OK && !a.NoEvidence }

type moved struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

func impactObject(a eventrulecycle.Assessment) map[string]any {
	mv := make([]moved, 0, len(a.Impact.Moved))
	for t, n := range a.Impact.Moved {
		mv = append(mv, moved{Type: string(t), Count: n})
	}
	sort.Slice(mv, func(i, j int) bool { return mv[i].Type < mv[j].Type })
	ex := a.Impact.Examples
	if ex == nil {
		ex = []string{}
	}
	return map[string]any{"moved": mv, "examples": ex, "matches": a.Impact.Matches, "retyped": a.Impact.Retyped}
}

func reasonsOf(a eventrulecycle.Assessment) []string {
	if a.Verdict.Reasons == nil {
		return []string{}
	}
	return a.Verdict.Reasons
}

// assessmentObject is the shape shared by the queue items and the edit result.
func assessmentObject(a eventrulecycle.Assessment) map[string]any {
	p := a.Proposal
	return map[string]any{
		"id": p.ID, "target_kind": string(p.TargetKind), "target": p.Target, "phrase": p.Phrase,
		"prompt_version": p.PromptVersion, "passes": a.Verdict.OK, "reasons": reasonsOf(a), "impact": impactObject(a),
	}
}

type evidence struct {
	ArticleID string `json:"article_id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
}

// evidenceOf resolves the cited articles from the operational database; a
// purged article keeps its sample text as title and has no URL.
func evidenceOf(ctx context.Context, env action.Env, c eventrulecycle.Context, ids []string) ([]evidence, error) {
	repo := sqliteadapter.NewArticleRepository(env.DB)
	out := make([]evidence, 0, len(ids))
	for _, id := range ids {
		e := evidence{ArticleID: id, Title: c.Text[id]}
		a, err := repo.FindByID(ctx, id)
		switch {
		case err == nil:
			e.Title, e.URL = a.Title, a.CanonicalURL
			if e.URL == "" {
				e.URL = a.OriginalURL
			}
		case errors.Is(err, article.ErrNotFound):
		default:
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func prepared(ctx context.Context, env action.Env) (eventrulecycle.Deps, eventrulecycle.Context, error) {
	d, err := ruleDeps(env, nil)
	if err != nil {
		return d, eventrulecycle.Context{}, err
	}
	c, err := eventrulecycle.Prepare(ctx, d)
	return d, c, err
}

func runRulesQueue(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	d, c, err := prepared(ctx, env)
	if err != nil {
		return action.Result{}, err
	}
	pending, err := eventrulecycle.Pending(ctx, d, c, eventruleproposal.DefaultCheckOptions())
	if err != nil {
		return action.Result{}, err
	}
	items, noEvidence := []map[string]any{}, []map[string]any{}
	for _, a := range pending {
		if a.NoEvidence {
			noEvidence = append(noEvidence, map[string]any{
				"id": a.Proposal.ID, "target_kind": string(a.Proposal.TargetKind), "target": a.Proposal.Target, "phrase": a.Proposal.Phrase,
			})
			continue
		}
		item := assessmentObject(a)
		if item["evidence"], err = evidenceOf(ctx, env, c, a.Proposal.EvidenceIDs); err != nil {
			return action.Result{}, err
		}
		items = append(items, item)
	}
	approvedNow, err := eventrulecycle.ApprovedNotApplied(ctx, d)
	if err != nil {
		return action.Result{}, err
	}
	approved := make([]map[string]any, 0, len(approvedNow))
	for _, p := range approvedNow {
		approved = append(approved, map[string]any{
			"id": p.ID, "target_kind": string(p.TargetKind), "target": p.Target, "phrase": p.Phrase,
			"decided_at": p.DecidedAt.UTC().Format(time.RFC3339),
		})
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{"sampled": len(c.Sample), "items": items, "no_evidence": noEvidence, "approved": approved},
		Summary: fmt.Sprintf("%d proposal(s) to review, %d without evidence, %d approved not applied",
			len(items), len(noEvidence), len(approved)),
	}, nil
}

func runRulesDecide(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*ruleDecideParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if p.Decision != "edit" && (p.Phrase != "" || p.Preview) {
		return action.Result{}, invalid("phrase and preview apply to edit only")
	}
	if p.Decision == "edit" && strings.TrimSpace(p.Phrase) == "" {
		return action.Result{}, invalid("phrase is required to edit")
	}
	d, err := ruleDeps(env, nil)
	if err != nil {
		return action.Result{}, err
	}
	cur, err := d.Proposals.Get(ctx, p.ID)
	if err != nil {
		return action.Result{}, ruleError(err)
	}
	if cur.Kind != eventruleproposal.KindPhrase {
		return action.Result{}, action.Conflict(fmt.Errorf("proposal %s is not an event rule phrase proposal", p.ID))
	}
	if p.Decision == "reject" {
		decided, err := eventrulecycle.Reject(ctx, d, cur, env.Now())
		if err != nil {
			return action.Result{}, ruleError(err)
		}
		env.Log.Info("event rule proposal decided", "id", decided.ID, "status", string(decided.Status), "by", env.Actor)
		return action.Result{Render: "object", Object: map[string]any{"id": decided.ID, "status": string(decided.Status)},
			Summary: fmt.Sprintf("Event rule %q rejected.", decided.Phrase)}, nil
	}
	if cur.Status != eventruleproposal.StatusPending {
		return action.Result{}, action.Conflict(fmt.Errorf("proposal %s is %s, not pending", p.ID, cur.Status))
	}
	c, err := eventrulecycle.Prepare(ctx, d)
	if err != nil {
		return action.Result{}, err
	}
	opts := eventruleproposal.DefaultCheckOptions()
	if p.Decision == "accept" {
		a := eventrulecycle.Assess(d, c, cur, opts)
		if !a.Verdict.OK {
			return action.Result{}, action.Conflict(fmt.Errorf("proposal %s fails the checks: %s", p.ID, strings.Join(a.Verdict.Reasons, "; ")))
		}
		decided, err := eventrulecycle.Approve(ctx, d, cur, env.Now())
		if err != nil {
			return action.Result{}, ruleError(err)
		}
		env.Log.Info("event rule proposal decided", "id", decided.ID, "status", string(decided.Status), "by", env.Actor)
		return action.Result{Render: "object", Object: map[string]any{"id": decided.ID, "status": string(decided.Status)},
			Summary: fmt.Sprintf("Event rule %q approved.", decided.Phrase)}, nil
	}
	a, err := eventrulecycle.Edit(ctx, d, c, cur, p.Phrase, opts, env.Now(), !p.Preview)
	if err != nil {
		return action.Result{}, ruleError(err)
	}
	obj := assessmentObject(a)
	obj["preview"] = p.Preview
	if p.Preview {
		obj["id"] = ""
		return action.Result{Render: "object", Object: obj, Summary: fmt.Sprintf("Preview of %q: passes the checks.", a.Proposal.Phrase)}, nil
	}
	env.Log.Info("event rule proposal edited", "id", cur.ID, "new_id", a.Proposal.ID, "by", env.Actor)
	return action.Result{Render: "object", Object: obj, Summary: fmt.Sprintf("Event rule %q replaced by %q and approved.", cur.Phrase, a.Proposal.Phrase)}, nil
}

func runRulesRejectFailing(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	d, c, err := prepared(ctx, env)
	if err != nil {
		return action.Result{}, err
	}
	pending, err := eventrulecycle.Pending(ctx, d, c, eventruleproposal.DefaultCheckOptions())
	if err != nil {
		return action.Result{}, err
	}
	rejected := []map[string]any{}
	for _, a := range pending {
		if !failing(a) {
			continue
		}
		if _, err := eventrulecycle.Reject(ctx, d, a.Proposal, env.Now()); err != nil {
			if errors.Is(err, eventrulecycle.ErrAlreadyDecided) {
				continue // decided elsewhere since the listing
			}
			return action.Result{}, ruleError(err)
		}
		rejected = append(rejected, map[string]any{"id": a.Proposal.ID, "phrase": a.Proposal.Phrase, "reasons": reasonsOf(a)})
	}
	env.Log.Info("event rule proposals rejected", "count", len(rejected), "by", env.Actor)
	return action.Result{Render: "object", Object: map[string]any{"rejected": rejected},
		Summary: fmt.Sprintf("%d failing proposal(s) rejected.", len(rejected))}, nil
}

func runRulesApply(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	d, err := ruleDeps(env, nil)
	if err != nil {
		return action.Result{}, err
	}
	action.ConfigFilesMu.Lock()
	defer action.ConfigFilesMu.Unlock()
	toApply, err := eventrulecycle.ApprovedNotApplied(ctx, d)
	if err != nil {
		return action.Result{}, err
	}
	r, err := eventrulecycle.Apply(ctx, d, toApply, env.Now())
	if err != nil {
		return action.Result{}, ruleError(err)
	}
	applied := make([]map[string]any, 0, len(r.Applied))
	for _, p := range r.Applied {
		applied = append(applied, map[string]any{"id": p.ID, "target_kind": string(p.TargetKind), "target": p.Target, "phrase": p.Phrase})
	}
	obj := map[string]any{"nothing": r.Nothing, "blocked": r.Blocked, "reason": r.Reason, "applied": applied, "generated_rules": d.GeneratedPath}
	if r.Candidate != nil {
		obj["before"], obj["after"] = metricsOf(r.Gate.Before), metricsOf(r.Gate.After)
		obj["dataset"] = eventrulecycle.GateExercise(r.Gate.Affected)
	}
	switch {
	case r.Nothing:
		return action.Result{Render: "object", Object: obj, Summary: "Nothing to apply."}, nil
	case r.Blocked:
		return action.Result{Render: "object", Object: obj, Summary: "BLOCKED: " + r.Reason}, nil
	}
	env.Log.Info("event rules applied", "count", len(applied), "path", d.GeneratedPath, "by", env.Actor)
	return action.Result{Render: "object", Object: obj, Summary: fmt.Sprintf("PASS: %d rule(s) applied to %s", len(applied), d.GeneratedPath)}, nil
}
