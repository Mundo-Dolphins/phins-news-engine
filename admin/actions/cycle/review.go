package cycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/activelearning"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialcycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/reviewdecision"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/eventrules"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Session statuses the review step reads (editorialcycle's).
const (
	sessionRunning   = "running"
	sessionCompleted = "completed"
)

// discoveryLLM builds the entity step's discovery LLM; nil is the default
// client. Tests substitute it.
var discoveryLLM entitywiring.DiscoveryLLMFactory

var errNoSession = action.Conflict(errors.New("open the article review first"))

// loadOpenStep loads the wizard at step and refuses a step that is running
// or already finished. The caller holds mu.
func loadOpenStep(ctx context.Context, env action.Env, step string) (*Wizard, error) {
	w, err := loadCurrent(ctx, env, step)
	if err != nil {
		return nil, err
	}
	if inflight[step] {
		return nil, action.Conflict(fmt.Errorf("step %s is running; wait for it to finish", step))
	}
	if w.step(step).finished() {
		return nil, action.Conflict(fmt.Errorf("step %s is already finished", step))
	}
	return w, nil
}

// reviewDeps wires the review session the way `editorial cycle` does.
func reviewDeps(env action.Env) editorialcycle.Deps {
	db := env.DB
	return editorialcycle.Deps{
		Sessions:          sqliteadapter.NewEditorialCycleSessionRepository(db),
		Candidates:        sqliteadapter.NewEditorialCycleSessionCandidateRepository(db),
		ReviewDecisions:   sqliteadapter.NewReviewDecisionRepository(db),
		Labels:            sqliteadapter.NewHumanLabelRepository(db),
		Articles:          sqliteadapter.NewArticleRepository(db),
		Events:            sqliteadapter.NewDecisionRepository(db),
		UOW:               sqliteadapter.NewUnitOfWork(db),
		Clock:             clock(env.Now),
		ExcludedSourceIDs: editorialcycle.ExcludedSourceIDs(env.Config),
	}
}

// excludeBypasses records the CLI's source-policy skip for the session's
// pending candidates from sources excluded from manual review, so they
// neither show nor block completion.
func excludeBypasses(ctx context.Context, deps editorialcycle.Deps, sessionID string) error {
	rows, err := deps.Candidates.ListPending(ctx, sessionID)
	if err != nil {
		return err
	}
	pending := editorialcycle.CandidatesFromFrozen(rows)
	for i, row := range rows {
		a, err := deps.Articles.FindByID(ctx, row.ArticleID)
		if err != nil {
			return fmt.Errorf("editorial cycle: hydrate article %s: %w", row.ArticleID, err)
		}
		pending[i].Article = a
	}
	_, err = editorialcycle.ExcludeSourceBypasses(ctx, deps, sessionID, pending)
	return err
}

// reviewSet is the session's manual review set: its frozen candidates in
// the CLI's order, hydrated, without purged articles and the sources
// excluded from manual review, with the decision of each one (if any).
type reviewSet struct {
	candidates []activelearning.Candidate
	decisions  map[string]out.ReviewDecision
}

func (s reviewSet) undecided() int {
	n := 0
	for _, c := range s.candidates {
		if _, ok := s.decisions[c.Article.ID]; !ok {
			n++
		}
	}
	return n
}

func (s reviewSet) contains(articleID string) bool {
	for _, c := range s.candidates {
		if c.Article.ID == articleID {
			return true
		}
	}
	return false
}

func loadReviewSet(ctx context.Context, deps editorialcycle.Deps, sessionID string) (reviewSet, error) {
	rows, err := deps.Candidates.ListFrozen(ctx, sessionID)
	if err != nil {
		return reviewSet{}, err
	}
	frozen := editorialcycle.CandidatesFromFrozen(rows)
	set := reviewSet{decisions: map[string]out.ReviewDecision{}}
	for i, row := range rows {
		a, err := deps.Articles.FindByID(ctx, row.ArticleID)
		if errors.Is(err, article.ErrNotFound) {
			continue // purged since the session froze it
		}
		if err != nil {
			return reviewSet{}, fmt.Errorf("editorial cycle: hydrate article %s: %w", row.ArticleID, err)
		}
		if deps.ExcludedSourceIDs[a.Source.ID.String()] {
			continue
		}
		frozen[i].Article = a
		set.candidates = append(set.candidates, frozen[i])
	}
	decisions, err := deps.ReviewDecisions.ListBySession(ctx, sessionID)
	if err != nil {
		return reviewSet{}, err
	}
	for _, d := range decisions {
		set.decisions[d.ArticleID] = d
	}
	return set, nil
}

