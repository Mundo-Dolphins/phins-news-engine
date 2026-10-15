package eventsignals

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

// unknownEntityConfidence is the fixed confidence assigned to every
// UnknownEntity in this version — there is no scoring model yet (issue
// #224, part 1: this deterministic detection intentionally never guesses
// how confident it is or what kind of entity a candidate might be; that is
// left to a future LLM-fallback sub-project).
const unknownEntityConfidence = 0.5

// UnknownEntity is a name-like span Extract found in an article's text that
// matched no EntityRule — a candidate for future resolution (LLM fallback,
// offline catalog discovery), not yet classified by kind.
type UnknownEntity struct {
	// Text is the name-like span's words as they appeared, trimmed of
	// surrounding punctuation and joined by single spaces — not
	// necessarily byte-identical to the original text (flush() rebuilds
	// it by joining the trimmed words, dropping punctuation and
	// collapsing whitespace).
	Text string
	// NormalizedText is entityproposal.Normalize(Text): accents/punctuation
	// folded, for future aggregation by a later sub-project.
	NormalizedText string
	// PossibleType is always "" in this version — deterministic detection
	// cannot distinguish player/coach/executive.
	PossibleType string
	// Reason is a fixed, human-readable explanation of why this was
	// flagged.
	Reason string
	// Field is "title" | "description" | "body".
	Field string
	// Confidence is unknownEntityConfidence for every entry in this
	// version.
	Confidence float64
}

// ruleKeys memoises matchKey for rule names and terms. The rule vocabulary is
// fixed for the life of the process and matchKey is pure, but with thousands of
// league rules recomputing it for every article dominated Extract's cost. Only
// rule text goes through here (never article text), and the memo stops growing
// at ruleKeysCap entries so differently configured Rules (tests, replays) cannot
// grow it without bound.
var (
	ruleKeys    sync.Map
	ruleKeysLen atomic.Int64
)

const ruleKeysCap = 200_000

func ruleKey(s string) string {
	if v, ok := ruleKeys.Load(s); ok {
		if key, isString := v.(string); isString {
			return key
		}
	}
	k := matchKey(s)
	if ruleKeysLen.Load() < ruleKeysCap {
		if _, loaded := ruleKeys.LoadOrStore(s, k); !loaded {
			ruleKeysLen.Add(1)
		}
	}
	return k
}

// detectUnknownEntities scans titleText, summaryText, and bodyText
// (original casing — NOT the lower-cased, synonym-normalized text Extract
// builds for the rest of its matching) for name-like spans: two or more
// consecutive Title-Case words that match no rules Terms/CanonicalName and
// are not in stoplist. A single capitalized word is never reported — the
// signal is deliberately conservative.
func detectUnknownEntities(titleText, summaryText, bodyText string, rules []EntityRule, stoplist []string) []UnknownEntity {
	known := make(map[string]bool, len(rules)*2)
	for _, r := range rules {
		known[ruleKey(r.CanonicalName)] = true
		for _, term := range r.Terms {
			known[ruleKey(term)] = true
		}
		for _, ct := range r.ConditionalTerms {
			known[ruleKey(ct.Term)] = true
		}
	}
	stop := make(map[string]bool, len(stoplist))
	for _, s := range stoplist {
		stop[matchKey(s)] = true
	}

	fields := []struct{ text, field string }{
		{titleText, "title"},
		{summaryText, "description"},
		{bodyText, "body"},
	}

	var out []UnknownEntity
	for _, fc := range fields {
		out = append(out, scanFieldForUnknownEntities(fc.text, fc.field, known, stop)...)
	}
	return out
}

// scanFieldForUnknownEntities walks text's whitespace-separated words,
// grouping consecutive name-like words (isNameWord) into runs, and reports
// every run of 2+ words that is not already known/stoplisted — once per
// distinct (field, normalized span), even if the same span repeats in
// text.
func scanFieldForUnknownEntities(text, field string, known, stop map[string]bool) []UnknownEntity {
	var out []UnknownEntity
	seen := map[string]bool{}
	var run []string

	emit := func(words []string) {
		if len(words) < 2 {
			return
		}
		span := strings.Join(words, " ")
		lower := matchKey(span)
		if seen[lower] {
			return
		}
		seen[lower] = true
		out = append(out, UnknownEntity{
			Text:           span,
			NormalizedText: entityproposal.Normalize(span),
			Reason:         "capitalized name-like span not found in entity catalog",
			Field:          field,
			Confidence:     unknownEntityConfidence,
		})
	}

	flush := func() {
		defer func() { run = nil }()
		if len(run) < 2 {
			return
		}
		for _, seg := range splitRunOnKnownOrStop(run, known, stop) {
			emit(seg)
		}
	}

	poisoned := false
	for _, w := range strings.Fields(text) {
		name, class, endsRun := classifyToken(w)
		name = originalSpelling(w, name)
		switch class {
		case tokenNotName:
			flush()
			poisoned = false
			continue
		case tokenContraction:
			// "I've Seen Enough": the capitalized stretch after a
			// contraction is a quote, not a name.
			flush()
			poisoned = !endsRun
			continue
		case tokenSuffix:
			if poisoned || len(run) < 2 {
				flush()
				poisoned = false
				continue
			}
		case tokenInitials:
			if poisoned {
				continue
			}
		default:
			if poisoned {
				if endsRun {
					poisoned = false
				}
				continue
			}
		}
		run = append(run, name)
		if endsRun {
			flush()
		}
	}
	flush()

	return out
}

// splitRunOnKnownOrStop scans run left to right, greedily matching the
// longest contiguous word sequence (case-insensitively) against known or
// stop at each position. Matched sequences are dropped; the words between
// matches are grouped back into contiguous sub-runs. This is what lets a
// Title-Case headline like "Isotopes Sign Random Newcomer" (run =
// [Isotopes Sign Random Newcomer], "isotopes" known) yield the sub-run
// [Sign Random Newcomer] instead of either discarding nothing (whole-span
// exact match never hits) or discarding everything. A run entirely
// consumed by known/stop matches yields no sub-runs, matching the previous
// whole-span "known/stoplisted -> discard" behavior; a sub-run shorter
// than 2 words is dropped, same as any other under-length candidate.
func splitRunOnKnownOrStop(run []string, known, stop map[string]bool) [][]string {
	var segments [][]string
	var current []string
	n := len(run)
	for i := 0; i < n; {
		matched := false
		for l := n - i; l >= 1; l-- {
			phrase := matchKey(strings.Join(run[i:i+l], " "))
			if known[phrase] || stop[phrase] {
				if len(current) > 0 {
					segments = append(segments, current)
					current = nil
				}
				i += l
				matched = true
				break
			}
		}
		if !matched {
			current = append(current, run[i])
			i++
		}
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	return segments
}
