package llmclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestClient_VerifyCandidate_ParsesFinalAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, `{"action":"final"}`))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	action, err := c.VerifyCandidate(context.Background(), appout.VerifyCandidateRequest{CandidateName: "Jeff Harrowby", CandidateType: "coach"})
	if err != nil {
		t.Fatalf("VerifyCandidate() error = %v", err)
	}
	if action.Action != weblookup.ActionFinal || action.Query != "" {
		t.Fatalf("VerifyCandidate() = %+v, want {Action: final, Query: \"\"}", action)
	}
}

func TestClient_VerifyCandidate_ParsesWebSearchAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, `{"action":"web_search","query":"\"Jai'Rene' Mossbank\" Isotopes"}`))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	action, err := c.VerifyCandidate(context.Background(), appout.VerifyCandidateRequest{CandidateName: "Jai'Rene' Mossbank", CandidateType: "player"})
	if err != nil {
		t.Fatalf("VerifyCandidate() error = %v", err)
	}
	if action.Action != weblookup.ActionWebSearch || action.Query != `"Jai'Rene' Mossbank" Isotopes` {
		t.Fatalf("VerifyCandidate() = %+v, want web_search action with the query preserved", action)
	}
}

func TestClient_VerifyCandidate_StripsMarkdownFences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, "```json\n{\"action\":\"final\"}\n```"))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	action, err := c.VerifyCandidate(context.Background(), appout.VerifyCandidateRequest{CandidateName: "Jeff Harrowby"})
	if err != nil {
		t.Fatalf("VerifyCandidate() error = %v", err)
	}
	if action.Action != weblookup.ActionFinal {
		t.Fatalf("VerifyCandidate() = %+v, want the fenced JSON parsed", action)
	}
}

func TestClient_VerifyCandidate_RejectsHallucinatedAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, `{"action":"browse_url","query":"http://example.com"}`))
	}))
	defer server.Close()

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	_, err := c.VerifyCandidate(context.Background(), appout.VerifyCandidateRequest{CandidateName: "Jeff Harrowby"})
	if err == nil || !strings.Contains(err.Error(), "llmclient") {
		t.Fatalf("VerifyCandidate() error = %v, want a wrapped llmclient error for an invalid action", err)
	}
}

func verifyWithContent(t *testing.T, content string) weblookup.AnalysisAction {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, content))
	}))
	t.Cleanup(server.Close)

	c := llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
	action, err := c.VerifyCandidate(context.Background(), appout.VerifyCandidateRequest{CandidateName: "Patrick Ostrander"})
	if err != nil {
		t.Fatalf("VerifyCandidate() error = %v", err)
	}
	return action
}

func TestParseAction_StructuredFinal(t *testing.T) {
	action := verifyWithContent(t, `{"action":"final","disposition":"keep","canonical_name":"Patrick Ostrander","entity_type":"player","reason":"QB","metadata":{"position":"QB","team":"Capital City Capitals","team_id":"capital-city-capitals","league":"NFL","sport":"football"}}`)
	if action.Action != weblookup.ActionFinal || action.Disposition != weblookup.DispositionKeep ||
		action.CanonicalName != "Patrick Ostrander" || action.EntityType != "player" || action.Reason != "QB" {
		t.Fatalf("action = %+v, want all structured fields decoded", action)
	}
	want := map[string]string{"position": "QB", "team": "Capital City Capitals", "team_id": "capital-city-capitals", "league": "NFL", "sport": "football"}
	if len(action.Metadata) != len(want) {
		t.Fatalf("Metadata = %v, want %v", action.Metadata, want)
	}
	for k, v := range want {
		if action.Metadata[k] != v {
			t.Fatalf("Metadata[%q] = %q, want %q", k, action.Metadata[k], v)
		}
	}
}

func TestParseAction_LegacyFinalIsUncertain(t *testing.T) {
	action := verifyWithContent(t, `{"action":"final"}`)
	if action.Disposition != weblookup.DispositionUncertain {
		t.Fatalf("Disposition = %q, want uncertain", action.Disposition)
	}
}

func TestParseAction_UnknownDispositionIsUncertain(t *testing.T) {
	action := verifyWithContent(t, `{"action":"final","disposition":"banana"}`)
	if action.Disposition != weblookup.DispositionUncertain {
		t.Fatalf("Disposition = %q, want uncertain", action.Disposition)
	}
}

func TestParseAction_MetadataAllowListAndCaps(t *testing.T) {
	long := strings.Repeat("x", 150)
	action := verifyWithContent(t, `{"action":"final","disposition":"keep","metadata":{"evil":"x","team":"  Springfield Isotopes  ","role":"`+long+`"}}`)
	if _, ok := action.Metadata["evil"]; ok {
		t.Fatalf("Metadata = %v, want key outside allow-list dropped", action.Metadata)
	}
	if action.Metadata["team"] != "Springfield Isotopes" {
		t.Fatalf("team = %q, want trimmed value", action.Metadata["team"])
	}
	if got := len(action.Metadata["role"]); got != 100 {
		t.Fatalf("role length = %d, want 100", got)
	}
}

func TestParseAction_AffiliationStatusAllowListed(t *testing.T) {
	for _, v := range []string{"team", "free_agent", "unknown"} {
		action := verifyWithContent(t, `{"action":"final","disposition":"keep","metadata":{"affiliation_status":"`+v+`"}}`)
		if action.Metadata["affiliation_status"] != v {
			t.Errorf("affiliation_status %q: Metadata = %v", v, action.Metadata)
		}
	}
	action := verifyWithContent(t, `{"action":"final","disposition":"keep","metadata":{"affiliation_status":"retired","position":"WR"}}`)
	if _, ok := action.Metadata["affiliation_status"]; ok {
		t.Errorf("invalid affiliation_status kept: %v", action.Metadata)
	}
	if action.Metadata["position"] != "WR" {
		t.Errorf("other metadata lost: %v", action.Metadata)
	}
}

func TestBuildVerifyPrompt_FinalOnlyAddsAnExplicitInstruction(t *testing.T) {
	req := appout.VerifyCandidateRequest{CandidateName: "Some Player", CandidateType: "player"}
	if got := llmclient.BuildVerifyPromptForTest(req); strings.Contains(got, "MUST be final") {
		t.Fatalf("a normal step must not carry the final-only instruction:\n%s", got)
	}
	req.FinalOnly = true
	if got := llmclient.BuildVerifyPromptForTest(req); !strings.Contains(got, "MUST be final") {
		t.Fatalf("a FinalOnly step must tell the model its next action MUST be final:\n%s", got)
	}
}
