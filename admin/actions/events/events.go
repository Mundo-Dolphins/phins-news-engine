// Package events holds the admin actions over event rule discovery: the new
// event type proposals, the clustering gate and the impact report of the
// generated rules. They mirror the `events type list|approve|reject|merge|add-phrase`,
// `events gate` and `events impact` CLI commands. Reads are
// available in both modes; deciding a type proposal is local-only.
package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/eventrules"
)

const group = "events"

// maxImpactExamples bounds the titles listed per bucket in the impact
// report, as the CLI does.
const maxImpactExamples = 5

var errBadParams = errors.New("events: unexpected params type")

var (
	bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}
	localOnly = []action.Mode{action.ModeLocal}
)

type noParams struct{}

type idParams struct {
	ID string `json:"id" help:"event type proposal id" required:"true"`
}

type approveParams struct {
	ID          string `json:"id" help:"event type proposal id" required:"true"`
	Window      string `json:"window" help:"match window of the approved type, a positive duration such as 72h (blank means 48h)"`
	Referential bool   `json:"referential" help:"the type is a framing (such as analysis or a quote), not a concrete event"`
}

type addPhraseParams struct {
	ID     string `json:"id" help:"approved event type proposal id" required:"true"`
	Phrase string `json:"phrase" help:"trigger phrase for the type; it is proposed pending and reviewed in the queue" required:"true"`
}

type mergeParams struct {
	ID   string `json:"id" help:"event type proposal id to fold away" required:"true"`
	Into string `json:"into" help:"id of the pending or approved type proposal that gains its evidence, or the name of a known event type or action that already covers it" required:"true"`
}

type gateParams struct {
	Dataset string `json:"dataset" help:"gold clustering dataset scored by the gate" default:"testdata/clustering_groups.yml" format:"path"`
}

type impactParams struct {
	Days int `json:"days" help:"look-back window in days (1 or more)" default:"14"`
}

// Actions returns events.type_list, .gate, .impact, .type_approve,
// .type_reject, .type_merge and .type_add_phrase, and the rule review actions events.rules_discover,
// .rules_queue, .rules_decide, .rules_reject_failing and .rules_apply.
func Actions() []action.Action {
	return append([]action.Action{
		{
			Name: "events.type_list", Group: group, Title: "List event type proposals",
			Description: "Lists the pending and approved new event type proposals with their evidence article ids, approval window, referential choice and count of pending or approved phrases.",
			Kind:        action.KindRead, Modes: bothModes, Params: noParams{}, Run: runTypeList,
		},
		{
			Name: "events.gate", Group: group, Title: "Clustering gate",
			Description: "Scores the gold clustering dataset without and with the generated rules file and reports the gate verdict. A blocked gate is reported in the result (BLOCKED), not as an error. Nothing is written.",
			Kind:        action.KindRead, Modes: bothModes, Params: gateParams{}, Run: runGate,
		},
		{
			Name: "events.impact", Group: group, Title: "Generated rules impact",
			Description: "Shadow report over the recent articles: which generic articles the generated rules file would type and which typed ones it would change. Nothing is written.",
			Kind:        action.KindRead, Modes: bothModes, LongRunning: true, Params: impactParams{}, Run: runImpact,
		},
		{
			Name: "events.type_approve", Group: group, Title: "Approve event type proposal",
			Description: "Marks a pending new event type proposal as approved with its match window (default 48h) and whether it is referential. Nothing is applied here: an approved type is applied with its first approved phrase by events.rules_apply.",
			Kind:        action.KindWrite, Modes: localOnly, Params: approveParams{}, Run: runApprove,
		},
		{
			Name: "events.type_reject", Group: group, Title: "Reject event type proposal",
			Description: "Marks a pending or approved new event type proposal as rejected, with its pending and approved phrases.",
			Kind:        action.KindWrite, Modes: localOnly, Params: idParams{}, Run: runReject,
		},
		{
			Name: "events.type_merge", Group: group, Title: "Merge event type proposal",
			Description: "Folds a pending or approved new event type proposal into another proposal (which gains its evidence) or into a known event type or action that already covers it. The source is rejected.",
			Kind:        action.KindWrite, Modes: localOnly, Params: mergeParams{}, Run: runMerge,
		},
		{
			Name: "events.type_add_phrase", Group: group, Title: "Add a phrase to an event type",
			Description: "Proposes a trigger phrase for an approved new event type: it is stored pending (prompt version manual, no evidence) and reviewed in the phrase queue with its impact.",
			Kind:        action.KindWrite, Modes: localOnly, Params: addPhraseParams{}, Run: runAddPhrase,
		},
	}, ruleActions()...)
}

