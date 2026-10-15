package editorialteacher

import (
	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// BuildTeacherArticle maps a stored article to the LLM teacher's input
// shape. Entities and TopicEvent are best-effort: a.PrimaryEntity and
// a.ClusterID are whatever clustering already resolved at accept time
// (see article.Article's doc comment); both are left empty/nil when
// clustering never ran for this article, rather than blocking
// classification on their absence. This
// function never reads anything from domain/editorial — that
// is the structural guarantee that the Naive Bayes prediction never
// reaches the LLM.
func BuildTeacherArticle(a article.Article) appout.EditorialTeacherArticle {
	var entities []string
	if a.PrimaryEntity != "" {
		entities = []string{a.PrimaryEntity}
	}
	return appout.EditorialTeacherArticle{
		ArticleID:  a.ID,
		Title:      a.Title,
		Summary:    a.Summary,
		Entities:   entities,
		TopicEvent: a.ClusterID,
		SourceID:   a.Source.ID.String(),
		SourceType: string(a.Source.Type),
		Domain:     a.Domain,
	}
}
