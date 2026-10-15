// ports/out/entity_discovery_llm.go
package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

// EntityDiscoveryArticle is one article's title/summary handed to the
// LLM for entity discovery — see infra/llmclient and
// app/entitydiscovery.
type EntityDiscoveryArticle struct {
	ArticleID string
	Title     string
	Summary   string
}

// EntityDiscoveryEntity is one entity as reported by the LLM, before any
// local validation, hallucination-evidence check, or catalog comparison.
type EntityDiscoveryEntity struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	Aliases          []string          `json:"aliases"`
	Metadata         map[string]string `json:"metadata"`
	Confidence       float64           `json:"confidence"`
	SourceArticleIDs []string          `json:"source_article_ids"`
}

// EntityDiscoveryResult is one batch's raw LLM output.
type EntityDiscoveryResult struct {
	Entities []EntityDiscoveryEntity
}

// FallbackSignal summarizes a candidate's online-fallback history
// as a local-evidence
// signal the verify prompt can weigh before deciding
// whether to search.
type FallbackSignal struct {
	EntityType  string
	Confidence  float64
	Occurrences int
}

// VerifyCandidateRequest is one step's input to VerifyCandidate: the
// candidate under review, its originating local evidence, any web
// search results accumulated so far this loop, and its online-fallback
// history, if any.
type VerifyCandidateRequest struct {
	CandidateName     string
	CandidateType     string
	CandidateMetadata map[string]string
	LocalEvidence     []EntityDiscoveryArticle
	SearchResults     []weblookup.SearchResult
	FallbackHistory   []FallbackSignal
	// FinalOnly tells the model its next action must be final: the search
	// budget is spent or it just repeated a query. The loop is stateless and
	// the model deterministic, so re-sending an identical prompt would only
	// repeat the same search request.
	FinalOnly bool
}

// EntityDiscoveryLLM is the driven port to an OpenAI-compatible chat-
// completions endpoint, called only by the offline `entities discover`
// command — never by the ingestion pipeline.
type EntityDiscoveryLLM interface {
	Discover(ctx context.Context, articles []EntityDiscoveryArticle) (EntityDiscoveryResult, error)
	// VerifyCandidate runs one step of the bounded catalog-verification
	// tool loop: the LLM either returns a "final" action
	// (evidence in req is sufficient) or a "web_search" action naming a
	// query to run next. Any other Action value is a hallucinated
	// action the caller must reject.
	VerifyCandidate(ctx context.Context, req VerifyCandidateRequest) (weblookup.AnalysisAction, error)
}