// reviewOpenResult is the shape of cycle.review_open.
type reviewOpenResult struct {
	SessionID string `json:"session_id"`
	Total     int    `json:"total"`
	Decided   int    `json:"decided"`
}

func runReviewOpen(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	mu.Lock()
	defer mu.Unlock()
	w, err := loadOpenStep(ctx, env, StepArticleReview)
	if err != nil {
		return action.Result{}, err
	}
	deps := reviewDeps(env)
	session, summary, err := openSession(ctx, env, deps, w)
	if err != nil {
		return action.Result{}, err
	}
	if session.Status != sessionCompleted {
		if err := excludeBypasses(ctx, deps, session.ID); err != nil {
			return action.Result{}, err
		}
	}
	w.SessionID = session.ID
	if err := save(context.WithoutCancel(ctx), env, w); err != nil {
		return action.Result{}, err
	}
	set, err := loadReviewSet(ctx, deps, session.ID)
	if err != nil {
		return action.Result{}, err
	}
	result := reviewOpenResult{SessionID: session.ID, Total: len(set.candidates), Decided: len(set.candidates) - set.undecided()}
	return action.Result{Render: "object", Object: result, Summary: summary}, nil
}

// openSession starts or resumes the review session as `editorial cycle
// --resume` does: the latest paused, failed or running session (a paused
// CLI session too) is resumed with its frozen set; otherwise a new session
// freezes a fresh selection. A session this cycle already completed (its
// finish not recorded) is returned as it is.
func openSession(ctx context.Context, env action.Env, deps editorialcycle.Deps, w *Wizard) (out.EditorialCycleSession, string, error) {
	if w.SessionID != "" {
		s, err := sqliteadapter.NewEditorialCycleSessionRepository(env.DB).Get(ctx, w.SessionID)
		if err == nil && s.Status == sessionCompleted {
			return s, "session " + s.ID + " is already complete; finish the review", nil
		}
	}
	sampleSize := w.SampleSize
	if sampleSize <= 0 {
		sampleSize = env.Config.EditorialCycle.SampleSize
	}
	fresh, err := editorialcycle.SelectFresh(ctx, deps, env.Config, sampleSize)
	if err != nil {
		return out.EditorialCycleSession{}, "", err
	}
	session, resumed, backfilled, err := editorialcycle.StartOrResume(ctx, deps, editorialcycle.Flags{SampleSize: sampleSize}, fresh)
	if err != nil {
		return out.EditorialCycleSession{}, "", err
	}
	if backfilled {
		env.Log.Warn(fmt.Sprintf("legacy session %s: no recorded candidate set — recomputed from the current corpus; membership may differ from the original run", session.ID))
	}
	if !resumed {
		return session, "started session " + session.ID, nil
	}
	resolvable, err := deps.Candidates.CountResolvable(ctx, session.ID)
	if err != nil {
		return out.EditorialCycleSession{}, "", fmt.Errorf("editorial cycle: count resolvable candidates: %w", err)
	}
	if resolvable < session.TotalCandidates {
		env.Log.Warn(fmt.Sprintf("warning: %d of %d originally-frozen candidate(s) are no longer in the corpus",
			session.TotalCandidates-resolvable, session.TotalCandidates))
	}
	return session, "resuming session " + session.ID, nil
}

