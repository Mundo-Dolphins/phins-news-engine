// Package dictionary holds the admin actions over the team dictionary file
// and its version history. They mirror the `dictionary validate|inspect|
// history|diff|regression-check|rollback|mine-candidates` CLI commands.
// Reads are available in both modes; rollback, regression checks and
// candidate mining are local-only, and the interactive approval loop of
// `dictionary mine-candidates` stays in the CLI.
package dictionary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/app/regressioncheck"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysnapshot"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarywiring"
)

const group = "dictionary"

var errBadParams = errors.New("dictionary: unexpected params type")

var (
	bothModes = []action.Mode{action.ModeLocal, action.ModeProduction}
	localOnly = []action.Mode{action.ModeLocal}
)

type pathParams struct {
	Path string `json:"path" help:"team dictionary YAML file (default dictionary.path)" format:"path"`
}

type historyParams struct {
	Version string `json:"version" help:"show the detail of this recorded version; empty lists every version"`
}

type diffParams struct {
	From        string `json:"from" help:"recorded history version to diff from (compared with to, or the dictionary on disk)"`
	To          string `json:"to" help:"recorded history version to diff to; requires from"`
	FromFile    string `json:"from_file" help:"dictionary file to diff from; requires to_file" format:"path"`
	ToFile      string `json:"to_file" help:"dictionary file to diff to; requires from_file" format:"path"`
	Dictionary  string `json:"dictionary" help:"team dictionary YAML file (default dictionary.path)" format:"path"`
	Snapshot    string `json:"snapshot" help:"fetched snapshot file to compare the dictionary with (default: latest snapshot per source)" format:"path"`
	SnapshotDir string `json:"snapshot_dir" help:"directory of fetched snapshots (default data/dictionary/snapshots)" format:"path"`
}

type regressionParams struct {
	Candidate     string `json:"candidate" help:"candidate dictionary file to check" required:"true" format:"path"`
	Previous      string `json:"previous" help:"previous dictionary file (default dictionary.path)" format:"path"`
	FlipThreshold int    `json:"flip_threshold" help:"maximum classification flips allowed before the check fails (0 or more)" default:"0"`
}

type rollbackParams struct {
	To       string `json:"to" help:"recorded version to restore the content of (exactly one of to or previous)"`
	Previous bool   `json:"previous" help:"restore the most recent recorded version other than the current one (exactly one of to or previous)"`
	DryRun   bool   `json:"dry_run" help:"show the diff without writing anything"`
}

type mineParams struct {
	Session string `json:"session" help:"review session id to mine (default: the latest completed session)"`
}

// Actions returns dictionary.validate, .inspect, .history, .diff,
// .regression_check, .rollback and .mine_candidates.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "dictionary.validate", Group: group, Title: "Validate dictionary",
			Description: "Loads the team dictionary and lists its validation issues. Error-severity issues are reported in the result (FAILED), not as an error.",
			Kind:        action.KindRead, Modes: bothModes, Params: pathParams{}, Run: runValidate,
		},
		{
			Name: "dictionary.inspect", Group: group, Title: "Inspect dictionary",
			Description: "Summarises the team dictionary: version, decision thresholds and per-category entry and alias counts.",
			Kind:        action.KindRead, Modes: bothModes, Params: pathParams{}, Run: runInspect,
		},
		{
			Name: "dictionary.history", Group: group, Title: "Dictionary history",
			Description: "Lists the recorded dictionary versions, newest first, or shows one version's record and diff.",
			Kind:        action.KindRead, Modes: bothModes, Params: historyParams{}, Run: runHistory,
		},
		{
			Name: "dictionary.diff", Group: group, Title: "Diff dictionary",
			Description: "Lists the change proposals between two recorded versions, two files, or the dictionary and the latest fetched snapshots. Unchanged entries are omitted.",
			Kind:        action.KindRead, Modes: bothModes, Params: diffParams{}, Run: runDiff,
		},
		{
			Name: "dictionary.regression_check", Group: group, Title: "Dictionary regression check",
			Description: "Compares classifier outcomes between the current and a candidate dictionary on the regression dataset; the result is FAILED when the flips exceed the threshold.",
			Kind:        action.KindRead, Modes: localOnly, LongRunning: true, Exclusive: true,
			Params: regressionParams{}, Run: runRegressionCheck,
		},
		{
			Name: "dictionary.rollback", Group: group, Title: "Roll back dictionary",
			Description: "Restores a recorded version's content as a new version of the dictionary file (history is never rewound). dry_run shows the diff and writes nothing.",
			Kind:        action.KindDestructive, Modes: localOnly, Params: rollbackParams{}, Run: runRollback,
		},
		{
			Name: "dictionary.mine_candidates", Group: group, Title: "Mine dictionary candidates",
			Description: "Lists dictionary-candidate terms mined from a completed review session. It never persists anything; the approval loop stays in the CLI.",
			Kind:        action.KindRead, Modes: localOnly, Params: mineParams{}, Run: runMine,
		},
	}
}

