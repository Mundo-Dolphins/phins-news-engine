// Package train holds the admin actions over the Naive Bayes training
// workflow: analysing LLM signals, building datasets, evaluating candidates,
// mining hard examples, shadow-comparing models, running the full pipeline,
// promoting a candidate model and rolling back. They mirror the `train
// analyze|build|evaluate|mine|shadow-compare|run|promote|rollback` CLI
// commands, plus `train audit-dataset|audit-gates|recheck-corpus|
// backfill-rejects|import-labels`. Write actions are local-only: training and
// promotion never happen on the production Pi.
package train

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainanalyze"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainrun"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const group = "train"

var errBadParams = errors.New("train: unexpected params type")

var (
	bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}
	localOnly = []action.Mode{action.ModeLocal}
)

type runParams struct {
	Force         bool     `json:"force" help:"promote even if the candidate fails the offline_training.promotion criteria; editorial_gates are a hard floor this cannot bypass"`
	Auto          bool     `json:"auto" help:"only run if offline_training.retrain_triggers.auto_enabled is true and a configured trigger fires"`
	Dataset       string   `json:"dataset" help:"path to write the built dataset to" default:"data/editorial_dataset.yml"`
	Candidates    string   `json:"candidates" help:"directory to write trained candidates to" default:"models/editorial/candidates"`
	MaxPerCluster int      `json:"max_per_cluster" help:"maximum number of examples kept from any one event cluster" default:"1"`
	IncludeDB     []string `json:"include_db" help:"read-only SQLite snapshots whose human labels should be merged into Gold"`
}

type promoteParams struct {
	CandidateDir string `json:"candidate_dir" help:"directory of the candidate model to promote" required:"true" format:"path"`
	Version      string `json:"version" help:"version to assign (default: the candidate's own manifest version)"`
	Force        bool   `json:"force" help:"promote even if the candidate fails one or more quality gates"`
}

type buildParams struct {
	Out           string   `json:"out" help:"path to write the built dataset to" default:"data/editorial_dataset.yml"`
	MaxPerCluster int      `json:"max_per_cluster" help:"maximum number of examples kept from any one event cluster; <= 0 disables capping" default:"1"`
	IncludeDB     []string `json:"include_db" help:"read-only SQLite snapshots whose human labels should be merged into Gold"`
}

type evaluateParams struct {
	Dataset               string   `json:"dataset" help:"path to the built training dataset" required:"true" format:"path"`
	Out                   string   `json:"out" help:"directory to write one subdirectory per trained candidate" default:"models/editorial/candidates"`
	Algorithm             []string `json:"algorithm" help:"algorithms to train (default: all four)"`
	Seed                  int64    `json:"seed" help:"seed for the Pegasos-based trainers (linear_svm, nbsvm)" default:"42"`
	AcceptTargetPrecision float64  `json:"accept_target_precision" help:"target precision for the accept side of the validation split" default:"0.9"`
	RejectTargetPrecision float64  `json:"reject_target_precision" help:"target precision for the reject side of the validation split" default:"0.9"`
	NearDupThreshold      float64  `json:"near_dup_threshold" help:"Jaccard similarity threshold for near-duplicate grouping before the split; <= 0 disables it" default:"0.8"`
	CostGrid              string   `json:"cost_grid" help:"comma-separated target-precision values to grid-sweep, reporting the pair minimizing expected cost (requires editorial_costs); empty disables the sweep"`
}

type mineParams struct {
	Limit int `json:"limit" help:"override offline_training.mining.batch_limit for this run (0 keeps the configured value)" default:"0"`
}

type shadowCompareParams struct {
	Candidate string `json:"candidate" help:"directory of the candidate model to compare against production" required:"true" format:"path"`
	Days      int    `json:"days" help:"how many days of recent stored articles to score" default:"7"`
	Limit     int    `json:"limit" help:"maximum number of articles to score" default:"5000"`
}

