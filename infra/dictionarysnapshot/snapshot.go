package dictionarysnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
)

const (
	ParserVersion = "1"
	SchemaVersion = 1
)

type Snapshot struct {
	FetchedAt     time.Time
	Source        dictionaryupdate.SourceKind
	URL           string
	Domain        string
	Mode          string
	StatusCode    int
	ContentHash   string
	Entities      []dictionaryupdate.SourceEntity
	Errors        []string
	ParserVersion string
	SchemaVersion int
}

// New builds a Snapshot with entities sorted deterministically (by
// canonical key) and a ContentHash computed over that sorted list only —
// independent of FetchedAt or the eventual filename — so two runs against
// unchanged source content are logically identical even though the
// snapshot filename differs.
func New(kind dictionaryupdate.SourceKind, url, domain, mode string, statusCode int, entities []dictionaryupdate.SourceEntity, fetchErr error, fetchedAt time.Time) Snapshot {
	sorted := make([]dictionaryupdate.SourceEntity, len(entities))
	copy(sorted, entities)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].CanonicalKey < sorted[j].CanonicalKey
	})

	var errs []string
	if fetchErr != nil {
		errs = append(errs, fetchErr.Error())
	}

	return Snapshot{
		FetchedAt:     fetchedAt,
		Source:        kind,
		URL:           url,
		Domain:        domain,
		Mode:          mode,
		StatusCode:    statusCode,
		ContentHash:   contentHash(sorted),
		Entities:      sorted,
		Errors:        errs,
		ParserVersion: ParserVersion,
		SchemaVersion: SchemaVersion,
	}
}

func contentHash(sortedEntities []dictionaryupdate.SourceEntity) string {
	redacted := make([]dictionaryupdate.SourceEntity, len(sortedEntities))
	for i, e := range sortedEntities {
		e.ObservedAt = time.Time{}
		redacted[i] = e
	}
	b, err := json.Marshal(redacted)
	if err != nil {
		panic("dictionarysnapshot: content hash marshal: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Write persists snap atomically (temp file + rename, matching
// dictionaryfile.Save's convention) under dir, named fetched-YYYYMMDD-
// HHMMSS.json from now, and returns the written path.
func Write(dir string, snap Snapshot, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("dictionarysnapshot: mkdir: %w", err)
	}
	name := fmt.Sprintf("fetched-%s-%s.json", snap.Source, now.UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", fmt.Errorf("dictionarysnapshot: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".snapshot-*.tmp")
	if err != nil {
		return "", fmt.Errorf("dictionarysnapshot: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("dictionarysnapshot: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("dictionarysnapshot: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("dictionarysnapshot: rename temp file: %w", err)
	}
	return path, nil
}

func Read(path string) (Snapshot, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-provided config value, not user input
	if err != nil {
		return Snapshot{}, fmt.Errorf("dictionarysnapshot: read: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("dictionarysnapshot: unmarshal: %w", err)
	}
	return snap, nil
}
