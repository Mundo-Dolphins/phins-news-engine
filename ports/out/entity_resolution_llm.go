// ports/out/entity_resolution_llm.go
package out

import "context"

// EntityResolutionCandidate is one unknown-entity span handed to the
// online resolution fallback's LLM — a single
// candidate, never a whole article's worth. Field-for-field mirrors
// domain/entityresolution.Candidate; kept as its own type here
// (rather than importing that domain package) so this port's types stay
// primitive, the same reason EntityDiscoveryArticle/EntityDiscoveryEntity
// don't import domain/entityproposal.
type EntityResolutionCandidate struct {
	Text           string
	NormalizedText string
	Field          string
	Context        string
}

// EntityResolutionResult is one candidate's raw LLM answer, before local
// validation (domain/entityresolution.Validate).
type EntityResolutionResult struct {
	Resolved      bool    `json:"resolved"`
	Type          string  `json:"type"`
	CanonicalName string  `json:"canonical_name"`
	Confidence    float64 `json:"confidence"`
}

// EntityResolutionLLM is the driven port to an OpenAI-compatible chat-
// completions endpoint used by the online, per-article resolution
// fallback — unlike EntityDiscoveryLLM (which is
// only ever called from the offline `entities discover` command), an
// EntityResolutionLLM implementation IS constructed and called from
// the service binary when entity_extraction.fallback.enabled is true.
type EntityResolutionLLM interface {
	Resolve(ctx context.Context, c EntityResolutionCandidate) (EntityResolutionResult, error)
}
