package dictionary_test

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

func checksumFixture() dictionary.Dictionary {
	return dictionary.Dictionary{
		Version:     "1.0.0",
		Name:        "isotopes",
		Language:    "en",
		GeneratedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Categories: []dictionary.Category{
			{
				Name:   "official_names",
				Weight: 1,
				Entries: []dictionary.Entry{
					{CanonicalName: "Springfield Isotopes", Confidence: 1.0},
				},
			},
		},
		Decision: dictionary.Decision{PositiveThresholdHigh: 5, MaxCountPerTerm: 3, TitleWeightMultiplier: 2},
	}
}

func TestContentHash_IgnoresMetadataOnlyFields(t *testing.T) {
	a := checksumFixture()
	b := checksumFixture()
	b.Version = "1.0.5"
	b.GeneratedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	b.Checksum = "stale-value"
	b.Tool = "phins-news-engine-tools dictionary update"

	if dictionary.ContentHash(a) != dictionary.ContentHash(b) {
		t.Fatalf("ContentHash should ignore Version/GeneratedAt/Checksum/Tool, got different hashes")
	}
}

func TestContentHash_ChangesWhenEntryContentChanges(t *testing.T) {
	a := checksumFixture()
	b := checksumFixture()
	b.Categories[0].Entries[0].Confidence = 0.5

	if dictionary.ContentHash(a) == dictionary.ContentHash(b) {
		t.Fatalf("ContentHash should change when entry confidence changes")
	}
}

func TestContentHash_StableAcrossEntryUpdatedAtSubDayPrecision(t *testing.T) {
	// dictionaryfile round-trips Entry.UpdatedAt through a "2006-01-02"
	// YAML field, so a checksum computed against a full-precision
	// timestamp (e.g. clk.Now()) would never match once the file is
	// reloaded. ContentHash must be insensitive to sub-day precision so a
	// checksum stamped right after writing a full-precision UpdatedAt
	// still validates after a later Load.
	a := checksumFixture()
	a.Categories[0].Entries[0].UpdatedAt = time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)

	b := checksumFixture()
	b.Categories[0].Entries[0].UpdatedAt = time.Date(2026, 3, 4, 17, 42, 9, 123, time.UTC)

	if dictionary.ContentHash(a) != dictionary.ContentHash(b) {
		t.Fatalf("ContentHash should ignore sub-day precision in Entry.UpdatedAt")
	}
}

func TestContentHash_StableAcrossCategoryOrder(t *testing.T) {
	a := checksumFixture()
	a.Categories = append(a.Categories, dictionary.Category{Name: "coaching_staff", Weight: 1})

	b := checksumFixture()
	b.Categories = []dictionary.Category{
		{Name: "coaching_staff", Weight: 1},
		checksumFixture().Categories[0],
	}

	if dictionary.ContentHash(a) != dictionary.ContentHash(b) {
		t.Fatalf("ContentHash should not depend on category slice order")
	}
}