func invalid(format string, args ...any) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

// fileError maps a missing file to NotFound and leaves other errors as they are.
func fileError(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, sql.ErrNoRows) {
		return action.NotFound(err)
	}
	return err
}

// versionParam is a history-version param, for naming it in errors.
type versionParam struct{ name, value string }

// versionError maps dictionaryhistory.ErrInvalidVersion to invalid_params
// naming the first given param whose value is not a valid version. When
// no given param is at fault (the version came from the ledger), it is a
// conflict. It returns nil for any other error.
func versionError(err error, params ...versionParam) error {
	if !errors.Is(err, dictionaryhistory.ErrInvalidVersion) {
		return nil
	}
	for _, p := range params {
		if dictionaryhistory.ValidateVersion(p.value) != nil {
			return invalid("%s: %v", p.name, err)
		}
	}
	return action.Conflict(err)
}

func failed(obj map[string]any, err error) action.Result {
	obj["ok"] = false
	obj["error"] = err.Error()
	return action.Result{Render: "object", Object: obj, Summary: "FAILED: " + err.Error()}
}

// dictionaryPath resolves a dictionary path param, refusing overrides in
// production.
func dictionaryPath(env action.Env, name, value string) (string, error) {
	if env.Config == nil {
		return "", errors.New("dictionary: no configuration loaded")
	}
	configured := env.Config.Dictionary.Path
	if err := action.RefusePathOverrides(env, action.PathParam{Name: name, Value: value, Configured: configured}); err != nil {
		return "", err
	}
	if value == "" {
		value = configured
	}
	if value == "" {
		return "", action.Conflict(errors.New("dictionary.path is empty; pass " + name))
	}
	return value, nil
}

func runValidate(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*pathParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	path, err := dictionaryPath(env, "path", p.Path)
	if err != nil {
		return action.Result{}, err
	}
	_, issues, err := dictionarywiring.Validate(path)
	if errors.Is(err, fs.ErrNotExist) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return failed(map[string]any{"path": path}, err), nil
	}
	rows := make([]map[string]any, 0, len(issues))
	for _, i := range issues {
		loc := i.Category
		if i.Entry != "" {
			loc += "/" + i.Entry
		}
		rows = append(rows, map[string]any{"severity": string(i.Severity), "location": loc, "message": i.Message})
	}
	summary := fmt.Sprintf("dictionary OK: %s (%d issue(s) reported)", path, len(issues))
	if dictionarywiring.HasErrors(issues) {
		summary = fmt.Sprintf("FAILED: %s failed validation", path)
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "severity", Label: "Severity"}, {Key: "location", Label: "Location"}, {Key: "message", Label: "Message"},
		},
		Rows: rows, Summary: summary,
	}, nil
}

type categoryJSON struct {
	Name           string  `json:"name"`
	Weight         float64 `json:"weight"`
	StrongOverride bool    `json:"strong_override"`
	Entries        int     `json:"entries"`
	Aliases        int     `json:"aliases"`
}

