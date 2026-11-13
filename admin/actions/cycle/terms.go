package cycle

import (
	"context"
	"errors"
	"fmt"
	"slices"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionarymining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarywiring"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const candidatePending = "pending"

// termsDeps wires the mined-term review; notices receives the reject lines.
func termsDeps(env action.Env, notices *logWriter) dictionarymining.ReviewDeps {
	d := dictionarywiring.MinedTermReview(env.DB, env.Config, notices)
	d.Clock = clock(env.Now)
	return d
}

// loadTermsStep loads the wizard at the mined-terms step with its result;
// open also refuses a finished or running step. The caller holds mu when
// open is set.
func loadTermsStep(ctx context.Context, env action.Env, open bool) (*Wizard, minedTermsResult, error) {
	var r minedTermsResult
	load := loadCurrent
	if open {
		load = loadOpenStep
	}
	w, err := load(ctx, env, StepMinedTerms)
	if err != nil {
		return nil, r, err
	}
	if w.SessionID == "" {
		return nil, r, errNoSession
	}
	decodeInto(w.step(StepMinedTerms), &r)
	return w, r, nil
}

func errNotPrepared() error {
	return action.Conflict(errors.New("prepare the mined terms first"))
}

// Mined-term queue shapes (cycle.terms_queue).
type (
	firstSeenJSON struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	termDecisionJSON struct {
		Status   string `json:"status"`
		Category string `json:"category"`
	}
	termItemJSON struct {
		Term             string            `json:"term"`
		Support          int               `json:"support"`
		FirstSeenArticle firstSeenJSON     `json:"first_seen_article"`
		CategoryGuess    string            `json:"category_guess"`
		Decision         *termDecisionJSON `json:"decision"`
	}
	termsQueueJSON struct {
		Items      []termItemJSON `json:"items"`
		Categories []string       `json:"categories"`
		Undecided  int            `json:"undecided"`
		Warnings   []string       `json:"warnings"`
	}
)

// offeredCandidates returns the stored candidate of each offered term, in
// the offered order, and how many are still undecided.
func offeredCandidates(ctx context.Context, d dictionarymining.ReviewDeps, sessionID string, r minedTermsResult) ([]out.DictionaryCandidate, int, error) {
	all, err := d.Candidates.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, 0, err
	}
	byTerm := make(map[string]out.DictionaryCandidate, len(all))
	for _, c := range all {
		byTerm[c.Term] = c
	}
	var offered []out.DictionaryCandidate
	undecided := 0
	for _, term := range r.Offered {
		c, ok := byTerm[term]
		if !ok {
			continue
		}
		if c.Status == candidatePending {
			undecided++
		}
		offered = append(offered, c)
	}
	return offered, undecided, nil
}

func termsQueue(ctx context.Context, env action.Env, d dictionarymining.ReviewDeps, sessionID string, r minedTermsResult) (termsQueueJSON, error) {
	offered, undecided, err := offeredCandidates(ctx, d, sessionID, r)
	if err != nil {
		return termsQueueJSON{}, err
	}
	q := termsQueueJSON{Items: make([]termItemJSON, 0, len(offered)), Categories: r.Categories, Undecided: undecided, Warnings: r.Warnings}
	if q.Categories == nil {
		q.Categories = []string{}
	}
	if q.Warnings == nil {
		q.Warnings = []string{}
	}
	articles := sqliteadapter.NewArticleRepository(env.DB)
	for _, c := range offered {
		item := termItemJSON{Term: c.Term, Support: c.SupportCount, CategoryGuess: c.CategoryGuess, FirstSeenArticle: firstSeenJSON{ID: c.FirstSeenArticle}}
		if c.FirstSeenArticle != "" {
			a, err := articles.FindByID(ctx, c.FirstSeenArticle)
			switch {
			case err == nil:
				item.FirstSeenArticle.Title, item.FirstSeenArticle.URL = a.Title, a.OriginalURL
			case errors.Is(err, article.ErrNotFound):
				// purged: the id alone
			default:
				return termsQueueJSON{}, err
			}
		}
		if c.Status != candidatePending {
			item.Decision = &termDecisionJSON{Status: c.Status, Category: c.CategoryGuess}
		}
		q.Items = append(q.Items, item)
	}
	return q, nil
}

