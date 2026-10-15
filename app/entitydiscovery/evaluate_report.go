// app/entitydiscovery/evaluate_report.go
package entitydiscovery

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Evaluation outcomes for one proposal.
const (
	OutcomeLocalCatalog   = "local_catalog"
	OutcomeVerifiedNFL    = "verified_nfl"
	OutcomeVerifiedNonNFL = "verified_non_nfl"
	OutcomeAmbiguous      = "ambiguous"
	OutcomeNotFound       = "not_found"
	OutcomeError          = "error"
)

// EvalCase is the evaluation of one stored proposal.
type EvalCase struct {
	ProposalID       string `json:"proposal_id"`
	Name             string `json:"name"`
	EntityType       string `json:"entity_type"`
	Kind             string `json:"kind"`
	Outcome          string `json:"outcome"`
	ProviderID       string `json:"provider_id,omitempty"`
	SearchHit        bool   `json:"search_hit"`
	HasTeam          bool   `json:"has_team"`
	HasPosition      bool   `json:"has_position"`
	FallbackRequired bool   `json:"fallback_required"`
}

// Counts aggregates cases.
type Counts struct {
	Evaluated        int `json:"evaluated"`
	LocalCatalog     int `json:"local_catalog"`
	SearchHits       int `json:"search_hits"`
	VerifiedNFL      int `json:"verified_nfl"`
	VerifiedNonNFL   int `json:"verified_non_nfl"`
	Ambiguous        int `json:"ambiguous"`
	NotFound         int `json:"not_found"`
	Errors           int `json:"errors"`
	TeamMetadata     int `json:"team_metadata"`
	PositionMetadata int `json:"position_metadata"`
	FallbackRequired int `json:"fallback_required"`
	ManualAvoided    int `json:"manual_avoided"`
}

func (c *Counts) add(e EvalCase) {
	c.Evaluated++
	if e.SearchHit {
		c.SearchHits++
	}
	switch e.Outcome {
	case OutcomeLocalCatalog:
		c.LocalCatalog++
	case OutcomeVerifiedNFL:
		c.VerifiedNFL++
		c.ManualAvoided++
		if e.HasTeam {
			c.TeamMetadata++
		}
		if e.HasPosition {
			c.PositionMetadata++
		}
	case OutcomeVerifiedNonNFL:
		c.VerifiedNonNFL++
		c.ManualAvoided++
	case OutcomeAmbiguous:
		c.Ambiguous++
	case OutcomeNotFound:
		c.NotFound++
	case OutcomeError:
		c.Errors++
	}
	if e.FallbackRequired {
		c.FallbackRequired++
	}
}

