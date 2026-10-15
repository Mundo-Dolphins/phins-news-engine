// ports/out/eventrulediscovery.go
package out

import "context"

// EventRuleArticle is one article's title/summary handed to the LLM for
// event rule discovery.
type EventRuleArticle struct {
	ArticleID string
	Title     string
	Summary   string
}

// EventTopicProposal is one event topic reported by the LLM, before any
// local validation.
type EventTopicProposal struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	ArticleIDs      []string `json:"article_ids"`
	SuggestedTarget string   `json:"suggested_target"` // existing action/signal it resembles, or ""
}

// EventPhraseProposal is one candidate phrase for an existing action or
// signal, before any local validation.
type EventPhraseProposal struct {
	TargetKind string   `json:"target_kind"` // "action" | "signal"
	Target     string   `json:"target"`
	Phrase     string   `json:"phrase"`
	ArticleIDs []string `json:"article_ids"`
}

// EventRuleDiscoveryLLM is the driven port to an OpenAI-compatible chat-
// completions endpoint for event rule discovery. Validation of targets and
// phrases against article text is the caller's job.
type EventRuleDiscoveryLLM interface {
	DiscoverTopics(ctx context.Context, articles []EventRuleArticle) ([]EventTopicProposal, error)
	// DiscoverPhrases proposes phrases for the closed lists of allowed
	// action and signal names, which the prompt enumerates.
	DiscoverPhrases(ctx context.Context, articles []EventRuleArticle, allowedActions, allowedSignals []string) ([]EventPhraseProposal, error)
}
