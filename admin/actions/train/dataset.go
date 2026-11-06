package train

import (
	"context"
	"errors"
	"fmt"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetaudit"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/humanlabels"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

type auditDatasetParams struct {
	Dataset string `json:"dataset" help:"dataset YAML to audit" default:"data/editorial_dataset.yml" format:"path"`
	Out     string `json:"out" help:"CSV path to write prioritized candidates" default:"data/editorial_dataset-review.csv" format:"path"`
}

type auditGatesParams struct {
	Dataset string `json:"dataset" help:"dataset YAML to audit" default:"data/editorial_dataset.yml" format:"path"`
	Out     string `json:"out" help:"CSV review queue to write (input of train review-dataset --audit)" default:"data/editorial_dataset-gates-review.csv" format:"path"`
	Limit   int    `json:"limit" help:"write at most this many rows, most certain first (0: all)" default:"0"`
}

type recheckCorpusParams struct {
	Dictionary string `json:"dictionary" help:"dictionary file to recheck against (default: dictionary.path from config)" format:"path"`
}

type backfillRejectsParams struct {
	Limit  int  `json:"limit" help:"maximum number of articles to backfill in one run" default:"20000"`
	DryRun bool `json:"dry_run" help:"report candidate counts without writing any labels"`
}

type importLabelsParams struct {
	FromDB              []string `json:"from_db" help:"old SQLite snapshots whose active labels to import, read-only; never overrides an existing label"`
	FromDataset         []string `json:"from_dataset" help:"dataset YAML files whose labels to import"`
	OverrideLabelSource []string `json:"override_label_source" help:"label_source values (e.g. interactive_review) allowed to supersede a disagreeing active label"`
}

func candidateResult(cs []datasetaudit.Candidate, summary string) action.Result {
	rows := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		rows = append(rows, map[string]any{"article_id": c.ArticleID, "reason": c.Reason})
	}
	return action.Result{
		Render:  "table",
		Columns: []action.Column{{Key: "article_id", Label: "Article"}, {Key: "reason", Label: "Reason"}},
		Rows:    rows, Summary: summary,
	}
}

func runAuditDataset(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*auditDatasetParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	dataset, err := editorialtrain.LoadDataset(p.Dataset)
	if err != nil {
		return action.Result{}, err
	}
	originalCount := len(dataset.Examples)
	dataset = dataset.ExcludeSources(trainwiring.ExcludedSources(env.Config))
	candidates := datasetaudit.AuditDataset(dataset.Examples)
	if err := datasetaudit.WriteCSV(p.Out, candidates); err != nil {
		return action.Result{}, fmt.Errorf("train audit-dataset: write %s: %w", p.Out, err)
	}
	return candidateResult(candidates, fmt.Sprintf("audited %d eligible dataset row(s), excluding %d official-source row(s); %d candidate(s) written to %s",
		len(dataset.Examples), originalCount-len(dataset.Examples), len(candidates), p.Out)), nil
}

func runAuditGates(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*auditGatesParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	g, err := trainwiring.NewGateAuditor(env.Config, env.Now())
	if err != nil {
		return action.Result{}, err
	}
	dataset, err := editorialtrain.LoadDataset(p.Dataset)
	if err != nil {
		return action.Result{}, err
	}
	candidates := g.Audit(dataset.Examples)
	total := len(candidates)
	if p.Limit > 0 && len(candidates) > p.Limit {
		candidates = candidates[:p.Limit]
	}
	if err := datasetaudit.WriteCSV(p.Out, candidates); err != nil {
		return action.Result{}, fmt.Errorf("train audit-gates: %w", err)
	}
	return candidateResult(candidates, fmt.Sprintf("audited %d row(s); %d publish-labeled row(s) would now be blocked, %d written to %s",
		len(dataset.Examples), total, len(candidates), p.Out)), nil
}

