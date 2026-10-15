// Package out defines driven ports (adapters implementing domain use cases).
package out

import "context"

// EditorialTeacherArticle is one article's context handed to the LLM
// teacher for independent publish/discard classification.
// It deliberately has no field that could carry the production Naive
// Bayes classifier's own output (domain/editorial.Score/Zone)
// — the absence is structural, not a runtime check.
type EditorialTeacherArticle struct {
	ArticleID string
	Title     string
	Summary   string
	// Entities is best-effort (e.g. the article's already-resolved
	// primary entity); empty when none is known.
	Entities []string
	// TopicEvent is a best-effort event/topic grouping signal (e.g. a
	// cluster id); "" when none is known.
	TopicEvent string
	SourceID   string
	SourceType string
	Domain     string
}

// EditorialTeacherLabel is one article's raw LLM classification, before
// any local validation.
type EditorialTeacherLabel struct {
	ArticleID  string  `json:"article_id"`
	Label      string  `json:"label"` // "publish" | "discard" | "uncertain"
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// EditorialTeacherResult is one batch's raw LLM output.
type EditorialTeacherResult struct {
	Labels []EditorialTeacherLabel
}

// EditorialTeacherLLM is the driven port to an OpenAI-compatible chat-
// completions endpoint, called only by the offline `editorial classify`
// command — never by the ingestion pipeline. policyText is
// the full publication-policy-v1 document (owned by
// app/editorialteacher, passed in here so this
// package has no dependency on it) prepended to the fixed output-
// schema instructions to form the system prompt.
type EditorialTeacherLLM interface {
	Classify(ctx context.Context, policyText string, articles []EditorialTeacherArticle) (EditorialTeacherResult, error)
}