// Rates are derived from Counts; a zero denominator yields 0.
type Rates struct {
	ResolverCoverage      float64 `json:"resolver_coverage"`
	SafeNFLRate           float64 `json:"safe_nfl_rate"`
	SafeNonNFLRate        float64 `json:"safe_non_nfl_rate"`
	AmbiguityNotFoundRate float64 `json:"ambiguity_not_found_rate"`
	TeamCoverage          float64 `json:"team_coverage"`
	PositionCoverage      float64 `json:"position_coverage"`
	RemainingLLMRate      float64 `json:"remaining_llm_rate"`
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func (c Counts) rates() Rates {
	return Rates{
		ResolverCoverage:      ratio(c.VerifiedNFL+c.VerifiedNonNFL, c.Evaluated),
		SafeNFLRate:           ratio(c.VerifiedNFL, c.Evaluated),
		SafeNonNFLRate:        ratio(c.VerifiedNonNFL, c.Evaluated),
		AmbiguityNotFoundRate: ratio(c.Ambiguous+c.NotFound, c.Evaluated),
		TeamCoverage:          ratio(c.TeamMetadata, c.VerifiedNFL),
		PositionCoverage:      ratio(c.PositionMetadata, c.VerifiedNFL),
		RemainingLLMRate:      ratio(c.FallbackRequired, c.Evaluated),
	}
}

// ReviewStats summarizes manual inspection of decisive cases.
type ReviewStats struct {
	Provided  int `json:"provided"`  // ids supplied in the reviewed file
	Reviewed  int `json:"reviewed"`  // ids matching an evaluated decisive case
	Unmatched int `json:"unmatched"` // Provided - Reviewed
	Wrong     int `json:"wrong"`
	// FalseResolutionRate is nil (n/a) when no reviewed id matched a
	// decisive case, so an unusable review never reads as 0%.
	FalseResolutionRate *float64 `json:"false_resolution_rate"`
}

// EvalReport is the full evaluation result; text and JSON render from it.
type EvalReport struct {
	Totals    Counts            `json:"totals"`
	Rates     Rates             `json:"rates"`
	Review    ReviewStats       `json:"review"`
	CacheHits int               `json:"wikidata_cache_hits"`
	ByType    map[string]Counts `json:"by_type"`
	ByKind    map[string]Counts `json:"by_kind"`
	Cases     []EvalCase        `json:"cases"`
}

func (r *EvalReport) add(e EvalCase) {
	if r.ByType == nil {
		r.ByType, r.ByKind = map[string]Counts{}, map[string]Counts{}
	}
	r.Totals.add(e)
	t, k := r.ByType[e.EntityType], r.ByKind[e.Kind]
	t.add(e)
	k.add(e)
	r.ByType[e.EntityType], r.ByKind[e.Kind] = t, k
	r.Cases = append(r.Cases, e)
}

func (r *EvalReport) finish(reviewed map[string]string) {
	if r.ByType == nil {
		r.ByType, r.ByKind = map[string]Counts{}, map[string]Counts{}
	}
	r.Rates = r.Totals.rates()
	for _, c := range r.Cases {
		if c.Outcome != OutcomeVerifiedNFL && c.Outcome != OutcomeVerifiedNonNFL {
			continue
		}
		v, ok := reviewed[c.ProposalID]
		if !ok {
			continue
		}
		r.Review.Reviewed++
		if v == "wrong" {
			r.Review.Wrong++
		}
	}
	r.Review.Provided = len(reviewed)
	r.Review.Unmatched = r.Review.Provided - r.Review.Reviewed
	if r.Review.Reviewed > 0 {
		rate := ratio(r.Review.Wrong, r.Review.Reviewed)
		r.Review.FalseResolutionRate = &rate
	}
}

// WriteJSON renders the report as indented JSON.
func (r EvalReport) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText renders the report for humans.
func (r EvalReport) WriteText(w io.Writer) error {
	var err error
	p := func(format string, a ...any) {
		if err == nil {
			_, err = fmt.Fprintf(w, format+"\n", a...)
		}
	}
	t := r.Totals
	p("proposals evaluated: %d", t.Evaluated)
	p("local catalog hits: %d", t.LocalCatalog)
	p("wikidata search hits: %d", t.SearchHits)
	p("wikidata cache hits: %d", r.CacheHits)
	p("verified NFL: %d", t.VerifiedNFL)
	p("verified non-NFL: %d", t.VerifiedNonNFL)
	p("ambiguous: %d", t.Ambiguous)
	p("not found: %d", t.NotFound)
	p("provider errors: %d", t.Errors)
	p("team metadata available: %d", t.TeamMetadata)
	p("position/role metadata available: %d", t.PositionMetadata)
	p("LLM fallback required: %d", t.FallbackRequired)
	p("estimated manual reviews avoided: %d", t.ManualAvoided)
	p("resolver coverage: %.1f%%", r.Rates.ResolverCoverage*100)
	p("safe NFL resolution rate: %.1f%%", r.Rates.SafeNFLRate*100)
	p("safe non-NFL rejection rate: %.1f%%", r.Rates.SafeNonNFLRate*100)
	p("ambiguity/not-found rate: %.1f%%", r.Rates.AmbiguityNotFoundRate*100)
	p("team enrichment coverage (of verified NFL): %.1f%%", r.Rates.TeamCoverage*100)
	p("position/role enrichment coverage (of verified NFL): %.1f%%", r.Rates.PositionCoverage*100)
	p("remaining LLM/SearXNG rate: %.1f%%", r.Rates.RemainingLLMRate*100)
	if r.Review.Provided > 0 {
		rate := "n/a"
		if r.Review.FalseResolutionRate != nil {
			rate = fmt.Sprintf("%.1f%%", *r.Review.FalseResolutionRate*100)
		}
		p("reviewed sample: %d reviewed, %d wrong, false-resolution rate %s", r.Review.Reviewed, r.Review.Wrong, rate)
		if r.Review.Unmatched > 0 {
			p("warning: %d of %d reviewed ids matched no decisive case in this run (different --seed/--status/--limit?)", r.Review.Unmatched, r.Review.Provided)
		}
	}
	for _, g := range []struct {
		title string
		m     map[string]Counts
	}{{"by entity type", r.ByType}, {"by proposal kind", r.ByKind}} {
		p("%s:", g.title)
		keys := make([]string, 0, len(g.m))
		for k := range g.m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			c := g.m[k]
			p("  %s: evaluated %d, NFL %d, non-NFL %d, ambiguous %d, not found %d, errors %d, fallback required %d",
				k, c.Evaluated, c.VerifiedNFL, c.VerifiedNonNFL, c.Ambiguous, c.NotFound, c.Errors, c.FallbackRequired)
		}
	}
	return err
}
