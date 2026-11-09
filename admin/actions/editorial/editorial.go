// Package editorial holds the admin actions over the editorial classifier:
// the reports (`editorial metrics`, `editorial-review`, `editorial canary
// status|promote|abort`, `editorial veto-report`) and the two LLM teacher
// runs (`editorial classify`, `editorial classify-eval`). The reports are
// read-only and available in both modes; canary promote and abort only
// print the settings change to make, like the CLI. The dataset reader and
// the LLM runs are local-only.
package editorial

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialmetrics"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/lookback"
	infraeditorialveto "github.com/Mundo-Dolphins/phins-news-engine/infra/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

const group = "editorial"

var errBadParams = errors.New("editorial: unexpected params type")

var (
	bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}
	localOnly = []action.Mode{action.ModeLocal}
)

type metricsParams struct {
	Since string `json:"since" help:"how far back to aggregate, as a Go duration (24h, 168h)" format:"duration" default:"24h"`
}

type reviewParams struct {
	Days  int `json:"days" help:"how many days back to report" default:"7"`
	Limit int `json:"limit" help:"maximum number of decisions to report" default:"50"`
}

type promoteParams struct {
	Force bool `json:"force" help:"approve promotion even if the canary criteria are failing (logged as a warning)"`
}

type vetoParams struct {
	Dataset    string `json:"dataset" help:"exported editorial dataset (YAML); never reads live SQLite" format:"path" default:"testdata/editorial_seed_dataset.yml"`
	VetoConfig string `json:"veto_config" help:"deterministic veto config (default editorial.veto_path)" format:"path"`
}

type classifyParams struct {
	Since              string `json:"since" help:"only consider articles fetched since this duration (24h, 7d, 30d) or RFC3339 date; empty = no lower bound" format:"duration|date"`
	All                bool   `json:"all" help:"consider every stored article, with no limit cap (since is ignored)"`
	Force              bool   `json:"force" help:"reprocess articles already marked processed for the current prompt version"`
	BatchSize          int    `json:"batch_size" help:"articles per LLM request (0 = offline_training.llm.batch_size, else 20)" default:"0"`
	Limit              int    `json:"limit" help:"maximum number of articles to consider (0 = offline_training.llm.limit, else 500; ignored with all)" default:"0"`
	LLMBaseURL         string `json:"llm_base_url" help:"override offline_training.llm.base_url for this run (empty = configured value)"`
	LLMModel           string `json:"llm_model" help:"override offline_training.llm.model for this run (empty = configured value)"`
	LLMMaxTokens       int    `json:"llm_max_tokens" help:"maximum LLM response tokens for this run (0 = configured value, else the provider default)" default:"0"`
	LLMDisableThinking bool   `json:"llm_disable_thinking" help:"ask compatible reasoning models (such as Ollama) not to generate thinking output"`
}

type classifyEvalParams struct {
	Dataset         string  `json:"dataset" help:"dataset with human labels to evaluate against" format:"path" default:"data/editorial_dataset-reviewed.yml"`
	Fold            string  `json:"fold" help:"dev, holdout (disjoint halves chosen by article id hash) or all; tune on dev, confirm on holdout" enum:"dev,holdout,all" default:"dev"`
	Sample          int     `json:"sample" help:"evaluate a random sample of this many examples (0 = all in the fold)" default:"0"`
	Seed            int64   `json:"seed" help:"seed for sample" default:"1"`
	Threshold       float64 `json:"threshold" help:"confidence at or above which a discard counts as confident" default:"0.95"`
	BatchSize       int     `json:"batch_size" help:"articles per LLM request" default:"20"`
	PolicyFile      string  `json:"policy_file" help:"evaluate this policy markdown instead of the embedded one" format:"path"`
	MissesOut       string  `json:"misses_out" help:"write the confident discards that disagree with the human label to this CSV" format:"path"`
	IncludeImported bool    `json:"include_imported" help:"also evaluate telegramimport publish labels (posted to the channel, not human-reviewed)"`
	LLMBaseURL      string  `json:"llm_base_url" help:"override offline_training.llm.base_url for this run (empty = configured value)"`
	LLMModel        string  `json:"llm_model" help:"override offline_training.llm.model for this run (empty = configured value)"`
}

