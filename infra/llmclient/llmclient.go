// infra/llmclient/llmclient.go

// Package llmclient implements independent OpenAI-compatible chat-
// completions callers over a shared HTTP transport (net/http +
// encoding/json only, no new go.mod dependency): Client, used by the
// offline `entities discover` command for batch entity-
// catalog mining and, via its Classify method, by the offline
// `editorial classify` command for batch publish/discard
// teacher classification; and Resolver, used by the online, per-
// candidate unknown-entity resolution fallback
// that runs inside the ingestion pipeline itself when configured. Each
// type/config instance is independently configured (base_url/model/
// temperature/timeout) and may point at entirely different providers.
// Client is never imported by the service binary (the ingestion
// binary); Resolver is imported there deliberately, gated by
// entity_extraction.fallback.enabled.
package llmclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Client implements ports/out.EntityDiscoveryLLM.
type Client struct {
	t *transport
}

// New builds a Client for cfg.
func New(cfg Config) *Client {
	return &Client{t: newTransport(cfg)}
}

var _ appout.EntityDiscoveryLLM = (*Client)(nil)

const defaultDiscoverySystemPrompt = `You are analysing supplied NFL news records to find named entities that should potentially exist in a deterministic entity catalog. Use only information present in the supplied records — never your prior knowledge of the NFL. Do not add a person merely because they are commonly associated with a team or its city; only extract what the text itself supports.

Extract players, coaches, executives and teams explicitly mentioned. Prefer full names. List aliases only when the text itself uses them. Only report metadata (e.g. position) explicitly stated in the text. Mark uncertain classifications with a lower confidence value.

Return at most 20 entities total, prioritising specific proper names over generic team references. Do not include source article IDs: the caller assigns evidence from the supplied records after validating it.

Return JSON only, matching this shape:
{"entities": [{"name": "...", "type": "player|coach|executive|team", "aliases": ["..."], "metadata": {"position": "..."}, "confidence": 0.0}]}`

// maxStructuredAttempts bounds the structured-output retry in Discover:
// one repeat after an empty or unparseable response, independent of the
// HTTP 429/5xx retry inside transport.complete.
const maxStructuredAttempts = 2

// retryMaxTokens returns the max_tokens budget for the structured-output
// retry: doubled when the previous completion hit the length limit and an
// explicit budget was configured, otherwise unchanged.
func retryMaxTokens(prev completion) int {
	if prev.FinishReason == "length" && prev.MaxTokens > 0 {
		return prev.MaxTokens * 2
	}
	return prev.MaxTokens
}

// structuredFailure describes why a completion was unusable, using only
// metadata — never the response content itself.
func structuredFailure(c completion, parseErr error) string {
	kind := "invalid JSON"
	switch {
	case strings.TrimSpace(c.Content) == "":
		kind = "empty response"
	case c.FinishReason == "length":
		kind = "truncated response"
	}
	return fmt.Sprintf("%s: finish_reason=%s content_bytes=%d completion_tokens=%d max_tokens=%d parse_error=%q",
		kind, c.FinishReason, len(c.Content), c.CompletionTokens, c.MaxTokens, parseErr.Error())
}

// Discover implements ports/out.EntityDiscoveryLLM. An empty or unparseable
// response is retried once (with a doubled max_tokens budget after
// finish_reason=length); two failures return an error so the caller leaves
// the batch unprocessed. Partial JSON is never repaired.
func (c *Client) Discover(ctx context.Context, articles []appout.EntityDiscoveryArticle) (appout.EntityDiscoveryResult, error) {
	userPrompt := buildUserPrompt(articles)
	maxTokens := c.t.cfg.MaxTokens
	var last completion
	var lastParseErr error
	for attempt := 1; attempt <= maxStructuredAttempts; attempt++ {
		comp, err := c.t.complete(ctx, CurrentPrompts().EntityDiscovery, userPrompt, maxTokens)
		if err != nil {
			return appout.EntityDiscoveryResult{}, fmt.Errorf("llmclient: %w", err)
		}
		entities, parseErr := parseEntities(comp.Content)
		if parseErr == nil {
			return appout.EntityDiscoveryResult{Entities: entities}, nil
		}
		last, lastParseErr = comp, parseErr
		if attempt < maxStructuredAttempts {
			maxTokens = retryMaxTokens(comp)
			c.t.logger().Warn("llmclient: unusable structured response, retrying once",
				"attempt", attempt, "finish_reason", comp.FinishReason, "content_bytes", len(comp.Content),
				"completion_tokens", comp.CompletionTokens, "max_tokens", comp.MaxTokens,
				"retry_max_tokens", maxTokens, "parse_error", parseErr.Error())
		}
	}
	return appout.EntityDiscoveryResult{}, fmt.Errorf("llmclient: structured response invalid after %d attempts: %s",
		maxStructuredAttempts, structuredFailure(last, lastParseErr))
}

func buildUserPrompt(articles []appout.EntityDiscoveryArticle) string {
	var b strings.Builder
	for _, a := range articles {
		fmt.Fprintf(&b, "ARTICLE_ID: %s\nTITLE: %s\nSUMMARY: %s\n\n", a.ArticleID, a.Title, a.Summary)
	}
	return b.String()
}

type entitiesPayload struct {
	Entities []appout.EntityDiscoveryEntity `json:"entities"`
}

// parseEntities decodes content as {"entities": [...]}, first stripping
// a code fence some small local models wrap their JSON output in.
func parseEntities(content string) ([]appout.EntityDiscoveryEntity, error) {
	trimmed := stripCodeFence(content)
	var payload entitiesPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return payload.Entities, nil
}
