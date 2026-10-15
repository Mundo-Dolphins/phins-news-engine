// Package modeltraining orchestrates fitting every candidate algorithm,
// saving each to disk, registering it in model_registry, and attaching a
// promotion-gate verdict — extracted from the `train run` CLI command so
// a later phase's orchestrator can call the identical logic.
package modeltraining

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// DirSize sums the size of every regular file directly inside dir
// (non-recursive — matches editorialmodel.Save's flat five-file layout).
func DirSize(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("modeltraining: read dir %s: %w", dir, err)
	}
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return 0, fmt.Errorf("modeltraining: stat %s: %w", filepath.Join(dir, e.Name()), err)
		}
		total += info.Size()
	}
	return total, nil
}

// LoadPromotedReport reads the promoted model's own Test report straight
// from its metrics.json (via editorialmodel.Inspect), never from
// model_registry — a service restart re-registers the promoted row with
// an empty Metrics field (a pre-existing, unrelated issue this phase
// works around rather than fixes), so the DB copy cannot be trusted
// between promotions. Returns nil — never an error — when modelPath is
// empty, the directory doesn't exist, or its metrics.json isn't the
// {Validate, Test Report} shape train run writes: all of these mean
// "there is nothing reliable to compare a new candidate against yet,"
// which the caller (EvaluateGates) already treats as "skip the
// regression check," not a fatal error.
func LoadPromotedReport(modelPath string) *editorialtrain.Report {
	if modelPath == "" {
		return nil
	}
	_, _, metricsJSON, err := editorialmodel.Inspect(modelPath)
	if err != nil {
		return nil
	}
	var parsed struct {
		Validate editorialtrain.Report
		Test     editorialtrain.Report
	}
	if err := json.Unmarshal([]byte(metricsJSON), &parsed); err != nil {
		return nil
	}
	return &parsed.Test
}

// EvaluatePromotedModel scores the currently promoted model on the same
// held-out examples used for a new candidate. Stored metrics from a past
// training corpus are not a valid regression baseline for a refreshed
// dataset or a changed ambiguous policy.
func EvaluatePromotedModel(modelPath string, dataset editorialtrain.Dataset, cfg editorialtrain.TrainConfig) (*editorialtrain.Report, error) {
	if modelPath == "" {
		return nil, nil
	}
	model, err := editorialmodel.Load(modelPath)
	if err != nil {
		// Older test fixtures and deliberately partial local workspaces can
		// carry only the prior metrics artifact. A real promoted model is
		// loaded and re-scored below; if that artifact is absent, preserve
		// the established conservative fallback behavior of the historical
		// report loader.
		return LoadPromotedReport(modelPath), nil
	}
	_, _, test := editorialtrain.SplitForTraining(dataset, cfg)
	report := editorialtrain.Evaluate(model, test.Examples, cfg.VetoCfg, cfg.AmbiguousPolicy)
	return &report, nil
}

// Deps are the ports RunAndRegister writes through.
type Deps struct {
	Registry out.ModelRegistryRepository
}

// Result is one trained-and-saved candidate, with its gate verdict
// attached.
type Result struct {
	Candidate editorialtrain.Candidate
	Version   string
	Dir       string
	Gates     editorialtrain.GateResult
}

// RunAndRegister fits every algorithm dataset+trainCfg requests
// (editorialtrain.Train never picks a winner), saves each
// candidate under outDir, registers it in model_registry with status
// "candidate", and evaluates its promotion gates against promoted (which
// may be nil — see LoadPromotedReport). Gate failure never blocks saving
// or registering a candidate: gates only affect whether a human can
// later promote-model it, never whether training records it.
func RunAndRegister(
	ctx context.Context, deps Deps, dataset editorialtrain.Dataset, trainCfg editorialtrain.TrainConfig,
	outDir string, gateCfg editorialtrain.GateConfig, promoted *editorialtrain.Report,
) ([]Result, error) {
	candidates, err := editorialtrain.Train(dataset, trainCfg)
	if err != nil {
		return nil, fmt.Errorf("modeltraining: train: %w", err)
	}

	configJSON, err := json.Marshal(struct {
		Seed           int64   `json:"seed"`
		BinaryFeatures bool    `json:"binary_features"`
		SilverWeight   float64 `json:"silver_weight"`
	}{trainCfg.Seed, trainCfg.BinaryFeatures, trainCfg.SilverWeight})
	if err != nil {
		return nil, fmt.Errorf("modeltraining: marshal training config: %w", err)
	}

	results := make([]Result, 0, len(candidates))
	for _, c := range candidates {
		version := fmt.Sprintf("%s-candidate-%d", c.Model.Algorithm, time.Now().UTC().UnixNano())
		model := c.Model
		model.Version = version

		metricsJSON, err := json.MarshalIndent(struct {
			Validate editorialtrain.Report
			Test     editorialtrain.Report
			Promoted *editorialtrain.Report
		}{c.ValidateReport, c.TestReport, promoted}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("modeltraining: marshal metrics for %s: %w", c.Model.Algorithm, err)
		}

		candidateDir := filepath.Join(outDir, c.Model.Algorithm)
		manifest := editorialmodel.Manifest{
			ID: version, Version: version, Algorithm: c.Model.Algorithm,
			FeatureSchemaVersion: c.Schema.Version, TrainedAt: time.Now().UTC(),
			DatasetHash: c.DatasetHash, MetricsSummary: string(metricsJSON),
			Thresholds: c.Model.Thresholds, Config: string(configJSON), Status: "candidate",
		}
		if err := editorialmodel.Save(candidateDir, model, manifest, string(metricsJSON),
			editorialmodel.FeatureSchema{Version: c.Schema.Version, Features: c.Schema.Features}); err != nil {
			return nil, fmt.Errorf("modeltraining: save candidate %s: %w", c.Model.Algorithm, err)
		}

		if err := deps.Registry.Register(ctx, training.ModelMetadata{
			Version: version, Kind: "editorial-classifier", Path: candidateDir,
			Algorithm: c.Model.Algorithm, FeatureSchemaVersion: c.Schema.Version,
			DatasetHash: c.DatasetHash, Status: "candidate", TrainedAt: time.Now().UTC(),
			Config: string(configJSON), Metrics: string(metricsJSON),
		}); err != nil {
			return nil, fmt.Errorf("modeltraining: register candidate %s: %w", c.Model.Algorithm, err)
		}

		artifactSize, err := DirSize(candidateDir)
		if err != nil {
			return nil, fmt.Errorf("modeltraining: compute artifact size for %s: %w", c.Model.Algorithm, err)
		}
		gates := editorialtrain.EvaluateGates(c.TestReport, promoted, artifactSize, gateCfg)

		results = append(results, Result{Candidate: c, Version: version, Dir: candidateDir, Gates: gates})
	}
	return results, nil
}

// SelectWinner picks the gate-passing candidate with the highest Test
// PR-AUC. ok is false when no candidate passed its gates — this is not
// an error condition, just "nothing to propose."
func SelectWinner(results []Result) (Result, bool) {
	var winner Result
	found := false
	for _, r := range results {
		if r.Candidate.Model.Algorithm == "trivial_majority" {
			continue // comparison-only floor, never an eligible winner — see editorialtrain.TrivialTrainer's doc comment
		}
		if !r.Gates.Passed {
			continue
		}
		if !found || r.Candidate.TestReport.PRAUC > winner.Candidate.TestReport.PRAUC {
			winner = r
			found = true
		}
	}
	return winner, found
}
