// Package publishing (this file) decides whether an already-accepted
// article may be published right now, or must wait — burst limiting,
// same-topic throttling, and quiet hours. It never rejects
// an article outright: every decision is either "publish now" or "publish
// later," so a pending article is never lost to this policy.
package publishing

import (
	"strings"
	"time"
)

// PolicyConfig tunes Evaluate's burst, same-topic, and quiet-hours checks.
type PolicyConfig struct {
	// BurstMaxPerWindow caps how many articles may publish within
	// BurstWindow; <= 0 disables the burst check entirely.
	BurstMaxPerWindow int
	BurstWindow       time.Duration
	// SameTopicMaxPerWindow caps how many articles about the same
	// PrimaryEntity may publish within SameTopicWindow; <= 0 disables the
	// check (as does an empty PolicyInput.PrimaryEntity).
	SameTopicMaxPerWindow int
	SameTopicWindow       time.Duration
	// SameTopicExemptEntities lists primary entities (matched
	// case-insensitively) the same-topic check never applies to — e.g. the
	// team itself, which is the fallback primary entity for any article
	// without a more specific subject and so is not a real "topic".
	SameTopicExemptEntities []string
	QuietHours              QuietHoursConfig
	// Live is the independent policy used by live-lane items.
	Live *PolicyConfig
}

// QuietHoursConfig defines an hour-of-day window (StartHour inclusive,
// EndHour exclusive, in whatever timezone the caller's `now` is in — the
// application layer is expected to pass UTC, matching every other
// timestamp in this codebase) during which Evaluate defers publication. A
// window where StartHour > EndHour wraps past midnight (e.g. 22 -> 6
// covers 22:00-05:59). Disabled entirely when Enabled is false, and also
// inert when StartHour == EndHour (a zero-width window).
type QuietHoursConfig struct {
	Enabled   bool
	StartHour int
	EndHour   int
}

// PolicyInput is what Evaluate needs to know about the article being
// considered and the current state of what's already published, gathered
// by the caller via ArticleRepository queries.
type PolicyInput struct {
	// PrimaryEntity is the article's clustered primary entity; "" skips
	// the same-topic check entirely.
	PrimaryEntity string
	// PublishedInBurstWindow is how many articles have already published
	// in the last BurstWindow.
	PublishedInBurstWindow int
	// PublishedForEntityInWindow is how many articles about PrimaryEntity
	// have already published in the last SameTopicWindow. Meaningless
	// (and ignored) when PrimaryEntity is "".
	PublishedForEntityInWindow int
}

// PolicyDecision is Evaluate's verdict.
type PolicyDecision struct {
	Allow   bool
	Reason  string
	RetryAt time.Time
}

// Reason values PolicyDecision.Reason takes when Allow is false.
const (
	ReasonBurstLimit     = "burst_limit"
	ReasonSameTopicLimit = "same_topic_limit"
	ReasonQuietHours     = "quiet_hours"
)

// Evaluate decides whether an article may publish right now, per input and
// cfg, as of now.
func Evaluate(input PolicyInput, cfg PolicyConfig, now time.Time) PolicyDecision {
	if cfg.BurstMaxPerWindow > 0 && input.PublishedInBurstWindow >= cfg.BurstMaxPerWindow {
		return PolicyDecision{Allow: false, Reason: ReasonBurstLimit, RetryAt: now.Add(cfg.BurstWindow)}
	}
	if cfg.SameTopicApplies(input.PrimaryEntity) && input.PublishedForEntityInWindow >= cfg.SameTopicMaxPerWindow {
		return PolicyDecision{Allow: false, Reason: ReasonSameTopicLimit, RetryAt: now.Add(cfg.SameTopicWindow)}
	}
	if cfg.QuietHours.Enabled {
		if retryAt, inQuietHours := quietHoursRetryAt(cfg.QuietHours, now); inQuietHours {
			return PolicyDecision{Allow: false, Reason: ReasonQuietHours, RetryAt: retryAt}
		}
	}
	return PolicyDecision{Allow: true}
}

// SameTopicApplies reports whether the same-topic check is enabled and
// covers entity: it is not for an empty or exempt entity.
func (cfg PolicyConfig) SameTopicApplies(entity string) bool {
	if entity == "" || cfg.SameTopicMaxPerWindow <= 0 {
		return false
	}
	for _, exempt := range cfg.SameTopicExemptEntities {
		if strings.EqualFold(strings.TrimSpace(exempt), entity) {
			return false
		}
	}
	return true
}

// quietHoursRetryAt reports whether now falls within cfg's quiet-hours
// window and, if so, the next moment (today or tomorrow) the window ends.
func quietHoursRetryAt(cfg QuietHoursConfig, now time.Time) (time.Time, bool) {
	if cfg.StartHour == cfg.EndHour {
		return time.Time{}, false
	}

	hour := now.Hour()
	var inWindow bool
	if cfg.StartHour < cfg.EndHour {
		inWindow = hour >= cfg.StartHour && hour < cfg.EndHour
	} else {
		inWindow = hour >= cfg.StartHour || hour < cfg.EndHour
	}
	if !inWindow {
		return time.Time{}, false
	}

	end := time.Date(now.Year(), now.Month(), now.Day(), cfg.EndHour, 0, 0, 0, now.Location())
	if !end.After(now) {
		end = end.Add(24 * time.Hour)
	}
	return end, true
}