func invalid(format string, args ...any) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

// proposalError maps the proposal errors to their action codes and leaves
// the rest as they are.
func proposalError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return action.NotFound(err)
	case errors.Is(err, eventrules.ErrBadMergeTarget), errors.Is(err, eventrules.ErrBadTypeApproval), errors.Is(err, eventrules.ErrEmptyPhrase):
		return &action.Error{Code: action.CodeInvalidParams, Message: err.Error()}
	case errors.Is(err, eventrules.ErrNotTypeProposal), errors.Is(err, eventrules.ErrAlreadyDecided), errors.Is(err, eventrules.ErrTypeNotApproved):
		return action.Conflict(err)
	}
	return err
}

// knownTypeNames returns the known event type and action names of env's
// configured taxonomy.
func knownTypeNames(env action.Env) ([]string, error) {
	if env.Config == nil {
		return nil, errors.New("events: no configuration loaded")
	}
	return eventrules.KnownTypeNames(env.Config)
}

func runTypeList(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	known, err := knownTypeNames(env)
	if err != nil {
		return action.Result{}, err
	}
	ps, err := eventrules.ListTypes(ctx, env.DB)
	if err != nil {
		return action.Result{}, err
	}
	phrases, err := eventrules.TypePhraseCounts(ctx, env.DB)
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		rows = append(rows, map[string]any{
			"id": p.ID, "status": string(p.Status), "type": p.TypeName, "description": p.Description,
			"evidence": strings.Join(p.EvidenceIDs, ", "),
			"window":   p.TypeWindow, "referential": p.TypeReferential, "phrases": phrases[p.TypeName],
		})
	}
	summary := fmt.Sprintf("%d event type proposal(s)", len(rows))
	if len(rows) == 0 {
		summary = "No pending or approved event type proposals."
	}
	return action.Result{
		Object: map[string]any{"known": known},
		Render: "table",
		Columns: []action.Column{
			{Key: "id", Label: "ID"}, {Key: "status", Label: "Status"}, {Key: "type", Label: "Type"},
			{Key: "description", Label: "Description"}, {Key: "evidence", Label: "Evidence"},
			{Key: "window", Label: "Window"}, {Key: "referential", Label: "Referential"}, {Key: "phrases", Label: "Phrases"},
		},
		Rows: rows, Summary: summary,
	}, nil
}

func runApprove(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*approveParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	return runDecide(ctx, env, p.ID, true, eventrules.TypeApproval{Window: p.Window, Referential: p.Referential})
}

func runReject(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*idParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	return runDecide(ctx, env, p.ID, false, eventrules.TypeApproval{})
}

func runDecide(ctx context.Context, env action.Env, id string, approve bool, opts eventrules.TypeApproval) (action.Result, error) {
	known, err := knownTypeNames(env)
	if err != nil {
		return action.Result{}, err
	}
	past := "rejected"
	if approve {
		past = "approved"
	}
	decided, err := eventrules.DecideType(ctx, env.DB, id, approve, opts, known, env.Now().UTC())
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	env.Log.Info("event type proposal decided", "id", decided.ID, "type", decided.TypeName, "status", string(decided.Status), "by", env.Actor)
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"id": decided.ID, "type": decided.TypeName, "status": string(decided.Status),
			"window": decided.TypeWindow, "referential": decided.TypeReferential,
		},
		Summary: fmt.Sprintf("Event type proposal %s (%s) %s.", decided.ID, decided.TypeName, past),
	}, nil
}

func runAddPhrase(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*addPhraseParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	added, err := eventrules.AddTypePhrase(ctx, env.DB, p.ID, p.Phrase, env.Now().UTC())
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	env.Log.Info("event type phrase proposed", "type_id", p.ID, "id", added.ID, "target", added.Target, "by", env.Actor)
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"id": added.ID, "target_kind": string(added.TargetKind), "target": added.Target,
			"phrase": added.Phrase, "status": string(added.Status),
		},
		Summary: fmt.Sprintf("Phrase %q proposed for %s; review it in the queue.", added.Phrase, added.Target),
	}, nil
}

