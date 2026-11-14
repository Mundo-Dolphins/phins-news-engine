package cycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialsync"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modelpromotion"
	"github.com/Mundo-Dolphins/phins-news-engine/app/telegramimport"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// syncResult is the sync step's result.
type syncResult struct {
	Merged            bool      `json:"merged"`
	Migrated          bool      `json:"migrated"`
	SourceHost        string    `json:"source_host"`
	SourcePath        string    `json:"source_path"`
	SnapshotCreatedAt time.Time `json:"snapshot_created_at"`
	SizeBytes         int64     `json:"size_bytes"`
	ChecksumSHA256    string    `json:"checksum_sha256"`
	FKWarnings        []string  `json:"fk_warnings"`
}

// telegramResult is the Telegram import step's result: the CLI's six counters.
type telegramResult struct {
	Processed    int `json:"processed"`
	Imported     int `json:"imported"`
	AlreadyKnown int `json:"already_known"`
	Unmatched    int `json:"unmatched"`
	Skipped      int `json:"skipped"`
	Failed       int `json:"failed"`
}

// rosterResult is the roster step's result: the players written, or why
// the step was skipped.
type rosterResult struct {
	Players    int    `json:"players,omitempty"`
	Path       string `json:"path,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// Promotion decisions recorded on the training result.
const (
	promotionPromoted = "promoted"
	promotionDeclined = "declined"
)

// winnerJSON is the training winner.
type winnerJSON struct {
	Version   string  `json:"version"`
	Algorithm string  `json:"algorithm"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	PRAUC     float64 `json:"prauc"`
	Dir       string  `json:"dir"`
}

