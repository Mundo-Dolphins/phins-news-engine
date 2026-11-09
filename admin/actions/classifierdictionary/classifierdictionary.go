// Package classifierdictionary holds the admin actions over the classifier
// dictionary proposals. They mirror the `entities classifier-dictionary
// analyze|list|show|approve|reject|metrics` CLI commands. Approving or
// rejecting a proposal, and mining new ones, are local-only: proposals are
// reviewed on the synced local copy and approval rewrites the dictionary file.
package classifierdictionary

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteclassifierproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteclassifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/classifierdictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/app/lookback"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
)

const group = "classifier_dictionary"

// allArticlesLimit is the article cap used when all is set, as in the CLI.
const allArticlesLimit = 10_000_000

var errBadParams = errors.New("classifier_dictionary: unexpected params type")

var (
	bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}
	localOnly = []action.Mode{action.ModeLocal}
)

type analyzeParams struct {
	Since            string `json:"since" help:"only consider articles fetched since this duration (24h, 7d, 30d) or RFC3339 date; empty = no lower bound" format:"duration|date"`
	All              bool   `json:"all" help:"consider every stored article, with no limit cap (since is ignored)"`
	Force            bool   `json:"force" help:"reprocess articles already marked processed for this analyzer version"`
	Limit            int    `json:"limit" help:"maximum number of articles to consider in this run (ignored with all)" default:"500"`
	MinOccurrences   int    `json:"min_occurrences" help:"minimum repeated evidence count before a proposal is created" default:"2"`
	AnalyzerVersion  string `json:"analyzer_version" help:"version tag for incremental analysis" default:"classifier-dictionary-v1"`
	Dictionary       string `json:"dictionary" help:"team dictionary YAML file (default dictionary.path)" format:"path"`
	PositiveCategory string `json:"positive_category" help:"dictionary category for positive classifier term proposals" default:"nfl_people_context"`
	NegativeCategory string `json:"negative_category" help:"dictionary category for negative classifier term proposals" default:"negative_other_sports_terms"`
}

type listParams struct {
	Kind   string `json:"kind" help:"only proposals of this kind"`
	Status string `json:"status" help:"only proposals in this status (all = every status)" enum:"pending,approved,rejected,all" default:"pending"`
}

type idParams struct {
	ID string `json:"id" help:"proposal id" required:"true"`
}

type approveParams struct {
	ID         string `json:"id" help:"proposal id" required:"true"`
	Dictionary string `json:"dictionary" help:"team dictionary YAML file (default dictionary.path)" format:"path"`
}

type metricsParams struct {
	Since string `json:"since" help:"how far back to aggregate, as a Go duration (168h, not 7d)" format:"duration" default:"168h"`
}

// Actions returns the classifier dictionary actions:
// classifier_dictionary.analyze, .list, .show, .approve, .reject and
// .metrics.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "classifier_dictionary.analyze", Group: group, Title: "Analyze classifier dictionary",
			Description: "Creates pending classifier dictionary proposals from stored article evidence.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true,
			Params: analyzeParams{}, Run: runAnalyze,
		},
		{
			Name: "classifier_dictionary.list", Group: group, Title: "List classifier dictionary proposals",
			Description: "Lists classifier dictionary proposals, best-scored first.",
			Kind:        action.KindRead, Modes: bothModes, Params: listParams{}, Run: runList,
		},
		{
			Name: "classifier_dictionary.show", Group: group, Title: "Show classifier dictionary proposal",
			Description: "Shows one classifier dictionary proposal with its metadata and evidence.",
			Kind:        action.KindRead, Modes: bothModes, Params: idParams{}, Run: runShow,
		},
		{
			Name: "classifier_dictionary.approve", Group: group, Title: "Approve classifier dictionary proposal",
			Description: "Approves a pending classifier dictionary proposal and writes it to the dictionary.",
			Kind:        action.KindWrite, Modes: localOnly, Params: approveParams{}, Run: runApprove,
		},
		{
			Name: "classifier_dictionary.reject", Group: group, Title: "Reject classifier dictionary proposal",
			Description: "Rejects a pending classifier dictionary proposal; the dictionary is not touched.",
			Kind:        action.KindWrite, Modes: localOnly, Params: idParams{}, Run: runReject,
		},
		{
			Name: "classifier_dictionary.metrics", Group: group, Title: "Classifier dictionary metrics",
			Description: "Reports catalog analysis and proposal metrics over a recent window.",
			Kind:        action.KindRead, Modes: bothModes, Params: metricsParams{}, Run: runMetrics,
		},
	}
}

