package train_test

import (
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/fsperm"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func TestAuditDatasetCmdWritesPrioritizedRows(t *testing.T) {
	previousConfigPath := shared.ConfigPath
	shared.ConfigPath = repofs.ExampleConfig(t, "settings.yml")
	t.Cleanup(func() { shared.ConfigPath = previousConfigPath })
	dir := t.TempDir()
	datasetPath := filepath.Join(dir, "dataset.yml")
	outPath := filepath.Join(dir, "audit.csv")
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "p1", Title: "Same title", Label: "publish", LabelSource: "cli:label", SourceID: "rss:a"},
		{ArticleID: "r1", Title: "Same title!", Label: "reject", LabelSource: "cli:label", SourceID: "rss:b"},
		{ArticleID: "p2", Title: "Short post", Label: "publish", LabelSource: "telegramimport", SourceID: "twitter:a"},
		{ArticleID: "r2", Title: "Weak reject", Label: "reject", LabelSource: "pipeline_backfill", SourceID: "rss:c"},
	}
	if err := editorialtrain.SaveDataset(datasetPath, examples); err != nil {
		t.Fatal(err)
	}
	output, err := testsupport.Execute(t, "train", "audit-dataset", "--dataset", datasetPath, "--out", outPath)
	if err != nil {
		t.Fatalf("Execute() error = %v; output: %s", err, output)
	}
	fsperm.Assert(t, outPath, 0o600)
	csvBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(csvBytes)
	for _, id := range []string{"p1", "r1", "p2", "r2"} {
		if !strings.Contains(text, id) {
			t.Errorf("audit CSV missing %s: %s", id, text)
		}
	}
}
