package editorialmodel_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

func sampleModel() editorial.Model {
	return editorial.Model{
		Version: "v1", Algorithm: "linear_svm", FeatureSchemaVersion: "fs-1",
		Bias: 0.5, Weights: map[string]float64{"uni:isotopes": 1.2, "uni:sponsored": -0.8},
		Thresholds: editorial.Thresholds{Reject: 20, Accept: 80},
	}
}

func sampleManifest() editorialmodel.Manifest {
	return editorialmodel.Manifest{
		ID: "m1", Version: "v1", Algorithm: "linear_svm", FeatureSchemaVersion: "fs-1",
		TrainedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DatasetHash: "dh1",
		MetricsSummary: `{"f1":0.85}`, Thresholds: editorial.Thresholds{Reject: 20, Accept: 80},
		Config: `{"seed":42}`, Status: "promoted",
		ArtifactHash: artifactHash(sampleModel()),
	}
}

// artifactHash recomputes the real artifact_hash formula modelpromotion.Promote
// uses when it stamps manifest.json (app/modelpromotion:
// sha256 of json.Marshal({Bias, Weights}) with that exact anonymous struct
// shape/field order), so tests exercise Validate's genuine checksum
// comparison instead of a hardcoded placeholder that would never actually
// be checked.
func artifactHash(m editorial.Model) string {
	data, err := json.Marshal(struct {
		Bias    float64
		Weights map[string]float64
	}{m.Bias, m.Weights})
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "current")
	m := sampleModel()
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}

	if err := editorialmodel.Save(dir, m, sampleManifest(), `{"f1":0.85}`, schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := editorialmodel.Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != m.Version || got.Algorithm != m.Algorithm || got.Bias != m.Bias {
		t.Errorf("Load() = %+v, want round-trip of %+v", got, m)
	}
	if got.Weights["uni:isotopes"] != 1.2 || got.Weights["uni:sponsored"] != -0.8 {
		t.Errorf("Weights did not round-trip: %v", got.Weights)
	}
	if got.Thresholds != m.Thresholds {
		t.Errorf("Thresholds = %+v, want %+v", got.Thresholds, m.Thresholds)
	}
}

func TestSave_WritesAllExpectedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "current")
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes"}}
	if err := editorialmodel.Save(dir, sampleModel(), sampleManifest(), `{}`, schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	for _, name := range []string{"model.json", "manifest.json", "metrics.json", "schema.json", "CHECKSUMS.sha256"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected file %s to exist: %v", name, err)
		}
	}
}

func TestSave_ReplacesExistingDirectoryAtomically(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "current")
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"a"}}

	first := sampleModel()
	first.Version = "v1"
	if err := editorialmodel.Save(dir, first, sampleManifest(), `{}`, schema); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	second := sampleModel()
	second.Version = "v2"
	manifest2 := sampleManifest()
	manifest2.Version = "v2"
	if err := editorialmodel.Save(dir, second, manifest2, `{}`, schema); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	got, err := editorialmodel.Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != "v2" {
		t.Errorf("Version = %q after second Save(), want %q (should fully replace, not merge)", got.Version, "v2")
	}
}

func TestSave_LeavesNoBackupDirectoryAfterSuccess(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "current")
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"a"}}

	first := sampleModel()
	first.Version = "v1"
	if err := editorialmodel.Save(dir, first, sampleManifest(), `{}`, schema); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	second := sampleModel()
	second.Version = "v2"
	manifest2 := sampleManifest()
	manifest2.Version = "v2"
	if err := editorialmodel.Save(dir, second, manifest2, `{}`, schema); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", parent, err)
	}
	for _, e := range entries {
		if e.Name() != "current" {
			t.Errorf("unexpected sibling entry %q left behind in %s after successful Save() (expected only %q)", e.Name(), parent, "current")
		}
	}

	got, err := editorialmodel.Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != "v2" {
		t.Errorf("Version = %q after second Save(), want %q", got.Version, "v2")
	}
}

