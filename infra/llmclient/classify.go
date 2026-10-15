// infra/llmclient/classify.go
package llmclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

var _ appout.EditorialTeacherLLM = (*Client)(nil)

const classifyOutputInstructions = `

Return JSON only, matching this shape:
{"labels": [{"article_id": "...", "label": "publish|discard|uncertain", "confidence": 0.0, "reason": "..."}]}`

// Classify implements ports/out.EditorialTeacherLLM. The
// system prompt is policyText (the full publication-policy-v1 document,
// owned by app/editorialteacher) plus the fixed output-
// schema instructions below — this package never embeds policy text
// itself, so it stays free of any dependency on that package.
func (c *Client) Classify(ctx context.Context, policyText string, articles []appout.EditorialTeacherArticle) (appout.EditorialTeacherResult, error) {
	systemPrompt := policyText + classifyOutputInstructions
	userPrompt := buildClassifyUserPrompt(articles)
	content, err := c.t.completeWithFallback(ctx, systemPrompt, userPrompt)
	if err != nil {
		return appout.EditorialTeacherResult{}, fmt.Errorf("llmclient: %w", err)
	}
	labels, err := parseClassifyLabels(content)
	if err != nil {
		return appout.EditorialTeacherResult{}, fmt.Errorf("llmclient: parse response content: %w", err)
	}
	return appout.EditorialTeacherResult{Labels: labels}, nil
}

func buildClassifyUserPrompt(articles []appout.EditorialTeacherArticle) string {
	var b strings.Builder
	for _, a := range articles {
		entities := "(none)"
		if len(a.Entities) > 0 {
			entities = strings.Join(a.Entities, ", ")
		}
		topicEvent := a.TopicEvent
		if topicEvent == "" {
			topicEvent = "(none)"
		}
		fmt.Fprintf(&b, "ARTICLE_ID: %s\nTITLE: %s\nSUMMARY: %s\nENTITIES: %s\nCLUSTER_ID: %s\nSOURCE: %s/%s\n\n",
			a.ArticleID, a.Title, a.Summary, entities, topicEvent, a.SourceType, a.Domain)
	}
	return b.String()
}

type classifyLabelsPayload struct {
	Labels []appout.EditorialTeacherLabel `json:"labels"`
}

// parseClassifyLabels decodes content as {"labels": [...]}, first
// stripping a code fence some small local models wrap their JSON output
// in.
func parseClassifyLabels(content string) ([]appout.EditorialTeacherLabel, error) {
	trimmed := stripCodeFence(content)
	var payload classifyLabelsPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return payload.Labels, nil
}