type decisionJSON struct {
	PositiveThresholdHigh   float64 `json:"positive_threshold_high"`
	PositiveThresholdMedium float64 `json:"positive_threshold_medium"`
	NegativeThresholdHigh   float64 `json:"negative_threshold_high"`
	TitleWeightMultiplier   float64 `json:"title_weight_multiplier"`
	MaxCountPerTerm         int     `json:"max_count_per_term"`
}

type inspectJSON struct {
	Path         string         `json:"path"`
	Version      string         `json:"version"`
	Name         string         `json:"name"`
	Language     string         `json:"language"`
	Categories   []categoryJSON `json:"categories"`
	TotalEntries int            `json:"total_entries"`
	TotalAliases int            `json:"total_aliases"`
	Decision     decisionJSON   `json:"decision"`
}

func runInspect(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*pathParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	path, err := dictionaryPath(env, "path", p.Path)
	if err != nil {
		return action.Result{}, err
	}
	r, err := dictionarywiring.Inspect(path)
	if errors.Is(err, fs.ErrNotExist) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return failed(map[string]any{"path": path}, err), nil
	}
	cats := make([]categoryJSON, 0, len(r.Categories))
	for _, c := range r.Categories {
		cats = append(cats, categoryJSON{Name: c.Name, Weight: c.Weight, StrongOverride: c.StrongOverride, Entries: c.Entries, Aliases: c.Aliases})
	}
	return action.Result{
		Render: "object",
		Object: inspectJSON{
			Path: path, Version: r.Version, Name: r.Name, Language: r.Language, Categories: cats,
			TotalEntries: r.TotalEntries, TotalAliases: r.TotalAliases, Decision: decisionJSON{
				PositiveThresholdHigh: r.Decision.PositiveThresholdHigh, PositiveThresholdMedium: r.Decision.PositiveThresholdMedium,
				NegativeThresholdHigh: r.Decision.NegativeThresholdHigh, TitleWeightMultiplier: r.Decision.TitleWeightMultiplier,
				MaxCountPerTerm: r.Decision.MaxCountPerTerm,
			},
		},
		Summary: fmt.Sprintf("dictionary %s v%s: %d entries, %d aliases", r.Name, r.Version, r.TotalEntries, r.TotalAliases),
	}, nil
}

const timeLayout = "2006-01-02 15:04"

func runHistory(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*historyParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if p.Version != "" {
		rec, artifacts, err := dictionarywiring.HistoryVersion(ctx, env.DB, p.Version)
		if err != nil {
			if verr := versionError(err, versionParam{"version", p.Version}); verr != nil {
				return action.Result{}, verr
			}
			return action.Result{}, fileError(err)
		}
		return action.Result{
			Render: "object",
			Object: map[string]any{
				"version": rec.Version, "updated_at": rec.CreatedAt.Format(timeLayout), "changes": rec.ChangeCount,
				"snapshot": rec.SourceSnapshotRef, "hash": rec.ContentHash, "operation": rec.OperationKind,
				"predecessor": rec.PredecessorVersion, "promoted": rec.Promoted, "diff": artifacts.Diff,
			},
			Summary: "dictionary version " + rec.Version,
		}, nil
	}
	list, err := dictionarywiring.History(ctx, env.DB)
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(list))
	for _, r := range list {
		rows = append(rows, map[string]any{
			"version": r.Version, "updated_at": r.CreatedAt.Format(timeLayout), "changes": r.ChangeCount,
			"snapshot": r.SourceSnapshotRef, "hash": r.ContentHash, "promoted": r.Promoted,
		})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "version", Label: "Version"}, {Key: "updated_at", Label: "Updated at"}, {Key: "changes", Label: "Changes"},
			{Key: "snapshot", Label: "Snapshot"}, {Key: "hash", Label: "Hash"}, {Key: "promoted", Label: "Promoted"},
		},
		Rows: rows, Summary: fmt.Sprintf("%d version(s)", len(rows)),
	}, nil
}