func runRecheckCorpus(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*recheckCorpusParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	path := p.Dictionary
	if path == "" {
		path = env.Config.Dictionary.Path
	}
	matcher, err := trainwiring.LoadValidatedMatcher(path)
	if err != nil {
		return action.Result{}, err
	}
	examples, err := sqliteadapter.NewHumanLabelRepository(env.DB).ListEditorialExamples(ctx)
	if err != nil {
		return action.Result{}, fmt.Errorf("train recheck-corpus: %w", err)
	}
	examined, flagged := datasetaudit.RecheckCorpus(examples, matcher)
	rows := make([]map[string]any, 0, len(flagged))
	for _, f := range flagged {
		rows = append(rows, map[string]any{
			"article_id": f.ArticleID, "score": fmt.Sprintf("%.2f", f.Score), "rule": orDash(f.Rule), "title": orDash(f.Title),
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "article_id", Label: "Article"}, {Key: "score", Label: "Score"},
			{Key: "rule", Label: "Rule"}, {Key: "title", Label: "Title"},
		},
		Rows:    rows,
		Summary: fmt.Sprintf("%d examined, %d flagged", examined, len(flagged)),
	}, nil
}

func runBackfillRejects(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*backfillRejectsParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	rep, err := humanlabels.BackfillRejects(ctx, sqliteadapter.NewArticleRepository(env.DB),
		sqliteadapter.NewHumanLabelRepository(env.DB), p.Limit, p.DryRun, env.Now().UTC())
	if err != nil {
		return action.Result{}, err
	}
	text := fmt.Sprintf("backfill-rejects: %d candidate(s) found, %d labeled, %d skipped (already labeled) (dry-run=%v)",
		rep.Candidates, rep.Labeled, rep.Skipped, p.DryRun)
	return action.Result{Render: "text", Text: text, Summary: text}, nil
}

func runImportLabels(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*importLabelsParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if len(p.FromDB) == 0 && len(p.FromDataset) == 0 {
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: "train import-labels: give at least one from_db or from_dataset"}
	}
	repo := sqliteadapter.NewHumanLabelRepository(env.DB)
	var rows []map[string]any
	add := func(path string, t humanlabels.ImportTally) {
		rows = append(rows, map[string]any{
			"path": path, "inserted": t.Inserted, "unchanged": t.Unchanged, "superseded": t.Superseded, "conflicts": t.Conflicts,
		})
	}
	result := func() action.Result {
		return action.Result{
			Render: "table",
			Columns: []action.Column{
				{Key: "path", Label: "Source"}, {Key: "inserted", Label: "Inserted"}, {Key: "unchanged", Label: "Unchanged"},
				{Key: "superseded", Label: "Superseded"}, {Key: "conflicts", Label: "Conflicts left as-is"},
			},
			Rows: rows, Summary: fmt.Sprintf("imported labels from %d source(s)", len(rows)),
		}
	}

	for _, path := range p.FromDB {
		archive, err := infrasqlite.OpenReadOnly(path, env.Config.SQLite.BusyTimeout())
		if err != nil {
			return action.Result{}, err
		}
		tally, err := humanlabels.ImportFromDB(ctx, repo, sqliteadapter.NewHumanLabelRepository(archive))
		closeErr := archive.Close()
		var readErr *humanlabels.ArchiveReadError
		if errors.As(err, &readErr) {
			return action.Result{}, fmt.Errorf("train import-labels: read %s: %w", path, err)
		}
		if err != nil {
			return action.Result{}, err
		}
		if closeErr != nil {
			return action.Result{}, closeErr
		}
		add(path, tally)
	}

	override := make(map[string]bool, len(p.OverrideLabelSource))
	for _, s := range p.OverrideLabelSource {
		override[s] = true
	}
	for _, path := range p.FromDataset {
		dataset, err := editorialtrain.LoadDataset(path)
		if err != nil {
			return action.Result{}, err
		}
		tally, err := humanlabels.ImportFromDataset(ctx, repo, dataset, override)
		if err != nil {
			return action.Result{}, err
		}
		add(path, tally)
	}
	return result(), nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
