// Package entityresolution models one online, per-candidate LLM
// resolution attempt for a name-like span the deterministic extractor
// (domain/eventsignals) could not match to any catalog entity.
// It has no I/O and no LLM dependency of its own —
// it defines the shape of a resolution attempt and the pure local
// validation an application-layer orchestrator runs on whatever a
// configured LLM returns, before that result is ever trusted.
package entityresolution

import (
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

// ValidTypes are the only entity types this fallback ever accepts —
// matches entityproposal.EntityType's values exactly.
var ValidTypes = []string{"player", "coach", "executive", "team"}

// Candidate is one unknown-entity span (eventsignals.UnknownEntity)
// prepared for a single narrow LLM prompt.
type Candidate struct {
	// Text is the exact span as eventsignals.UnknownEntity reported it.
	Text string
	// NormalizedText is entityproposal.Normalize(Text) — the aggregation/
	// cache key.
	NormalizedText string
	// Field is "title" | "description" | "body" — where Text was found.
	Field string
	// Context is the full text of that Field (title, summary, or body)
	// from the source article — never the whole article when Field is
	// "body", keeping the prompt narrow.
	Context string
}

// Resolution is what a configured LLM returned for one Candidate, before
// any validation.
type Resolution struct {
	Resolved      bool
	Type          string // player|coach|executive|team, meaningful only if Resolved
	CanonicalName string
	Confidence    float64
}

// Validate applies the local, deterministic guard every LLM resolution
// must pass before it can be trusted: an unresolved Resolution always
// passes trivially (there's nothing to check); a resolved one must name
// a known type, clear minConfidence, carry a non-empty CanonicalName, and
// pass an anti-hallucination check — CanonicalName, normalized, must
// either contain c's normalized text or share its surname
// (entityproposal.Surname), i.e. there must be a real textual
// relationship between what the extractor actually found and what the
// LLM claims it resolves to. A resolution with no such relationship
// (e.g. a candidate "Random Newcomer" resolved to "Tom Bront") is
// discarded as unrelated/hallucinated, never reaching the article.
func Validate(c Candidate, r Resolution, minConfidence float64) error {
	if !r.Resolved {
		return nil
	}
	if !isValidType(r.Type) {
		return fmt.Errorf("entityresolution: unknown type %q", r.Type)
	}
	if r.Confidence < minConfidence {
		return fmt.Errorf("entityresolution: confidence %.2f below floor %.2f", r.Confidence, minConfidence)
	}
	if strings.TrimSpace(r.CanonicalName) == "" {
		return fmt.Errorf("entityresolution: empty canonical name")
	}
	normCandidate := entityproposal.Normalize(c.Text)
	normCanonical := entityproposal.Normalize(r.CanonicalName)
	if normCandidate == "" || normCanonical == "" {
		return fmt.Errorf("entityresolution: empty normalized text")
	}
	related := strings.Contains(normCanonical, normCandidate) ||
		strings.Contains(normCandidate, normCanonical) ||
		(entityproposal.Surname(r.CanonicalName) != "" && entityproposal.Surname(r.CanonicalName) == entityproposal.Surname(c.Text))
	if !related {
		return fmt.Errorf("entityresolution: canonical name %q has no textual relationship to candidate %q", r.CanonicalName, c.Text)
	}
	return nil
}

func isValidType(t string) bool {
	for _, v := range ValidTypes {
		if v == t {
			return true
		}
	}
	return false
}
