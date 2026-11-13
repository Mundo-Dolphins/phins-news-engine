// Package cycle holds the admin actions of the editorial cycle wizard: the
// `editorial cycle` CLI run as a step-by-step flow (sync with the remote
// host, Telegram import, roster, article review, entities, event rules,
// mined terms, training, summary). The wizard's state lives in the admin
// store under one key, so it survives a sync of the operational database
// and a restart of the panel; one cycle runs at a time. Deploy stays in the
// terminal. Every action is local only.
package cycle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/eventrules"
)

const (
	group = "cycle"
	// stateKey is the admin state key holding the wizard state.
	stateKey = "editorial_cycle_wizard"
)

// Step names, in the order the wizard runs them.
const (
	StepSync          = "sync"
	StepTelegram      = "telegram_import"
	StepRoster        = "roster_sync"
	StepArticleReview = "article_review"
	StepEntities      = "entities"
	StepEventRules    = "event_rules"
	StepMinedTerms    = "mined_terms"
	StepTraining      = "training"
	StepSummary       = "summary"
)

// Step statuses.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// Steps lists the wizard's steps in order.
var Steps = []string{
	StepSync, StepTelegram, StepRoster, StepArticleReview, StepEntities,
	StepEventRules, StepMinedTerms, StepTraining, StepSummary,
}

// skippable are the steps the CLI can skip with a flag.
var skippable = []string{
	StepSync, StepTelegram, StepRoster, StepEntities, StepEventRules, StepMinedTerms, StepTraining,
}

// Texts shown to the operator.
const (
	msgSyncUnavailable     = "sync is not available on this host"
	msgTelegramUnavailable = "telegram import is not available on this host"
	msgRosterOff           = "entity step off or roster disabled"
	msgSkipped             = "skipped from the admin panel"
	msgDeploy              = "run the deploy from the terminal"
	// msgTelegramSkipWarning is the CLI's --skip-telegram-import warning.
	msgTelegramSkipWarning = "WARNING: --skip-telegram-import is set — this session's publications will be reconstructed without Telegram history. Only use this when Telegram credentials are genuinely unavailable."
)

var (
	errBadParams = errors.New("cycle: unexpected params type")
	errNoCycle   = action.Conflict(errors.New("no editorial cycle in progress; start one"))
	localOnly    = []action.Mode{action.ModeLocal}
)

// Wizard is the persisted state of the cycle in progress.
type Wizard struct {
	CycleID   string    `json:"cycle_id"`
	StartedAt time.Time `json:"started_at"`
	Step      string    `json:"step"`
	// SessionID is the review session the article review step created or
	// resumed; empty before that step.
	SessionID            string                `json:"session_id"`
	Steps                map[string]*StepState `json:"steps"`
	SampleSize           int                   `json:"sample_size"`
	EntitiesVerification string                `json:"entities_verification"`
}

// StepState is one step's status and outcome.
type StepState struct {
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result"`
	Error     string          `json:"error"`
	UpdatedAt time.Time       `json:"updated_at"`
	// EnteredAt is when the wizard moved onto the step (nil until then).
	EnteredAt *time.Time `json:"entered_at,omitempty"`
}

// finished reports whether the wizard may move past the step.
func (s *StepState) finished() bool { return s.Status == StatusDone || s.Status == StatusSkipped }

// step returns the named step, creating it when missing.
func (w *Wizard) step(name string) *StepState {
	if w.Steps == nil {
		w.Steps = map[string]*StepState{}
	}
	s, ok := w.Steps[name]
	if !ok || s == nil {
		s = &StepState{Status: StatusPending}
		w.Steps[name] = s
	}
	return s
}

// set records a step's status, result and error.
func (w *Wizard) set(name, status string, result any, errMsg string, at time.Time) error {
	s := w.step(name)
	raw, err := marshalResult(result)
	if err != nil {
		return err
	}
	s.Status, s.Result, s.Error, s.UpdatedAt = status, raw, errMsg, at
	return nil
}

func marshalResult(result any) (json.RawMessage, error) {
	switch r := result.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return r, nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("cycle: encode step result: %w", err)
	}
	return raw, nil
}

