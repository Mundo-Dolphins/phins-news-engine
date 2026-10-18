// Package publishing decides whether an article is fresh enough, and in what
// state, to be published.
package publishing

import (
	"regexp"
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
	// LiveUpdates shortens the window of in-game social posts: a score line
	// or "touchdown" post is worthless once the play is over.
	LiveUpdates LiveUpdatePolicy
}

// LiveUpdatePolicy gives social posts whose text reads as news from a game in
// progress (running score, kickoff, halftime, a turnover) a short maximum
// age. Finished-game news (final score, recap, post-game stats) is not live. The zero
// value (no patterns) changes nothing.
type LiveUpdatePolicy struct {
	MaxAge   time.Duration
	Patterns []*regexp.Regexp
	// Exclude marks a post as finished-game news even when a Pattern matches
	// (a final score reads like a running score): finished-game news keeps
	// the normal window.
	Exclude []*regexp.Regexp
}

// Matches reports whether a is a social post that reads as a live game update.
func (p LiveUpdatePolicy) Matches(a article.Article) bool {
	if p.MaxAge <= 0 || a.ContentType != article.ContentTypeSocialPost {
		return false
	}
	for _, re := range p.Exclude {
		if re.MatchString(a.Title) {
			return false
		}
	}
	for _, re := range p.Patterns {
		if re.MatchString(a.Title) {
			return true
		}
	}
	return false
}

// MaxAgeFor returns the maximum age of a: the content-type window, cut to
// the live-update window when a reads as a live game update.
func (p FreshnessPolicy) MaxAgeFor(a article.Article) time.Duration {
	age := p.MaxAge(a.ContentType)
	if p.LiveUpdates.Matches(a) && p.LiveUpdates.MaxAge < age {
		return p.LiveUpdates.MaxAge
	}
	return age
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

// MaxAgeDuringLiveGame is MaxAgeFor for an article processed while the
// team's game is in progress: every social post is then live commentary and
// gets the live-update window, whatever its text says.
func (p FreshnessPolicy) MaxAgeDuringLiveGame(a article.Article) time.Duration {
	age := p.MaxAgeFor(a)
	if a.ContentType == article.ContentTypeSocialPost && p.LiveUpdates.MaxAge > 0 && p.LiveUpdates.MaxAge < age {
		return p.LiveUpdates.MaxAge
	}
	return age
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
	return age <= policy.MaxAgeFor(a)
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
