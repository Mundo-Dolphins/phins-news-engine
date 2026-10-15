// domain/eventruleproposal/impact.go
package eventruleproposal

import (
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

const maxExamples = 5

// Sample is one article of the evaluation sample with its cluster.
type Sample struct {
	Article   article.Article
	ClusterID string
}

// ImpactResult summarises what a phrase would change over a sample.
type ImpactResult struct {
	// Moved counts generic -> typed changes, by new type.
	Moved map[eventsignals.EventType]int
	// Examples holds up to five titles of generic -> typed moves.
	Examples []string
	// Retyped counts articles whose non-generic baseline type changed.
	Retyped int
	// DistinctClusters is the number of distinct clusters among changed articles.
	DistinctClusters int
	// Matches is the number of articles whose type changed.
	Matches int
	// InVocabulary names the bucket ("action_terms.sign",
	// "content_cues.rumor") that already holds the phrase in base, any
	// bucket, case-insensitive; "" when the phrase is new.
	InVocabulary string
}

// Impact measures p against base over sample without mutating base. Only
// phrase proposals have an impact; others return an empty result.
func Impact(base eventsignals.Rules, p Proposal, sample []Sample, now time.Time) ImpactResult {
	res := ImpactResult{Moved: map[eventsignals.EventType]int{}}
	if p.Kind != KindPhrase {
		return res
	}
	res.InVocabulary = VocabularyBucket(base, p.Phrase)
	with := withPhrase(base, p)
	clusters := map[string]bool{}
	for _, s := range sample {
		before := eventsignals.Extract(s.Article, base, now).EventType
		after := eventsignals.Extract(s.Article, with, now).EventType
		if before == after {
			continue
		}
		res.Matches++
		clusters[s.ClusterID] = true
		if before == eventsignals.EventTypeGeneric || before == "" {
			res.Moved[after]++
			if len(res.Examples) < maxExamples {
				res.Examples = append(res.Examples, s.Article.Title)
			}
		} else {
			res.Retyped++
		}
	}
	res.DistinctClusters = len(clusters)
	return res
}

// VocabularyBucket returns the first bucket of base (sorted, actions before
// signals) holding phrase, or "".
func VocabularyBucket(base eventsignals.Rules, phrase string) string {
	want := strings.ToLower(strings.TrimSpace(phrase))
	if want == "" {
		return ""
	}
	var found []string
	for a, ps := range base.ActionTerms {
		for _, x := range ps {
			if strings.ToLower(strings.TrimSpace(x)) == want {
				found = append(found, "action_terms."+string(a))
			}
		}
	}
	for c, ps := range base.ContentCues {
		for _, x := range ps {
			if strings.ToLower(strings.TrimSpace(x)) == want {
				found = append(found, "content_cues."+string(c))
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	return found[0]
}

// withPhrase returns a copy of base with p's phrase added to its bucket.
func withPhrase(base eventsignals.Rules, p Proposal) eventsignals.Rules {
	out := base
	switch p.TargetKind {
	case TargetAction:
		m := make(map[eventsignals.Action][]string, len(base.ActionTerms)+1)
		for k, v := range base.ActionTerms {
			m[k] = append([]string(nil), v...)
		}
		k := eventsignals.Action(p.Target)
		m[k] = append(m[k], p.Phrase)
		out.ActionTerms = m
	case TargetSignal:
		m := make(map[eventsignals.ContentSignal][]string, len(base.ContentCues)+1)
		for k, v := range base.ContentCues {
			m[k] = append([]string(nil), v...)
		}
		k := eventsignals.ContentSignal(p.Target)
		m[k] = append(m[k], p.Phrase)
		out.ContentCues = m
	}
	return out
}
