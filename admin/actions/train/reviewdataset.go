package train

import (
	"context"
	"errors"
	"fmt"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetreview"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

type reviewDatasetQueueParams struct {
	Dataset string `json:"dataset" help:"source dataset YAML (never modified)" default:"data/editorial_dataset.yml" format:"path"`
	Audit   string `json:"audit" help:"prioritized audit CSV with article_id and audit_reason columns" default:"data/editorial_dataset-review.csv" format:"path"`
	Out     string `json:"out" help:"path of the reviewed dataset copy; its decision log is <out>.review.csv" default:"data/editorial_dataset-reviewed.yml" format:"path"`
}

type reviewDatasetDecideParams struct {
	Dataset   string `json:"dataset" help:"source dataset YAML (never modified)" default:"data/editorial_dataset.yml" format:"path"`
	Audit     string `json:"audit" help:"prioritized audit CSV with article_id and audit_reason columns" default:"data/editorial_dataset-review.csv" format:"path"`
	Out       string `json:"out" help:"path of the reviewed dataset copy; its decision log is <out>.review.csv" default:"data/editorial_dataset-reviewed.yml" format:"path"`
	ArticleID string `json:"article_id" help:"audit row to decide" required:"true"`
	Decision  string `json:"decision" help:"publish or reject relabels the row; confirm keeps its current label" required:"true" enum:"publish,reject,confirm"`
}

// excludedSources is the set of sources the CLI drops from dataset review:
// model-bypass and unconditional-publish sources.
func excludedSources(env action.Env) map[string]bool {
	out := map[string]bool{}
	for _, id := range env.Config.Editorial.ModelBypassSourceIDs {
		out[id] = true
	}
	for _, id := range env.Config.Editorial.UnconditionalPublishSourceIDs {
		out[id] = true
	}
	return out
}

// openDatasetReview opens the session and maps its sentinels to action errors.
func openDatasetReview(env action.Env, p datasetreview.Paths) (*datasetreview.Session, error) {
	s, err := datasetreview.Open(p, excludedSources(env))
	switch {
	case err == nil:
		return s, nil
	case errors.Is(err, datasetreview.ErrSamePath):
		return nil, &action.Error{Code: action.CodeInvalidParams, Message: err.Error()}
	case errors.Is(err, datasetreview.ErrResumeMismatch), errors.Is(err, datasetreview.ErrAbsentFromDataset), errors.Is(err, datasetreview.ErrEmptyAudit):
		return nil, action.Conflict(err)
	}
	return nil, err
}

func runReviewDatasetQueue(ctx context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*reviewDatasetQueueParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	paths := datasetreview.Paths{Dataset: p.Dataset, Audit: p.Audit, Out: p.Out}
	s, err := openDatasetReview(env, paths)
	if err != nil {
		return action.Result{}, err
	}
	byID := make(map[string]int, len(s.Examples))
	for i, ex := range s.Examples {
		byID[ex.ArticleID] = i
	}
	articles := sqliteadapter.NewArticleRepository(env.DB)
	pending := s.Pending()
	items := make([]map[string]any, 0, len(pending))
	for _, c := range pending {
		ex := s.Examples[byID[c.ArticleID]]
		url := ""
		switch a, findErr := articles.FindByID(ctx, c.ArticleID); {
		case findErr == nil:
			url = a.OriginalURL
		case !errors.Is(findErr, article.ErrNotFound):
			return action.Result{}, findErr
		}
		items = append(items, map[string]any{
			"article_id": ex.ArticleID, "audit_reason": c.Reason, "title": ex.Title, "body": ex.Body,
			"source_id": ex.SourceID, "source_type": ex.SourceType, "domain": ex.Domain, "content_type": ex.ContentType,
			"published_at": rfc3339(ex.PublishedAt), "label": ex.Label, "label_source": ex.LabelSource, "url": url,
		})
	}
	return action.Result{
		Render: "object",
		Object: map[string]any{
			"paths":         map[string]any{"dataset": paths.Dataset, "audit": paths.Audit, "out": paths.Out, "log": paths.Log()},
			"total":         len(s.Candidates),
			"pending_count": len(pending),
			"items":         items,
		},
		Summary: fmt.Sprintf("%d of %d dataset row(s) pending review", len(pending), len(s.Candidates)),
	}, nil
}

func runReviewDatasetDecide(_ context.Context, env action.Env, params any) (action.Result, error) {
	p, ok := params.(*reviewDatasetDecideParams)
	if !ok {
		return action.Result{}, errBadParams
	}
	action.ConfigFilesMu.Lock()
	defer action.ConfigFilesMu.Unlock()
	s, err := openDatasetReview(env, datasetreview.Paths{Dataset: p.Dataset, Audit: p.Audit, Out: p.Out})
	if err != nil {
		return action.Result{}, err
	}
	now := env.Now().UTC()
	entry, err := s.ApplyPending(p.ArticleID, datasetreview.Decision(p.Decision), reviewer(env.Actor), now)
	switch {
	case errors.Is(err, datasetreview.ErrNotInAudit):
		return action.Result{}, action.NotFound(err)
	case errors.Is(err, datasetreview.ErrAlreadyReviewed):
		return action.Result{}, action.Conflict(fmt.Errorf("%s: %w", p.ArticleID, datasetreview.ErrAlreadyReviewed))
	case errors.Is(err, datasetreview.ErrInvalidDecision):
		return action.Result{}, &action.Error{Code: action.CodeInvalidParams, Message: err.Error()}
	case err != nil:
		return action.Result{}, err
	}
	if err := s.Save(now, []datasetreview.LogEntry{entry}); err != nil {
		return action.Result{}, err
	}
	return action.Result{
		Render:  "object",
		Object:  map[string]any{"article_id": entry.ArticleID, "previous_label": entry.Previous, "reviewed_label": entry.Label},
		Summary: fmt.Sprintf("%s: %s → %s", entry.ArticleID, entry.Previous, entry.Label),
	}, nil
}
