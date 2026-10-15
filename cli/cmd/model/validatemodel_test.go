package model_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

func writeSampleCandidateModel(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Bias: 0, Weights: map[string]float64{"uni:x": 1}, Thresholds: editorial.Thresholds{Reject: 20, Accept: 80}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(dir, m, manifest, `{"f1":0.5}`, schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return dir
}

func TestValidateModelCmd_ValidDirectorySucceeds(t *testing.T) {
	dir := writeSampleCandidateModel(t)
	out, err := testsupport.Execute(t, "validate-model", dir)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "naive_bayes") {
		t.Errorf("output = %q, want it to mention the algorithm", out)
	}
}

func TestValidateModelCmd_MissingDirectoryFails(t *testing.T) {
	if _, err := testsupport.Execute(t, "validate-model", filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("Execute() error = nil, want an error for a missing directory")
	}
}