// Actions returns the editorial actions: editorial.metrics,
// editorial.review, editorial.canary_status, editorial.canary_promote,
// editorial.canary_abort, editorial.veto_report, editorial.classify and
// editorial.classify_eval.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "editorial.metrics", Group: group, Title: "Editorial metrics",
			Description: "Reports editorial-classifier decision volume, zone rates and score distribution over a time window.",
			Kind:        action.KindRead, Modes: bothModes, Params: metricsParams{}, Run: runMetrics,
		},
		{
			Name: "editorial.review", Group: group, Title: "Review editorial decisions",
			Description: "Lists recent editorial-classifier decisions against each article's actual outcome.",
			Kind:        action.KindRead, Modes: bothModes, Params: reviewParams{}, Run: runReview,
		},
		{
			Name: "editorial.canary_status", Group: group, Title: "Canary status",
			Description: "Evaluates real canary-arm data against editorial_rollout's configured criteria.",
			Kind:        action.KindRead, Modes: bothModes, Run: runCanaryStatus,
		},
		{
			Name: "editorial.canary_promote", Group: group, Title: "Approve canary promotion",
			Description: "Approves promotion to enforce when the canary criteria pass (or with force). Only prints the settings change to make; nothing is edited or deployed.",
			Kind:        action.KindRead, Modes: bothModes, Params: promoteParams{}, Run: runCanaryPromote,
		},
		{
			Name: "editorial.canary_abort", Group: group, Title: "Abort canary",
			Description: "Prints the steps to return editorial.mode to shadow. Nothing is edited or deployed.",
			Kind:        action.KindRead, Modes: bothModes, Run: runCanaryAbort,
		},
		{
			Name: "editorial.veto_report", Group: group, Title: "Veto impact report",
			Description: "Dry-runs the deterministic editorial vetoes against an exported dataset.",
			Kind:        action.KindRead, Modes: localOnly, Params: vetoParams{}, Run: runVetoReport,
		},
		{
			Name: "editorial.classify", Group: group, Title: "Classify stored news",
			Description: "Classifies stored news publish/discard/uncertain through the independent LLM teacher, saving label proposals and syncing the review queue.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true,
			Params: classifyParams{}, Run: runClassify,
		},
		{
			Name: "editorial.classify_eval", Group: group, Title: "Evaluate the classifier prompt",
			Description: "Measures the LLM teacher prompt against human-reviewed dataset labels; writes nothing to the database (may write the misses CSV).",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true,
			Params: classifyEvalParams{}, Run: runClassifyEval,
		},
	}
}

func invalid(field string, err error) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: field + ": " + err.Error(), Err: err}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func runMetrics(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*metricsParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	d, err := time.ParseDuration(p.Since)
	if err != nil {
		return action.Result{}, invalid("since", err)
	}
	rep, err := editorialmetrics.Metrics(ctx, sqliteadapter.NewDecisionRepository(env.DB), env.Now().UTC().Add(-d))
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render: "object", Object: rep,
		Summary: fmt.Sprintf("total=%d reject_rate=%.3f ambiguous_rate=%.3f", rep.Total, rep.RejectRate, rep.AmbiguousRate),
	}, nil
}

func runReview(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*reviewParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	since := env.Now().UTC().AddDate(0, 0, -p.Days)
	rep, err := editorialmetrics.Review(ctx, sqliteadapter.NewDecisionRepository(env.DB), sqliteadapter.NewArticleRepository(env.DB), since, p.Limit)
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(rep.Rows))
	for _, r := range rep.Rows {
		rows = append(rows, map[string]any{
			"occurred_at": r.OccurredAt.Format(time.RFC3339), "event_type": r.EventType,
			"score": fmt.Sprintf("%.1f", r.Score), "actual_status": r.ActualStatus, "title": r.Title,
		})
	}
	summary := fmt.Sprintf("%d decisions in the last %d days", len(rep.Rows), p.Days)
	var counts []string
	for _, et := range editorialmetrics.ReviewEventTypes {
		if n := rep.Counts[et]; n > 0 {
			counts = append(counts, fmt.Sprintf("%s: %d", et, n))
		}
	}
	if len(counts) > 0 {
		summary += " (" + strings.Join(counts, ", ") + ")"
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "occurred_at", Label: "Occurred at"}, {Key: "event_type", Label: "Event"}, {Key: "score", Label: "Score"},
			{Key: "actual_status", Label: "Actual status"}, {Key: "title", Label: "Title"},
		},
		Rows: rows, Summary: summary,
	}, nil
}

func canary(ctx context.Context, env action.Env) (editorialmetrics.CanaryResult, error) {
	return editorialmetrics.EvaluateCanary(ctx, sqliteadapter.NewDecisionRepository(env.DB), env.Config.EditorialRollout, env.Now().UTC())
}

func canaryCounts(r editorialmetrics.CanaryResult) string {
	return fmt.Sprintf("articles=%d elapsed_days=%d precision=n/a (not evaluated) false_positive_rate=n/a (not evaluated)", r.ArticleCount, r.ElapsedDays)
}

func runCanaryStatus(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	r, err := canary(ctx, env)
	if err != nil {
		return action.Result{}, err
	}
	verdict := "PASS"
	if !r.Passed {
		verdict = "FAIL"
	}
	return action.Result{Render: "object", Object: r, Summary: verdict + " " + editorialmetrics.CanaryCoverageNote}, nil
}

func runCanaryPromote(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*promoteParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	r, err := canary(ctx, env)
	if err != nil {
		return action.Result{}, err
	}
	if !r.Passed && !p.Force {
		return action.Result{}, action.Conflict(fmt.Errorf("criteria failing (%v); re-run with force to override", r.Failures))
	}
	if p.Force {
		env.Log.Warn("canary promotion forced", "actor", env.Actor, "criteria_passed", r.Passed, "failures", r.Failures)
	}
	text := fmt.Sprintf("promotion approved (forced=%v) — set editorial.mode: enforce in the settings file and deploy (tools CLI deploy run / editorial cycle)\n  %s\n  %s",
		p.Force, canaryCounts(r), editorialmetrics.CanaryCoverageNote)
	return action.Result{Render: "text", Text: text}, nil
}