func runDiff(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*diffParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("dictionary: no configuration loaded")
	}
	if err := action.RefusePathOverrides(env,
		action.PathParam{Name: "dictionary", Value: p.Dictionary, Configured: env.Config.Dictionary.Path},
		action.PathParam{Name: "from_file", Value: p.FromFile},
		action.PathParam{Name: "to_file", Value: p.ToFile},
		action.PathParam{Name: "snapshot", Value: p.Snapshot},
		action.PathParam{Name: "snapshot_dir", Value: p.SnapshotDir, Configured: dictionarysnapshot.DefaultDir},
	); err != nil {
		return action.Result{}, err
	}
	switch {
	case p.From == "" && p.To != "":
		return action.Result{}, invalid("to requires from")
	case (p.FromFile == "") != (p.ToFile == ""):
		return action.Result{}, invalid("from_file and to_file must be given together")
	}
	dict := p.Dictionary
	if dict == "" {
		dict = env.Config.Dictionary.Path
	}
	proposals, err := dictionarywiring.Diff(dictionarywiring.DiffOptions{
		DictionaryPath: dict, SnapshotPath: p.Snapshot, SnapshotDir: p.SnapshotDir,
		FromFile: p.FromFile, ToFile: p.ToFile, From: p.From, To: p.To,
	}, env.Now())
	if err != nil {
		if errors.Is(err, dictionarywiring.ErrNoSnapshots) {
			return action.Result{}, action.Conflict(err)
		}
		if verr := versionError(err, versionParam{"from", p.From}, versionParam{"to", p.To}); verr != nil {
			return action.Result{}, verr
		}
		return action.Result{}, fileError(err)
	}
	var rows []map[string]any
	counts := map[string]int{}
	for _, pr := range proposals {
		if pr.Kind == dictionaryupdate.ProposalNoChange {
			continue
		}
		counts[string(pr.Kind)]++
		detail := pr.Detail
		if len(pr.ConflictWith) > 0 {
			detail += " (conflicts with " + strings.Join(pr.ConflictWith, ", ") + ")"
		}
		rows = append(rows, map[string]any{"kind": string(pr.Kind), "category": pr.Category, "term": pr.CanonicalKey, "detail": detail})
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "kind", Label: "Kind"}, {Key: "category", Label: "Category"}, {Key: "term", Label: "Term"}, {Key: "detail", Label: "Detail"},
		},
		Rows: rows, Summary: diffSummary(counts),
	}, nil
}