// trainResult is the training step's result. The step is done when there
// is no winner or once the promote decision is recorded.
type trainResult struct {
	Candidates int         `json:"candidates"`
	Winner     *winnerJSON `json:"winner"`
	NoWinner   bool        `json:"no_winner"`
	// Promotion is "", "promoted" or "declined".
	Promotion  string `json:"promotion"`
	PromotedTo string `json:"promoted_to,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// The review steps' results, read by the summary. The review-step actions
// record them with these keys.
type (
	articleReviewResult struct {
		ArticlesReviewed int `json:"articles_reviewed"`
		Publish          int `json:"publish"`
		Reject           int `json:"reject"`
	}
	entitiesResult struct {
		Articles        int    `json:"articles"`
		Proposed        int    `json:"proposed"`
		Approved        int    `json:"approved"`
		Rejected        int    `json:"rejected"`
		DiscoveryFailed bool   `json:"discovery_failed"`
		SkipReason      string `json:"skip_reason"`
		// Warnings are the discovery's warning lines.
		Warnings []string `json:"warnings,omitempty"`
		// Discovered is set once cycle.entities_discover has run.
		Discovered  bool `json:"discovered,omitempty"`
		Regenerated bool `json:"regenerated,omitempty"`
	}
	eventRulesResult struct {
		Sampled          int    `json:"sampled"`
		Proposed         int    `json:"proposed"`
		Approved         int    `json:"approved"`
		Applied          int    `json:"applied"`
		DiscoveryFailed  bool   `json:"discovery_failed"`
		GateBlocked      bool   `json:"gate_blocked"`
		GateNotExercised bool   `json:"gate_not_exercised"`
		SkipReason       string `json:"skip_reason"`
	}
	// minedTermsResult holds the offered terms while the step is pending
	// and, once finished, what the dictionary write did.
	minedTermsResult struct {
		// Added is the session's approved terms, as the CLI reports them.
		Added int `json:"added"`
		// Entries is how many new entries the write added; From and To the
		// dictionary versions (empty when it was not modified).
		Entries int    `json:"entries"`
		From    string `json:"from"`
		To      string `json:"to"`
		// Prepared is set by cycle.terms_prepare; Offered lists the terms
		// offered for review, in the CLI's order.
		Prepared   bool     `json:"prepared"`
		Offered    []string `json:"offered"`
		Categories []string `json:"categories"`
		Warnings   []string `json:"warnings"`
		Mined      int      `json:"mined"`
	}
)

func runSync(ctx context.Context, env action.Env, _ any) (res action.Result, err error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	run, err := beginStep(ctx, env, StepSync, func(*StepState) error {
		if !syncAvailable(env) {
			return action.Conflict(errors.New(msgSyncUnavailable))
		}
		if env.SwapDB == nil {
			return errors.New("cycle: sync must run through the admin job runner")
		}
		return nil
	})
	if err != nil {
		return action.Result{}, err
	}
	// closed: the panel's handle is closed; handled: a new handle was
	// installed or its reopen already failed.
	closed, handled := false, false
	defer func() {
		panicked := recover()
		if closed && !handled {
			// A panic between closing the handle and the swap: install a
			// working handle so the panel keeps running.
			if db, rerr := reopenDB(cfg); rerr == nil {
				env.SwapDB(db)
			} else {
				env.Log.Error("cycle: sync: reopen after a panic", "error", rerr)
			}
		}
		run.finish(ctx, panicked, &err)
	}()
	// The snapshot replaces the database file under the panel's handle.
	// Close that handle first: closing it later would checkpoint and delete
	// the WAL file by name, which by then belongs to the new database.
	if env.DB != nil {
		_ = env.DB.Close()
	}
	closed = true
	script := env.Remote.SyncScriptPath
	if script == "" {
		script = action.DefaultSyncScriptPath
	}
	synced, sres, err := editorialsync.Run(ctx, editorialsync.Deps{Sync: env.Remote.NewSync(script, env.Remote.KeyPath)}, editorialsync.Request{
		Host: env.Remote.Host, RemoteDBHint: cfg.Remote.SQLiteRemoteDBHint, LocalDBPath: cfg.SQLite.Path,
		RetentionDays: cfg.Remote.RetentionDays, BusyTimeout: cfg.SQLite.BusyTimeout(), MigrationsFS: migrations.FS,
	})
	if synced != nil {
		// Reopen below the way the panel opens its database.
		_ = synced.Close()
	}
	// On failure the local file is untouched or rolled back: reopen it.
	db, openErr := reopenDB(cfg)
	handled = true
	if openErr != nil {
		if err != nil {
			return action.Result{}, run.fail(ctx, nil, fmt.Errorf("sync: %w; %w", err, openErr))
		}
		return action.Result{}, run.fail(ctx, nil, fmt.Errorf("sync: %w", openErr))
	}
	env.SwapDB(db)
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, fmt.Errorf("sync: %w", err))
	}
	warnings := sres.FKWarnings
	if warnings == nil {
		warnings = []string{}
	}
	for _, wn := range warnings {
		env.Log.Warn("cycle: sync", "warning", wn)
	}
	result := syncResult{
		Merged: sres.Merged, Migrated: sres.Migrated, SourceHost: sres.SourceHost, SourcePath: sres.SourcePath,
		SnapshotCreatedAt: sres.SnapshotCreatedAt, SizeBytes: sres.IncomingSizeBytes, ChecksumSHA256: sres.IncomingChecksumSHA256,
		FKWarnings: warnings,
	}
	if err := run.end(ctx, StatusDone, result, ""); err != nil {
		return action.Result{}, err
	}
	// The CLI's "sync complete" line.
	summary := fmt.Sprintf("sync complete (merged=%v migrated=%v, source=%s:%s @ %s, %d bytes, sha256=%s)",
		sres.Merged, sres.Migrated, sres.SourceHost, sres.SourcePath, sres.SnapshotCreatedAt.Format(time.RFC3339),
		sres.IncomingSizeBytes, sres.IncomingChecksumSHA256)
	return action.Result{Render: "object", Object: result, Summary: summary}, nil
}

// reopenDB opens the operational database file the way the panel does. It
// refuses a missing file rather than creating an empty, unmigrated one.
func reopenDB(cfg *config.Config) (*sql.DB, error) {
	path := cfg.SQLite.Path
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("the database file %s cannot be reopened (%w); restart the panel", path, err)
	}
	db, err := infrasqlite.OpenImmediate(path, cfg.SQLite.BusyTimeout())
	if err != nil {
		return nil, fmt.Errorf("reopen the database %s: %w; restart the panel", path, err)
	}
	return db, nil
}

// isTelegramAuthError reports whether err comes from the Telegram login:
// the non-interactive client refusing to prompt, or the session being
// rejected.
func isTelegramAuthError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"authentication failed", "auth_key", "non-interactive", "session_revoked", "session_expired", "unauthorized"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func telegramError(stage string, err error) error {
	if isTelegramAuthError(err) {
		return fmt.Errorf("log in to Telegram from the terminal first (%w)", err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}

func runTelegram(ctx context.Context, env action.Env, _ any) (_ action.Result, err error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	run, err := beginStep(ctx, env, StepTelegram, func(*StepState) error {
		if env.Remote.NewTelegramClient == nil {
			return action.Conflict(errors.New(msgTelegramUnavailable))
		}
		return nil
	})
	if err != nil {
		return action.Result{}, err
	}
	defer func() { run.finish(ctx, recover(), &err) }()
	if err := cfg.Telegram.ValidateImport(); err != nil {
		return action.Result{}, run.fail(ctx, nil,
			fmt.Errorf("telegram import required: %w (skip the step to run without it)", err))
	}
	client, err := env.Remote.NewTelegramClient(out.TelegramImportClientConfig{
		APIID: cfg.Telegram.APIID, APIHash: cfg.Telegram.APIHash, Phone: cfg.Telegram.Phone,
		SessionPath: cfg.Telegram.SessionPath, NonInteractive: true,
	})
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, telegramError("telegram client", err))
	}
	messages, err := client.ExportChannelMessages(ctx, out.TelegramImportRequest{ChannelRef: cfg.Telegram.ChannelID, Limit: 1000})
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, telegramError("telegram import", err))
	}
	urlCfg, resolvers := cfg.Normalization.URL.Policy()
	res, err := telegramimport.Import(ctx, telegramimport.Deps{
		Articles: sqliteadapter.NewArticleRepository(env.DB), Labels: sqliteadapter.NewHumanLabelRepository(env.DB), Clock: clock(env.Now),
	}, messages, urlCfg, resolvers, false)
	result := telegramResult{
		Processed: res.Processed, Imported: res.Imported, AlreadyKnown: res.AlreadyKnown,
		Unmatched: res.Unmatched, Skipped: res.Skipped, Failed: res.Failed,
	}
	if err != nil {
		return action.Result{}, run.fail(ctx, result, fmt.Errorf("telegram import: %w", err))
	}
	if err := run.end(ctx, StatusDone, result, ""); err != nil {
		return action.Result{}, err
	}
	summary := fmt.Sprintf("telegram import: processed=%d imported=%d already_known=%d unmatched=%d skipped=%d failed=%d",
		res.Processed, res.Imported, res.AlreadyKnown, res.Unmatched, res.Skipped, res.Failed)
	return action.Result{Render: "object", Object: result, Summary: summary}, nil
}

func runRoster(ctx context.Context, env action.Env, _ any) (_ action.Result, err error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	run, err := beginStep(ctx, env, StepRoster, nil)
	if err != nil {
		return action.Result{}, err
	}
	defer func() { run.finish(ctx, recover(), &err) }()
	if entitywiring.CycleStepDisabledReason(cfg) != "" || !cfg.EntityDiscovery.Roster.Enabled {
		result := rosterResult{SkipReason: msgRosterOff}
		if err := run.end(ctx, StatusSkipped, result, ""); err != nil {
			return action.Result{}, err
		}
		return action.Result{Render: "object", Object: result, Summary: "roster sync skipped: " + msgRosterOff}, nil
	}
	players, dest, err := func() (int, string, error) {
		action.ConfigFilesMu.Lock()
		defer action.ConfigFilesMu.Unlock()
		return entitywiring.SyncRoster(ctx, cfg.EntityDiscovery.Roster, "", "", env.Now())
	}()
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, fmt.Errorf("roster sync: %w (skip the step to continue with the existing roster)", err))
	}
	result := rosterResult{Players: players, Path: dest}
	if err := run.end(ctx, StatusDone, result, ""); err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: result, Summary: fmt.Sprintf("roster sync: %d players written to %s", players, dest)}, nil
}

func decodeTrain(s *StepState) (trainResult, error) {
	var r trainResult
	if len(s.Result) == 0 || string(s.Result) == "null" {
		return r, nil
	}
	if err := json.Unmarshal(s.Result, &r); err != nil {
		return r, fmt.Errorf("cycle: decode training result: %w", err)
	}
	return r, nil
}

func runTrain(ctx context.Context, env action.Env, _ any) (_ action.Result, err error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	run, err := beginStep(ctx, env, StepTraining, func(s *StepState) error {
		r, err := decodeTrain(s)
		if err != nil {
			return err
		}
		if r.Winner != nil && r.Promotion == "" {
			return action.Conflict(fmt.Errorf("winner %s is waiting for the promote decision", r.Winner.Version))
		}
		return nil
	})
	if err != nil {
		return action.Result{}, err
	}
	defer func() { run.finish(ctx, recover(), &err) }()
	trainingCfg, err := trainwiring.CycleTrainingConfig(cfg, editorialcycle.ExcludedSourceIDs(cfg), fmt.Sprintf("fs-%d", env.Now().UTC().UnixNano()))
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, fmt.Errorf("training config: %w", err))
	}
	res, err := editorialcycle.RunTraining(ctx, editorialcycle.TrainingDeps{
		Labels: sqliteadapter.NewHumanLabelRepository(env.DB), Registry: sqliteadapter.NewModelRegistryRepository(env.DB),
	}, trainingCfg)
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, fmt.Errorf("training: %w", err))
	}
	result := trainResult{Candidates: len(res.Results), NoWinner: !res.HasWinner}
	status, summary := StatusDone, "no candidate passed gates"
	if res.HasWinner {
		wr := res.Winner
		rep := wr.Candidate.TestReport
		result.Winner = &winnerJSON{
			Version: wr.Version, Algorithm: wr.Candidate.Model.Algorithm,
			Precision: rep.Overall.Precision(), Recall: rep.Overall.Recall(), F1: rep.Overall.F1(), PRAUC: rep.PRAUC,
			Dir: wr.Dir,
		}
		// Waiting for the promote decision.
		status = StatusPending
		summary = fmt.Sprintf("winner: %s (version %s) precision=%.3f recall=%.3f f1=%.3f prauc=%.3f",
			wr.Candidate.Model.Algorithm, wr.Version, rep.Overall.Precision(), rep.Overall.Recall(), rep.Overall.F1(), rep.PRAUC)
	}
	if err := run.end(ctx, status, result, ""); err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: result, Summary: summary}, nil
}

type promoteParams struct {
	Promote bool `json:"promote" help:"true promotes the winner to the production model path; false declines it"`
}

func runPromote(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*promoteParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	mu.Lock()
	defer mu.Unlock()
	w, err := loadCurrent(ctx, env, StepTraining)
	if err != nil {
		return action.Result{}, err
	}
	if inflight[StepTraining] {
		return action.Result{}, action.Conflict(errors.New("training is running; wait for it to finish"))
	}
	s := w.step(StepTraining)
	r, err := decodeTrain(s)
	if err != nil {
		return action.Result{}, err
	}
	switch {
	case r.Promotion != "":
		return action.Result{}, action.Conflict(fmt.Errorf("the winner was already %s", r.Promotion))
	case r.Winner == nil:
		return action.Result{}, action.Conflict(errors.New("there is no winner to promote; run the training first"))
	}
	summary := "winner " + r.Winner.Version + " declined"
	r.Promotion = promotionDeclined
	if p.Promote {
		if cfg.Model.Path == "" {
			return action.Result{}, action.Conflict(errors.New("model.path is empty"))
		}
		// Once the model file is written, a client disconnect must not
		// cancel the registry update that follows it.
		ctx = context.WithoutCancel(ctx)
		err := func() error {
			action.ConfigFilesMu.Lock()
			defer action.ConfigFilesMu.Unlock()
			return modelpromotion.Promote(ctx, modelpromotion.Deps{
				Registry: sqliteadapter.NewModelRegistryRepository(env.DB), UOW: sqliteadapter.NewUnitOfWork(env.DB),
			}, r.Winner.Dir, "", cfg.Model.Path)
		}()
		if err != nil {
			return action.Result{}, fmt.Errorf("promote: %w", err)
		}
		r.Promotion, r.PromotedTo = promotionPromoted, cfg.Model.Path
		summary = fmt.Sprintf("promoted %s to %s", r.Winner.Version, cfg.Model.Path)
	}
	if err := w.set(StepTraining, StatusDone, r, "", env.Now()); err != nil {
		return action.Result{}, err
	}
	if err := save(context.WithoutCancel(ctx), env, w); err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: r, Summary: summary}, nil
}

// summaryView is the JSON shape of cycle.summary.
type summaryView struct {
	// Report has the CLI audit report's fields (Go field names, as in the
	// audit file).
	Report   editorialcycle.AuditReport `json:"report"`
	Markdown string                     `json:"markdown"`
	Deploy   string                     `json:"deploy"`
}

func runSummary(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	w, err := loadCurrent(ctx, env, StepSummary)
	if err != nil {
		return action.Result{}, err
	}
	report := buildReport(w)
	return action.Result{
		Render: "object",
		Object: summaryView{Report: report, Markdown: report.MarkdownSummary(), Deploy: msgDeploy},
		Text:   report.MarkdownSummary(), Summary: "editorial cycle summary; " + msgDeploy,
	}, nil
}

// decodeInto decodes a step's result into v; a missing or malformed result
// leaves v zero.
func decodeInto(s *StepState, v any) {
	if len(s.Result) > 0 {
		_ = json.Unmarshal(s.Result, v)
	}
}

func skipReasonOf(s *StepState) string {
	var r skipReason
	decodeInto(s, &r)
	if r.SkipReason == "" {
		return msgSkipped
	}
	return r.SkipReason
}

// buildReport assembles the CLI's audit report from the step results.
// Deploy never runs in the panel, so its fields stay false.
func buildReport(w *Wizard) editorialcycle.AuditReport {
	rep := editorialcycle.AuditReport{SessionID: w.SessionID, EntitiesVerification: w.EntitiesVerification}

	if s := w.step(StepTelegram); s.Status == StatusSkipped {
		rep.TelegramImportSkipped, rep.TelegramImportSkipReason = true, skipReasonOf(s)
	} else {
		var r telegramResult
		decodeInto(s, &r)
		rep.TelegramImportProcessed, rep.TelegramImportImported, rep.TelegramImportAlreadyKnown = r.Processed, r.Imported, r.AlreadyKnown
		rep.TelegramImportUnmatched, rep.TelegramImportSkippedMessages, rep.TelegramImportFailed = r.Unmatched, r.Skipped, r.Failed
	}

	switch s := w.step(StepRoster); s.Status {
	case StatusDone:
		var r rosterResult
		decodeInto(s, &r)
		rep.RosterSync = fmt.Sprintf("synced:%d", r.Players)
	case StatusSkipped:
		rep.RosterSync = "skipped:" + skipReasonOf(s)
	case StatusFailed:
		rep.RosterSync = "failed"
	}

	var review articleReviewResult
	decodeInto(w.step(StepArticleReview), &review)
	rep.ArticlesReviewed, rep.PublishCount, rep.RejectCount = review.ArticlesReviewed, review.Publish, review.Reject

	if s := w.step(StepEntities); s.Status == StatusSkipped {
		rep.EntitiesSkipped, rep.EntitiesSkipReason = true, skipReasonOf(s)
	} else {
		var r entitiesResult
		decodeInto(s, &r)
		rep.EntitiesArticles, rep.EntitiesProposed, rep.EntitiesApproved, rep.EntitiesRejected = r.Articles, r.Proposed, r.Approved, r.Rejected
		rep.EntitiesDiscoveryFailed = r.DiscoveryFailed
	}

	if s := w.step(StepEventRules); s.Status == StatusSkipped {
		rep.EventRulesSkipped, rep.EventRulesSkipReason = true, skipReasonOf(s)
	} else {
		var r eventRulesResult
		decodeInto(s, &r)
		rep.EventRulesSampled, rep.EventRulesProposed, rep.EventRulesApproved, rep.EventRulesApplied = r.Sampled, r.Proposed, r.Approved, r.Applied
		rep.EventRulesDiscoveryFailed, rep.EventRulesGateBlocked, rep.EventRulesGateNotExercised = r.DiscoveryFailed, r.GateBlocked, r.GateNotExercised
	}

	var mined minedTermsResult
	decodeInto(w.step(StepMinedTerms), &mined)
	rep.DictionaryTermsAdded = mined.Added

	var tr trainResult
	decodeInto(w.step(StepTraining), &tr)
	rep.TrainingCandidates = tr.Candidates
	if tr.Winner != nil {
		rep.Winner = tr.Winner.Version
		rep.WinnerDeclined = tr.Promotion != promotionPromoted
	}

	for _, name := range Steps {
		if s := w.step(name); s.Status == StatusFailed && s.Error != "" {
			rep.Error = s.Error
		}
	}
	return rep
}
