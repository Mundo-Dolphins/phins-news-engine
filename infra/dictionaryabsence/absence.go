package dictionaryabsence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
)

// Save persists log atomically (temp file + rename), matching every other
// writer in this plan.
func Save(path string, log dictionaryupdate.AbsenceLog) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("dictionaryabsence: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return fmt.Errorf("dictionaryabsence: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".absences-*.tmp")
	if err != nil {
		return fmt.Errorf("dictionaryabsence: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("dictionaryabsence: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("dictionaryabsence: close temp file: %w", err)
	}
	return os.Rename(tmpPath, path)
}

// Load returns an empty AbsenceLog (not an error) when path doesn't exist
// yet — the very first fetch run has no absence history to load.
func Load(path string) (dictionaryupdate.AbsenceLog, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided config value, not user input
	if errors.Is(err, os.ErrNotExist) {
		return dictionaryupdate.AbsenceLog{Records: map[string]dictionaryupdate.AbsenceRecord{}}, nil
	}
	if err != nil {
		return dictionaryupdate.AbsenceLog{}, fmt.Errorf("dictionaryabsence: read: %w", err)
	}
	var log dictionaryupdate.AbsenceLog
	if err := json.Unmarshal(data, &log); err != nil {
		return dictionaryupdate.AbsenceLog{}, fmt.Errorf("dictionaryabsence: unmarshal: %w", err)
	}
	return log, nil
}
