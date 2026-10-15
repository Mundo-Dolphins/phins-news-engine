package editorialteacher

import (
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// ValidateLabel checks one LLM-produced label before it is saved:
// Label must be one of the three known values, Confidence must be a
// valid probability, and ArticleID must belong to the batch actually
// sent (a minimal anti-hallucination guard — the LLM must not invent an
// article_id outside what it was given).
func ValidateLabel(l appout.EditorialTeacherLabel, batchArticleIDs map[string]bool) error {
	switch l.Label {
	case "publish", "discard", "uncertain":
	default:
		return fmt.Errorf("editorialteacher: invalid label %q for article %s", l.Label, l.ArticleID)
	}
	if l.Confidence < 0 || l.Confidence > 1 {
		return fmt.Errorf("editorialteacher: confidence %.3f out of range for article %s", l.Confidence, l.ArticleID)
	}
	if !batchArticleIDs[l.ArticleID] {
		return fmt.Errorf("editorialteacher: article_id %q not in requested batch", l.ArticleID)
	}
	return nil
}
