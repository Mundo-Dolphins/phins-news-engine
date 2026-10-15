package llmclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// defaultVerifySystemPrompt implements the verification prompt concept,
// extended to a structured final action.
const defaultVerifySystemPrompt = `You are maintaining a deterministic catalog of the NFL domain: players, coaches, executives and teams from any NFL team, not only the team this catalog follows.

Use the supplied local evidence first. If it is sufficient, respond with a final action. If an important fact (entity type, player position, team affiliation) cannot be established reliably from the supplied information, request a web search instead. A search query must include the fact being resolved, for example "Jordan Sample NFL team position".

Do not request a web search for facts already supported by the local evidence. Do not invent external facts. You may request at most 2 searches for this candidate. Only use returned web evidence to support your final answer.

Never repeat a query. After receiving web results, prefer a final action; if you have requested two searches, your next action must be final, even when the evidence is inconclusive.

In a final action, set disposition to one of keep, reject or uncertain. Use reject only when the web results show the entity belongs to another league (MLB, NBA, NHL, NCAA). A generic place name with no specific NFL entity is uncertain or reject, never keep. Use uncertain when the evidence is inconclusive.

For players, set affiliation_status to "team" (with team and team_id) when the current team is established, "free_agent" only when current web evidence explicitly says the player is unsigned (leave team and team_id empty), and "unknown" otherwise. A missing team in the evidence is never proof of free agency.

Return JSON only, matching one of these shapes:
{"action":"final","disposition":"keep|reject|uncertain","canonical_name":"...","entity_type":"player|coach|executive|team","reason":"...","metadata":{"sport":"","league":"","position":"","role":"","team":"","team_id":"","affiliation_status":"","nfl_context":""}}
or
{"action": "web_search", "query": "..."}`

// VerifyCandidate implements ports/out.EntityDiscoveryLLM.
func (c *Client) VerifyCandidate(ctx context.Context, req appout.VerifyCandidateRequest) (weblookup.AnalysisAction, error) {
	userPrompt := buildVerifyPrompt(req)
	content, err := c.t.completeWithFallback(ctx, CurrentPrompts().EntityVerification, userPrompt)
	if err != nil {
		return weblookup.AnalysisAction{}, fmt.Errorf("llmclient: %w", err)
	}
	action, err := parseAction(content)
	if err != nil {
		return weblookup.AnalysisAction{}, fmt.Errorf("llmclient: parse response content: %w", err)
	}
	return action, nil
}

func buildVerifyPrompt(req appout.VerifyCandidateRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CANDIDATE_NAME: %s\nCANDIDATE_TYPE: %s\n", req.CandidateName, req.CandidateType)
	for k, v := range req.CandidateMetadata {
		fmt.Fprintf(&b, "METADATA_%s: %s\n", strings.ToUpper(k), v)
	}
	b.WriteString("\nLOCAL_EVIDENCE:\n")
	for _, a := range req.LocalEvidence {
		fmt.Fprintf(&b, "ARTICLE_ID: %s\nTITLE: %s\nSUMMARY: %s\n\n", a.ArticleID, a.Title, a.Summary)
	}
	if len(req.FallbackHistory) > 0 {
		b.WriteString("FALLBACK_HISTORY:\n")
		for _, f := range req.FallbackHistory {
			fmt.Fprintf(&b, "type=%s confidence=%.2f occurrences=%d\n", f.EntityType, f.Confidence, f.Occurrences)
		}
	}
	if len(req.SearchResults) > 0 {
		b.WriteString("\nWEB_SEARCH_RESULTS:\n")
		for _, r := range req.SearchResults {
			fmt.Fprintf(&b, "TITLE: %s\nDOMAIN: %s\nSNIPPET: %s\n\n", r.Title, r.Domain, r.Snippet)
		}
	}
	if req.FinalOnly {
		b.WriteString("\nSEARCH_STATUS: web results are above; no more searches are allowed. Your next action MUST be final.\n")
	}
	return b.String()
}

type actionPayload struct {
	Action        string            `json:"action"`
	Query         string            `json:"query,omitempty"`
	Disposition   string            `json:"disposition,omitempty"`
	CanonicalName string            `json:"canonical_name,omitempty"`
	EntityType    string            `json:"entity_type,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// allowedMetadataKeys is the only set of metadata keys accepted from the
// LLM; anything else is dropped.
var allowedMetadataKeys = map[string]struct{}{
	"sport": {}, "league": {}, "position": {}, "role": {},
	"team": {}, "team_id": {}, "nfl_context": {}, "affiliation_status": {},
}

// validAffiliationStatuses are the only affiliation_status values kept.
var validAffiliationStatuses = map[string]struct{}{"team": {}, "free_agent": {}, "unknown": {}}

// maxMetadataValueLength caps each accepted metadata value.
const maxMetadataValueLength = 100

// filterMetadata keeps only allow-listed keys, trimming each value and
// capping it at maxMetadataValueLength runes.
func filterMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if _, ok := allowedMetadataKeys[k]; !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if r := []rune(v); len(r) > maxMetadataValueLength {
			v = string(r[:maxMetadataValueLength])
		}
		if _, ok := validAffiliationStatuses[v]; k == "affiliation_status" && !ok {
			continue
		}
		out[k] = v
	}
	return out
}

// parseAction decodes content as a structured action, first stripping a
// code fence some small local models wrap their JSON output in, and
// rejects any action other than the two the application supports.
// A missing or unknown disposition
// becomes uncertain.
func parseAction(content string) (weblookup.AnalysisAction, error) {
	trimmed := stripCodeFence(content)
	var payload actionPayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return weblookup.AnalysisAction{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if payload.Action != weblookup.ActionFinal && payload.Action != weblookup.ActionWebSearch {
		return weblookup.AnalysisAction{}, fmt.Errorf("invalid action %q", payload.Action)
	}
	disposition, _ := weblookup.ParseDisposition(payload.Disposition)
	return weblookup.AnalysisAction{
		Action:        payload.Action,
		Query:         payload.Query,
		Disposition:   disposition,
		CanonicalName: strings.TrimSpace(payload.CanonicalName),
		EntityType:    strings.TrimSpace(payload.EntityType),
		Metadata:      filterMetadata(payload.Metadata),
		Reason:        strings.TrimSpace(payload.Reason),
	}, nil
}
