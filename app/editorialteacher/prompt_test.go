package editorialteacher

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func mustSourceID(t *testing.T, platform source.Type, rawIdentifier string) source.ID {
	t.Helper()
	id, err := source.NewID(platform, rawIdentifier)
	if err != nil {
		t.Fatalf("source.NewID(%q, %q) error = %v", platform, rawIdentifier, err)
	}
	return id
}

func TestBuildTeacherArticle_FullFields(t *testing.T) {
	a := article.Article{
		ID: "a1", Title: "Isotopes sign WR", Summary: "The Isotopes signed...",
		PrimaryEntity: "Tavion Dale", ClusterID: "c1", Domain: "isotopes.example.com",
		Source: source.Source{ID: mustSourceID(t, source.TypeWeb, "src-1"), Type: source.TypeWeb, Domain: "isotopes.example.com"},
	}
	got := BuildTeacherArticle(a)
	want := appout.EditorialTeacherArticle{
		ArticleID: "a1", Title: "Isotopes sign WR", Summary: "The Isotopes signed...",
		Entities: []string{"Tavion Dale"}, TopicEvent: "c1",
		SourceID: "web:src-1", SourceType: "web", Domain: "isotopes.example.com",
	}
	if got.ArticleID != want.ArticleID || got.Title != want.Title || got.Summary != want.Summary ||
		got.TopicEvent != want.TopicEvent || got.SourceID != want.SourceID ||
		got.SourceType != want.SourceType || got.Domain != want.Domain {
		t.Errorf("BuildTeacherArticle() = %+v, want %+v", got, want)
	}
	if len(got.Entities) != 1 || got.Entities[0] != "Tavion Dale" {
		t.Errorf("Entities = %v, want [Tavion Dale]", got.Entities)
	}
}

func TestBuildTeacherArticle_EmptyOptionalFields(t *testing.T) {
	a := article.Article{ID: "a2", Title: "T", Summary: "S"}
	got := BuildTeacherArticle(a)
	if got.Entities != nil {
		t.Errorf("Entities = %v, want nil for empty PrimaryEntity", got.Entities)
	}
	if got.TopicEvent != "" {
		t.Errorf("TopicEvent = %q, want empty for empty ClusterID", got.TopicEvent)
	}
}
