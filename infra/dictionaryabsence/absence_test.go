package dictionaryabsence_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryabsence"
)

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absences.json")
	log := dictionaryupdate.AbsenceLog{Records: map[string]dictionaryupdate.AbsenceRecord{
		"missing player|roster": {CanonicalKey: "missing player", Source: dictionaryupdate.SourceKindRoster, FirstAbsentAt: time.Now(), ConsecutiveAbsences: 2},
	}}

	if err := dictionaryabsence.Save(path, log); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := dictionaryabsence.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec, ok := got.Get("missing player", dictionaryupdate.SourceKindRoster)
	if !ok || rec.ConsecutiveAbsences != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestLoad_MissingFileReturnsEmptyLogNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")

	log, err := dictionaryabsence.Load(path)
	if err != nil {
		t.Fatalf("Load of a missing file should return an empty log, not an error: %v", err)
	}
	if len(log.Records) != 0 {
		t.Fatalf("expected an empty log, got %+v", log)
	}
}
