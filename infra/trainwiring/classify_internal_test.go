package trainwiring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialteacher"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
)

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

func TestWriteEvalMisses_UsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "misses.csv")
	if err := writeEvalMisses(path, nil, 0.9); err != nil {
		t.Fatal(err)
	}
	fsperm.Assert(t, path, 0o600)
}
