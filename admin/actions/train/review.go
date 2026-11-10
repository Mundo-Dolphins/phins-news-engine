package train

import (
	"context"
	"fmt"
	"strings"
	"time"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteeditoriallabelproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeditoriallabelproposal"
	sqlitereviewqueue "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlitereviewqueue"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/trainreview"
)

const (
	reviewSource = "admin:train-review"
	// reviewBodyLimit is the most body characters a queue item carries.
	reviewBodyLimit = 4000
)

type reviewQueueParams struct {
	Limit int `json:"limit" help:"maximum number of queue items to return, highest priority first (1..500)" default:"50"`
}

type reviewDecideParams struct {
	ArticleID  string `json:"article_id" help:"article of the queue entry to decide" required:"true"`
	ProposalID string `json:"proposal_id" help:"proposal the queue entry was loaded with; guards against deciding a stale entry" required:"true"`
	Decision   string `json:"decision" help:"publish labels the article publish and approves the proposal; discard labels it discard and rejects the proposal" required:"true" enum:"publish,discard"`
}

func reviewDeps(env action.Env) trainreview.Deps {
	return trainreview.Deps{
		Articles:  sqliteadapter.NewArticleRepository(env.DB),
		Labels:    sqliteadapter.NewHumanLabelRepository(env.DB),
		Proposals: sqliteeditoriallabelproposal.NewEditorialLabelProposalRepository(env.DB),
		Queue:     sqlitereviewqueue.NewReviewQueueRepository(env.DB),
	}
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

func runReviewQueue(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*reviewQueueParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	if p.Limit < 1 || p.Limit > 500 {
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf("field %q must be between 1 and 500, got %d", "limit", p.Limit)}
	}
	deps := reviewDeps(env)
	queue, err := trainreview.Queue(ctx, deps, deps.Labels, p.Limit)
	if err != nil {
		return action.Result{}, err
	}
	items := make([]map[string]any, 0, len(queue))
	for _, it := range queue {
		prior := make([]map[string]any, 0, len(it.PriorLabels))
		for _, l := range it.PriorLabels {
			prior = append(prior, map[string]any{
				"label": l.Label, "labeled_by": l.LabeledBy, "labeled_at": rfc3339(l.LabeledAt), "source": l.Source,
			})
		}
		a := it.Article
		items = append(items, map[string]any{
			"article_id": it.Entry.ArticleID, "proposal_id": it.Entry.ProposalID,
			"score": it.Entry.Score, "reason": it.Entry.Reason,
			"nb_zone": string(it.Entry.NBZone), "nb_value": it.Entry.NBValue,
			"llm_label": it.Entry.LLMLabel, "llm_confidence": it.Entry.LLMConfidence, "llm_reason": it.LLMReason,
			"suggested_label": it.Entry.SuggestedLabel, "purged": it.Purged,
			"title": a.Title, "summary": a.Summary, "body": truncateRunes(a.Body, reviewBodyLimit),
			"url": a.OriginalURL, "source_name": a.Source.Name, "source_type": string(a.Source.Type),
			"domain": a.Domain, "published_at": rfc3339(a.PublishedAt), "cluster_id": a.ClusterID,
			"prior_labels": prior,
		})
	}
	return action.Result{
		Render:  "object",
		Object:  map[string]any{"items": items},
		Summary: fmt.Sprintf("%d item(s) in the review queue", len(items)),
	}, nil
}

// reviewer is who a decision is attributed to: the bare panel username for
// "user:<name>", "mcp" for the MCP server.
func reviewer(actor string) string {
	return strings.TrimPrefix(actor, "user:")
}

func runReviewDecide(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*reviewDecideParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	dec := trainreview.Decision(p.Decision)
	if dec != trainreview.Publish && dec != trainreview.Discard {
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: fmt.Sprintf("field %q must be publish or discard, got %q", "decision", p.Decision)}
	}
	deps := reviewDeps(env)
	entry, found, err := trainreview.Find(ctx, deps.Queue, p.ArticleID)
	if err != nil {
		return action.Result{}, err
	}
	if !found {
		return action.Result{}, action.NotFound(fmt.Errorf("%s is not in the review queue", p.ArticleID))
	}
	if entry.ProposalID != p.ProposalID {
		return action.Result{}, action.NotFound(fmt.Errorf("proposal %s is not the queued proposal of %s", p.ProposalID, p.ArticleID))
	}
	outcome, err := trainreview.Decide(ctx, deps, entry, dec, reviewer(env.Actor), reviewSource, env.Now().UTC())
	if err != nil {
		return action.Result{}, err
	}
	var summary string
	switch outcome {
	case trainreview.DroppedAlreadyLabeled:
		summary = fmt.Sprintf("%s was already labeled elsewhere; dropped from the queue", p.ArticleID)
	case trainreview.DroppedPurged:
		summary = fmt.Sprintf("%s no longer exists; dropped from the queue", p.ArticleID)
	case trainreview.Decided:
		if dec == trainreview.Publish {
			summary = "published " + p.ArticleID
		} else {
			summary = "discarded " + p.ArticleID
		}
	default:
		return action.Result{}, fmt.Errorf("train review: unexpected outcome %q", outcome)
	}
	return action.Result{
		Render:  "object",
		Object:  map[string]any{"outcome": string(outcome), "article_id": p.ArticleID},
		Summary: summary,
	}, nil
}
