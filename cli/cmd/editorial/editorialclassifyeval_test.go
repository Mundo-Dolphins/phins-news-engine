// cli/cmd/editorial/editorialclassifyeval_test.go
package editorial

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialteacher"
)

func TestNewClassifyEvalCommand_HasExpectedFlagsAndDefaults(t *testing.T) {
	cmd := newClassifyEvalCommand()
	for _, name := range []string{"dataset", "fold", "sample", "seed", "threshold", "batch-size", "policy-file", "misses-out", "include-imported", "llm-base-url", "llm-model"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag %q", name)
		}
	}
	if fold, _ := cmd.Flags().GetString("fold"); fold != "dev" {
		t.Errorf("fold default = %q, want dev (holdout must be an explicit choice)", fold)
	}
	if imported, _ := cmd.Flags().GetBool("include-imported"); imported {
		t.Error("include-imported default = true, want false: telegramimport labels are not human-reviewed")
	}
}

func TestWriteEvalMisses_OnlyConfidentDiscardsOfHumanPublishes(t *testing.T) {
	preds := []editorialteacher.EvalPrediction{
		{EvalExample: editorialteacher.EvalExample{ArticleID: "hit", Truth: "publish", Title: "keep me"}, Label: "discard", Confidence: 0.96, Reason: "r1"},
		{EvalExample: editorialteacher.EvalExample{ArticleID: "low", Truth: "publish"}, Label: "discard", Confidence: 0.80},
		{EvalExample: editorialteacher.EvalExample{ArticleID: "right", Truth: "reject"}, Label: "discard", Confidence: 0.99},
		{EvalExample: editorialteacher.EvalExample{ArticleID: "pub", Truth: "publish"}, Label: "publish", Confidence: 0.99},
	}
	path := filepath.Join(t.TempDir(), "misses.csv")
	if err := writeEvalMisses(path, preds, 0.95); err != nil {
		t.Fatalf("writeEvalMisses() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "hit,0.96") {
		t.Errorf("csv = %q, want a header plus only the 'hit' row", lines)
	}
}
