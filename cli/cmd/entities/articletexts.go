// cli/cmd/entities/articletexts.go
package entities

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

type articleFinder interface {
	FindByID(ctx context.Context, id string) (article.Article, error)
}

// articleTexts implements entitydiscovery.ArticleTextLoader over the article repository.
type articleTexts struct{ finder articleFinder }

var _ entitydiscovery.ArticleTextLoader = articleTexts{}

func (l articleTexts) Texts(ctx context.Context, ids []string) ([]entitydiscovery.ArticleText, error) {
	out := make([]entitydiscovery.ArticleText, 0, len(ids))
	for _, id := range ids {
		a, err := l.finder.FindByID(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, entitydiscovery.ArticleText{ID: a.ID, Title: a.Title, Summary: a.Summary})
	}
	return out, nil
}
