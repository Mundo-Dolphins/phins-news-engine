package llmclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/llmclient"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// newChooseClient returns a Client whose server always answers with content.
func newChooseClient(t *testing.T, content string) *llmclient.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(chatCompletionResponse(t, content))
	}))
	t.Cleanup(server.Close)
	return llmclient.New(llmclient.Config{BaseURL: server.URL, Model: "test-model", Timeout: 5 * time.Second})
}

func chooseReq() appout.ChooseCandidateRequest {
	return appout.ChooseCandidateRequest{
		Mention:  "Brackwater",
		Articles: []appout.EntityDiscoveryArticle{{ArticleID: "a1", Title: "T", Summary: "S"}},
		Candidates: []appout.ChooseCandidate{
			{ID: "0", FullName: "Osric Brackwater", Team: "HC", Position: "DT", Status: "ACT"},
			{ID: "1", FullName: "Zorin Brackwater", Team: "SPR", Position: "S", Status: "ACT"},
		},
	}
}

func TestChooseCandidate_ParsesChoiceConfidenceAndEvidence(t *testing.T) {
	c := newChooseClient(t, `{"choice": "1", "confidence": "high", "evidence": "Safeties ... Brackwater"}`)
	got, err := c.ChooseCandidate(context.Background(), chooseReq())
	if err != nil || got.ChoiceID != "1" || got.Confidence != "high" || got.Evidence == "" {
		t.Fatalf("ChooseCandidate() = %+v, %v", got, err)
	}
}

func TestChooseCandidate_NullChoiceIsAnAbstention(t *testing.T) {
	c := newChooseClient(t, `{"choice": null, "confidence": "low", "evidence": "no support"}`)
	got, err := c.ChooseCandidate(context.Background(), chooseReq())
	if err != nil || got.ChoiceID != "" {
		t.Fatalf("ChooseCandidate() = %+v, %v; want an abstention", got, err)
	}
}

func TestChooseCandidate_NumericChoiceIsAccepted(t *testing.T) {
	c := newChooseClient(t, `{"choice": 1, "confidence": "HIGH ", "evidence": "e"}`)
	got, err := c.ChooseCandidate(context.Background(), chooseReq())
	if err != nil || got.ChoiceID != "1" || got.Confidence != "high" {
		t.Fatalf("ChooseCandidate() = %+v, %v; want id 1 and a normalised confidence", got, err)
	}
}

func TestChooseCandidate_ToleratesTrailingTextAfterTheJSON(t *testing.T) {
	c := newChooseClient(t, `{"choice": "0", "confidence": "high", "evidence": "e"}`+"\nWait, one more thing.")
	got, err := c.ChooseCandidate(context.Background(), chooseReq())
	if err != nil || got.ChoiceID != "0" {
		t.Fatalf("ChooseCandidate() = %+v, %v; want the first JSON value to be used", got, err)
	}
}

func TestChooseCandidate_InvalidResponseIsAnError(t *testing.T) {
	c := newChooseClient(t, `not json at all`)
	if _, err := c.ChooseCandidate(context.Background(), chooseReq()); err == nil {
		t.Fatal("ChooseCandidate() error = nil, want a parse error")
	}
}

func TestBuildChoosePrompt_ListsMentionArticlesAndCandidates(t *testing.T) {
	got := llmclient.BuildChoosePromptForTest(appout.ChooseCandidateRequest{
		Mention:    "Brackwater",
		Articles:   []appout.EntityDiscoveryArticle{{Title: "Isotopes snap counts", Summary: "Safeties played"}},
		Candidates: []appout.ChooseCandidate{{ID: "0", FullName: "Zorin Brackwater", Team: "SPR", Position: "S", Status: "ACT"}},
	})
	for _, want := range []string{"MENTION: Brackwater", "Isotopes snap counts", "0: Zorin Brackwater | SPR | S | status ACT"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
}
