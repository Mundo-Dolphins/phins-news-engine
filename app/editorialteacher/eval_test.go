package editorialteacher

import (
	"context"
	"errors"
	"strings"
	"testing"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

type fakeTeacher struct {
	calls  int
	policy string
	// verdict maps a title prefix to the label/confidence returned.
	verdict map[string]appout.EditorialTeacherLabel
	failOn  string
}

func (f *fakeTeacher) Classify(_ context.Context, policy string, arts []appout.EditorialTeacherArticle) (appout.EditorialTeacherResult, error) {
	f.calls++
	f.policy = policy
	var res appout.EditorialTeacherResult
	for _, a := range arts {
		if f.failOn != "" && strings.HasPrefix(a.Title, f.failOn) {
			return appout.EditorialTeacherResult{}, errors.New("boom")
		}
		for prefix, l := range f.verdict {
			if strings.HasPrefix(a.Title, prefix) {
				l.ArticleID = a.ArticleID
				res.Labels = append(res.Labels, l)
			}
		}
	}
	return res, nil
}

func evalExamples() []EvalExample {
	return []EvalExample{
		{ArticleID: "1", Title: "greeting", Truth: "reject"},
		{ArticleID: "2", Title: "promo", Truth: "reject"},
		{ArticleID: "3", Title: "hype", Truth: "publish"},
		{ArticleID: "4", Title: "injury", Truth: "publish"},
		{ArticleID: "5", Title: "vague", Truth: "reject"},
		{ArticleID: "6", Title: "missing", Truth: "publish"},
	}
}

func TestEvaluate_BatchesAndSummarizes(t *testing.T) {
	llm := &fakeTeacher{verdict: map[string]appout.EditorialTeacherLabel{
		"greeting": {Label: "discard", Confidence: 0.97},
		"promo":    {Label: "discard", Confidence: 0.90},
		"hype":     {Label: "discard", Confidence: 0.96}, // false discard, confident
		"injury":   {Label: "publish", Confidence: 0.99},
		"vague":    {Label: "uncertain", Confidence: 0.5},
	}}

	report, err := Evaluate(context.Background(), llm, "POLICY", evalExamples(), 4)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if llm.calls != 2 {
		t.Errorf("LLM calls = %d, want 2 (6 examples, batch size 4)", llm.calls)
	}
	if llm.policy != "POLICY" {
		t.Errorf("policy passed = %q, want POLICY", llm.policy)
	}
	if report.Missing != 1 {
		t.Errorf("Missing = %d, want 1 (no label returned for 'missing')", report.Missing)
	}

	s := report.Summarize(0.95)
	if s.DiscardTotal != 3 || s.DiscardCorrect != 2 {
		t.Errorf("discard total/correct = %d/%d, want 3/2", s.DiscardTotal, s.DiscardCorrect)
	}
	if s.ConfidentDiscardTotal != 2 || s.ConfidentDiscardCorrect != 1 {
		t.Errorf("confident discard total/correct = %d/%d, want 2/1", s.ConfidentDiscardTotal, s.ConfidentDiscardCorrect)
	}
	if got := s.ConfidentDiscardPrecision(); got != 0.5 {
		t.Errorf("ConfidentDiscardPrecision() = %v, want 0.5", got)
	}
	if s.RejectTotal != 3 || s.RejectRecall() != 1.0/3.0 {
		t.Errorf("reject total/recall = %d/%v, want 3/0.333 (only 'greeting' is a confident correct discard)", s.RejectTotal, s.RejectRecall())
	}
	if s.Uncertain != 1 {
		t.Errorf("Uncertain = %d, want 1", s.Uncertain)
	}
}

func TestEvaluate_FailedBatchIsCountedNotFatal(t *testing.T) {
	llm := &fakeTeacher{failOn: "greeting", verdict: map[string]appout.EditorialTeacherLabel{"injury": {Label: "publish", Confidence: 0.9}}}
	report, err := Evaluate(context.Background(), llm, "P", evalExamples(), 2)
	if err != nil {
		t.Fatalf("Evaluate() error = %v, want a failed batch to be counted, not returned", err)
	}
	if report.BatchesFailed != 1 {
		t.Errorf("BatchesFailed = %d, want 1", report.BatchesFailed)
	}
}

func TestSelectEvalExamples_SplitsDeterministicallyAndDisjointly(t *testing.T) {
	var all []EvalExample
	for i := 0; i < 200; i++ {
		all = append(all, EvalExample{ArticleID: "id-" + string(rune('A'+i%26)) + string(rune('a'+i/26)), Truth: "reject"})
	}
	dev := SelectEvalExamples(all, "dev", 0, 1)
	hold := SelectEvalExamples(all, "holdout", 0, 1)
	if len(dev)+len(hold) != len(all) || len(dev) == 0 || len(hold) == 0 {
		t.Fatalf("dev/holdout = %d/%d, want a partition of %d", len(dev), len(hold), len(all))
	}
	seen := map[string]bool{}
	for _, e := range dev {
		seen[e.ArticleID] = true
	}
	for _, e := range hold {
		if seen[e.ArticleID] {
			t.Fatalf("%s is in both dev and holdout", e.ArticleID)
		}
	}
	if again := SelectEvalExamples(all, "dev", 0, 1); len(again) != len(dev) {
		t.Error("split is not deterministic")
	}
	if sample := SelectEvalExamples(all, "dev", 10, 1); len(sample) != 10 {
		t.Errorf("sample size = %d, want 10", len(sample))
	}
}