// Review card shapes (cycle.review_queue).
type (
	reviewSourceJSON struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	priorLabelJSON struct {
		Label string `json:"label"`
		By    string `json:"by"`
	}
	clusterOtherJSON struct {
		ArticleID   string    `json:"article_id"`
		Title       string    `json:"title"`
		Source      string    `json:"source"`
		URL         string    `json:"url"`
		PublishedAt time.Time `json:"published_at"`
	}
	clusterJSON struct {
		ID     string             `json:"id"`
		Role   string             `json:"role"`
		Others []clusterOtherJSON `json:"others"`
	}
	reviewDecisionJSON struct {
		Action string `json:"action"`
		Notes  string `json:"notes"`
	}
	reviewItemJSON struct {
		ArticleID string `json:"article_id"`
		// Index is the 1-based position in the review, as the CLI's [i/n].
		Index       int              `json:"index"`
		Title       string           `json:"title"`
		Summary     string           `json:"summary"`
		URL         string           `json:"url"`
		Source      reviewSourceJSON `json:"source"`
		PublishedAt time.Time        `json:"published_at"`
		Score       float64          `json:"score"`
		// Ambiguity is the CLI's "ambiguity signal".
		Ambiguity  float64             `json:"ambiguity"`
		PriorLabel *priorLabelJSON     `json:"prior_label"`
		Cluster    clusterJSON         `json:"cluster"`
		Decision   *reviewDecisionJSON `json:"decision"`
	}
	reviewQueueJSON struct {
		Items     []reviewItemJSON `json:"items"`
		Total     int              `json:"total"`
		Undecided int              `json:"undecided"`
	}
)

func runReviewQueue(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	w, err := loadCurrent(ctx, env, StepArticleReview)
	if err != nil {
		return action.Result{}, err
	}
	if w.SessionID == "" {
		return action.Result{}, errNoSession
	}
	deps := reviewDeps(env)
	set, err := loadReviewSet(ctx, deps, w.SessionID)
	if err != nil {
		return action.Result{}, err
	}
	articles := sqliteadapter.NewArticleRepository(env.DB)
	labels := sqliteadapter.NewHumanLabelRepository(env.DB)
	q := reviewQueueJSON{Items: make([]reviewItemJSON, 0, len(set.candidates)), Total: len(set.candidates), Undecided: set.undecided()}
	for i, c := range set.candidates {
		item, err := reviewItem(ctx, articles, labels, i, c)
		if err != nil {
			return action.Result{}, err
		}
		if d, ok := set.decisions[c.Article.ID]; ok {
			item.Decision = &reviewDecisionJSON{Action: d.Action, Notes: d.Notes}
		}
		q.Items = append(q.Items, item)
	}
	return action.Result{Render: "object", Object: q,
		Summary: fmt.Sprintf("%d article(s) to review, %d undecided", q.Total, q.Undecided)}, nil
}

// reviewItem is the CLI's review screen for one candidate, with every other
// article of its cluster rather than their count.
func reviewItem(ctx context.Context, articles *sqliteadapter.ArticleRepository, labels *sqliteadapter.HumanLabelRepository, index int, c activelearning.Candidate) (reviewItemJSON, error) {
	a := c.Article
	item := reviewItemJSON{
		ArticleID: a.ID, Index: index + 1, Title: a.Title, Summary: a.Summary, URL: a.OriginalURL,
		Source:      reviewSourceJSON{Name: a.Source.Name, Type: string(a.Source.Type)},
		PublishedAt: a.PublishedAt, Score: c.Score, Ambiguity: c.Signals.EventScore,
		Cluster: clusterJSON{ID: a.ClusterID, Role: a.ClusterMemberRole, Others: []clusterOtherJSON{}},
	}
	history, err := labels.ListByArticle(ctx, a.ID)
	if err != nil {
		return item, err
	}
	if len(history) > 0 {
		last := history[len(history)-1]
		item.PriorLabel = &priorLabelJSON{Label: last.Label, By: last.LabeledBy}
	}
	if a.ClusterID == "" {
		return item, nil
	}
	// A negative limit is no limit in SQLite.
	others, err := articles.ListByClusterID(ctx, a.ClusterID, a.ID, -1)
	if err != nil {
		return item, err
	}
	for _, o := range others {
		item.Cluster.Others = append(item.Cluster.Others, clusterOtherJSON{
			ArticleID: o.ID, Title: o.Title, Source: o.Source.Name, URL: o.OriginalURL, PublishedAt: o.PublishedAt,
		})
	}
	return item, nil
}