func termsSummary(q termsQueueJSON) string {
	return fmt.Sprintf("%d mined term(s) offered, %d undecided", len(q.Items), q.Undecided)
}

func runTermsPrepare(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	mu.Lock()
	defer mu.Unlock()
	w, r, err := loadTermsStep(ctx, env, true)
	if err != nil {
		return action.Result{}, err
	}
	notices := newLogWriter(env.Log)
	d := termsDeps(env, notices)
	p, err := dictionarymining.Prepare(ctx, d, w.SessionID)
	for _, wn := range p.Warnings {
		env.Log.Warn(wn)
	}
	if err != nil {
		return action.Result{}, fmt.Errorf("dictionary update: %w", err)
	}
	// Terms decided earlier stay offered: Prepare offers only pending ones.
	for _, c := range p.Offered {
		if !slices.Contains(r.Offered, c.Term) {
			r.Offered = append(r.Offered, c.Term)
		}
	}
	if r.Offered == nil {
		r.Offered = []string{}
	}
	r.Prepared, r.Categories, r.Warnings, r.Mined = true, p.Categories, p.Warnings, p.MinedCount
	if err := w.set(StepMinedTerms, StatusPending, r, "", env.Now()); err != nil {
		return action.Result{}, err
	}
	if err := save(context.WithoutCancel(ctx), env, w); err != nil {
		return action.Result{}, err
	}
	q, err := termsQueue(ctx, env, d, w.SessionID, r)
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: q,
		Summary: fmt.Sprintf("session %s: %d candidate term(s) mined; %s", w.SessionID, p.MinedCount, termsSummary(q))}, nil
}

func runTermsQueue(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	w, r, err := loadTermsStep(ctx, env, false)
	if err != nil {
		return action.Result{}, err
	}
	if !r.Prepared {
		return action.Result{}, errNotPrepared()
	}
	q, err := termsQueue(ctx, env, termsDeps(env, newLogWriter(env.Log)), w.SessionID, r)
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: q, Summary: termsSummary(q)}, nil
}

type termsDecideParams struct {
	Term     string `json:"term" help:"offered mined term to decide" required:"true"`
	Action   string `json:"action" help:"approve (into category, or the term's guess when it is one of the dictionary's categories), reject (also added to the mining policy's ignored_terms) or categorize (approve into category)" enum:"approve,reject,categorize" required:"true"`
	Category string `json:"category" help:"dictionary category for approve or categorize"`
}

