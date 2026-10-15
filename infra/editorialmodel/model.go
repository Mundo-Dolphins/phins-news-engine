// Package editorialmodel loads and atomically saves the git-versioned
// editorial-classifier artifact directory (models/editorial/current/):
// model.json (the linear artifact), manifest.json (registry-shaped
// metadata), metrics.json (the full offline evaluation report),
// schema.json (the pruned feature list), and CHECKSUMS.sha256 (integrity).
// Mirrors infrastructure/dictionaryfile's split between the on-disk
// (JSON-tagged) shape and domain/editorial's pure domain types.
package editorialmodel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorial"
)

// Manifest is the registry-shaped metadata stored alongside a promoted or
// candidate model artifact — the on-disk mirror of training.ModelMetadata's
// new fields (Task 4), kept separate so domain/training never carries JSON
// tags.
type Manifest struct {
	ID                   string               `json:"id"`
	Version              string               `json:"version"`
	Algorithm            string               `json:"algorithm"`
	FeatureSchemaVersion string               `json:"feature_schema_version"`
	TrainedAt            time.Time            `json:"trained_at"`
	DatasetHash          string               `json:"dataset_hash"`
	MetricsSummary       string               `json:"metrics_summary"` // JSON
	Thresholds           editorial.Thresholds `json:"thresholds"`
	Config               string               `json:"config"` // JSON
	ArtifactHash         string               `json:"artifact_hash"`
	Status               string               `json:"status"`
}

// FeatureSchema is the pruned, ordered, versioned feature list a trained
// Model embeds.
type FeatureSchema struct {
	Version  string   `json:"version"`
	Features []string `json:"features"`
}

type modelFile struct {
	Version              string               `json:"version"`
	Algorithm            string               `json:"algorithm"`
	FeatureSchemaVersion string               `json:"feature_schema_version"`
	Bias                 float64              `json:"bias"`
	Weights              map[string]float64   `json:"weights"`
	Thresholds           editorial.Thresholds `json:"thresholds"`
	BinaryFeatures       bool                 `json:"binary_features,omitempty"`
}

