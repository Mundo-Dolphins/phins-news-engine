package train_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func TestPromoteModelCmd_WritesToConfiguredPathAndUpdatesRegistry(t *testing.T) {
	configPath := testsupport.WriteFixture(t) // model.path is "" in the base fixture; override below
	dir := filepath.Dir(configPath)
	promotedPath := filepath.Join(dir, "models", "editorial", "current")

	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, promotedPath)

	candidateDir := testsupport.WriteGatePassingCandidateModel(t)

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "promote", candidateDir, "--version", "v1.0.0")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "v1.0.0") {
		t.Errorf("output = %q, want it to mention the promoted version", out)
	}

	m, err := editorialmodel.Load(promotedPath)
	if err != nil {
		t.Fatalf("editorialmodel.Load(%s) error = %v", promotedPath, err)
	}
	if m.Version != "v1.0.0" {
		t.Errorf("promoted model Version = %q, want %q", m.Version, "v1.0.0")
	}

	manifest, _, _, err := editorialmodel.Inspect(promotedPath)
	if err != nil {
		t.Fatalf("editorialmodel.Inspect(%s) error = %v", promotedPath, err)
	}
	// manifest.ID must be rewritten to match the new Version on promotion
	// (the same convention train.go uses when a candidate is first
	// created: ID and Version are set to the same value) — otherwise a
	// promoted artifact's manifest.json is left with the candidate's
	// stale "-candidate-<timestamp>" ID alongside status "promoted".
	if manifest.ID != "v1.0.0" {
		t.Errorf("promoted manifest ID = %q, want %q (must match Version, not the original candidate ID)", manifest.ID, "v1.0.0")
	}

	dbPath := filepath.Join(dir, "operational.db")
	db, err := infrasqlite.Open(dbPath, 5000)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	registry := sqliteadapter.NewModelRegistryRepository(db)

	promoted, err := registry.ListByStatus(context.Background(), "promoted")
	if err != nil {
		t.Fatalf("ListByStatus(promoted) error = %v", err)
	}
	if len(promoted) != 1 || promoted[0].Version != "v1.0.0" {
		t.Errorf("promoted = %+v, want exactly one row for v1.0.0", promoted)
	}

	retired, err := registry.ListByStatus(context.Background(), "retired")
	if err != nil {
		t.Fatalf("ListByStatus(retired) error = %v", err)
	}
	foundPlaceholder := false
	for _, r := range retired {
		if r.Version == "placeholder-v0" {
			foundPlaceholder = true
		}
	}
	if !foundPlaceholder {
		t.Errorf("retired = %+v, want placeholder-v0 (the migration-seeded promoted row) demoted to retired", retired)
	}
}

func TestPromoteModelCmd_MissingCandidateDirFails(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	if _, err := testsupport.Execute(t, "--config", configPath, "train", "promote", filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("Execute() error = nil, want an error for a missing candidate directory")
	}
}

func TestPromoteModelCmd_RejectsNonNaiveBayesCandidate(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	promotedPath := filepath.Join(dir, "models", "editorial", "current")
	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, promotedPath)

	candidateDir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "logistic_regression", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "logistic_regression", Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(candidateDir, m, manifest, testsupport.PassingGateReportJSON(), schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	_, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "promote", candidateDir)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-Naive-Bayes candidates to be rejected")
	}
	if !strings.Contains(err.Error(), "must remain naive_bayes") {
		t.Errorf("Execute() error = %q, want it to explain the Naive Bayes production invariant", err)
	}
	if _, loadErr := editorialmodel.Load(promotedPath); loadErr == nil {
		t.Error("editorialmodel.Load(promotedPath) succeeded, want the promoted path untouched")
	}
}

func TestPromoteModelCmd_FailingGatesBlockPromotionWithoutForce(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	promotedPath := filepath.Join(dir, "models", "editorial", "current")
	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, promotedPath)

	failingReport := editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 99, FN: 5, TN: 900}, PRAUC: 0.1}
	data, err := json.Marshal(struct {
		Validate editorialtrain.Report
		Test     editorialtrain.Report
	}{failingReport, failingReport})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	candidateDir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(candidateDir, m, manifest, string(data), schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "promote", candidateDir, "--version", "v1.0.0")
	if err == nil {
		t.Fatal("Execute() error = nil, want an error when the candidate fails gates without --force")
	}
	if !strings.Contains(out, "precision") {
		t.Errorf("output = %q, want it to explain the failing precision gate", out)
	}

	if _, loadErr := editorialmodel.Load(promotedPath); loadErr == nil {
		t.Error("editorialmodel.Load(promotedPath) succeeded, want the promoted path untouched when promotion was blocked")
	}
}

func TestPromoteModelCmd_ForceOverridesFailingGates(t *testing.T) {
	configPath := testsupport.WriteFixture(t)
	dir := filepath.Dir(configPath)
	promotedPath := filepath.Join(dir, "models", "editorial", "current")
	overriddenConfig := filepath.Join(dir, "settings-with-model-path.yml")
	testsupport.RewriteFixtureModelPath(t, configPath, overriddenConfig, promotedPath)

	failingReport := editorialtrain.Report{Overall: editorialtrain.BinaryMetrics{TP: 1, FP: 99, FN: 5, TN: 900}, PRAUC: 0.1}
	data, err := json.Marshal(struct {
		Validate editorialtrain.Report
		Test     editorialtrain.Report
	}{failingReport, failingReport})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	candidateDir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: "naive_bayes", FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: "naive_bayes", Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(candidateDir, m, manifest, string(data), schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	out, err := testsupport.Execute(t, "--config", overriddenConfig, "train", "promote", candidateDir, "--version", "v1.0.0", "--force")
	if err != nil {
		t.Fatalf("Execute() error = %v, want --force to override failing gates", err)
	}
	if !strings.Contains(out, "force") {
		t.Errorf("output = %q, want a visible warning that --force bypassed gate failures", out)
	}

	if _, loadErr := editorialmodel.Load(promotedPath); loadErr != nil {
		t.Errorf("editorialmodel.Load(promotedPath) error = %v, want the promotion to have succeeded", loadErr)
	}
}