func runTermsDecide(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*termsDecideParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	mu.Lock()
	defer mu.Unlock()
	w, r, err := loadTermsStep(ctx, env, true)
	if err != nil {
		return action.Result{}, err
	}
	if !r.Prepared {
		return action.Result{}, errNotPrepared()
	}
	notices := newLogWriter(env.Log)
	d := termsDeps(env, notices)
	offered, _, err := offeredCandidates(ctx, d, w.SessionID, r)
	if err != nil {
		return action.Result{}, err
	}
	i := slices.IndexFunc(offered, func(c out.DictionaryCandidate) bool { return c.Term == p.Term })
	if i < 0 {
		return action.Result{}, action.NotFound(fmt.Errorf("%q is not a mined term offered in this cycle", p.Term))
	}
	c := offered[i]
	if p.Category != "" && !slices.Contains(r.Categories, p.Category) {
		return action.Result{}, invalid(fmt.Sprintf("%q is not one of the dictionary's categories", p.Category))
	}
	switch p.Action {
	case dictionarymining.ActionApprove:
		// As the CLI: a guess outside the dictionary's categories asks for one.
		if p.Category == "" && !slices.Contains(r.Categories, c.CategoryGuess) {
			return action.Result{}, invalid(fmt.Sprintf("choose a category for %q: its guess is not one of the dictionary's categories", p.Term))
		}
	case dictionarymining.ActionCategorize:
		if p.Category == "" {
			return action.Result{}, invalid(fmt.Sprintf("choose a category for %q", p.Term))
		}
	case dictionarymining.ActionReject:
		if p.Category != "" {
			return action.Result{}, invalid("category applies to approve and categorize only")
		}
	}
	err = func() error {
		if p.Action == dictionarymining.ActionReject {
			// Reject appends the term to the mining policy file.
			action.ConfigFilesMu.Lock()
			defer action.ConfigFilesMu.Unlock()
		}
		return dictionarymining.Decide(context.WithoutCancel(ctx), d, w.SessionID, p.Term, p.Action, p.Category)
	}()
	if err != nil {
		return action.Result{}, err
	}
	notes := notices.flush()
	_, undecided, err := offeredCandidates(ctx, d, w.SessionID, r)
	if err != nil {
		return action.Result{}, err
	}
	category := p.Category
	if category == "" && p.Action == dictionarymining.ActionApprove {
		category = c.CategoryGuess
	}
	summary := fmt.Sprintf("%s: %s", p.Term, p.Action)
	if p.Action != dictionarymining.ActionReject {
		summary += " into " + category
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"term": p.Term, "action": p.Action, "category": category, "undecided": undecided,
			"notices": append([]string{}, notes...),
		},
		Summary: summary,
	}, nil
}

// termsFinishResult is the shape of cycle.terms_finish.
type termsFinishResult struct {
	Added   int    `json:"added"`
	Entries int    `json:"entries"`
	From    string `json:"from"`
	To      string `json:"to"`
}

func runTermsFinish(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if env.Config == nil {
		return action.Result{}, errNoConfig
	}
	mu.Lock()
	defer mu.Unlock()
	w, r, err := loadTermsStep(ctx, env, true)
	if err != nil {
		return action.Result{}, err
	}
	if !r.Prepared {
		return action.Result{}, errNotPrepared()
	}
	d := termsDeps(env, newLogWriter(env.Log))
	_, undecided, err := offeredCandidates(ctx, d, w.SessionID, r)
	if err != nil {
		return action.Result{}, err
	}
	if undecided > 0 {
		return action.Result{}, action.Conflict(fmt.Errorf("%d term(s) still undecided", undecided))
	}
	ctx = context.WithoutCancel(ctx)
	res, err := func() (dictionarymining.WriteResult, error) {
		action.ConfigFilesMu.Lock()
		defer action.ConfigFilesMu.Unlock()
		return dictionarymining.Write(ctx, d, w.SessionID)
	}()
	if err != nil {
		return action.Result{}, fmt.Errorf("dictionary update: %w", err)
	}
	r.Added, r.Entries, r.From, r.To = res.Approved, res.Added, res.FromVersion, res.ToVersion
	if err := w.set(StepMinedTerms, StatusDone, r, "", env.Now()); err != nil {
		return action.Result{}, err
	}
	if err := save(ctx, env, w); err != nil {
		return action.Result{}, err
	}
	// The CLI's closing line.
	summary := fmt.Sprintf("dictionary updated: %d entr%s added, version %s -> %s", res.Added, plural(res.Added), res.FromVersion, res.ToVersion)
	switch {
	case res.Approved == 0:
		summary = "no candidates approved — dictionary not modified"
	case res.Added == 0:
		summary = "no new candidates to write — dictionary not modified"
	}
	return action.Result{
		Render:  "object",
		Object:  termsFinishResult{Added: r.Added, Entries: r.Entries, From: r.From, To: r.To},
		Summary: summary,
	}, nil
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