func repo(env action.Env) *sqliteclassifierproposal.ClassifierProposalRepository {
	return sqliteclassifierproposal.NewClassifierProposalRepository(env.DB)
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// proposalError maps the repository and review errors of one proposal.
func proposalError(err error) error {
	switch {
	case errors.Is(err, classifierproposal.ErrNotFound):
		return action.NotFound(err)
	case errors.Is(err, classifierdictionary.ErrNotPending):
		return action.Conflict(err)
	}
	return err
}

func runAnalyze(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*analyzeParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	since, err := lookback.Parse(p.Since, env.Now().UTC())
	if err != nil {
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: "since: " + err.Error(), Err: err}
	}
	limit := p.Limit
	if p.All {
		since = time.Time{}
		limit = allArticlesLimit
	}
	dict, err := classifierdictionary.LoadValidated(or(p.Dictionary, env.Config.Dictionary.Path))
	if err != nil {
		return action.Result{}, err
	}
	res, err := classifierdictionary.Analyze(ctx, sqliteadapter.NewArticleRepository(env.DB), repo(env), dict,
		classifierdictionary.AnalyzeOptions{
			Since: since, Limit: limit, Force: p.Force, AnalyzerVersion: p.AnalyzerVersion,
			MinOccurrences: p.MinOccurrences, PositiveCategory: p.PositiveCategory, NegativeCategory: p.NegativeCategory,
		}, env.Now().UTC())
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render: "object", Object: res,
		Summary: fmt.Sprintf("%d articles, %d article proposals, %d unknown-entity proposals",
			res.ArticlesConsidered, res.ArticleProposalsUpserted, res.UnknownProposalsUpserted),
	}, nil
}

func runList(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*listParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	status := p.Status
	if status == "all" {
		status = ""
	}
	proposals, err := repo(env).List(ctx, p.Kind, status)
	if err != nil {
		return action.Result{}, err
	}
	sort.SliceStable(proposals, func(i, j int) bool {
		return classifierproposal.Score(proposals[i]) > classifierproposal.Score(proposals[j])
	})
	rows := make([]map[string]any, 0, len(proposals))
	for _, pr := range proposals {
		rows = append(rows, map[string]any{
			"id": pr.ID, "kind": string(pr.Kind), "category": pr.Category, "value": pr.Value,
			"occurrences": pr.Occurrences, "confidence": fmt.Sprintf("%.2f", pr.Confidence), "status": string(pr.Status),
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "id", Label: "ID"}, {Key: "kind", Label: "Kind"}, {Key: "category", Label: "Category"},
			{Key: "value", Label: "Value"}, {Key: "occurrences", Label: "Occurrences"},
			{Key: "confidence", Label: "Confidence"}, {Key: "status", Label: "Status"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("%d proposal(s)", len(rows)),
	}, nil
}

type evidenceJSON struct {
	ArticleID    string `json:"article_id"`
	ArticleTitle string `json:"article_title"`
}

type proposalJSON struct {
	ID          string            `json:"id"`
	Value       string            `json:"value"`
	Kind        string            `json:"kind"`
	Category    string            `json:"category"`
	Operation   string            `json:"operation"`
	TargetEntry string            `json:"target_entry,omitempty"`
	Metadata    map[string]string `json:"metadata"`
	Occurrences int               `json:"occurrences"`
	Confidence  float64           `json:"confidence"`
	Status      string            `json:"status"`
	FirstSeen   string            `json:"first_seen"`
	LastSeen    string            `json:"last_seen"`
	Evidence    []evidenceJSON    `json:"evidence"`
}

func runShow(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*idParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	pr, err := repo(env).Get(ctx, p.ID)
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	obj := proposalJSON{
		ID: pr.ID, Value: pr.Value, Kind: string(pr.Kind), Category: pr.Category, Operation: pr.Operation,
		TargetEntry: pr.TargetEntry, Metadata: map[string]string{}, Occurrences: pr.Occurrences,
		Confidence: pr.Confidence, Status: string(pr.Status),
		FirstSeen: pr.FirstSeen.Format("2006-01-02"), LastSeen: pr.LastSeen.Format("2006-01-02"),
		Evidence: []evidenceJSON{},
	}
	for k, v := range pr.Metadata {
		obj.Metadata[k] = v
	}
	for _, s := range pr.Sources {
		obj.Evidence = append(obj.Evidence, evidenceJSON{ArticleID: s.ArticleID, ArticleTitle: s.ArticleTitle})
	}
	return action.Result{Render: "object", Object: obj,
		Summary: fmt.Sprintf("%s (%s, %s): %s", pr.Value, pr.Kind, pr.Category, pr.Status)}, nil
}

func runApprove(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*approveParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	action.ConfigFilesMu.Lock()
	defer action.ConfigFilesMu.Unlock()
	pr, err := classifierdictionary.Approve(ctx, repo(env), p.ID, or(p.Dictionary, env.Config.Dictionary.Path), env.Now().UTC())
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	env.Log.Info("classifier dictionary proposal approved", "id", pr.ID, "value", pr.Value, "by", env.Actor)
	return action.Result{Render: "text", Text: fmt.Sprintf("approved %s (%s)", pr.Value, pr.ID)}, nil
}

func runReject(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*idParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	pr, err := classifierdictionary.Reject(ctx, repo(env), p.ID, env.Now().UTC())
	if err != nil {
		return action.Result{}, proposalError(err)
	}
	env.Log.Info("classifier dictionary proposal rejected", "id", pr.ID, "value", pr.Value, "by", env.Actor)
	return action.Result{Render: "text", Text: fmt.Sprintf("rejected %s (%s)", pr.Value, pr.ID)}, nil
}

func runMetrics(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*metricsParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	d, err := time.ParseDuration(p.Since)
	if err != nil {
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: "since: " + err.Error(), Err: err}
	}
	report, err := repo(env).Metrics(ctx, env.Now().UTC().Add(-d))
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render: "object", Object: report,
		Summary: fmt.Sprintf("%d proposals, %d approved, %d rejected",
			report.ProposalsTotal, report.ProposalsApprovedTotal, report.ProposalsRejectedTotal),
	}, nil
}
