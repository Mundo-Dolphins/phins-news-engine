package entities_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/entities"
)

func TestGenerateCommand_WritesEntityCatalogFromDictionary(t *testing.T) {
	dictPath := "testdata/mini_dictionary.yml"
	outPath := filepath.Join(t.TempDir(), "generated.yml")

	cmd := entities.NewCommand()
	cmd.SetArgs([]string{"generate", "--dictionary", dictPath, "--out", outPath})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Contains(data, []byte("casey-fixture")) {
		t.Errorf("output %s does not contain expected entity id; got:\n%s", outPath, data)
	}
	if !bytes.Contains(data, []byte("DO NOT EDIT")) {
		t.Errorf("output missing generated-file warning header")
	}
}

func TestGenerateCatalog_WritesTheGeneratedFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "generated.yml")
	n, err := entities.GenerateCatalog("testdata/mini_dictionary.yml", out)
	if err != nil || n == 0 {
		t.Fatalf("GenerateCatalog() = %d, %v", n, err)
	}
	if b, _ := os.ReadFile(out); !bytes.Contains(b, []byte("entities:")) {
		t.Fatalf("generated file = %q", b)
	}
	if _, err := entities.GenerateCatalog("testdata/does-not-exist.yml", out); err == nil {
		t.Fatal("a missing dictionary must be an error")
	}
}
