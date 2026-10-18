package classification

import (
	"strings"
	"time"
)

// RuleNoAnchor is the RuleApplied of a Result that scored as related but was
// demoted because none of its positive evidence is an anchor.
const RuleNoAnchor = "no_anchor"

// AnchorPolicy requires a related Result to carry at least one piece of
// positive evidence that names the team itself (team names, official
// hashtags, owners, staff, rostered players by full name, or a team URL),
// as opposed to context that merely co-occurs with NFL talk: a venue the
// team shares with other events, a common surname, a rival, a generic
// football term. Without an anchor, the summed score is not trusted.
//
// The zero value (Enabled false) changes nothing.
type AnchorPolicy struct {
	Enabled bool
	// Categories are the dictionary category names whose evidence can anchor.
	Categories map[string]bool
	// MultiWordCategories is the subset of Categories whose single-token
	// entries (a bare surname such as "jones") do not anchor. Hashtags and
	// URL patterns are always accepted as written.
	MultiWordCategories map[string]bool
	// MinConfidence, when above zero, stops entries with a lower confidence
	// from anchoring: machine-discovered names (other teams' players an LLM
	// proposed) are not evidence of a Isotopes story.
	MinConfidence float64
}

// ApplyAnchor demotes r to unrelated when p is enabled, r is related and no
// positive evidence anchors it. It returns r unchanged otherwise.
func ApplyAnchor(r Result, p AnchorPolicy, now time.Time) Result {
	if !p.Enabled || !r.Related {
		return r
	}
	for _, ev := range r.PositiveEvidence {
		if p.anchors(ev, now) {
			return r
		}
	}
	r.Related = false
	r.RuleApplied = RuleNoAnchor
	return r
}

func (p AnchorPolicy) anchors(ev Evidence, now time.Time) bool {
	if !p.Categories[ev.Category] {
		return false
	}
	if ev.Confidence < p.MinConfidence {
		return false
	}
	// A retired entry (a player who left the team) no longer names the team.
	if ev.ActiveUntil != nil && ev.ActiveUntil.Before(now) {
		return false
	}
	if !p.MultiWordCategories[ev.Category] {
		return true
	}
	name := strings.TrimSpace(ev.EntryName)
	return strings.ContainsAny(name, " \t") || strings.HasPrefix(name, "#") || strings.Contains(name, "/")
}
