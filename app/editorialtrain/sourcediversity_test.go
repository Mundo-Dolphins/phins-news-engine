package editorialtrain_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

func TestSourceConcentration_ReturnsLargestSourceAndItsShare(t *testing.T) {
	examples := []editorialtrain.TrainingExample{
		{SourceID: "src-a"}, {SourceID: "src-a"}, {SourceID: "src-a"}, {SourceID: "src-b"},
	}
	source, share := editorialtrain.SourceConcentration(examples)
	if source != "src-a" || share != 0.75 {
		t.Errorf("SourceConcentration() = (%q, %v), want (\"src-a\", 0.75)", source, share)
	}
}

func TestSourceConcentration_TieBreaksAlphabetically(t *testing.T) {
	examples := []editorialtrain.TrainingExample{{SourceID: "src-b"}, {SourceID: "src-a"}}
	if source, _ := editorialtrain.SourceConcentration(examples); source != "src-a" {
		t.Errorf("SourceConcentration() source = %q, want src-a (deterministic tie-break)", source)
	}
}

func TestSourceConcentration_EmptyDataset(t *testing.T) {
	if source, share := editorialtrain.SourceConcentration(nil); source != "" || share != 0 {
		t.Errorf("SourceConcentration(nil) = (%q, %v), want (\"\", 0)", source, share)
	}
}

func TestWriteManifest_RecordsSourceConcentration(t *testing.T) {
	datasetPath := filepath.Join(t.TempDir(), "dataset.yml")
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", SourceID: "src-a", Label: "publish"},
		{ArticleID: "a2", SourceID: "src-a", Label: "reject"},
		{ArticleID: "a3", SourceID: "src-b", Label: "publish"},
		{ArticleID: "a4", SourceID: "src-a", Label: "publish"},
	}
	if err := editorialtrain.WriteManifest(datasetPath, examples, "h", time.Now(), 0, 0); err != nil {
		t.Fatalf("WriteManifest() error = %v", err)
	}
	data, err := os.ReadFile(datasetPath + ".manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m editorialtrain.DatasetManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.TopSource != "src-a" || m.TopSourceShare != 0.75 {
		t.Errorf("TopSource/TopSourceShare = %q/%v, want src-a/0.75", m.TopSource, m.TopSourceShare)
	}
}
