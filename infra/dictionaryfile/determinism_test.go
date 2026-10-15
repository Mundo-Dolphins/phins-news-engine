package dictionaryfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func TestSave_IsDeterministicAcrossRepeatedLoadSaveCycles(t *testing.T) {
	dictPath := repofs.ExampleConfig(t, "example_dictionary.yml")

	d1, err := dictionaryfile.Load(dictPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.yml")
	pathB := filepath.Join(dir, "b.yml")

	if err := dictionaryfile.Save(pathA, d1); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	d2, err := dictionaryfile.Load(pathA)
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	if err := dictionaryfile.Save(pathB, d2); err != nil {
		t.Fatalf("Save B: %v", err)
	}

	bytesA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	bytesB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if string(bytesA) != string(bytesB) {
		t.Fatalf("Save is not deterministic across a load/save/load/save cycle — the example dictionary file's Categories/Entries ordering or field encoding is not stable")
	}
}
