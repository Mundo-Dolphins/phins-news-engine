package classifierdictionary

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
)

func TestApplyApprovalWritesVersionedChecksummedDictionary(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "dictionary.yml")
	seed := dictionary.Dictionary{
		Version:       "1.0.0",
		Name:          "test",
		Language:      "en",
		SchemaVersion: 2,
		Categories: []dictionary.Category{{
			Name:   "nfl_people_context",
			Weight: 2,
			Entries: []dictionary.Entry{{
				CanonicalName: "Ezo Quarrington",
				Type:          dictionary.EntryTypePerson,
				Source:        "test",
				UpdatedAt:     now,
				Confidence:    0.9,
			}},
		}},
		Decision: dictionary.Decision{
			PositiveThresholdHigh: 10, PositiveThresholdMedium: 7, MaxCountPerTerm: 3, TitleWeightMultiplier: 1,
		},
	}
	seed.Checksum = dictionary.ContentHash(seed)
	if err := dictionaryfile.Save(path, seed); err != nil {
		t.Fatalf("Save(seed) error = %v", err)
	}

	p := classifierproposal.Proposal{
		ID: "p1", Kind: classifierproposal.KindTerm, Category: "nfl_people_context",
		Value: "Jai Rene Mossbank", NormalizedValue: "jai rene mossbank",
		Operation: "add", Confidence: 0.8, Occurrences: 3,
	}
	if err := ApplyApproval(p, path, now); err != nil {
		t.Fatalf("ApplyApproval() error = %v", err)
	}
	got, err := dictionaryfile.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != "1.0.1" {
		t.Errorf("Version = %q, want 1.0.1", got.Version)
	}
	if got.Checksum == "" || got.Checksum != dictionary.ContentHash(got) {
		t.Errorf("Checksum = %q, want stamped content hash", got.Checksum)
	}
	found := false
	for _, cat := range got.Categories {
		for _, e := range cat.Entries {
			if e.CanonicalName == "Jai Rene Mossbank" {
				found = true
				if e.Source != "classifier_dictionary_proposals" {
					t.Errorf("Source = %q", e.Source)
				}
			}
		}
	}
	if !found {
		t.Fatal("added entry not found")
	}
}
