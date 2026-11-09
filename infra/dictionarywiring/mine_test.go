package dictionarywiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func TestMineNoCompletedSession(t *testing.T) {
	db := migratedDB(t)
	_, err := Mine(context.Background(), db, &config.Config{}, "")
	if !errors.Is(err, ErrNoCompletedSession) {
		t.Fatalf("error = %v, want ErrNoCompletedSession", err)
	}
	var setup *MineSetupError
	if !errors.As(err, &setup) {
		t.Errorf("error %v is not a *MineSetupError", err)
	}
}

func TestMineSessionLookupFailureIsNotNoSession(t *testing.T) {
	db := migratedDB(t)
	if _, err := db.Exec(`DROP TABLE editorial_cycle_sessions`); err != nil {
		t.Fatal(err)
	}
	_, err := Mine(context.Background(), db, &config.Config{}, "")
	if err == nil || errors.Is(err, ErrNoCompletedSession) {
		t.Fatalf("error = %v, want a lookup error without ErrNoCompletedSession", err)
	}
	var setup *MineSetupError
	if !errors.As(err, &setup) || !strings.HasPrefix(err.Error(), "no completed session found and --session not given: ") {
		t.Errorf("error = %v, want the session-lookup MineSetupError text", err)
	}
}

func TestMineWarnings(t *testing.T) {
	db := migratedDB(t)
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Dictionary.Path = write(t, validYAML)
	cfg.DictionaryMining.Path = filepath.Join(dir, "dictionary_mining.yml")
	if err := os.WriteFile(cfg.DictionaryMining.Path, []byte("min_support: 1\nignored_terms: []\nignored_tokens: []\nignored_substrings: []\ncategory_guesses: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rosterPath := filepath.Join(dir, "missing-roster.csv")
	cfg.Clustering.EntityCatalog.League.RosterPath = rosterPath

	report, err := Mine(context.Background(), db, cfg, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"WARNING: nflverse roster " + rosterPath + " not found; people it covers are not filtered out"}
	if !reflect.DeepEqual(report.Warnings, want) {
		t.Errorf("Warnings = %q, want %q", report.Warnings, want)
	}
	if report.Session != "sess-1" || len(report.Candidates) != 0 {
		t.Errorf("report = %+v", report)
	}
}