type reviewDecideParams struct {
	ArticleID string `json:"article_id" help:"article of the review session to decide" required:"true"`
	Action    string `json:"action" help:"the CLI's review keys: p publish, r reject, a ambiguous, d duplicate, s same_news, u update, e edit_category (withdraws the prior human label, no category asked), k skip" enum:"publish,reject,ambiguous,duplicate,same_news,update,edit_category,skip" required:"true"`
}

func runReviewDecide(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*reviewDecideParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	mu.Lock()
	defer mu.Unlock()
	w, err := loadOpenStep(ctx, env, StepArticleReview)
	if err != nil {
		return action.Result{}, err
	}
	if w.SessionID == "" {
		return action.Result{}, errNoSession
	}
	deps := reviewDeps(env)
	set, err := loadReviewSet(ctx, deps, w.SessionID)
	if err != nil {
		return action.Result{}, err
	}
	if !set.contains(p.ArticleID) {
		return action.Result{}, action.NotFound(fmt.Errorf("article %s is not in review session %s", p.ArticleID, w.SessionID))
	}
	if err := editorialcycle.ApplyDecision(ctx, deps, w.SessionID, p.ArticleID, reviewdecision.Action(p.Action), ""); err != nil {
		return action.Result{}, err
	}
	set.decisions[p.ArticleID] = out.ReviewDecision{ArticleID: p.ArticleID, Action: p.Action}
	undecided := set.undecided()
	return action.Result{
		Render:  "object",
		Object:  map[string]any{"article_id": p.ArticleID, "action": p.Action, "undecided": undecided},
		Summary: fmt.Sprintf("%s: %s (%d undecided)", p.ArticleID, p.Action, undecided),
	}, nil
}

func runReviewFinish(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	mu.Lock()
	defer mu.Unlock()
	w, err := loadOpenStep(ctx, env, StepArticleReview)
	if err != nil {
		return action.Result{}, err
	}
	if w.SessionID == "" {
		return action.Result{}, errNoSession
	}
	deps := reviewDeps(env)
	session, err := sqliteadapter.NewEditorialCycleSessionRepository(env.DB).Get(ctx, w.SessionID)
	if err != nil {
		return action.Result{}, err
	}
	if session.Status != sessionCompleted {
		if err := excludeBypasses(ctx, deps, session.ID); err != nil {
			return action.Result{}, err
		}
	}
	set, err := loadReviewSet(ctx, deps, session.ID)
	if err != nil {
		return action.Result{}, err
	}
	if n := set.undecided(); n > 0 {
		return action.Result{}, action.Conflict(fmt.Errorf("%d article(s) still undecided", n))
	}
	ctx = context.WithoutCancel(ctx)
	if session.Status != sessionCompleted {
		if err := editorialcycle.Complete(ctx, deps, session); err != nil {
			return action.Result{}, err
		}
	}
	// As the CLI: the session's decisions counted by label.
	result := articleReviewResult{ArticlesReviewed: len(set.candidates)}
	for _, d := range set.decisions {
		switch d.Label {
		case "publish":
			result.Publish++
		case "reject":
			result.Reject++
		}
	}
	if err := w.set(StepArticleReview, StatusDone, result, "", env.Now()); err != nil {
		return action.Result{}, err
	}
	if err := save(ctx, env, w); err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: result, Summary: "review complete"}, nil
}

func runReviewPause(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	mu.Lock()
	defer mu.Unlock()
	w, err := loadOpenStep(ctx, env, StepArticleReview)
	if err != nil {
		return action.Result{}, err
	}
	if w.SessionID == "" {
		return action.Result{}, errNoSession
	}
	session, err := sqliteadapter.NewEditorialCycleSessionRepository(env.DB).Get(ctx, w.SessionID)
	if err != nil {
		return action.Result{}, err
	}
	if session.Status == sessionRunning {
		if err := editorialcycle.Pause(context.WithoutCancel(ctx), reviewDeps(env), session, "save-and-exit"); err != nil {
			return action.Result{}, err
		}
		session.Status = "paused"
	}
	return action.Result{
		Render:  "object",
		Object:  map[string]any{"session_id": session.ID, "status": session.Status},
		Summary: "session saved — open the review again to resume",
	}, nil
}

