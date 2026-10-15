// Package dictionaryhistory persists the full on-disk artifacts for a
// dictionary version — the dictionary itself, the diff that produced it,
// the decision log behind that diff, and its validation result — under
// baseDir/v<version>/, independent of (but recorded alongside) the SQLite
// dictionary_history ledger.
package dictionaryhistory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysession"
)

// Artifacts is the full set of file artifacts recorded for one dictionary
// version.
type Artifacts struct {
	Version    string
	Dictionary dictionary.Dictionary
	Diff       string
	Decisions  []dictionarysession.Decision
	Validation []dictionary.Issue
}

// WriteVersion persists a full set of history artifacts under
// baseDir/v<version>/ — the full dictionary backup, a text diff, the
// decision log, and the validation result — atomically (temp dir +
// rename), so a partial write never leaves a half-populated version
// directory that dictionary history/rollback could read incorrectly.
func WriteVersion(baseDir string, a Artifacts) error {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return fmt.Errorf("dictionaryhistory: mkdir base: %w", err)
	}
	tmpDir, err := os.MkdirTemp(baseDir, ".history-*.tmp")
	if err != nil {
		return fmt.Errorf("dictionaryhistory: mkdir temp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := dictionaryfile.Save(filepath.Join(tmpDir, "dictionary.yml"), a.Dictionary); err != nil {
		return fmt.Errorf("dictionaryhistory: write dictionary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "diff.txt"), []byte(a.Diff), 0o600); err != nil {
		return fmt.Errorf("dictionaryhistory: write diff: %w", err)
	}
	decisionsJSON, err := json.MarshalIndent(a.Decisions, "", "  ")
	if err != nil {
		return fmt.Errorf("dictionaryhistory: marshal decisions: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "decisions.json"), decisionsJSON, 0o600); err != nil {
		return fmt.Errorf("dictionaryhistory: write decisions: %w", err)
	}
	var validationText strings.Builder
	for _, iss := range a.Validation {
		validationText.WriteString(iss.String() + "\n")
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "validation.txt"), []byte(validationText.String()), 0o600); err != nil {
		return fmt.Errorf("dictionaryhistory: write validation: %w", err)
	}

	finalDir := filepath.Join(baseDir, "v"+a.Version)
	if err := os.RemoveAll(finalDir); err != nil {
		return fmt.Errorf("dictionaryhistory: clear existing dir: %w", err)
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return fmt.Errorf("dictionaryhistory: rename: %w", err)
	}
	return nil
}

// ReadVersion loads the artifacts previously written by WriteVersion for
// version from baseDir/v<version>/.
func ReadVersion(baseDir, version string) (Artifacts, error) {
	dir := filepath.Join(baseDir, "v"+version)
	d, err := dictionaryfile.Load(filepath.Join(dir, "dictionary.yml"))
	if err != nil {
		return Artifacts{}, fmt.Errorf("dictionaryhistory: load dictionary: %w", err)
	}
	diffBytes, err := os.ReadFile(filepath.Join(dir, "diff.txt")) // #nosec G304 -- baseDir/version are operator-provided, not user input
	if err != nil {
		return Artifacts{}, fmt.Errorf("dictionaryhistory: read diff: %w", err)
	}
	decisionsBytes, err := os.ReadFile(filepath.Join(dir, "decisions.json")) // #nosec G304 -- baseDir/version are operator-provided, not user input
	if err != nil {
		return Artifacts{}, fmt.Errorf("dictionaryhistory: read decisions: %w", err)
	}
	var decisions []dictionarysession.Decision
	if err := json.Unmarshal(decisionsBytes, &decisions); err != nil {
		return Artifacts{}, fmt.Errorf("dictionaryhistory: unmarshal decisions: %w", err)
	}
	return Artifacts{Version: version, Dictionary: d, Diff: string(diffBytes), Decisions: decisions}, nil
}
