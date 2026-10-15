package train_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func TestReviewDatasetCmdWritesReviewedCopyAndDecisionLog(t *testing.T) {
	previousConfigPath := shared.ConfigPath
	shared.ConfigPath = repofs.ExampleConfig(t, "settings.yml")
	t.Cleanup(func() { shared.ConfigPath = previousConfigPath })
	dir := t.TempDir()
	inPath := filepath.Join(dir, "dataset.yml")
	auditPath := filepath.Join(dir, "audit.csv")
	outPath := filepath.Join(dir, "reviewed.yml")
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", Title: "Short social post", Body: "", SourceID: "twitter:test", Label: "publish", LabelSource: "telegramimport", Revision: 1},
		{ArticleID: "a2", Title: "Good news", Body: "Details", SourceID: "rss:test", Label: "reject", LabelSource: "cli:label", Revision: 2},
	}
	if err := editorialtrain.SaveDataset(inPath, examples); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auditPath, []byte("article_id,audit_reason\na1,short title\na2,manual check\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := testsupport.ExecuteWithStdin(t, "r\nk\n", "train", "review-dataset", "--dataset", inPath, "--audit", auditPath, "--out", outPath, "--by", "reviewer-test")
	if err != nil {
		t.Fatalf("ExecuteWithStdin() error = %v; output: %s", err, output)
	}
	got, err := editorialtrain.LoadDataset(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Examples[0].Label != "reject" || got.Examples[0].LabelSource != "interactive_review" {
		t.Errorf("first reviewed example = %+v, want confirmed reject from interactive_review", got.Examples[0])
	}
	if got.Examples[1].Label != "reject" || got.Examples[1].LabelSource != "interactive_review" {
		t.Errorf("second reviewed example = %+v, want confirmed current reject label", got.Examples[1])
	}
	original, err := editorialtrain.LoadDataset(inPath)
	if err != nil {
		t.Fatal(err)
	}
	if original.Examples[0].Label != "publish" {
		t.Errorf("source dataset label = %q, want unchanged publish", original.Examples[0].Label)
	}
	log, err := os.ReadFile(outPath + ".review.csv")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "reviewer-test") || !strings.Contains(string(log), "short title") {
		t.Errorf("review log = %q, want reviewer and audit reason", log)
	}
	if _, err := testsupport.ExecuteWithStdin(t, "k\nk\n", "train", "review-dataset", "--dataset", inPath, "--audit", auditPath, "--out", outPath, "--by", "reviewer-test"); err != nil {
		t.Fatalf("resume ExecuteWithStdin() error = %v", err)
	}
	resumed, err := editorialtrain.LoadDataset(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Examples[0].Label != "reject" {
		t.Errorf("resumed review label = %q, want prior correction reject", resumed.Examples[0].Label)
	}
}

func TestReviewDatasetCmdRejectsOverwritingSource(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "dataset.yml")
	if err := editorialtrain.SaveDataset(inPath, []editorialtrain.TrainingExample{{ArticleID: "a1", Title: "A", Label: "publish"}}); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(dir, "audit.csv")
	if err := os.WriteFile(auditPath, []byte("article_id,audit_reason\na1,check\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := testsupport.ExecuteWithStdin(t, "k\n", "train", "review-dataset", "--dataset", inPath, "--audit", auditPath, "--out", inPath, "--by", "reviewer-test")
	if err == nil {
		t.Fatal("ExecuteWithStdin() error = nil, want overwrite protection")
	}
}
