// infra/llmclient/choose.go
package llmclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// chooseSystemPrompt is the prompt validated in the 2026-10-08 spike.
const chooseSystemPrompt = `You resolve a short player mention found in news articles to ONE player from a fixed candidate list.
You are given the mention, the article title/summary text, and candidates (id, name, team, position, status).
Choose a candidate only if the article text itself supports it (the full name, the team, the position or other clear context point to exactly one candidate).
If the text does not let you tell the candidates apart, or the mention probably refers to someone who is not in the list (a retired player, a coach, another sport, a different person), answer null. Never guess.
The article must be about American football / the NFL; if it is about another sport (baseball, hockey, basketball, soccer, college-only), answer null.
Rate your confidence: "high" ONLY if the text contains the player's full name, his team or another detail that identifies exactly one candidate; otherwise "medium" or "low". With a single candidate you must still find that evidence in the text.
Return JSON only: {"choice": "<candidate id or null>", "confidence": "high|medium|low", "evidence": "<short quote from the text>"}`

func buildChoosePrompt(req appout.ChooseCandidateRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "MENTION: %s\n\nARTICLES:\n", req.Mention)
	for _, a := range req.Articles {
		fmt.Fprintf(&b, "TITLE: %s\nSUMMARY: %s\n\n", a.Title, a.Summary)
	}
	b.WriteString("CANDIDATES:\n")
	for _, c := range req.Candidates {
		fmt.Fprintf(&b, "%s: %s | %s | %s | status %s\n", c.ID, c.FullName, c.Team, c.Position, c.Status)
	}
	return b.String()
}

type choosePayload struct {
	Choice     any    `json:"choice"`
	Confidence string `json:"confidence"`
	Evidence   string `json:"evidence"`
}

// parseChoice decodes the first JSON value of content (a code fence and any
// text after the object are tolerated).
func parseChoice(content string) (appout.ChooseCandidateResult, error) {
	var p choosePayload
	if err := json.NewDecoder(strings.NewReader(stripCodeFence(content))).Decode(&p); err != nil {
		return appout.ChooseCandidateResult{}, fmt.Errorf("invalid JSON: %w", err)
	}
	res := appout.ChooseCandidateResult{Confidence: strings.ToLower(strings.TrimSpace(p.Confidence)), Evidence: p.Evidence}
	switch v := p.Choice.(type) {
	case nil:
	case string:
		if s := strings.TrimSpace(v); s != "" && !strings.EqualFold(s, "null") {
			res.ChoiceID = s
		}
	case float64:
		res.ChoiceID = fmt.Sprintf("%d", int(v))
	}
	return res, nil
}

// ChooseCandidate implements ports/out.CandidateChooser.
func (c *Client) ChooseCandidate(ctx context.Context, req appout.ChooseCandidateRequest) (appout.ChooseCandidateResult, error) {
	content, err := c.t.completeWithFallback(ctx, chooseSystemPrompt, buildChoosePrompt(req))
	if err != nil {
		return appout.ChooseCandidateResult{}, fmt.Errorf("llmclient: %w", err)
	}
	res, err := parseChoice(content)
	if err != nil {
		return appout.ChooseCandidateResult{}, fmt.Errorf("llmclient: parse response content: %w", err)
	}
	return res, nil
}

var _ appout.CandidateChooser = (*Client)(nil)