// Actions returns the training actions: train.analyze, train.build,
// train.evaluate, train.mine, train.shadow_compare, train.run,
// train.promote, train.rollback, train.audit_dataset, train.audit_gates,
// train.recheck_corpus, train.backfill_rejects and train.import_labels.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "train.analyze", Group: group, Title: "Analyze training signals",
			Description: "Reports LLM label counts and review-queue composition (read-only).",
			Kind:        action.KindRead, Modes: bothModes, Run: runAnalyze,
		},
		{
			Name: "train.build", Group: group, Title: "Build training dataset",
			Description: "Assembles the Gold+Silver training dataset from human labels and approved LLM proposals, writing it and its manifest to disk.",
			Kind:        action.KindWrite, Modes: localOnly, Params: buildParams{}, Run: runBuild,
		},
		{
			Name: "train.evaluate", Group: group, Title: "Evaluate candidate models",
			Description: "Fits and evaluates every requested candidate algorithm from a built dataset, saving and registering each candidate.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true,
			Params: evaluateParams{}, Run: runEvaluate,
		},
		{
			Name: "train.mine", Group: group, Title: "Mine hard examples",
			Description: "Scans stored articles for strong NB/LLM disagreement (hard negatives and hard positives); calls the LLM.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true,
			Params: mineParams{}, Run: runMine,
		},
		{
			Name: "train.shadow_compare", Group: group, Title: "Shadow-compare candidate model",
			Description: "Scores the same recent stored articles with the production and a candidate model offline, storing every disagreement.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true,
			Params: shadowCompareParams{}, Run: runShadowCompare,
		},
		{
			Name: "train.run", Group: group, Title: "Run training pipeline",
			Description: "Runs the full safe-promotion workflow end-to-end: build, analyze, evaluate and, when the gates pass, promote (destructive).",
			Kind:        action.KindDestructive, Modes: localOnly, LongRunning: true, Exclusive: true,
			Params: runParams{}, Run: runRun,
		},
		{
			Name: "train.promote", Group: group, Title: "Promote candidate model",
			Description: "Atomically promotes a candidate editorial-classifier model to production, gated by quality thresholds.",
			Kind:        action.KindDestructive, Modes: localOnly, Exclusive: true,
			Params: promoteParams{}, Run: runPromote,
		},
		{
			Name: "train.rollback", Group: group, Title: "Roll back model",
			Description: "Rolls back the promoted editorial-classifier model to the previous slot.",
			Kind:        action.KindDestructive, Modes: localOnly, Exclusive: true, Run: runRollback,
		},
		{
			Name: "train.audit_dataset", Group: group, Title: "Audit dataset",
			Description: "Writes a prioritized CSV of heuristic dataset rows (conflicting titles, weak Telegram positives, pipeline-backfill rejects) for review.",
			Kind:        action.KindWrite, Modes: localOnly, Params: auditDatasetParams{}, Run: runAuditDataset,
		},
		{
			Name: "train.audit_gates", Group: group, Title: "Audit dataset against the gates",
			Description: "Writes a review CSV of publish-labeled dataset rows the current relevance, anchor and veto gates would block. Never changes labels.",
			Kind:        action.KindWrite, Modes: localOnly, Params: auditGatesParams{}, Run: runAuditGates,
		},
		{
			Name: "train.recheck_corpus", Group: group, Title: "Recheck corpus against the dictionary",
			Description: "Lists publish-labeled examples the current dictionary would now classify as not related (read-only).",
			Kind:        action.KindRead, Modes: bothModes, Params: recheckCorpusParams{}, Run: runRecheckCorpus,
		},
		{
			Name: "train.backfill_rejects", Group: group, Title: "Backfill reject labels",
			Description: "Labels real pipeline-rejected (rejected_quality) articles as reject training examples.",
			Kind:        action.KindWrite, Modes: localOnly, Exclusive: true, Params: backfillRejectsParams{}, Run: runBackfillRejects,
		},
		{
			Name: "train.import_labels", Group: group, Title: "Import labels",
			Description: "Restores label snapshots into editorial_examples from old database backups or a reviewed dataset file; additive, never deletes.",
			Kind:        action.KindWrite, Modes: localOnly, Exclusive: true, Params: importLabelsParams{}, Run: runImportLabels,
		},
	}
}

func runAnalyze(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	result, err := trainanalyze.Analyze(ctx,
		sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(env.DB),
		sqlitereviewqueue.NewReviewQueueRepository(env.DB),
		env.Config.OfflineTraining.LLM.AutoLabelThreshold, 100000)
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"proposed":               result.ProposedCount,
			"auto_labeled":           result.AutoLabeledCount,
			"sent_to_review":         result.SentToReviewCount,
			"queue_size":             result.QueueSize,
			"queue_disagreement":     result.QueueDisagreementCount,
			"queue_novel_vocabulary": result.QueueNovelVocabularyCount,
			"queue_other":            result.QueueOtherCount,
		},
		Summary: fmt.Sprintf("LLM labels: %d proposed, %d auto-labeled, %d sent to review; review queue: %d items",
			result.ProposedCount, result.AutoLabeledCount, result.SentToReviewCount, result.QueueSize),
	}, nil
}

func configError(format string, args ...any) error {
	return &action.Error{Code: action.CodeConflict, Message: fmt.Sprintf(format, args...)}
}