var (
	// mu serialises every read-modify-write of the wizard state.
	mu sync.Mutex
	// inflight names the steps whose action is running in this process; a
	// "running" status without an entry here is left over from a restart
	// and may be retried.
	inflight = map[string]bool{}
)

// load returns the stored wizard, or nil when no cycle is in progress.
func load(ctx context.Context, env action.Env) (*Wizard, error) {
	if env.State == nil {
		return nil, errors.New("cycle: the admin state store is not available")
	}
	raw, ok, err := env.State.GetState(ctx, stateKey)
	if err != nil {
		return nil, fmt.Errorf("cycle: read wizard state: %w", err)
	}
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	var w Wizard
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("cycle: decode wizard state: %w", err)
	}
	for _, s := range Steps {
		w.step(s)
	}
	return &w, nil
}

func save(ctx context.Context, env action.Env, w *Wizard) error {
	raw, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("cycle: encode wizard state: %w", err)
	}
	if err := env.State.PutState(ctx, stateKey, raw); err != nil {
		return fmt.Errorf("cycle: save wizard state: %w", err)
	}
	return nil
}

// clear deletes the wizard state (an empty value means no cycle).
func clear(ctx context.Context, env action.Env) error {
	if err := env.State.PutState(ctx, stateKey, nil); err != nil {
		return fmt.Errorf("cycle: clear wizard state: %w", err)
	}
	return nil
}

// loadCurrent loads the wizard and checks it is at step.
func loadCurrent(ctx context.Context, env action.Env, step string) (*Wizard, error) {
	w, err := load(ctx, env)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errNoCycle
	}
	if w.Step != step {
		return nil, action.Conflict(fmt.Errorf("the cycle is at step %s, not %s", w.Step, step))
	}
	return w, nil
}

// stepRun is a step begun with beginStep. Its outcome is recorded once,
// through end or fail; finish, deferred right after beginStep, covers a
// panic or a return without an outcome.
type stepRun struct {
	env   action.Env
	w     *Wizard
	step  string
	ended bool
}

// beginStep checks the cycle is at step and that the step can run (check
// may refuse it further), then marks it running. The caller must defer
// finish on the returned run.
func beginStep(ctx context.Context, env action.Env, step string, check func(*StepState) error) (*stepRun, error) {
	mu.Lock()
	defer mu.Unlock()
	w, err := loadCurrent(ctx, env, step)
	if err != nil {
		return nil, err
	}
	s := w.step(step)
	switch {
	case inflight[step]:
		return nil, action.Conflict(fmt.Errorf("step %s is already running", step))
	case s.finished():
		return nil, action.Conflict(fmt.Errorf("step %s is already finished", step))
	}
	if check != nil {
		if err := check(s); err != nil {
			return nil, err
		}
	}
	s.Status, s.Error, s.UpdatedAt = StatusRunning, "", env.Now()
	if err := save(ctx, env, w); err != nil {
		return nil, err
	}
	inflight[step] = true
	return &stepRun{env: env, w: w, step: step}, nil
}

// end records the step's outcome. When the cycle was abandoned (or
// replaced) meanwhile the outcome is only logged.
func (r *stepRun) end(ctx context.Context, status string, result any, errMsg string) error {
	r.ended = true
	mu.Lock()
	defer mu.Unlock()
	delete(inflight, r.step)
	ctx = context.WithoutCancel(ctx)
	w, err := load(ctx, r.env)
	if err != nil {
		return err
	}
	if w == nil || w.CycleID != r.w.CycleID {
		r.env.Log.Warn("cycle: step finished after its cycle ended", "step", r.step, "status", status)
		return nil
	}
	if err := w.set(r.step, status, result, errMsg, r.env.Now()); err != nil {
		return err
	}
	return save(ctx, r.env, w)
}

// fail records err as the step's error and returns it.
func (r *stepRun) fail(ctx context.Context, result any, err error) error {
	if serr := r.end(ctx, StatusFailed, result, err.Error()); serr != nil {
		r.env.Log.Error("cycle: record step failure", "step", r.step, "error", serr)
	}
	return err
}