func runMerge(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*mergeParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	known, err := knownTypeNames(env)
	if err != nil {
		return action.Result{}, err
	}
	res, err := eventrules.MergeType(ctx, env.DB, p.ID, p.Into, known, env.Now().UTC())
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	env.Log.Info("event type proposal merged", "id", res.Source.ID, "into", p.Into, "by", env.Actor)
	summary := fmt.Sprintf("Event type proposal %s (%s) merged into %s (%s).", res.Source.ID, res.Source.TypeName, res.Target.ID, res.Target.TypeName)
	if res.CoveredBy != "" {
		summary = fmt.Sprintf("Event type proposal %s (%s) rejected: covered by %s.", res.Source.ID, res.Source.TypeName, res.CoveredBy)
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"id": res.Source.ID, "type": res.Source.TypeName, "status": string(res.Source.Status),
			"into": p.Into, "covered_by": res.CoveredBy,
		},
		Summary: summary,
	}, nil
}

type metricsJSON struct {
	Precision   float64 `json:"precision"`
	Recall      float64 `json:"recall"`
	Purity      float64 `json:"purity"`
	OverGrouped int     `json:"over_grouped"`
	Fragmented  int     `json:"fragmented"`
}

func metricsOf(r clustereval.Report) metricsJSON {
	return metricsJSON{
		Precision: r.Pairwise.Precision(), Recall: r.Pairwise.Recall(), Purity: r.Purity,
		OverGrouped: r.OverGroupedClusters, Fragmented: r.FragmentedClusters,
	}
}

func runGate(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*gateParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("events: no configuration loaded")
	}
	if err := action.RefusePathOverrides(env, action.PathParam{Name: "dataset", Value: p.Dataset, Configured: eventrules.DefaultDatasetPath}); err != nil {
		return action.Result{}, err
	}
	dataset := p.Dataset
	if dataset == "" {
		dataset = eventrules.DefaultDatasetPath
	}
	if env.Config.Clustering.RulesPath == "" {
		return action.Result{}, action.Conflict(errors.New("clustering.rules_path is empty"))
	}
	path := eventrules.GeneratedPath(env.Config)
	rules, err := eventrules.ReadApplied(path)
	if err != nil {
		return action.Result{}, err
	}
	res, err := eventrules.RunGate(ctx, env.Config, dataset, rules)
	if errors.Is(err, eventrules.ErrGateDatasetMissing) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	summary := "PASS"
	if !res.OK {
		summary = "BLOCKED: " + res.Reason
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"generated_rules": path, "phrases": len(rules.Rules),
			"before": metricsOf(res.Before), "after": metricsOf(res.After),
			"dataset": eventrulecycle.GateExercise(res.Affected),
			"ok":      res.OK, "reason": res.Reason,
		},
		Summary: summary,
	}, nil
}

type bucketJSON struct {
	Key    string   `json:"key"`
	Count  int      `json:"count"`
	Titles []string `json:"titles"`
}

// buckets lists each key with its count and the first titles, keys sorted.
func buckets(groups map[string][]string) []bucketJSON {
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]bucketJSON, 0, len(keys))
	for _, k := range keys {
		titles := groups[k]
		out = append(out, bucketJSON{Key: k, Count: len(titles), Titles: titles[:min(len(titles), maxImpactExamples)]})
	}
	return out
}

func runImpact(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*impactParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("events: no configuration loaded")
	}
	if p.Days <= 0 {
		return action.Result{}, invalid("days must be positive")
	}
	rep, err := eventrules.Impact(ctx, env.DB, env.Config, p.Days, env.Now())
	switch {
	case errors.Is(err, eventrules.ErrNoRulesPath):
		return action.Result{}, action.Conflict(err)
	case errors.Is(err, eventrules.ErrInvalidDays):
		return action.Result{}, invalid("%s", err.Error())
	case err != nil:
		return action.Result{}, err
	}
	for _, w := range rep.Warnings {
		env.Log.Warn(w)
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"generated_rules": rep.GeneratedPath, "generated_missing": rep.GeneratedMissing, "days": rep.Days,
			"generic": rep.Generic, "changed": rep.Changed, "generic_truncated": rep.GenericTruncated,
			"typed": rep.Typed, "retyped": rep.Retyped, "typed_truncated": rep.TypedTruncated,
			"moved": buckets(rep.Moved), "retyped_by": buckets(rep.RetypedBy),
			"reframed": rep.Reframed, "reframed_by": buckets(rep.ReframedBy),
		},
		Summary: fmt.Sprintf("last %d day(s): %d of %d generic article(s) would change type, %d of %d typed article(s) would change",
			rep.Days, rep.Changed, rep.Generic, rep.Retyped, rep.Typed),
	}, nil
}