func runRun(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*runParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	cfg := env.Config
	if cfg.Model.Path == "" {
		return action.Result{}, configError("train run: model.path is not configured")
	}
	if cfg.OfflineTraining.LLM.AutoLabelThreshold <= 0 {
		return action.Result{}, configError("train run: offline_training.llm.auto_label_threshold must be configured above 0")
	}
	if p.Auto && !cfg.OfflineTraining.RetrainTriggers.AutoEnabled {
		return action.Result{Render: "text", Text: "auto training disabled (offline_training.retrain_triggers.auto_enabled=false)",
			Summary: "auto training disabled"}, nil
	}

	var archivedDBs []*sql.DB
	defer func() {
		for _, archived := range archivedDBs {
			_ = archived.Close()
		}
	}()
	var archivedLabels []out.HumanLabelRepository
	for _, path := range p.IncludeDB {
		archive, err := infrasqlite.OpenReadOnly(path, cfg.SQLite.BusyTimeout())
		if err != nil {
			return action.Result{}, err
		}
		archivedDBs = append(archivedDBs, archive)
		archivedLabels = append(archivedLabels, sqliteadapter.NewHumanLabelRepository(archive))
	}

	deps := trainwiring.RunDeps(env.DB, cfg, archivedLabels)
	now := env.Now().UTC()
	opts, err := trainwiring.RunOptions(cfg, trainwiring.RunFlags{
		DatasetPath: p.Dataset, CandidatesDir: p.Candidates, MaxPerCluster: p.MaxPerCluster,
		Force: p.Force, Auto: p.Auto,
	}, now)
	if err != nil {
		return action.Result{}, err
	}
	if p.Auto {
		triggered, reasons, err := trainwiring.EvaluateAuto(ctx, deps, opts, cfg)
		if err != nil {
			return action.Result{}, err
		}
		if !triggered {
			return action.Result{Render: "text", Text: "no retrain triggered: no configured criteria were met",
				Summary: "no retrain triggered"}, nil
		}
		opts.TriggerReasons = reasons
	}

	outcome, err := trainrun.Run(ctx, deps, opts, now)
	if err != nil {
		return action.Result{}, err
	}
	for _, warning := range outcome.Warnings {
		env.Log.Warn(warning)
	}
	return action.Result{Render: "text", Text: outcome.Report,
		Summary: fmt.Sprintf("run %s: %s", outcome.Run.ID, outcome.Run.Status)}, nil
}

func runPromote(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*promoteParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	cfg := env.Config
	if cfg.Model.Path == "" {
		return action.Result{}, configError("train promote: model.path is not configured")
	}
	report, err := trainwiring.PromoteCandidate(ctx, env.DB, cfg, p.CandidateDir, p.Version, p.Force)
	if err != nil {
		if errors.Is(err, trainwiring.ErrNotNaiveBayes) || errors.Is(err, trainwiring.ErrGatesFailed) {
			if len(report.GateFailures) > 0 {
				err = fmt.Errorf("%w: %s", err, strings.Join(report.GateFailures, "; "))
			}
			return action.Result{}, action.Conflict(err)
		}
		return action.Result{}, err
	}
	failures := report.GateFailures
	if failures == nil {
		failures = []string{}
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"version":            report.Version,
			"gate_failures":      failures,
			"forced":             report.Forced,
			"vocabulary_overlap": report.VocabularyOverlap,
			"added":              report.Added,
			"removed":            report.Removed,
		},
		Summary: fmt.Sprintf("promoted %s to %s — run `make deploy` to ship it to the Pi", report.Version, cfg.Model.Path),
	}, nil
}

func runRollback(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	cfg := env.Config
	if cfg.Model.Path == "" {
		return action.Result{}, configError("train rollback: model.path is not configured")
	}
	if err := trainwiring.Rollback(ctx, env.DB, cfg); err != nil {
		return action.Result{}, err
	}
	text := fmt.Sprintf("rolled back %s to the previous promoted model", cfg.Model.Path)
	return action.Result{Render: "text", Text: text, Summary: text}, nil
}

func runBuild(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*buildParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config.OfflineTraining.LLM.AutoLabelThreshold <= 0 {
		return action.Result{}, configError("train build: offline_training.llm.auto_label_threshold must be configured above 0 before Silver examples can be included in a training dataset")
	}
	report, err := trainwiring.BuildDataset(ctx, env.DB, env.Config,
		trainwiring.BuildFlags{OutPath: p.Out, MaxPerCluster: p.MaxPerCluster, IncludeDB: p.IncludeDB}, env.Now().UTC())
	if err != nil {
		return action.Result{}, err
	}
	text := fmt.Sprintf("built %d examples to %s (%d gold, %d silver; %d dropped by --max-per-cluster)",
		report.Examples, report.OutPath, report.Gold, report.Silver, report.Dropped)
	if report.UnresolvedConflicts > 0 {
		text = fmt.Sprintf("WARNING: %d article(s) have an unresolved legacy label conflict and are excluded from this export — see `label conflicts` and `label resolve-conflict`\n%s",
			report.UnresolvedConflicts, text)
	}
	return action.Result{Render: "text", Text: text, Summary: fmt.Sprintf("built %d examples to %s", report.Examples, report.OutPath)}, nil
}