func TestLoad_CorruptChecksumFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "current")
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"a"}}
	if err := editorialmodel.Save(dir, sampleModel(), sampleManifest(), `{}`, schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Corrupt model.json after the fact — CHECKSUMS.sha256 now disagrees.
	if err := os.WriteFile(filepath.Join(dir, "model.json"), []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatalf("corrupt model.json: %v", err)
	}

	_, err := editorialmodel.Load(dir)
	if err == nil {
		t.Fatal("Load() error = nil, want a checksum-mismatch error for a corrupted model.json")
	}
}

func TestLoad_MissingDirectoryFails(t *testing.T) {
	_, err := editorialmodel.Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("Load() error = nil, want an error for a missing directory")
	}
}

func TestInspect_ReturnsManifestSchemaAndMetrics(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "current")
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes"}}
	if err := editorialmodel.Save(dir, sampleModel(), sampleManifest(), `{"f1":0.85}`, schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	manifest, gotSchema, metricsJSON, err := editorialmodel.Inspect(dir)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if manifest.Version != "v1" || manifest.Algorithm != "linear_svm" {
		t.Errorf("Manifest = %+v, unexpected", manifest)
	}
	if len(gotSchema.Features) != 1 || gotSchema.Features[0] != "uni:isotopes" {
		t.Errorf("FeatureSchema = %+v, unexpected", gotSchema)
	}
	if metricsJSON != `{"f1":0.85}` {
		t.Errorf("metricsJSON = %q, want %q", metricsJSON, `{"f1":0.85}`)
	}
}

func TestInspect_CorruptManifestFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "current")
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"a"}}
	if err := editorialmodel.Save(dir, sampleModel(), sampleManifest(), `{}`, schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	// Load() (the runtime path) doesn't check manifest.json, so corrupting
	// it must still succeed for Load but fail for Inspect.
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatalf("corrupt manifest.json: %v", err)
	}

	if _, err := editorialmodel.Load(dir); err != nil {
		t.Errorf("Load() error = %v, want nil — Load only checks model.json, not manifest.json", err)
	}
	if _, _, _, err := editorialmodel.Inspect(dir); err == nil {
		t.Error("Inspect() error = nil, want a checksum-mismatch error for a corrupted manifest.json")
	}
}

func TestMoveAside_MovesExistingDirAndReplacesDest(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "current")
	dest := filepath.Join(base, "previous")
	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "marker.txt"), []byte("src-content"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	// dest already exists with different, stale content — MoveAside must
	// replace it entirely (single fixed slot, not a history).
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "stale.txt"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	if err := editorialmodel.MoveAside(src, dest); err != nil {
		t.Fatalf("MoveAside() error = %v", err)
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("src %s still exists after MoveAside, want gone", src)
	}
	data, err := os.ReadFile(filepath.Join(dest, "marker.txt"))
	if err != nil || string(data) != "src-content" {
		t.Errorf("dest marker.txt = %q, err=%v, want %q", data, err, "src-content")
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.txt")); !os.IsNotExist(err) {
		t.Error("stale.txt from the old dest still exists, want fully replaced")
	}
}

func TestValidate_ChecksumMismatchFails(t *testing.T) {
	m := sampleModel()
	manifest := sampleManifest()
	manifest.ArtifactHash = "0000000000000000000000000000000000000000000000000000000000000000"
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}
	err := editorialmodel.Validate(m, manifest, schema)
	if err == nil {
		t.Fatal("Validate() error = nil, want checksum mismatch error")
	}
}

func TestValidate_InvertedThresholdsFails(t *testing.T) {
	m := sampleModel()
	m.Thresholds = editorial.Thresholds{Reject: 90, Accept: 10} // inverted
	manifest := sampleManifest()
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}
	err := editorialmodel.Validate(m, manifest, schema)
	if err == nil {
		t.Fatal("Validate() error = nil, want error for Reject >= Accept")
	}
}

