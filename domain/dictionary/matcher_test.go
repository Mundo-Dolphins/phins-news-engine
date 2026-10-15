package dictionary_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

func testDictionary() dictionary.Dictionary {
	return dictionary.Dictionary{
		Version: "1.0.0",
		Categories: []dictionary.Category{
			{
				Name: "strong", Weight: 12, StrongOverride: true,
				Entries: []dictionary.Entry{{CanonicalName: "Springfield Isotopes", Source: "x"}},
			},
			{
				Name: "people", Weight: 6,
				Entries: []dictionary.Entry{{CanonicalName: "Ezo Quarrington", Aliases: []string{"Ezo"}, Source: "x"}},
			},
			{
				Name: "hashtags", Weight: 12, StrongOverride: true,
				Entries: []dictionary.Entry{{CanonicalName: "#FinsUp", Source: "x"}},
			},
			{
				Name: "urls", Weight: 20, StrongOverride: true,
				Entries: []dictionary.Entry{{CanonicalName: "isotopes.example.com", Source: "x"}},
			},
		},
		Decision: dictionary.Decision{PositiveThresholdHigh: 10, PositiveThresholdMedium: 6, TitleWeightMultiplier: 2, MaxCountPerTerm: 3},
	}
}

func TestPrepare_ThenFind_MatchesCanonicalNameCaseInsensitively(t *testing.T) {
	m, err := dictionary.Prepare(testDictionary())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	occ := m.Find("The SPRINGFIELD ISOTOPES won last night")
	if len(occ) != 1 || occ[0].EntryName != "Springfield Isotopes" || occ[0].Count != 1 {
		t.Errorf("Find() = %+v, want one match on Springfield Isotopes", occ)
	}
}

func TestFind_MatchesAliasAndMergesIntoOneOccurrencePerEntry(t *testing.T) {
	m, err := dictionary.Prepare(testDictionary())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	occ := m.Find("Ezo Quarrington threw to himself, or maybe it was just Ezo again")
	if len(occ) != 1 {
		t.Fatalf("Find() = %+v, want exactly one merged occurrence for the entry", occ)
	}
	if occ[0].EntryName != "Ezo Quarrington" || occ[0].Count != 2 {
		t.Errorf("Find() = %+v, want EntryName=Ezo Quarrington Count=2 (one canonical + one alias match)", occ[0])
	}
}

func TestFind_MultipleNonOverlappingMatchesOfSameTermAreCountedSeparately(t *testing.T) {
	d := dictionary.Dictionary{Categories: []dictionary.Category{
		{Name: "ambiguous", Weight: 4, Entries: []dictionary.Entry{{CanonicalName: "isotopes", Source: "x"}}},
	}, Decision: dictionary.Decision{MaxCountPerTerm: 3, TitleWeightMultiplier: 1}}
	m, err := dictionary.Prepare(d)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	occ := m.Find("Isotopes Isotopes Isotopes injury report")
	if len(occ) != 1 || occ[0].Count != 3 {
		t.Errorf("Find() = %+v, want exactly one occurrence with Count=3 (three separate, non-overlapping mentions must not collapse into one)", occ)
	}
}

func TestFind_WordBoundaryDoesNotMatchInsideAnotherWord(t *testing.T) {
	d := dictionary.Dictionary{Categories: []dictionary.Category{
		{Name: "weak", Weight: 4, Entries: []dictionary.Entry{{CanonicalName: "topes", Source: "x"}}},
	}, Decision: dictionary.Decision{MaxCountPerTerm: 3, TitleWeightMultiplier: 1}}
	m, err := dictionary.Prepare(d)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if occ := m.Find("The lab measured isotopes for hours"); len(occ) != 0 {
		t.Errorf("Find(%q) = %+v, want no match ('topes' inside 'isotopes' must not match)", "isotopes", occ)
	}
	if occ := m.Find("Topes up today!"); len(occ) != 1 {
		t.Errorf("Find(%q) = %+v, want one match on the standalone word", "Topes up today!", occ)
	}
}

func TestFind_LeadingSymbolTermMatchesWithoutRequiringWordBoundaryBeforeIt(t *testing.T) {
	m, err := dictionary.Prepare(testDictionary())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	occ := m.Find("Topes Up! #FinsUp is trending tonight")
	found := false
	for _, o := range occ {
		if o.EntryName == "#FinsUp" {
			found = true
		}
	}
	if !found {
		t.Errorf("Find() = %+v, want a match on #FinsUp even though a space (non-word) precedes it", occ)
	}
}

func TestFind_URLPatternMatchesAsSubstringWithinALongerURL(t *testing.T) {
	m, err := dictionary.Prepare(testDictionary())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	occ := m.Find("Read more at https://www.isotopes.example.com/news/story-1")
	found := false
	for _, o := range occ {
		if o.EntryName == "isotopes.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("Find() = %+v, want a match on isotopes.example.com inside the full URL", occ)
	}
}

func TestFind_NoMatchesReturnsEmpty(t *testing.T) {
	m, err := dictionary.Prepare(testDictionary())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if occ := m.Find("completely unrelated weather report"); len(occ) != 0 {
		t.Errorf("Find() = %+v, want no matches", occ)
	}
}

func TestMatcher_CategoriesDecisionVersionExposePreparedDictionary(t *testing.T) {
	m, err := dictionary.Prepare(testDictionary())
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if m.Version() != "1.0.0" {
		t.Errorf("Version() = %q, want 1.0.0", m.Version())
	}
	if m.Decision().PositiveThresholdHigh != 10 {
		t.Errorf("Decision().PositiveThresholdHigh = %v, want 10", m.Decision().PositiveThresholdHigh)
	}
	if len(m.Categories()) != 4 {
		t.Errorf("len(Categories()) = %d, want 4", len(m.Categories()))
	}
}