func runEvaluate(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*evaluateParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	report, err := trainwiring.Evaluate(ctx, env.DB, env.Config, trainwiring.EvaluateFlags{
		DatasetPath: p.Dataset, OutDir: p.Out, Algorithms: p.Algorithm, Seed: p.Seed,
		AcceptTargetPrecision: p.AcceptTargetPrecision, RejectTargetPrecision: p.RejectTargetPrecision,
		NearDupThreshold: p.NearDupThreshold, CostGrid: p.CostGrid,
	}, env.Now().UTC())
	if err != nil {
		return action.Result{}, err
	}
	if report.UnparseableSourceIDs > 0 {
		env.Log.Warn(fmt.Sprintf("%d of %d dataset example(s) have a source_id that does not parse as \"platform:identifier\"; those rows trained with no source_id feature",
			report.UnparseableSourceIDs, report.DatasetSize))
	}

	f3 := func(v float64) string { return fmt.Sprintf("%.3f", v) }
	var rows []map[string]any
	if pr := report.Promoted; pr != nil {
		rows = append(rows, map[string]any{
			"algorithm": "(current promoted)", "val_prec": "-", "val_rec": "-", "val_f1": "-", "val_prauc": "-",
			"test_prec": f3(pr.Overall.Precision()), "test_rec": f3(pr.Overall.Recall()), "test_f1": f3(pr.Overall.F1()),
			"gates": "n/a",
		})
	}
	for _, r := range report.Results {
		c := r.Candidate
		gates := "pass"
		if !r.Gates.Passed {
			gates = strings.Join(r.Gates.Failures, "; ")
		}
		rows = append(rows, map[string]any{
			"algorithm": c.Model.Algorithm,
			"val_prec":  f3(c.ValidateReport.Overall.Precision()), "val_rec": f3(c.ValidateReport.Overall.Recall()),
			"val_f1": f3(c.ValidateReport.Overall.F1()), "val_prauc": f3(c.ValidateReport.PRAUC),
			"test_prec": f3(c.TestReport.Overall.Precision()), "test_rec": f3(c.TestReport.Overall.Recall()),
			"test_f1": f3(c.TestReport.Overall.F1()),
			"gates":   gates,
		})
	}

	lines := []string{fmt.Sprintf("excluded %d first-party official source example(s) from NB training", report.ExcludedOfficial)}
	for _, w := range report.CostGrid {
		if !w.Found {
			lines = append(lines, fmt.Sprintf("%-20s cost-grid: no pair among the swept grid clears the promotion gates", w.Algorithm))
			continue
		}
		lines = append(lines, fmt.Sprintf("%-20s cost-grid winner: accept=%.2f reject=%.2f expected_cost=%.2f", w.Algorithm, w.Accept, w.Reject, w.ExpectedCost))
	}
	lines = append(lines, fmt.Sprintf("candidates written under %s — inspect with `validate-model`, then `train promote` the one you choose.", report.OutDir))
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "algorithm", Label: "Algorithm"}, {Key: "val_prec", Label: "Val prec"}, {Key: "val_rec", Label: "Val rec"},
			{Key: "val_f1", Label: "Val F1"}, {Key: "val_prauc", Label: "Val PR-AUC"}, {Key: "test_prec", Label: "Test prec"},
			{Key: "test_rec", Label: "Test rec"}, {Key: "test_f1", Label: "Test F1"}, {Key: "gates", Label: "Gates"},
		},
		Rows:    rows,
		Summary: strings.Join(lines, "\n"),
	}, nil
}

func runMine(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*mineParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	result, err := trainwiring.Mine(ctx, env.DB, env.Config, p.Limit, env.Now().UTC())
	if err != nil {
		return action.Result{}, err
	}
	text := result.SkipReason
	if !result.Skipped {
		text = fmt.Sprintf("scored %d articles: %d hard negatives, %d hard positives",
			result.ArticlesScored, result.HardNegativeCount, result.HardPositiveCount)
	}
	return action.Result{Render: "text", Text: text, Summary: text}, nil
}

func runShadowCompare(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*shadowCompareParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config.Model.Path == "" {
		return action.Result{}, configError("train shadow-compare: model.path is not configured — there is no production model to compare against")
	}
	result, err := trainwiring.ShadowCompare(ctx, env.DB, env.Config, p.Candidate, p.Days, p.Limit, env.Now().UTC())
	if err != nil {
		return action.Result{}, err
	}
	text := fmt.Sprintf("shadow-compare run %s: %d article(s) scored, %d agreement(s), %d disagreement(s) stored",
		result.RunID, result.TotalArticles, result.Agreements, len(result.Disagreements))
	return action.Result{Render: "text", Text: text, Summary: text}, nil
}
