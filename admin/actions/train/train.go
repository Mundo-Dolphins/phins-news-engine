// Package train holds the admin actions over the Naive Bayes training
// workflow: analysing LLM signals, running the full pipeline, promoting a
// candidate model and rolling back. They mirror the `train analyze|run|
// promote|rollback` CLI commands. Write actions are local-only: training and
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

// Actions returns the training actions: train.analyze, train.run,
// train.promote and train.rollback.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "train.analyze", Group: group, Title: "Analyze training signals",
			Description: "Reports LLM label counts and review-queue composition (read-only).",
			Kind:        action.KindRead, Modes: bothModes, Run: runAnalyze,
		},
		{
			Name: "train.run", Group: group, Title: "Run training pipeline",
			Description: "Runs the full safe-promotion workflow end-to-end: build, analyze, evaluate, promote.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true,
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
