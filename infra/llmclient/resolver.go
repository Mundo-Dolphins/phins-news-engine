// infra/llmclient/resolver.go
package llmclient

import (
	"context"
	"encoding/json"
	"fmt"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Resolver implements ports/out.EntityResolutionLLM: one narrow, per-candidate resolution call, never a batch of
// articles.
type Resolver struct {
	t *transport
}

// NewResolver builds a Resolver for cfg — a Config instance independent
// from any Client's, per the requirement that the online
// fallback and the offline discovery flow be configured separately.
func NewResolver(cfg Config) *Resolver {
	return &Resolver{t: newTransport(cfg)}
}

var _ appout.EntityResolutionLLM = (*Resolver)(nil)

const defaultResolutionSystemPrompt = `You are resolving a single candidate name found in an NFL news article. Decide whether it names a specific NFL player, coach, executive, or team, using ONLY the context text provided — never your prior knowledge. If the context does not clearly support a confident classification, or the name is not actually a person/team (e.g. a place, a headline phrase, an organization that is not an NFL team), return resolved: false. Never invent a canonical name that isn't supported by the context.

Return JSON only, matching this shape:
{"resolved": true, "type": "player|coach|executive|team", "canonical_name": "...", "confidence": 0.0}
or
{"resolved": false}`

// Resolve implements ports/out.EntityResolutionLLM.
func (r *Resolver) Resolve(ctx context.Context, c appout.EntityResolutionCandidate) (appout.EntityResolutionResult, error) {
	userPrompt := fmt.Sprintf("CANDIDATE: %s\nFIELD: %s\nCONTEXT: %s\n", c.Text, c.Field, c.Context)
	content, err := r.t.completeWithFallback(ctx, CurrentPrompts().EntityResolution, userPrompt)
	if err != nil {
		return appout.EntityResolutionResult{}, fmt.Errorf("llmclient: %w", err)
	}
	trimmed := stripCodeFence(content)
	var result appout.EntityResolutionResult
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return appout.EntityResolutionResult{}, fmt.Errorf("llmclient: invalid JSON: %w", err)
	}
	return result, nil
}