// finish is deferred right after beginStep with the value of recover(). A
// panic becomes the action's error and, unless an outcome was already
// recorded, the step's failure; a step that returned without recording an
// outcome is marked failed too, so the step never stays running.
func (r *stepRun) finish(ctx context.Context, panicked any, errp *error) {
	if panicked != nil {
		*errp = fmt.Errorf("panic: %v", panicked)
		r.env.Log.Error("cycle: step panicked", "step", r.step, "panic", fmt.Sprint(panicked), "stack", string(debug.Stack()))
	}
	if r.ended {
		return
	}
	if *errp == nil {
		*errp = fmt.Errorf("step %s ended without an outcome", r.step)
	}
	_ = r.fail(ctx, nil, *errp)
}

// errNoConfig is returned by actions run without a loaded configuration.
var errNoConfig = errors.New("cycle: no configuration loaded")

// Actions returns the cycle.* actions.
func Actions() []action.Action {
	return []action.Action{
		{
			Name: "cycle.state", Group: group, Title: "Editorial cycle state",
			Description: "The editorial cycle in progress (null when none) with each step's status and result, what this host offers (sync, Telegram) and why steps are disabled.",
			Kind:        action.KindRead, Modes: localOnly, Run: runState,
		},
		{
			Name: "cycle.start", Group: group, Title: "Start editorial cycle",
			Description: "Starts an editorial cycle at the sync step. Refused while another cycle is in progress.",
			Kind:        action.KindWrite, Modes: localOnly, Params: startParams{}, Run: runStart,
		},
		{
			Name: "cycle.next", Group: group, Title: "Next cycle step",
			Description: "Moves to the next step once the current one is finished, or skips it (skippable steps only). On the summary step it finishes the cycle and writes the audit report.",
			Kind:        action.KindWrite, Modes: localOnly, Params: nextParams{}, Run: runNext,
		},
		{
			Name: "cycle.abandon", Group: group, Title: "Abandon editorial cycle",
			Description: "Cancels the cycle in progress and its review session (unless already completed).",
			Kind:        action.KindDestructive, Modes: localOnly, Run: runAbandon,
		},
		{
			Name: "cycle.sync", Group: group, Title: "Sync database from remote host",
			Description: "Downloads the remote host's operational database, migrates it, merges the local editorial tables into it and makes it the panel's database.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true, SwapsDB: true, Run: runSync,
		},
		{
			Name: "cycle.telegram_import", Group: group, Title: "Import Telegram history",
			Description: "Imports the channel's message history with the stored Telegram session; never prompts (log in from the terminal when the session has expired).",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Run: runTelegram,
		},
		{
			Name: "cycle.roster_sync", Group: group, Title: "Sync league roster",
			Description: "Downloads the nflverse roster for the entity step, or records why it is skipped.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Run: runRoster,
		},
		{
			Name: "cycle.train", Group: group, Title: "Train candidates",
			Description: "Exports the labeled dataset, trains the candidate models and picks a winner that passes the gates.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true, Run: runTrain,
		},
		{
			Name: "cycle.promote", Group: group, Title: "Promote winner",
			Description: "Promotes the training winner to the production model path, or records that it was declined. Decided once per cycle.",
			Kind:        action.KindDestructive, Modes: localOnly, Params: promoteParams{}, Run: runPromote,
		},
		{
			Name: "cycle.review_open", Group: group, Title: "Open article review",
			Description: "Starts the cycle's review session with a fresh selection, or resumes the latest paused, failed or running one (a paused CLI session too) with its frozen articles and decisions.",
			Kind:        action.KindWrite, Modes: localOnly, Run: runReviewOpen,
		},
		{
			Name: "cycle.review_queue", Group: group, Title: "Article review queue",
			Description: "Every article of the review session in the CLI's order, decided ones included: headline, full summary, link, source, date, score, prior label, the other articles of its cluster and its decision.",
			Kind:        action.KindRead, Modes: localOnly, Run: runReviewQueue,
		},
		{
			Name: "cycle.review_decide", Group: group, Title: "Decide an article",
			Description: "Records the decision on one article of the review session, exactly as the CLI's review keys; an article already decided can be decided again.",
			Kind:        action.KindWrite, Modes: localOnly, Params: reviewDecideParams{}, Run: runReviewDecide,
		},
		{
			Name: "cycle.review_finish", Group: group, Title: "Finish article review",
			Description: "Completes the review session once every article is decided and finishes the step.",
			Kind:        action.KindWrite, Modes: localOnly, Run: runReviewFinish,
		},
		{
			Name: "cycle.review_pause", Group: group, Title: "Pause article review",
			Description: "Pauses the review session (the CLI's save-and-exit); opening the review again resumes it.",
			Kind:        action.KindWrite, Modes: localOnly, Run: runReviewPause,
		},
		{
			Name: "cycle.entities_discover", Group: group, Title: "Discover entities in the session",
			Description: "Runs entity discovery over the articles published or rejected in the review session; the proposals are then reviewed with entities.review_queue (session_id) and entities.approve/reject.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Run: runEntitiesDiscover,
		},
		{
			Name: "cycle.entities_finish", Group: group, Title: "Finish entity review",
			Description: "Regenerates the entity catalog when a proposal of the session was approved and finishes the step with the approvals and rejections since the discovery.",
			Kind:        action.KindWrite, Modes: localOnly, Run: runEntitiesFinish,
		},
		{
			Name: "cycle.event_rules_discover", Group: group, Title: "Discover event rule phrases",
			Description: "Runs the event rule discovery of the cycle (as events.rules_discover) and records its counters; the proposals are then decided with events.rules_queue and events.rules_decide.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true, Run: runEventRulesDiscover,
		},
		{
			Name: "cycle.event_rules_apply", Group: group, Title: "Apply event rule phrases",
			Description: "Applies the approved event rule phrases when the clustering gate passes (as events.rules_apply) and records the outcome in the step.",
			Kind:        action.KindWrite, Modes: localOnly, LongRunning: true, Exclusive: true, Run: runEventRulesApply,
		},
		{
			Name: "cycle.event_rules_finish", Group: group, Title: "Finish event rules review",
			Description: "Finishes the event rules step with the counters recorded by the cycle's discover and apply; nothing is rejected automatically.",
			Kind:        action.KindWrite, Modes: localOnly, Run: runEventRulesFinish,
		},
		{
			Name: "cycle.terms_prepare", Group: group, Title: "Mine dictionary terms",
			Description: "Mines candidate dictionary terms from the review session and offers the pending ones; decisions already made are kept.",
			Kind:        action.KindWrite, Modes: localOnly, Run: runTermsPrepare,
		},
		{
			Name: "cycle.terms_queue", Group: group, Title: "Mined terms queue",
			Description: "The offered mined terms with support, first article, category guess and decision, and the dictionary's categories.",
			Kind:        action.KindRead, Modes: localOnly, Run: runTermsQueue,
		},
		{
			Name: "cycle.terms_decide", Group: group, Title: "Decide a mined term",
			Description: "Approves (a category is required when the guess is not one of the dictionary's), rejects (added to the mining policy's ignored_terms) or categorizes one mined term. The dictionary is written when the step finishes.",
			Kind:        action.KindWrite, Modes: localOnly, Params: termsDecideParams{}, Run: runTermsDecide,
		},
		{
			Name: "cycle.terms_finish", Group: group, Title: "Write mined terms",
			Description: "Once every offered term is decided, adds the approved ones to the dictionary (patch version bump) and finishes the step.",
			Kind:        action.KindDestructive, Modes: localOnly, Run: runTermsFinish,
		},
		{
			Name: "cycle.summary", Group: group, Title: "Cycle summary",
			Description: "The cycle's audit report assembled from the step results; deploy stays in the terminal.",
			Kind:        action.KindRead, Modes: localOnly, Run: runSummary,
		},
	}
}