func TestValidate_EmptyFeatureSchemaFails(t *testing.T) {
	m := sampleModel()
	manifest := sampleManifest()
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: nil}
	err := editorialmodel.Validate(m, manifest, schema)
	if err == nil {
		t.Fatal("Validate() error = nil, want error for empty feature schema/vocabulary")
	}
}

func TestValidate_FeatureSchemaVersionMismatchFails(t *testing.T) {
	m := sampleModel() // FeatureSchemaVersion: "fs-1"
	manifest := sampleManifest()
	schema := editorialmodel.FeatureSchema{Version: "fs-2", Features: []string{"uni:isotopes"}} // mismatched
	err := editorialmodel.Validate(m, manifest, schema)
	if err == nil {
		t.Fatal("Validate() error = nil, want error for model/schema feature_schema_version mismatch")
	}
}

func TestValidate_EmptyVersionFails(t *testing.T) {
	m := sampleModel()
	m.Version = ""
	manifest := sampleManifest()
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}
	err := editorialmodel.Validate(m, manifest, schema)
	if err == nil {
		t.Fatal("Validate() error = nil, want error for empty model version")
	}
}

func TestValidate_EmptyWeightsFails(t *testing.T) {
	m := sampleModel()
	m.Weights = nil
	manifest := sampleManifest()
	manifest.ArtifactHash = artifactHash(m)
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}
	err := editorialmodel.Validate(m, manifest, schema)
	if err == nil {
		t.Fatal("Validate() error = nil, want error for empty model weights")
	}
}

func TestValidate_WellFormedModelPasses(t *testing.T) {
	m := sampleModel()
	manifest := sampleManifest()
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}
	if err := editorialmodel.Validate(m, manifest, schema); err != nil {
		t.Errorf("Validate() error = %v, want nil for a well-formed model", err)
	}
}

func TestMoveAside_NoOpWhenSrcDoesNotExist(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "does-not-exist")
	dest := filepath.Join(base, "previous")
	if err := editorialmodel.MoveAside(src, dest); err != nil {
		t.Fatalf("MoveAside() error = %v, want nil (no-op when src is absent)", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("dest was created even though src did not exist")
	}
}

func TestMoveAside_RestoresDestBackupOnSuccess(t *testing.T) {
	// Verify that when dest already exists with content, it is preserved
	// in a backup file (.moveaside-old) during the move, and cleaned up
	// after a successful move.
	base := t.TempDir()
	src := filepath.Join(base, "current")
	dest := filepath.Join(base, "previous")
	backup := dest + ".moveaside-old"

	if err := os.MkdirAll(src, 0o750); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "new.txt"), []byte("new-content"), 0o600); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "old.txt"), []byte("old-content"), 0o600); err != nil {
		t.Fatalf("write old file: %v", err)
	}

	if err := editorialmodel.MoveAside(src, dest); err != nil {
		t.Fatalf("MoveAside() error = %v", err)
	}

	// After successful move:
	// - dest should have src's content
	data, err := os.ReadFile(filepath.Join(dest, "new.txt"))
	if err != nil || string(data) != "new-content" {
		t.Errorf("dest new.txt = %q, err=%v, want %q", data, err, "new-content")
	}

	// - src should be gone
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("src still exists, should have been moved")
	}

	// - backup should have been cleaned up (best-effort)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("backup still exists, should have been cleaned up after successful move")
	}
}

func TestSaveLoad_RoundTripsBinaryFeatures(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "current")
	m := sampleModel()
	m.BinaryFeatures = true
	schema := editorialmodel.FeatureSchema{Version: "fs-1", Features: []string{"uni:isotopes", "uni:sponsored"}}
	if err := editorialmodel.Save(dir, m, sampleManifest(), `{}`, schema); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := editorialmodel.Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !got.BinaryFeatures {
		t.Error("BinaryFeatures did not round-trip; inference would score counts for a presence-trained model")
	}
}