func diffSummary(counts map[string]int) string {
	if len(counts) == 0 {
		return "no changes"
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s: %d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

func runRegressionCheck(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*regressionParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if p.FlipThreshold < 0 {
		return action.Result{}, invalid("flip_threshold must be 0 or more")
	}
	if p.Candidate == "" {
		return action.Result{}, invalid("candidate is required")
	}
	prev := p.Previous
	if prev == "" {
		var err error
		if prev, err = dictionaryPath(env, "previous", ""); err != nil {
			return action.Result{}, err
		}
	}
	report, err := dictionarywiring.RegressionCheck(prev, p.Candidate)
	if errors.Is(err, regressioncheck.ErrCasesDoNotFitDictionary) {
		return action.Result{}, action.Conflict(err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return action.Result{}, action.NotFound(err)
	}
	if err != nil {
		return failed(map[string]any{"previous": prev, "candidate": p.Candidate}, err), nil
	}
	rows := make([]map[string]any, 0, len(report.Flips))
	for _, f := range report.Flips {
		rows = append(rows, map[string]any{
			"case": f.CaseName, "direction": f.Direction,
			"score_before": fmt.Sprintf("%.2f", f.ScoreBefore), "score_after": fmt.Sprintf("%.2f", f.ScoreAfter),
			"rule_before": f.RuleBefore, "rule_after": f.RuleAfter,
		})
	}
	summary := fmt.Sprintf("%d/%d cases flipped", report.FlipCount, report.TotalCases)
	if report.FlipCount > p.FlipThreshold {
		summary = fmt.Sprintf("FAILED: %s: %d classification flip(s) exceeds threshold %d — review required before promotion", summary, report.FlipCount, p.FlipThreshold)
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "case", Label: "Case"}, {Key: "direction", Label: "Direction"},
			{Key: "score_before", Label: "Score before"}, {Key: "score_after", Label: "Score after"},
			{Key: "rule_before", Label: "Rule before"}, {Key: "rule_after", Label: "Rule after"},
		},
		Rows: rows, Summary: summary,
	}, nil
}

func regressionJSON(res dictionarywiring.RollbackResult) any {
	switch {
	case res.Regression != nil:
		flips := make([]map[string]any, 0, len(res.Regression.Flips))
		for _, f := range res.Regression.Flips {
			flips = append(flips, map[string]any{"case": f.CaseName, "direction": f.Direction})
		}
		return map[string]any{"total_cases": res.Regression.TotalCases, "flip_count": res.Regression.FlipCount, "flips": flips}
	case res.RegressionErr != nil:
		return map[string]any{"skipped": res.RegressionErr.Error()}
	}
	return nil
}

func runRollback(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*rollbackParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("dictionary: no configuration loaded")
	}
	switch {
	case p.To == "" && !p.Previous:
		return action.Result{}, invalid("set exactly one of to or previous")
	case p.To != "" && p.Previous:
		return action.Result{}, invalid("to and previous are mutually exclusive")
	}
	if !p.DryRun {
		action.ConfigFilesMu.Lock()
		defer action.ConfigFilesMu.Unlock()
		// Once the dictionary file is saved, a client disconnect must not
		// cancel the ledger writes that follow it.
		ctx = context.WithoutCancel(ctx)
	}
	res, err := dictionarywiring.Rollback(ctx, env.DB, env.Config,
		dictionarywiring.RollbackOptions{To: p.To, Previous: p.Previous, DryRun: p.DryRun}, env.Now())
	if err != nil {
		if res.Written != "" {
			// The dictionary file has already changed: log the written
			// version and say so in the error.
			env.Log.Error("dictionary rollback: version written but not fully recorded", "from", res.From, "written", res.Written, "restored_from", res.RestoredFrom, "error", err)
			return action.Result{}, fmt.Errorf("dictionary %s written (restored from v%s) but not recorded: %w", res.Written, res.RestoredFrom, err)
		}
		switch {
		case errors.Is(err, dictionarywiring.ErrRollbackChecksum), errors.Is(err, dictionarywiring.ErrNoPreviousVersion):
			return action.Result{}, action.Conflict(err)
		}
		var named []versionParam
		if p.To != "" {
			named = append(named, versionParam{"to", p.To})
		}
		if verr := versionError(err, named...); verr != nil {
			return action.Result{}, verr
		}
		return action.Result{}, fileError(err)
	}
	summary := fmt.Sprintf("dictionary rolled back: %s -> %s (content restored from v%s)", res.From, res.Written, res.RestoredFrom)
	if res.DryRun {
		summary = "dry-run: no changes written"
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"from": res.From, "written": res.Written, "restored_from": res.RestoredFrom,
			"dry_run": res.DryRun, "regression": regressionJSON(res),
		},
		Text: res.DiffText, Summary: summary,
	}, nil
}

func runMine(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*mineParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errors.New("dictionary: no configuration loaded")
	}
	report, err := dictionarywiring.Mine(ctx, env.DB, env.Config, p.Session)
	for _, w := range report.Warnings {
		env.Log.Warn(w)
	}
	if errors.Is(err, dictionarywiring.ErrNoCompletedSession) {
		return action.Result{}, action.Conflict(err)
	}
	if err != nil {
		return action.Result{}, fileError(err)
	}
	rows := make([]map[string]any, 0, len(report.Candidates))
	for _, c := range report.Candidates {
		rows = append(rows, map[string]any{"term": c.Term, "support": c.SupportCount, "guess": c.CategoryGuess})
	}
	summary := fmt.Sprintf("%d candidate(s) from session %s", len(rows), report.Session)
	if len(report.Warnings) > 0 {
		summary += "; " + strings.Join(report.Warnings, "; ")
	}
	return action.Result{
		Render: "table",
		Columns: []action.Column{
			{Key: "term", Label: "Term"}, {Key: "support", Label: "Support"}, {Key: "guess", Label: "Category guess"},
		},
		Rows: rows, Summary: summary,
	}, nil
}