type startParams struct {
	SampleSize           int    `json:"sample_size" help:"cap on articles to review (0 = editorial_cycle.sample_size)" default:"0"`
	EntitiesVerification string `json:"entities_verification" help:"verification depth of the entity step" enum:"local,full,none" default:"local"`
}

type nextParams struct {
	Skip bool `json:"skip" help:"skip the current step instead of requiring it to be finished"`
}

type availability struct {
	Sync     bool `json:"sync"`
	Telegram bool `json:"telegram"`
}

// stateView is the JSON shape of cycle.state, cycle.start and cycle.next.
type stateView struct {
	Cycle     *Wizard           `json:"cycle"`
	Available availability      `json:"available"`
	Order     []string          `json:"order"`
	Skippable []string          `json:"skippable"`
	Disabled  map[string]string `json:"disabled"`
}

func syncAvailable(env action.Env) bool {
	return env.Remote.Host != "" && env.Remote.NewSync != nil
}

// disabledReasons returns, per step, why it cannot run on this host or
// with this configuration; steps that can run are absent.
func disabledReasons(env action.Env) map[string]string {
	r := map[string]string{}
	if !syncAvailable(env) {
		r[StepSync] = msgSyncUnavailable
	}
	if env.Remote.NewTelegramClient == nil {
		r[StepTelegram] = msgTelegramUnavailable
	} else if err := env.Config.Telegram.ValidateImport(); err != nil {
		r[StepTelegram] = "telegram import required: " + err.Error()
	}
	entityReason := entitywiring.CycleStepDisabledReason(env.Config)
	if entityReason != "" || !env.Config.EntityDiscovery.Roster.Enabled {
		r[StepRoster] = msgRosterOff
	}
	if entityReason != "" {
		r[StepEntities] = entityReason
	}
	if reason := eventrules.CycleStepDisabledReason(env.Config); reason != "" {
		r[StepEventRules] = reason
	}
	return r
}