// logWriter turns the lines written to it into job log lines, keeping the
// warnings.
type logWriter struct {
	mu       sync.Mutex
	log      *slog.Logger
	buf      bytes.Buffer
	warnings []string
}

func newLogWriter(log *slog.Logger) *logWriter { return &logWriter{log: log} }

func (l *logWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			// Incomplete line: keep it for the next write.
			l.buf.Reset()
			l.buf.WriteString(line)
			return len(p), nil
		}
		l.emit(strings.TrimRight(line, "\n"))
	}
}

func (l *logWriter) emit(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if strings.HasPrefix(line, "WARNING") {
		l.warnings = append(l.warnings, line)
		l.log.Warn(line)
		return
	}
	l.log.Info(line)
}

// flush logs a trailing partial line and returns the warnings.
func (l *logWriter) flush() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rest := l.buf.String(); rest != "" {
		l.buf.Reset()
		l.emit(rest)
	}
	return append([]string{}, l.warnings...)
}

const msgNoReviewedArticles = "entities: the session has no reviewed articles"

func runEntitiesDiscover(ctx context.Context, env action.Env, _ any) (_ action.Result, err error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	var prior entitiesResult
	run, err := beginStep(ctx, env, StepEntities, func(s *StepState) error {
		if reason := entitywiring.CycleStepDisabledReason(cfg); reason != "" {
			return action.Conflict(fmt.Errorf("entities: step disabled (%s)", reason))
		}
		decodeInto(s, &prior)
		return nil
	})
	if err != nil {
		return action.Result{}, err
	}
	defer func() { run.finish(ctx, recover(), &err) }()
	if run.w.SessionID == "" {
		return action.Result{}, run.fail(ctx, nil, errNoSession)
	}
	// Approvals count from the first discovery of the step on.
	started := env.Now()
	if prior.DiscoveryStartedAt != nil {
		started = *prior.DiscoveryStartedAt
	}
	logs := newLogWriter(env.Log)
	deps, in, err := entitywiring.CycleDeps(ctx, cfg, env.DB, run.w.SessionID, run.w.EntitiesVerification, logs, discoveryLLM)
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, err)
	}
	if len(in.SessionArticleIDs) == 0 {
		// As the CLI: nothing to discover or review.
		if err := run.end(ctx, StatusDone, entitiesResult{}, ""); err != nil {
			return action.Result{}, err
		}
		return action.Result{Render: "object", Object: entitiesResult{}, Summary: msgNoReviewedArticles}, nil
	}
	in.Out = logs
	res, err := entitycycle.Discover(ctx, deps, in)
	warnings := logs.flush()
	if err != nil {
		return action.Result{}, run.fail(ctx, nil, fmt.Errorf("entities: %w", err))
	}
	result := entitiesResult{
		Articles: res.Articles, Proposed: res.Proposed, DiscoveryFailed: res.DiscoveryFailed,
		Warnings: warnings, DiscoveryStartedAt: &started,
	}
	// The step waits for the review of the session's proposals.
	if err := run.end(ctx, StatusPending, result, ""); err != nil {
		return action.Result{}, err
	}
	summary := fmt.Sprintf("entities: %d articles, %d batches (%d failed), %d proposals created or updated",
		res.Articles, res.Batches, res.BatchesFailed, res.Proposed)
	return action.Result{Render: "object", Object: result, Summary: summary}, nil
}

