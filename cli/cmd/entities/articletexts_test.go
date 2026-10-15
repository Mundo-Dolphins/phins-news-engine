package entities

import (
	"context"
	"errors"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

type fakeArticleFinder struct{ byID map[string]article.Article }

func (f fakeArticleFinder) FindByID(_ context.Context, id string) (article.Article, error) {
	a, ok := f.byID[id]
	if !ok {
		return article.Article{}, errors.New("not found")
	}
	return a, nil
}

func TestArticleTexts_LoadsTitlesAndSummariesAndSkipsMissing(t *testing.T) {
	l := articleTexts{finder: fakeArticleFinder{byID: map[string]article.Article{"a1": {ID: "a1", Title: "T1", Summary: "S1"}}}}
	got, err := l.Texts(context.Background(), []string{"a1", "gone"})
	if err != nil || len(got) != 1 || got[0].ID != "a1" || got[0].Summary != "S1" {
		t.Fatalf("Texts() = %+v, %v; want only the existing article", got, err)
	}
}
