package entities

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycycle"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

type reviewQueueParams struct {
	Limit int `json:"limit" help:"maximum number of pending proposals to return, best-scored first (1..500)" default:"100"`
	// SessionID restricts the queue to the editorial cycle's entity step.
	SessionID string `json:"session_id" help:"only proposals sourced from the articles published or rejected in this editorial cycle review session"`
}

type catalogSearchParams struct {
	Query string `json:"query" help:"text to look for in entity names and terms (case and accent insensitive)" required:"true"`
	Limit int    `json:"limit" help:"maximum number of results (1..100)" default:"20"`
}

type verificationJSON struct {
	Position string `json:"position"`
	Role     string `json:"role"`
	Team     string `json:"team"`
	League   string `json:"league"`
}

type suggestionJSON struct {
	Entity     string `json:"entity"`
	Team       string `json:"team"`
	Basis      string `json:"basis"`
	Candidates string `json:"candidates"`
}

type evidenceJSON struct {
	ArticleID string `json:"article_id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
}

type queueItemJSON struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Kind           string            `json:"kind"`
	TargetEntityID string            `json:"target_entity_id"`
	Confidence     float64           `json:"confidence"`
	Occurrences    int               `json:"occurrences"`
	Score          float64           `json:"score"`
	Aliases        []string          `json:"aliases"`
	Verification   verificationJSON  `json:"verification"`
	Affiliation    string            `json:"affiliation"`
	Suggestion     suggestionJSON    `json:"suggestion"`
	Metadata       map[string]string `json:"metadata"`
	Evidence       []evidenceJSON    `json:"evidence"`
	WebSources     []webSourceJSON   `json:"web_sources"`
	FirstSeen      string            `json:"first_seen"`
	LastSeen       string            `json:"last_seen"`
}

type catalogResultJSON struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Team  string   `json:"team"`
	Terms []string `json:"terms"`
}

func limitError(limit, max int) error {
	return &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf("field %q must be between 1 and %d, got %d", "limit", max, limit)}
}

// runReviewQueue returns the pending proposals the way `entities review`
// shows them: best-scored first, with the verification and evidence the
// reviewer decides on.
func runReviewQueue(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*reviewQueueParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if p.Limit < 1 || p.Limit > 500 {
		return action.Result{}, limitError(p.Limit, 500)
	}
	proposals, err := repo(env).List(ctx, "", string(entityproposal.StatusPending))
	if err != nil {
		return action.Result{}, err
	}
	if p.SessionID != "" {
		ids, err := entitywiring.SessionArticleIDs(ctx, env.DB, p.SessionID)
		if err != nil {
			return action.Result{}, err
		}
		proposals = entitycycle.SourcedFrom(proposals, ids)
	}
	sort.SliceStable(proposals, func(i, j int) bool {
		return entityproposal.Score(proposals[i]) > entityproposal.Score(proposals[j])
	})
	if len(proposals) > p.Limit {
		proposals = proposals[:p.Limit]
	}
	articles := &memoArticles{finder: sqliteadapter.NewArticleRepository(env.DB), seen: map[string]articleLookup{}}
	items := make([]queueItemJSON, 0, len(proposals))
	for _, pr := range proposals {
		item, err := queueItem(ctx, articles, pr)
		if err != nil {
			return action.Result{}, err
		}
		items = append(items, item)
	}
	return action.Result{Render: "object", Object: map[string]any{"items": items},
		Summary: fmt.Sprintf("%d pending entity proposal(s)", len(items))}, nil
}

type articleFinder interface {
	FindByID(ctx context.Context, id string) (article.Article, error)
}

type articleLookup struct {
	art article.Article
	err error
}

// memoArticles answers FindByID once per article id, as one article is often
// the evidence of several proposals.
type memoArticles struct {
	finder articleFinder
	seen   map[string]articleLookup
}

func (m *memoArticles) FindByID(ctx context.Context, id string) (article.Article, error) {
	if l, ok := m.seen[id]; ok {
		return l.art, l.err
	}
	a, err := m.finder.FindByID(ctx, id)
	m.seen[id] = articleLookup{a, err}
	return a, err
}

func queueItem(ctx context.Context, articles articleFinder, pr entityproposal.Proposal) (queueItemJSON, error) {
	md := pr.Metadata
	item := queueItemJSON{
		ID: pr.ID, Name: pr.CanonicalName, Type: string(pr.EntityType), Kind: string(pr.Kind),
		TargetEntityID: pr.TargetEntityID, Confidence: pr.Confidence, Occurrences: pr.Occurrences,
		Score: entityproposal.Score(pr), Aliases: append([]string{}, pr.Aliases...),
		Verification: verificationJSON{Position: md["position"], Role: md["role"], Team: md["team"], League: md["league"]},
		Affiliation:  entitydiscovery.AffiliationLabel(pr),
		Suggestion: suggestionJSON{Entity: md["suggested_entity"], Team: md["suggested_team"],
			Basis: md["suggested_basis"], Candidates: md["suggested_candidates"]},
		Metadata: map[string]string{}, Evidence: []evidenceJSON{}, WebSources: []webSourceJSON{},
		FirstSeen: pr.FirstSeen.Format("2006-01-02"), LastSeen: pr.LastSeen.Format("2006-01-02"),
	}
	for k, v := range md {
		if k != entitydiscovery.MetadataVerifyAttemptedAt {
			item.Metadata[k] = v
		}
	}
	for _, w := range pr.WebSources {
		item.WebSources = append(item.WebSources, webSourceJSON{w.Domain, w.URL, w.Title})
	}
	for _, s := range pr.Sources {
		ev := evidenceJSON{ArticleID: s.ArticleID, Title: s.ArticleTitle}
		a, err := articles.FindByID(ctx, s.ArticleID)
		switch {
		case err == nil:
			ev.URL = a.OriginalURL
		case errors.Is(err, article.ErrNotFound):
			// purged article: keep the title, no link
		default:
			return queueItemJSON{}, err
		}
		item.Evidence = append(item.Evidence, ev)
	}
	return item, nil
}

// runCatalogSearch looks the query up in the merged entity catalog by
// canonical name and terms, ignoring case, accents and punctuation (the same
// entityproposal.Normalize the proposals are keyed with).
func runCatalogSearch(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*catalogSearchParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	query := entityproposal.Normalize(p.Query)
	if query == "" {
		return action.Result{}, invalid(errors.New(`field "query" must not be empty`))
	}
	if p.Limit < 1 || p.Limit > 100 {
		return action.Result{}, limitError(p.Limit, 100)
	}
	rules, err := loadCatalog(env.Config.Clustering.EntityCatalog.ManualPath, env.Config.Clustering.EntityCatalog.GeneratedPath)
	if err != nil {
		return action.Result{}, err
	}
	type ranked struct {
		rule eventsignals.EntityRule
		tier int
	}
	var matches []ranked
	for _, r := range rules {
		if tier := matchTier(r.CanonicalName, r.Terms, query); tier > 0 {
			matches = append(matches, ranked{r, tier})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].tier < matches[j].tier })
	if len(matches) > p.Limit {
		matches = matches[:p.Limit]
	}
	results := make([]catalogResultJSON, 0, len(matches))
	for _, m := range matches {
		r := m.rule
		results = append(results, catalogResultJSON{
			ID: r.ID, Name: r.CanonicalName, Type: string(r.Kind), Team: r.TeamID, Terms: append([]string{}, r.Terms...),
		})
	}
	return action.Result{Render: "object", Object: map[string]any{"results": results},
		Summary: fmt.Sprintf("%d catalog entit(ies) match", len(results))}, nil
}

// loadCatalog reads the manual catalog merged with the generated one; the
// generated file is optional (it does not exist until entities.generate has
// run), in which case the manual catalog alone is used.
func loadCatalog(manualPath, generatedPath string) ([]eventsignals.EntityRule, error) {
	if _, err := os.Stat(generatedPath); errors.Is(err, fs.ErrNotExist) {
		return entitycatalog.LoadOne(manualPath)
	}
	return entitycatalog.Load(manualPath, generatedPath)
}

// matchTier ranks how well query matches an entity: 1 when the name or a term
// equals it, 2 when one starts with it, 3 when one contains it, 0 for no
// match. All comparisons use the normalised text.
func matchTier(name string, terms []string, query string) int {
	best := 0
	for _, text := range append([]string{name}, terms...) {
		n := entityproposal.Normalize(text)
		tier := 0
		switch {
		case n == query:
			return 1
		case strings.HasPrefix(n, query):
			tier = 2
		case strings.Contains(n, query):
			tier = 3
		}
		if tier != 0 && (best == 0 || tier < best) {
			best = tier
		}
	}
	return best
}