// Save atomically writes m/manifest/metricsJSON/schema to dir: it builds
// the full directory in a temporary sibling first, computes
// CHECKSUMS.sha256 over the four content files, then swaps it into place
// using a rename-aside strategy: any existing dir is first os.Rename()d to
// a sibling backup path, the new tmp directory is then os.Rename()d into
// dir, and only once that succeeds is the backup removed. Because each
// swap step is a single POSIX rename (not a delete followed by a rename),
// dir is never partially written or corrupted — a reader either sees the
// old artifact, the new one, or (only in the narrow window between the two
// renames) briefly nothing at that path. A crash in that window leaves dir
// absent but fully recoverable: the old artifact survives at
// dir+".old-<suffix>" and the new one at the temp dir, so no data is ever
// lost, just not yet moved into its final location. If the final rename
// into dir fails outright (not a crash, an observed error), Save attempts
// to restore the backup back to dir before returning the original error.
func Save(dir string, m editorial.Model, manifest Manifest, metricsJSON string, schema FeatureSchema) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("editorialmodel: create parent dir %s: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, ".editorialmodel-tmp-*")
	if err != nil {
		return fmt.Errorf("editorialmodel: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp) // no-op once renamed; cleans up on any early-return error

	mf := modelFile{
		Version: m.Version, Algorithm: m.Algorithm, FeatureSchemaVersion: m.FeatureSchemaVersion,
		Bias: m.Bias, Weights: m.Weights, Thresholds: m.Thresholds, BinaryFeatures: m.BinaryFeatures,
	}

	checksums := map[string]string{}
	for name, v := range map[string]any{
		"model.json": mf, "manifest.json": manifest, "schema.json": schema,
	} {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Errorf("editorialmodel: marshal %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(tmp, name), data, 0o600); err != nil {
			return fmt.Errorf("editorialmodel: write %s: %w", name, err)
		}
		checksums[name] = sha256Hex(data)
	}
	metricsBytes := []byte(metricsJSON)
	if err := os.WriteFile(filepath.Join(tmp, "metrics.json"), metricsBytes, 0o600); err != nil {
		return fmt.Errorf("editorialmodel: write metrics.json: %w", err)
	}
	checksums["metrics.json"] = sha256Hex(metricsBytes)

	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	checksumLines := ""
	for _, name := range names {
		checksumLines += fmt.Sprintf("%s  %s\n", checksums[name], name)
	}
	if err := os.WriteFile(filepath.Join(tmp, "CHECKSUMS.sha256"), []byte(checksumLines), 0o600); err != nil {
		return fmt.Errorf("editorialmodel: write CHECKSUMS.sha256: %w", err)
	}

	// Rename-aside swap: move any existing dir to a sibling backup path
	// first, then rename tmp into dir. Each step is a single POSIX
	// rename, so dir is never partially written or corrupted — only
	// briefly absent (not missing data, just not yet in its final spot)
	// in the narrow window between the two renames. See Save's doc
	// comment for the full recovery story.
	backup := dir + ".old-" + filepath.Base(tmp)
	dirExisted := false
	if _, statErr := os.Lstat(dir); statErr == nil {
		dirExisted = true
		if err := os.Rename(dir, backup); err != nil {
			return fmt.Errorf("editorialmodel: rename existing %s aside to %s: %w", dir, backup, err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("editorialmodel: stat existing %s: %w", dir, statErr)
	}

	if err := os.Rename(tmp, dir); err != nil {
		renameErr := fmt.Errorf("editorialmodel: rename temp dir into %s: %w", dir, err)
		if dirExisted {
			if restoreErr := os.Rename(backup, dir); restoreErr != nil {
				return fmt.Errorf("%w (also failed to restore backup %s to %s: %v)", renameErr, backup, dir, restoreErr)
			}
		}
		return renameErr
	}

	if dirExisted {
		// Best-effort cleanup: the save already succeeded, so a failure
		// here is just an orphaned backup directory, not a correctness
		// problem. Not treated as a Save error.
		_ = os.RemoveAll(backup)
	}
	return nil
}

// MoveAside atomically replaces dest with the contents of src, if src
// exists. If src does not exist, MoveAside is a silent no-op — nothing
// to preserve (e.g. the very first promotion). If dest already exists,
// it is moved aside to a single fixed backup slot before src takes its
// place: a single fixed slot, not a retained history — the same
// convention the deploy rollback and the DB sync already
// use. Used by modelpromotion.Promote to preserve the outgoing
// current/ artifact as models/editorial/previous/ before writing a new
// one, and by modelpromotion.Rollback to swap them back. The rename-aside
// strategy ensures no data is lost even if the final rename fails:
// dest's prior contents either end up at dest or are safely restored
// at dest.moveaside-old; src is either moved to dest or left untouched.
func MoveAside(src, dest string) error {
	if _, err := os.Lstat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("editorialmodel: stat %s: %w", src, err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("editorialmodel: create parent dir for %s: %w", dest, err)
	}

	// Rename-aside strategy: move any existing dest to a sibling backup path
	// first, then rename src into dest. Each step is a single POSIX rename,
	// so dest is never partially written or corrupted — only briefly absent
	// in the narrow window between the two renames. If the second rename fails,
	// we restore the backup so no data is lost.
	backup := dest + ".moveaside-old"
	destExisted := false
	if _, statErr := os.Lstat(dest); statErr == nil {
		destExisted = true
		// Clean up any stale prior backup first
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("editorialmodel: remove stale backup %s: %w", backup, err)
		}
		if err := os.Rename(dest, backup); err != nil {
			return fmt.Errorf("editorialmodel: rename existing %s aside to %s: %w", dest, backup, err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("editorialmodel: stat existing %s: %w", dest, statErr)
	}

	if err := os.Rename(src, dest); err != nil {
		renameErr := fmt.Errorf("editorialmodel: rename %s to %s: %w", src, dest, err)
		if destExisted {
			if restoreErr := os.Rename(backup, dest); restoreErr != nil {
				return fmt.Errorf("%w (also failed to restore backup %s to %s: %v)", renameErr, backup, dest, restoreErr)
			}
		}
		return renameErr
	}

	if destExisted {
		// Best-effort cleanup: the move already succeeded, so a failure
		// here is just an orphaned backup directory, not a correctness
		// problem. Not treated as a MoveAside error.
		_ = os.RemoveAll(backup)
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Load reads model.json from dir and verifies it against the hash recorded
// for it in CHECKSUMS.sha256 before decoding — a corrupted model.json fails
// closed with a wrapped error rather than silently loading bad weights.
// Load only reads CHECKSUMS.sha256 and model.json; it does not open or
// verify manifest.json, schema.json, metrics.json, or any other file in
// dir.
func Load(dir string) (editorial.Model, error) {
	checksumData, err := os.ReadFile(filepath.Join(dir, "CHECKSUMS.sha256")) // #nosec G304 -- dir is an operator-provided config value, not user input
	if err != nil {
		return editorial.Model{}, fmt.Errorf("editorialmodel: read CHECKSUMS.sha256 in %s: %w", dir, err)
	}
	want := parseChecksums(string(checksumData))

	modelData, err := readAndVerify(dir, "model.json", want)
	if err != nil {
		return editorial.Model{}, err
	}
	var mf modelFile
	if err := json.Unmarshal(modelData, &mf); err != nil {
		return editorial.Model{}, fmt.Errorf("editorialmodel: parse model.json in %s: %w", dir, err)
	}

	return editorial.Model{
		Version: mf.Version, Algorithm: mf.Algorithm, FeatureSchemaVersion: mf.FeatureSchemaVersion,
		Bias: mf.Bias, Weights: mf.Weights, Thresholds: mf.Thresholds, BinaryFeatures: mf.BinaryFeatures,
	}, nil
}

func parseChecksums(s string) map[string]string {
	out := map[string]string{}
	lines := splitLines(s)
	for _, line := range lines {
		var hash, name string
		if _, err := fmt.Sscanf(line, "%s %s", &hash, &name); err == nil && hash != "" && name != "" {
			out[name] = hash
		}
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// Inspect verifies every file's checksum (unlike Load, which only checks
// model.json — the one file the runtime hot path actually reads) and
// returns the parsed Manifest, FeatureSchema, and raw metrics.json
// contents, for the `validate-model` CLI command.
func Inspect(dir string) (Manifest, FeatureSchema, string, error) {
	checksumData, err := os.ReadFile(filepath.Join(dir, "CHECKSUMS.sha256")) // #nosec G304 -- dir is an operator-provided config value, not user input
	if err != nil {
		return Manifest{}, FeatureSchema{}, "", fmt.Errorf("editorialmodel: read CHECKSUMS.sha256 in %s: %w", dir, err)
	}
	want := parseChecksums(string(checksumData))

	manifestData, err := readAndVerify(dir, "manifest.json", want)
	if err != nil {
		return Manifest{}, FeatureSchema{}, "", err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return Manifest{}, FeatureSchema{}, "", fmt.Errorf("editorialmodel: parse manifest.json in %s: %w", dir, err)
	}

	schemaData, err := readAndVerify(dir, "schema.json", want)
	if err != nil {
		return Manifest{}, FeatureSchema{}, "", err
	}
	var schema FeatureSchema
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		return Manifest{}, FeatureSchema{}, "", fmt.Errorf("editorialmodel: parse schema.json in %s: %w", dir, err)
	}

	metricsData, err := readAndVerify(dir, "metrics.json", want)
	if err != nil {
		return Manifest{}, FeatureSchema{}, "", err
	}

	if _, err := readAndVerify(dir, "model.json", want); err != nil {
		return Manifest{}, FeatureSchema{}, "", err
	}

	return manifest, schema, string(metricsData), nil
}

// Validate checks m, manifest, and featureSchema are internally
// consistent and usable before ProcessCycle runs a single cycle. Load
// and Inspect already verify each file's own on-disk checksum via
// CHECKSUMS.sha256 (tamper/corruption detection per-file); Validate adds
// the cross-file consistency check CHECKSUMS.sha256 alone cannot catch —
// that model.json's actual weights match what manifest.json's
// artifact_hash recorded at promotion time (recomputed here using the
// exact same struct shape modelpromotion.Promote hashed) — plus
// threshold ordering and non-empty vocabulary/weights checks. Called by
// main.go for every mode except "disabled" — a failure here must abort
// startup, never degrade silently.
func Validate(m editorial.Model, manifest Manifest, featureSchema FeatureSchema) error {
	artifactData, err := json.Marshal(struct {
		Bias    float64
		Weights map[string]float64
	}{m.Bias, m.Weights})
	if err != nil {
		return fmt.Errorf("editorialmodel: marshal model for checksum: %w", err)
	}
	sum := sha256.Sum256(artifactData)
	got := hex.EncodeToString(sum[:])
	if got != manifest.ArtifactHash {
		return fmt.Errorf("editorialmodel: artifact checksum mismatch: manifest says %s, computed %s (model.json and manifest.json are inconsistent)", manifest.ArtifactHash, got)
	}
	if m.Thresholds.Reject >= m.Thresholds.Accept {
		return fmt.Errorf("editorialmodel: invalid thresholds: reject (%.2f) must be strictly less than accept (%.2f)", m.Thresholds.Reject, m.Thresholds.Accept)
	}
	if len(featureSchema.Features) == 0 {
		return fmt.Errorf("editorialmodel: feature schema has no features (empty vocabulary)")
	}
	if m.FeatureSchemaVersion != featureSchema.Version {
		return fmt.Errorf("editorialmodel: model feature_schema_version %q does not match schema file version %q", m.FeatureSchemaVersion, featureSchema.Version)
	}
	if m.Version == "" {
		return fmt.Errorf("editorialmodel: model version is empty")
	}
	if len(m.Weights) == 0 {
		return fmt.Errorf("editorialmodel: model has no weights")
	}
	return nil
}

func readAndVerify(dir, name string, want map[string]string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- dir is an operator-provided config value, not user input
	if err != nil {
		return nil, fmt.Errorf("editorialmodel: read %s in %s: %w", name, dir, err)
	}
	got := sha256Hex(data)
	if want[name] != got {
		return nil, fmt.Errorf("editorialmodel: checksum mismatch for %s in %s: recorded %s, computed %s (model artifact is corrupt)", name, dir, want[name], got)
	}
	return data, nil
}
