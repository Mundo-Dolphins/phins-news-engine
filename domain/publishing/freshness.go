// Package publishing decides whether an article is fresh enough, and in what
// state, to be published.
package publishing

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// FreshnessPolicy holds the maximum age, per content type, an article can
// have before it is considered stale.
type FreshnessPolicy struct {
	HardNewsHours int
	SocialHours   int
	AnalysisHours int
	DefaultHours  int
}

// MaxAge returns the maximum allowed age for the given content type.
func (p FreshnessPolicy) MaxAge(ct article.ContentType) time.Duration {
	switch ct {
	case article.ContentTypeArticle:
		return time.Duration(p.HardNewsHours) * time.Hour
	case article.ContentTypeSocialPost:
		return time.Duration(p.SocialHours) * time.Hour
	case article.ContentTypeVideo:
		return time.Duration(p.AnalysisHours) * time.Hour
	default:
		return time.Duration(p.DefaultHours) * time.Hour
	}
}

// Decide reports whether a (already Validate()d — PublishedAt is non-zero)
// is fresh enough, per policy, to continue through the pipeline at instant
// now.
func Decide(a article.Article, policy FreshnessPolicy, now time.Time) bool {
	age := now.Sub(a.PublishedAt)
	if age < 0 {
		// Published in the future (clock skew between source and us): treat as fresh.
		return true
	}
	return age <= policy.MaxAge(a.ContentType)
}

// DecideStored applies the freshness window captured on the article when it
// passed ingestion. Publication must use that stored policy, not available_at.
func DecideStored(a article.Article, now time.Time) bool {
	if a.PublishedAt.IsZero() || a.FreshnessMaxAge <= 0 {
		return true
	}
	age := now.Sub(a.PublishedAt)
	if age < 0 {
		return true
	}
	return age <= a.FreshnessMaxAge
}
