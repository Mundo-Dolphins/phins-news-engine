// Package modelpromotion orchestrates atomically promoting a candidate
// editorial-classifier model to production — extracted from the
// `promote-model` CLI command so a later phase's orchestrator can call
// the identical logic, plus a new gate pre-check `promote-model` uses to
// refuse a failing candidate without --force.
package modelpromotion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/app/modeltraining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/training"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// PromotionCriteria is the promotion-specific quality bar evaluated in addition to editorialtrain.GateConfig — see
// evaluatePromotionCriteria. Mirrors config.PromotionConfig one-to-one;
// the cmd layer converts between them (see EditorialGatesConfig's doc
// comment for why this package cannot import infrastructure/config).
type PromotionCriteria struct {
	MinPrecision     float64
	MaxPrecisionDrop float64
	MinF1Improvement float64
}

// evaluatePromotionCriteria checks candidate against cfg, using promoted
// (nil for the very first promotion — the relative checks are then
// skipped, only MinPrecision applies) as the production baseline. Every
// failing criterion produces one specific, human-readable string.
func evaluatePromotionCriteria(candidate editorialtrain.Report, promoted *editorialtrain.Report, cfg PromotionCriteria) (bool, []string) {
	var failures []string
	if p := candidate.Overall.Precision(); p < cfg.MinPrecision {
		failures = append(failures, fmt.Sprintf("promotion: precision %.3f < min %.3f", p, cfg.MinPrecision))
	}
	if promoted != nil {
		promotedPrecision, candidatePrecision := promoted.Overall.Precision(), candidate.Overall.Precision()
		if drop := promotedPrecision - candidatePrecision; drop > cfg.MaxPrecisionDrop {
			failures = append(failures, fmt.Sprintf(
				"promotion: precision drop %.3f > max %.3f (promoted %.3f, candidate %.3f)",
				drop, cfg.MaxPrecisionDrop, promotedPrecision, candidatePrecision))
		}
		promotedF1, candidateF1 := promoted.Overall.F1(), candidate.Overall.F1()
		if improvement := candidateF1 - promotedF1; improvement < cfg.MinF1Improvement {
			failures = append(failures, fmt.Sprintf(
				"promotion: F1 improvement %.3f < min %.3f (promoted %.3f, candidate %.3f)",
				improvement, cfg.MinF1Improvement, promotedF1, candidateF1))
		}
	}
	return len(failures) == 0, failures
}

// CheckGates evaluates candidateDir's own Test report (parsed from its
// metrics.json) against both gateCfg (editorial_gates — also used by the
// editorial canary rollout) and promotionCfg (offline_training.promotion
// settings), using promoted (possibly nil — see
// modeltraining.LoadPromotedReport) as the regression/baseline. A
// candidate must clear both sets of criteria; failing either produces a
// failure string in the combined result.
func CheckGates(candidateDir string, gateCfg editorialtrain.GateConfig, promotionCfg PromotionCriteria, promoted *editorialtrain.Report) (editorialtrain.GateResult, error) {
	_, _, metricsJSON, err := editorialmodel.Inspect(candidateDir)
	if err != nil {
		return editorialtrain.GateResult{}, err
	}
	var parsed struct {
		Validate editorialtrain.Report
		Test     editorialtrain.Report
		Promoted *editorialtrain.Report
	}
	if err := json.Unmarshal([]byte(metricsJSON), &parsed); err != nil {
		return editorialtrain.GateResult{}, fmt.Errorf("modelpromotion: parse candidate metrics in %s: %w", candidateDir, err)
	}
	if parsed.Promoted != nil {
		promoted = parsed.Promoted
	}
	size, err := modeltraining.DirSize(candidateDir)
	if err != nil {
		return editorialtrain.GateResult{}, err
	}
	gateResult := editorialtrain.EvaluateGates(parsed.Test, promoted, size, gateCfg)
	promotionPassed, promotionFailures := evaluatePromotionCriteria(parsed.Test, promoted, promotionCfg)
	return editorialtrain.GateResult{
		Passed:   gateResult.Passed && promotionPassed,
		Failures: append(gateResult.Failures, promotionFailures...),
	}, nil
}

// Deps are the ports Promote writes through.
type Deps struct {
	Registry out.ModelRegistryRepository
	UOW      out.UnitOfWork
}

