// Package events holds the admin actions over event rule discovery: the new
// event type proposals, the clustering gate and the impact report of the
// generated rules. They mirror the `events type list|approve|reject`,
// `events brief`, `events gate` and `events impact` CLI commands. Reads are
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

type gateParams struct {
	Dataset string `json:"dataset" help:"gold clustering dataset scored by the gate" default:"testdata/clustering_groups.yml" format:"path"`
}

type impactParams struct {
	Days int `json:"days" help:"look-back window in days (1 or more)" default:"14"`
}

// Actions returns events.type_list, .brief, .gate, .impact, .type_approve
// and .type_reject, and the rule review actions events.rules_discover,
// .rules_queue, .rules_decide, .rules_reject_failing and .rules_apply.
func Actions() []action.Action {
	return append([]action.Action{
		{
			Name: "events.type_list", Group: group, Title: "List event type proposals",
			Description: "Lists the pending and approved new event type proposals with their evidence article ids.",
			Kind:        action.KindRead, Modes: bothModes, Params: noParams{}, Run: runTypeList,
		},
		{
			Name: "events.brief", Group: group, Title: "Event type brief",
			Description: "Returns the implementation brief of a pending or approved new event type proposal.",
			Kind:        action.KindRead, Modes: bothModes, Params: idParams{}, Run: runBrief,
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
			Description: "Marks a pending new event type proposal as approved. Nothing is applied: an approved type is implemented through its brief.",
			Kind:        action.KindWrite, Modes: localOnly, Params: idParams{},
			Run: func(ctx context.Context, env action.Env, params any) (action.Result, error) {
				return runDecide(ctx, env, params, true)
			},
		},
		{
			Name: "events.type_reject", Group: group, Title: "Reject event type proposal",
			Description: "Marks a pending or approved new event type proposal as rejected.",
			Kind:        action.KindWrite, Modes: localOnly, Params: idParams{},
			Run: func(ctx context.Context, env action.Env, params any) (action.Result, error) {
				return runDecide(ctx, env, params, false)
			},
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
	case errors.Is(err, eventrules.ErrNotTypeProposal), errors.Is(err, eventrules.ErrNoBrief), errors.Is(err, eventrules.ErrAlreadyDecided):
		return action.Conflict(err)
	}
	return err
}

func runTypeList(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	ps, err := eventrules.ListTypes(ctx, env.DB)
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		rows = append(rows, map[string]any{
			"id": p.ID, "status": string(p.Status), "type": p.TypeName, "description": p.Description,
			"evidence": strings.Join(p.EvidenceIDs, ", "),
		})
	}
	summary := fmt.Sprintf("%d event type proposal(s)", len(rows))
	if len(rows) == 0 {
		summary = "No pending or approved event type proposals."
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "id", Label: "ID"}, {Key: "status", Label: "Status"}, {Key: "type", Label: "Type"},
			{Key: "description", Label: "Description"}, {Key: "evidence", Label: "Evidence"},
		},
		Rows: rows, Summary: summary,
	}, nil
}

func runBrief(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*idParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	brief, err := eventrules.Brief(ctx, env.DB, p.ID)
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	return action.Result{Render: "text", Text: brief, Summary: "brief for event type proposal " + p.ID}, nil
}

func runDecide(ctx context.Context, env action.Env, params any, approve bool) (action.Result, error) {
	p, ok := params.(*idParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	past := "rejected"
	if approve {
		past = "approved"
	}
	decided, err := eventrules.DecideType(ctx, env.DB, p.ID, approve, env.Now().UTC())
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	env.Log.Info("event type proposal decided", "id", decided.ID, "type", decided.TypeName, "status", string(decided.Status), "by", env.Actor)
	return action.Result{
		Render:  "object",
		Object:  map[string]any{"id": decided.ID, "type": decided.TypeName, "status": string(decided.Status)},
		Summary: fmt.Sprintf("Event type proposal %s (%s) %s.", decided.ID, decided.TypeName, past),
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
			"generated_rules": path, "phrases": len(rules),
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
		},
		Summary: fmt.Sprintf("last %d day(s): %d of %d generic article(s) would change type, %d of %d typed article(s) would change",
			rep.Days, rep.Changed, rep.Generic, rep.Retyped, rep.Typed),
	}, nil
}
