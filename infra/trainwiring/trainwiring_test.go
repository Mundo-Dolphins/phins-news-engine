package trainwiring_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/trainwiring"
)

func TestGateConfigMapsAllFields(t *testing.T) {
	cfg := &config.Config{EditorialGates: config.EditorialGatesConfig{
		MinPrecision: 0.11, MaxRecallRegression: 0.12, MinRegressionBaselineExamples: 13,
		MinPRAUC: 0.14, MaxFalsePositiveRate: 0.15, MinPerSourcePrecision: 0.16,
		MinSourceExamples: 17, ExcludedSources: []string{"a", "b"},
		MaxArtifactSizeBytes: 18, MaxInferenceNanos: 19,
	}}
	got := trainwiring.GateConfig(cfg)
	want := reflect.ValueOf(cfg.EditorialGates)
	gv := reflect.ValueOf(got)
	if gv.NumField() != want.NumField() {
		t.Fatalf("GateConfig has %d fields, EditorialGatesConfig %d", gv.NumField(), want.NumField())
	}
	for i := 0; i < want.NumField(); i++ {
		name := want.Type().Field(i).Name
		if want.Field(i).IsZero() {
			t.Fatalf("test fixture leaves %s zero", name)
		}
		if g := gv.FieldByName(name); !g.IsValid() || !reflect.DeepEqual(g.Interface(), want.Field(i).Interface()) {
			t.Errorf("field %s = %v, want %v", name, g, want.Field(i))
		}
	}
}

func TestRunOptionsForcesNaiveBayes(t *testing.T) {
	cfg := &config.Config{}
	cfg.Model.Path = "models/current"
	cfg.Classification.TrustedSourceIDs = []string{"s1"}
	cfg.OfflineTraining.LLM.AutoLabelThreshold = 0.8
	now := time.Unix(100, 7)
	opts, err := trainwiring.RunOptions(cfg, trainwiring.RunFlags{DatasetPath: "d.yml", CandidatesDir: "c", MaxPerCluster: 3, Force: true, Auto: true}, now)
	if err != nil {
		t.Fatalf("RunOptions() error = %v", err)
	}
	if !reflect.DeepEqual(opts.TrainCfg.Algorithms, []string{"naive_bayes"}) {
		t.Errorf("Algorithms = %v, want [naive_bayes]", opts.TrainCfg.Algorithms)
	}
	if want := "fs-100000000007"; opts.TrainCfg.SchemaVersion != want {
		t.Errorf("SchemaVersion = %q, want %q", opts.TrainCfg.SchemaVersion, want)
	}
	if opts.DatasetPath != "d.yml" || opts.CandidatesDir != "c" || opts.MaxPerCluster != 3 || !opts.Force || !opts.Auto {
		t.Errorf("flags not carried: %+v", opts)
	}
	if opts.ModelPath != "models/current" || !opts.TrustedSourceIDs["s1"] || opts.AutoLabelThreshold != 0.8 {
		t.Errorf("config not carried: %+v", opts)
	}
}

func writeCandidate(t *testing.T, algorithm, metricsJSON string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "candidate")
	m := editorial.Model{Version: "v1", Algorithm: algorithm, FeatureSchemaVersion: "fs-1", Weights: map[string]float64{"uni:x": 1}, Thresholds: editorial.Thresholds{Reject: 20, Accept: 80}}
	manifest := editorialmodel.Manifest{ID: "v1", Version: "v1", Algorithm: algorithm, Status: "candidate"}
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:x"}}
	if err := editorialmodel.Save(dir, m, manifest, metricsJSON, schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return dir
}

func modelPathCfg(t *testing.T) (*config.Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models", "current")
	cfg := &config.Config{}
	cfg.Model.Path = path
	cfg.EditorialGates.MinPrecision = 0.9
	cfg.EditorialGates.MaxArtifactSizeBytes = 1 << 20
	cfg.EditorialGates.MaxInferenceNanos = 1 << 30
	return cfg, path
}

func TestPromoteCandidateRejectsNonNB(t *testing.T) {
	cfg, path := modelPathCfg(t)
	dir := writeCandidate(t, "logistic_regression", "{}")
	_, err := trainwiring.PromoteCandidate(context.Background(), nil, cfg, dir, "", false)
	if !errors.Is(err, trainwiring.ErrNotNaiveBayes) {
		t.Fatalf("error = %v, want ErrNotNaiveBayes", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("model path touched: %v", statErr)
	}
}

func TestPromoteCandidateGateFailureWithoutForce(t *testing.T) {
	cfg, path := modelPathCfg(t)
	dir := writeCandidate(t, "naive_bayes", "{}") // zero precision fails MinPrecision
	report, err := trainwiring.PromoteCandidate(context.Background(), nil, cfg, dir, "", false)
	if !errors.Is(err, trainwiring.ErrGatesFailed) {
		t.Fatalf("error = %v, want ErrGatesFailed", err)
	}
	if len(report.GateFailures) == 0 || report.Forced {
		t.Errorf("report = %+v, want gate failures and Forced=false", report)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("model path touched: %v", statErr)
	}
}