func runCanaryAbort(context.Context, action.Env, any) (action.Result, error) {
	return action.Result{
		Render: "text",
		Text:   "set editorial.mode: shadow in the settings file and deploy; run train.rollback if the promoted model itself needs reverting",
	}, nil
}

func runVetoReport(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*vetoParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	vetoCfg, err := infraeditorialveto.Load(or(p.VetoConfig, env.Config.Editorial.VetoPath))
	if err != nil {
		return action.Result{}, err
	}
	dataset, err := editorialtrain.LoadDataset(p.Dataset)
	if err != nil {
		return action.Result{}, err
	}
	rep := editorialmetrics.VetoImpact(dataset.Examples, vetoCfg, p.Dataset)
	return action.Result{
		Render: "object", Object: rep,
		Summary: fmt.Sprintf("blocked=%d/%d reduction=%.3f", rep.Blocked, rep.Total, rep.Reduction),
	}, nil
}

func runClassify(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*classifyParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	f := trainwiring.ClassifyFlags{
		All: p.All, Force: p.Force, BatchSize: p.BatchSize, Limit: p.Limit,
		BaseURL: p.LLMBaseURL, Model: p.LLMModel,
	}
	if p.LLMMaxTokens != 0 {
		f.MaxTokens = &p.LLMMaxTokens
	}
	if p.LLMDisableThinking {
		f.DisableThinking = &p.LLMDisableThinking
	}
	// Config problems first, as the CLI reports them before --since.
	if _, err := trainwiring.ClassifyLLMConfig(env.Config, f); err != nil {
		return action.Result{}, action.Conflict(err)
	}
	if !p.All {
		since, err := lookback.Parse(p.Since, env.Now().UTC())
		if err != nil {
			return action.Result{}, invalid("since", err)
		}
		f.Since = since
	}
	rep, err := trainwiring.Classify(ctx, env.DB, env.Config, f, env.Now().UTC())
	r := rep.Result
	for _, e := range r.BatchErrors {
		env.Log.Warn("classify batch failed", "error", e)
	}
	for _, e := range r.LabelErrors {
		env.Log.Warn("classify label error", "error", e)
	}
	if err != nil {
		if errors.Is(err, trainwiring.ErrReviewQueueSync) {
			env.Log.Warn("classify saved proposals but the review queue sync failed", "error", err,
				"articles_considered", r.ArticlesConsidered, "proposals_saved", r.ProposalsSaved,
				"batches_total", r.BatchesTotal, "batches_failed", r.BatchesFailed)
		}
		return action.Result{}, err
	}
	queue := "skipped: " + rep.SyncSkipped
	if rep.Sync != nil {
		queue = fmt.Sprintf("%d queued", rep.Sync.Queued)
	}
	summary := fmt.Sprintf("%d proposals saved, review queue %s", r.ProposalsSaved, queue)
	if r.BatchesFailed > 0 {
		summary = fmt.Sprintf("WARNING: %d of %d batches failed; %s", r.BatchesFailed, r.BatchesTotal, summary)
	}
	return action.Result{Render: "object", Object: rep, Summary: summary}, nil
}

func runClassifyEval(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*classifyEvalParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	llm := env.Config.OfflineTraining.LLM
	if or(p.LLMBaseURL, llm.BaseURL) == "" || or(p.LLMModel, llm.Model) == "" || llm.TimeoutSeconds <= 0 {
		return action.Result{}, action.Conflict(errors.New("offline_training.llm base_url, model and timeout_seconds are required"))
	}
	rep, err := trainwiring.ClassifyEval(ctx, env.Config, trainwiring.ClassifyEvalFlags{
		DatasetPath: p.Dataset, Fold: p.Fold, PolicyFile: p.PolicyFile, MissesOut: p.MissesOut,
		BaseURL: p.LLMBaseURL, Model: p.LLMModel, Sample: p.Sample, BatchSize: p.BatchSize,
		Seed: p.Seed, Threshold: p.Threshold, IncludeImported: p.IncludeImported,
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "no examples selected") {
			return action.Result{}, action.Conflict(err)
		}
		return action.Result{}, err
	}
	s := rep.Summary
	summary := fmt.Sprintf("precision=%.3f recall=%.3f on %d examples", s.ConfidentDiscardPrecision(), s.RejectRecall(), rep.Examples)
	if rep.Report.BatchesFailed > 0 {
		for _, e := range rep.Report.BatchErrors {
			env.Log.Warn("classify-eval batch failed", "error", e)
		}
		summary = fmt.Sprintf("WARNING: %d of %d batches failed; %s", rep.Report.BatchesFailed, rep.Report.BatchesTotal, summary)
	}
	return action.Result{Render: "object", Object: rep, Summary: summary}, nil
}