func newView(env action.Env, w *Wizard) stateView {
	return stateView{
		Cycle:     w,
		Available: availability{Sync: syncAvailable(env), Telegram: env.Remote.NewTelegramClient != nil},
		Order:     Steps, Skippable: skippable, Disabled: disabledReasons(env),
	}
}

func view(env action.Env, w *Wizard) action.Result {
	v := newView(env, w)
	summary := "no editorial cycle in progress"
	if w != nil {
		summary = fmt.Sprintf("editorial cycle %s at step %s", w.CycleID, w.Step)
	}
	return action.Result{Render: "object", Object: v, Summary: summary}
}

func runState(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	w, err := load(ctx, env)
	if err != nil {
		return action.Result{}, err
	}
	return view(env, w), nil
}

func newCycleID(at time.Time) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cycle: id: %w", err)
	}
	return at.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b), nil
}

func runStart(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*startParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	if p.SampleSize < 0 {
		return action.Result{}, invalid("sample_size must be 0 or more")
	}
	if p.EntitiesVerification == "" {
		p.EntitiesVerification = entitywiring.CycleVerificationLocal
	}
	if err := entitywiring.ValidateCycleVerification(p.EntitiesVerification); err != nil {
		return action.Result{}, invalid("entities_verification must be local, full or none")
	}
	mu.Lock()
	defer mu.Unlock()
	existing, err := load(ctx, env)
	if err != nil {
		return action.Result{}, err
	}
	if existing != nil {
		return action.Result{}, action.Conflict(fmt.Errorf("editorial cycle %s is already in progress; continue or abandon it", existing.CycleID))
	}
	at := env.Now()
	id, err := newCycleID(at)
	if err != nil {
		return action.Result{}, err
	}
	w := &Wizard{CycleID: id, StartedAt: at, Step: StepSync, SampleSize: p.SampleSize, EntitiesVerification: p.EntitiesVerification}
	for _, s := range Steps {
		w.step(s).UpdatedAt = at
	}
	w.step(StepSync).EnteredAt = &at
	if err := save(ctx, env, w); err != nil {
		return action.Result{}, err
	}
	return view(env, w), nil
}

