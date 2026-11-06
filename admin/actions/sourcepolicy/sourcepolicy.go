// Package sourcepolicy holds the admin actions over per-source deduplication
// and publication policies: validating the policy file, inspecting a
// source's effective policy, auditing recorded decisions and simulating one
// article. They mirror the `source-policy validate`, `inspect`, `audit` and
// `simulate` CLI commands.
package sourcepolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/usecase"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcepolicyfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/sourcesfile"
)

const group = "source_policy"

const dateLayout = "2006-01-02"

var errBadParams = errors.New("source_policy: unexpected params type")

var bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}

type noParams struct{}

type inspectParams struct {
	Source string `json:"source" help:"source_id to inspect, e.g. rss:sprisotopes" required:"true"`
}

type auditParams struct {
	From   string `json:"from" help:"start date, YYYY-MM-DD" format:"date" required:"true"`
	To     string `json:"to" help:"end date, YYYY-MM-DD" format:"date" required:"true"`
	Source string `json:"source" help:"filter by source_id"`
	Mode   string `json:"mode" help:"filter by policy mode" enum:"disabled,shadow,enforce"`
}

type simulateParams struct {
	ArticleID string `json:"article_id" help:"article whose recorded source-policy decisions to show" required:"true"`
}

// Actions returns the source-policy actions: source_policy.validate,
// source_policy.inspect, source_policy.audit and source_policy.simulate.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "source_policy.validate", Group: group, Title: "Validate source policies",
			Description: "Validates the source-policy file (schema and orphaned policies); issues are listed as rows, not errors.",
			Kind:        action.KindRead, Modes: bothModes, Params: noParams{}, Run: runValidate,
		},
		{
			Name: "source_policy.inspect", Group: group, Title: "Inspect source policy",
			Description: "Shows the effective resolved policy for one source.",
			Kind:        action.KindRead, Modes: bothModes, Params: inspectParams{}, Run: runInspect,
		},
		{
			Name: "source_policy.audit", Group: group, Title: "Audit source policy",
			Description: "Summarizes the source-policy decisions recorded over a date range.",
			Kind:        action.KindRead, Modes: bothModes, Params: auditParams{}, Run: runAudit,
		},
		{
			Name: "source_policy.simulate", Group: group, Title: "Simulate source policy",
			Description: "Shows every recorded source-policy decision for one article.",
			Kind:        action.KindRead, Modes: bothModes, Params: simulateParams{}, Run: runSimulate,
		},
	}
}

func runValidate(_ context.Context, env action.Env, params any) (action.Result, error) {
	if _, ok := params.(*noParams); !ok {
		return action.Result{}, errBadParams
	}
	sources, err := sourcesfile.Load(env.Config.Fetch.SourcesPath)
	if err != nil {
		return action.Result{}, err
	}
	policies, err := sourcepolicyfile.Load(env.Config.SourcePolicy.Path)
	if err != nil {
		return action.Result{}, err
	}
	issues := usecase.ValidateSourcePolicies(sources, policies).Issues
	rows := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		rows = append(rows, map[string]any{"issue": issue})
	}
	summary := "OK"
	if len(issues) > 0 {
		summary = fmt.Sprintf("%d issue(s)", len(issues))
	}
	return action.Result{
		Render:  "table",
		Columns: []action.Column{{Key: "issue", Label: "Issue"}},
		Rows:    rows,
		Summary: summary,
	}, nil
}

func runInspect(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*inspectParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	file, err := sourcepolicyfile.Load(env.Config.SourcePolicy.Path)
	if err != nil {
		return action.Result{}, err
	}
	resolver := sourcepolicyfile.NewResolver(file)
	policy := resolver.Resolve(p.Source)
	return action.Result{Render: "object", Object: map[string]any{
		"source_id":                   fmt.Sprint(policy.SourceID),
		"authority":                   fmt.Sprint(policy.Authority),
		"mode":                        fmt.Sprint(resolver.Mode()),
		"retain_near_duplicates":      policy.RetainNearDuplicates,
		"representative_preference":   fmt.Sprint(policy.RepresentativePreference),
		"allow_official_confirmation": policy.AllowOfficialConfirmation,
		"allow_substantial_update":    policy.AllowSubstantialUpdate,
		"publish_confirmation_without_new_information": policy.PublishConfirmationWithoutNewInformation,
		"minimum_update_novelty":                       policy.MinimumUpdateNovelty,
		"event_cooldown":                               fmt.Sprint(policy.EventCooldown),
	}}, nil
}

func parseDate(field, v string) (time.Time, error) {
	t, err := time.Parse(dateLayout, v)
	if err != nil {
		err = fmt.Errorf("source_policy: %s: want YYYY-MM-DD: %w", field, err)
		return time.Time{}, &action.Error{Code: action.CodeInvalidParams, Message: err.Error(), Err: err}
	}
	return t, nil
}

func runAudit(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*auditParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	from, err := parseDate("from", p.From)
	if err != nil {
		return action.Result{}, err
	}
	to, err := parseDate("to", p.To)
	if err != nil {
		return action.Result{}, err
	}
	report, err := usecase.AuditSourcePolicies(ctx, sqliteadapter.NewSourcePolicyRepository(env.DB), from, to, p.Source, p.Mode)
	if err != nil {
		return action.Result{}, err
	}
	// Same rename as the CLI: the empty outcome means "not applicable".
	cooldown := make(map[string]int, len(report.ByCooldownOutcome))
	for outcome, n := range report.ByCooldownOutcome {
		if outcome == "" {
			outcome = "not_applicable"
		}
		cooldown[outcome] += n
	}
	return action.Result{Render: "object", Object: map[string]any{
		"total_decisions":     report.TotalDecisions,
		"divergences":         report.Divergences,
		"by_decision_code":    report.ByDecisionCode,
		"by_cooldown_outcome": cooldown,
		"by_source":           report.BySource,
	}}, nil
}

func runSimulate(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*simulateParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	sim, err := usecase.SimulateSourcePolicy(ctx, sqliteadapter.NewSourcePolicyRepository(env.DB), sqliteadapter.NewArticleRepository(env.DB), p.ArticleID)
	if errors.Is(err, sql.ErrNoRows) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(sim.Decisions))
	for _, d := range sim.Decisions {
		rows = append(rows, map[string]any{
			"decided_at": d.DecidedAt.Format(time.RFC3339), "match_type": orDash(d.DedupMatchType), "code": d.DecisionCode,
			"standard": d.WouldPublishStandard, "special": d.WouldPublishSpecial, "mode": d.Mode, "explanation": orDash(d.Explanation),
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "decided_at", Label: "Decided at"}, {Key: "match_type", Label: "Match"}, {Key: "code", Label: "Code"},
			{Key: "standard", Label: "Standard"}, {Key: "special", Label: "Special"}, {Key: "mode", Label: "Mode"},
			{Key: "explanation", Label: "Explanation"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("article %s (source %s)", sim.ArticleID, sim.SourceID),
	}, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