// Promote atomically promotes candidateDir to modelPath: Inspect+Load the
// candidate, assign version (or keep the candidate's own manifest
// version if version == ""), compute an artifact hash, write the
// promoted artifact to modelPath (editorialmodel.Save — disk write
// happens before the registry transaction, matching the two-step
// atomicity design), then inside one UOW.Do: retire every currently-
// "promoted" model_registry row and register the new one. Promote itself
// enforces no gates — it is a primitive callers (the CLI, this task; a
// future orchestrator) build a gate-checked confirmation flow on top of,
// via CheckGates.
func Promote(ctx context.Context, deps Deps, candidateDir, version, modelPath string) error {
	manifest, schema, metricsJSON, err := editorialmodel.Inspect(candidateDir)
	if err != nil {
		return err
	}
	model, err := editorialmodel.Load(candidateDir)
	if err != nil {
		return err
	}

	v := version
	if v == "" {
		v = manifest.Version
	}
	model.Version = v
	manifest.ID = v
	manifest.Version = v
	manifest.Status = "promoted"

	artifactData, err := json.Marshal(struct {
		Bias    float64
		Weights map[string]float64
	}{model.Bias, model.Weights})
	if err != nil {
		return fmt.Errorf("modelpromotion: marshal artifact for hashing: %w", err)
	}
	sum := sha256.Sum256(artifactData)
	manifest.ArtifactHash = hex.EncodeToString(sum[:])

	if modelPath == "" {
		return fmt.Errorf("modelpromotion: model path is not configured")
	}

	previousPath := filepath.Join(filepath.Dir(modelPath), "previous")
	if err := editorialmodel.MoveAside(modelPath, previousPath); err != nil {
		return fmt.Errorf("modelpromotion: preserve previous artifact: %w", err)
	}

	if err := editorialmodel.Save(modelPath, model, manifest, metricsJSON, schema); err != nil {
		return fmt.Errorf("modelpromotion: write promoted artifact: %w", err)
	}

	now := time.Now().UTC()
	err = deps.UOW.Do(ctx, func(txCtx context.Context) error {
		currentlyPromoted, err := deps.Registry.ListByStatus(txCtx, "promoted")
		if err != nil {
			return err
		}
		for _, p := range currentlyPromoted {
			p.Status = "retired"
			if err := deps.Registry.Register(txCtx, p); err != nil {
				return err
			}
		}
		return deps.Registry.Register(txCtx, training.ModelMetadata{
			Version: v, Kind: "editorial-classifier", Path: modelPath,
			Algorithm: model.Algorithm, FeatureSchemaVersion: model.FeatureSchemaVersion,
			DatasetHash:  manifest.DatasetHash,
			Thresholds:   fmt.Sprintf(`{"reject":%v,"accept":%v}`, model.Thresholds.Reject, model.Thresholds.Accept),
			ArtifactHash: manifest.ArtifactHash, Status: "promoted",
			TrainedAt: manifest.TrainedAt, PromotedAt: now, Metrics: metricsJSON,
		})
	})
	if err != nil {
		return fmt.Errorf("modelpromotion: registry transaction: %w", err)
	}
	return nil
}

// Rollback swaps modelPath (current) with its previous/ sibling slot —
// the inverse of Promote's own preservation step — and re-registers the
// restored version as "promoted" in model_registry (retiring whatever
// was promoted before the swap) inside one UOW.Do. Returns an error if
// previous/ does not exist: the normal state right after the very first
// promotion, not corruption, but there is genuinely nothing to roll
// back to.
func Rollback(ctx context.Context, deps Deps, modelPath string) error {
	previousPath := filepath.Join(filepath.Dir(modelPath), "previous")
	if _, err := os.Lstat(previousPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("modelpromotion: no previous model at %s to roll back to", previousPath)
		}
		return fmt.Errorf("modelpromotion: stat %s: %w", previousPath, err)
	}

	manifest, _, metricsJSON, err := editorialmodel.Inspect(previousPath)
	if err != nil {
		return err
	}

	swapTmp := modelPath + ".rollback-tmp"
	if _, err := os.Lstat(swapTmp); err == nil {
		return fmt.Errorf("modelpromotion: %s already exists from an interrupted rollback; inspect and remove it before retrying", swapTmp)
	}
	if err := editorialmodel.MoveAside(modelPath, swapTmp); err != nil {
		return fmt.Errorf("modelpromotion: set aside current artifact: %w", err)
	}
	if err := editorialmodel.MoveAside(previousPath, modelPath); err != nil {
		return fmt.Errorf("modelpromotion: restore previous artifact: %w", err)
	}
	if err := editorialmodel.MoveAside(swapTmp, previousPath); err != nil {
		return fmt.Errorf("modelpromotion: relocate outgoing artifact to previous slot: %w", err)
	}

	model, err := editorialmodel.Load(modelPath)
	if err != nil {
		return fmt.Errorf("modelpromotion: rolled-back artifact failed integrity check: %w", err)
	}

	now := time.Now().UTC()
	err = deps.UOW.Do(ctx, func(txCtx context.Context) error {
		currentlyPromoted, err := deps.Registry.ListByStatus(txCtx, "promoted")
		if err != nil {
			return err
		}
		for _, p := range currentlyPromoted {
			p.Status = "retired"
			if err := deps.Registry.Register(txCtx, p); err != nil {
				return err
			}
		}
		return deps.Registry.Register(txCtx, training.ModelMetadata{
			Version: manifest.Version, Kind: "editorial-classifier", Path: modelPath,
			Algorithm: model.Algorithm, FeatureSchemaVersion: model.FeatureSchemaVersion,
			DatasetHash:  manifest.DatasetHash,
			Thresholds:   fmt.Sprintf(`{"reject":%v,"accept":%v}`, model.Thresholds.Reject, model.Thresholds.Accept),
			ArtifactHash: manifest.ArtifactHash, Status: "promoted",
			TrainedAt: manifest.TrainedAt, PromotedAt: now, Metrics: metricsJSON,
		})
	})
	if err != nil {
		return fmt.Errorf("modelpromotion: registry transaction: %w", err)
	}
	return nil
}