func runNext(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*nextParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	mu.Lock()
	defer mu.Unlock()
	w, err := load(ctx, env)
	if err != nil {
		return action.Result{}, err
	}
	if w == nil {
		return action.Result{}, errNoCycle
	}
	if inflight[w.Step] {
		return action.Result{}, action.Conflict(fmt.Errorf("step %s is running; wait for it to finish", w.Step))
	}
	if w.Step == StepSummary {
		if p.Skip {
			return action.Result{}, action.Conflict(errors.New("summary cannot be skipped"))
		}
		return finish(ctx, env, w)
	}
	cur := w.step(w.Step)
	switch {
	case p.Skip:
		if !slices.Contains(skippable, w.Step) {
			return action.Result{}, action.Conflict(fmt.Errorf("step %s cannot be skipped", w.Step))
		}
		if cur.finished() {
			return action.Result{}, action.Conflict(fmt.Errorf("step %s is already finished; move on without skip", w.Step))
		}
		if err := w.set(w.Step, StatusSkipped, skipResult(env, w.Step, cur), "", env.Now()); err != nil {
			return action.Result{}, err
		}
	case !cur.finished():
		return action.Result{}, action.Conflict(fmt.Errorf("step %s is not finished (%s)", w.Step, cur.Status))
	}
	w.Step = Steps[slices.Index(Steps, w.Step)+1]
	entered := env.Now()
	w.step(w.Step).EnteredAt = &entered
	if w.Step == StepSummary {
		// The summary has nothing to run: it is ready as soon as it is reached.
		if err := w.set(StepSummary, StatusDone, nil, "", env.Now()); err != nil {
			return action.Result{}, err
		}
	}
	if err := save(ctx, env, w); err != nil {
		return action.Result{}, err
	}
	return view(env, w), nil
}

// skipReason is the result recorded for a skipped step.
type skipReason struct {
	SkipReason string `json:"skip_reason"`
	// Warning is set on a skipped Telegram import (the CLI's warning).
	Warning string `json:"warning,omitempty"`
}

func skipResult(env action.Env, step string, cur *StepState) any {
	reason := disabledReasons(env)[step]
	if reason == "" {
		reason = msgSkipped
	}
	r := skipReason{SkipReason: reason}
	if step == StepTelegram {
		r.Warning = msgTelegramSkipWarning
	}
	if step == StepTraining && len(cur.Result) > 0 {
		// A winner waiting for a decision is declined by skipping training.
		var tr trainResult
		if json.Unmarshal(cur.Result, &tr) == nil && tr.Winner != nil && tr.Promotion == "" {
			tr.Promotion = promotionDeclined
			tr.SkipReason = reason
			return tr
		}
	}
	return r
}

// finish ends the cycle on the summary step: it writes the audit report
// next to the operational database, as the CLI does, and clears the state.
func finish(ctx context.Context, env action.Env, w *Wizard) (action.Result, error) {
	report := buildReport(w)
	dir := filepath.Join(filepath.Dir(env.Config.SQLite.Path), "audit")
	path, err := editorialcycle.WriteAuditReport(dir, report, env.Now())
	if err != nil {
		return action.Result{}, err
	}
	if err := clear(ctx, env); err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render:  "object",
		Object:  finishView{stateView: newView(env, nil), AuditReport: path},
		Summary: "editorial cycle finished; audit report: " + path,
	}, nil
}

type finishView struct {
	stateView
	AuditReport string `json:"audit_report"`
}

func runAbandon(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	mu.Lock()
	defer mu.Unlock()
	w, err := load(ctx, env)
	if err != nil {
		return action.Result{}, err
	}
	if w == nil {
		return action.Result{}, errNoCycle
	}
	for s := range inflight {
		return action.Result{}, action.Conflict(fmt.Errorf("step %s is running; wait for it to finish", s))
	}
	ctx = context.WithoutCancel(ctx)
	if w.SessionID != "" {
		if err := cancelSession(ctx, env, w.SessionID); err != nil {
			return action.Result{}, err
		}
	}
	if err := clear(ctx, env); err != nil {
		return action.Result{}, err
	}
	res := view(env, nil)
	res.Summary = fmt.Sprintf("editorial cycle %s abandoned", w.CycleID)
	return res, nil
}

// cancelSession cancels the review session unless it already ended
// (completed or cancelled); a session the database no longer has is
// ignored.
func cancelSession(ctx context.Context, env action.Env, id string) error {
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(env.DB)
	session, err := sessions.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if session.Status == "completed" || session.Status == "cancelled" {
		return nil
	}
	return editorialcycle.Abandon(ctx, editorialcycle.Deps{Sessions: sessions, Clock: clock(env.Now)}, session)
}

func invalid(msg string) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: msg}
}

// clock adapts env.Now to the out.Clock port.
type clock func() time.Time

func (c clock) Now() time.Time { return c() }