func runEntitiesFinish(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	cfg := env.Config
	mu.Lock()
	defer mu.Unlock()
	w, err := loadOpenStep(ctx, env, StepEntities)
	if err != nil {
		return action.Result{}, err
	}
	if w.SessionID == "" {
		return action.Result{}, errNoSession
	}
	var r entitiesResult
	decodeInto(w.step(StepEntities), &r)
	if r.DiscoveryStartedAt == nil {
		return action.Result{}, action.Conflict(errors.New("run the entity discovery first"))
	}
	ids, err := entitywiring.SessionArticleIDs(ctx, env.DB, w.SessionID)
	if err != nil {
		return action.Result{}, err
	}
	all, err := sqliteentityproposal.NewEntityProposalRepository(env.DB).List(ctx, "", "")
	if err != nil {
		return action.Result{}, err
	}
	// The operator's decisions on the session's proposals since the
	// discovery (automatic rejections carry a reason).
	r.Approved, r.Rejected = 0, 0
	for _, p := range entitycycle.SourcedFrom(all, ids) {
		if p.DecisionReason != "" || p.DecidedAt.Before(*r.DiscoveryStartedAt) {
			continue
		}
		switch p.Status {
		case entityproposal.StatusApproved:
			r.Approved++
		case entityproposal.StatusRejected:
			r.Rejected++
		}
	}
	ctx = context.WithoutCancel(ctx)
	if r.Approved > 0 {
		// Regeneration is idempotent: an approval of the session regenerates.
		r.Regenerated, err = func() (bool, error) {
			action.ConfigFilesMu.Lock()
			defer action.ConfigFilesMu.Unlock()
			return entitycycle.FinishReview(ctx, entitycycle.Deps{Regenerate: func() error {
				_, err := entitywiring.GenerateCatalog(cfg.Dictionary.Path, cfg.Clustering.EntityCatalog.GeneratedPath)
				return err
			}}, true)
		}()
		if err != nil {
			return action.Result{}, err
		}
	}
	if err := w.set(StepEntities, StatusDone, r, "", env.Now()); err != nil {
		return action.Result{}, err
	}
	if err := save(ctx, env, w); err != nil {
		return action.Result{}, err
	}
	summary := fmt.Sprintf("Entity review complete. approved: %d, rejected: %d", r.Approved, r.Rejected)
	if r.Regenerated {
		summary += "; entity catalog regenerated"
	}
	return action.Result{Render: "object", Object: r, Summary: summary}, nil
}

type eventRulesFinishParams struct {
	DiscoveryFailed  bool `json:"discovery_failed" help:"every discovery call failed (from events.rules_discover)"`
	Applied          int  `json:"applied" help:"rules applied by events.rules_apply in this step"`
	GateBlocked      bool `json:"gate_blocked" help:"the clustering gate blocked events.rules_apply"`
	GateNotExercised bool `json:"gate_not_exercised" help:"the clustering gate ran but changed no benchmark article"`
}

func runEventRulesFinish(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*eventRulesFinishParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	if p.Applied < 0 {
		return action.Result{}, invalid("applied must be 0 or more")
	}
	cfg := env.Config
	mu.Lock()
	defer mu.Unlock()
	w, err := loadOpenStep(ctx, env, StepEventRules)
	if err != nil {
		return action.Result{}, err
	}
	// Only discovery needs the LLM: the review can finish without it.
	if reason := eventrules.ReviewDisabledReason(cfg); reason != "" {
		return action.Result{}, action.Conflict(fmt.Errorf("event rules: step disabled (%s)", reason))
	}
	// The counters events.rules_queue shows; nothing is decided here.
	d := eventrules.CycleDeps(cfg, env.DB, eventrules.DefaultDatasetPath, nil, func(m string) { env.Log.Warn(m) }, env.Now)
	c, err := eventrulecycle.Prepare(ctx, d)
	if err != nil {
		return action.Result{}, err
	}
	var pending []eventrulecycle.Assessment
	if len(c.Sample) > 0 {
		if pending, err = eventrulecycle.Pending(ctx, d, c, eventruleproposal.DefaultCheckOptions()); err != nil {
			return action.Result{}, err
		}
	}
	approved, err := eventrulecycle.ApprovedNotApplied(ctx, d)
	if err != nil {
		return action.Result{}, err
	}
	r := eventRulesResult{
		Sampled: len(c.Sample), Proposed: len(pending), Approved: len(approved) + p.Applied, Applied: p.Applied,
		DiscoveryFailed: p.DiscoveryFailed, GateBlocked: p.GateBlocked, GateNotExercised: p.GateNotExercised,
	}
	if err := w.set(StepEventRules, StatusDone, r, "", env.Now()); err != nil {
		return action.Result{}, err
	}
	if err := save(context.WithoutCancel(ctx), env, w); err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: r, Summary: fmt.Sprintf(
		"event rules: %d sampled, %d pending, %d approved, %d applied", r.Sampled, r.Proposed, r.Approved, r.Applied)}, nil
}
