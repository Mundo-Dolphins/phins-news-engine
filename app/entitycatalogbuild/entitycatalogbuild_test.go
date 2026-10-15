package entitycatalogbuild_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitycatalogbuild"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func testDictionary() dictionary.Dictionary {
	return dictionary.Dictionary{
		Version: "test",
		Categories: []dictionary.Category{
			{Name: "active_roster_core", Entries: []dictionary.Entry{
				{CanonicalName: "Pierce Quenby", Type: dictionary.EntryTypePerson, Position: "OT"},
				{CanonicalName: "Ezo Quarrington", Aliases: []string{"Ezo"}, Type: dictionary.EntryTypePerson, Position: "QB"},
			}},
			{Name: "coaching_staff", Entries: []dictionary.Entry{
				{CanonicalName: "Mike Thistlewood", Aliases: []string{"Thistlewood"}, Type: dictionary.EntryTypeCoach},
			}},
			{Name: "front_office", Entries: []dictionary.Entry{
				{CanonicalName: "Chris Gravenor", Type: dictionary.EntryTypeFrontOffice},
			}},
			{Name: "alumni", Entries: []dictionary.Entry{
				{CanonicalName: "Dan Pellingham", Type: dictionary.EntryTypeAlumnus},
			}},
			{Name: "rivals", Entries: []dictionary.Entry{
				{CanonicalName: "Josh Wrenfield", Type: dictionary.EntryTypeRival},
			}},
		},
	}
}

func TestBuild_IncludesOnlyRosterCoachingFrontOffice(t *testing.T) {
	entities, err := entitycatalogbuild.Build(testDictionary())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(entities) != 4 {
		t.Fatalf("len(entities) = %d, want 4 (alumni/rivals excluded)", len(entities))
	}
	for _, e := range entities {
		if e.CanonicalName == "Dan Pellingham" || e.CanonicalName == "Josh Wrenfield" {
			t.Errorf("unexpected entity %q — alumni/rivals must be excluded", e.CanonicalName)
		}
	}
}

func TestBuild_MapsCategoryToKindAndCarriesFields(t *testing.T) {
	entities, err := entitycatalogbuild.Build(testDictionary())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byID := map[string]eventsignals.EntityRule{}
	for _, e := range entities {
		byID[e.ID] = e
	}

	quenby, ok := byID["pierce-quenby"]
	if !ok {
		t.Fatalf("no entity with id pierce-quenby in %+v", entities)
	}
	if quenby.Kind != eventsignals.EntityKindPlayer || quenby.Position != "OT" {
		t.Errorf("pierce-quenby = %+v, want kind=player position=OT", quenby)
	}
	for _, e := range entities {
		if e.TeamID != "" || e.Role != "" {
			t.Errorf("generated entity %q has TeamID=%q Role=%q, want empty", e.ID, e.TeamID, e.Role)
		}
	}

	thistlewood, ok := byID["mike-thistlewood"]
	if !ok || thistlewood.Kind != eventsignals.EntityKindCoach {
		t.Errorf("mike-thistlewood = %+v, want kind=coach present", thistlewood)
	}
	if len(thistlewood.Terms) != 2 {
		t.Errorf("mike-thistlewood.Terms = %v, want canonical name + 1 alias", thistlewood.Terms)
	}

	gravenor, ok := byID["chris-gravenor"]
	if !ok || gravenor.Kind != eventsignals.EntityKindExecutive {
		t.Errorf("chris-gravenor = %+v, want kind=executive present", gravenor)
	}
}

// TestBuild_SkipsSingleTokenCanonicalNames is the regression test for
// C1: a malformed dictionary entry with a single-token (no space)
// canonical name — like the real dictionary.yml's bogus
// "bradley" entry — must be silently skipped, not turned into an entity
// that can collide with (and outrank) a real multi-word name sharing that
// same token (e.g. "Bramwell Oxtoby").
func TestBuild_SkipsSingleTokenCanonicalNames(t *testing.T) {
	d := testDictionary()
	d.Categories[0].Entries = append(d.Categories[0].Entries, dictionary.Entry{
		CanonicalName: "bradley", Type: dictionary.EntryTypePerson,
	})
	entities, err := entitycatalogbuild.Build(d)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, e := range entities {
		if e.ID == "bradley" || e.CanonicalName == "bradley" {
			t.Errorf("unexpected single-token entity %+v — malformed entries must be skipped", e)
		}
	}
}

// TestBuild_SynthesizesLastNameAlias is the regression test for C2: every
// multi-word canonical name gets its last name appended as a term, so
// surname-only headlines ("Wickersham returns to practice") still resolve.
func TestBuild_SynthesizesLastNameAlias(t *testing.T) {
	entities, err := entitycatalogbuild.Build(testDictionary())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byID := map[string]eventsignals.EntityRule{}
	for _, e := range entities {
		byID[e.ID] = e
	}

	quenby := byID["pierce-quenby"]
	if !containsTerm(quenby.Terms, "Quenby") {
		t.Errorf("pierce-quenby.Terms = %v, want to contain synthesized last-name alias %q", quenby.Terms, "Quenby")
	}

	// Ezo already has a hand-authored alias ("Ezo") that isn't the last
	// name, so the synthesized last-name alias ("Quarrington") should be
	// added in addition to it, not instead of it.
	ezo := byID["ezo-quarrington"]
	if !containsTerm(ezo.Terms, "Ezo") || !containsTerm(ezo.Terms, "Quarrington") {
		t.Errorf("ezo-quarrington.Terms = %v, want both %q and %q", ezo.Terms, "Ezo", "Quarrington")
	}
}

// TestBuild_SkipsAmbiguousSurnameAlias verifies the C2 collision guard:
// when two distinct entities' canonical names share the same last name,
// neither gets that surname synthesized as an alias.
func TestBuild_SkipsAmbiguousSurnameAlias(t *testing.T) {
	d := testDictionary()
	d.Categories[0].Entries = append(d.Categories[0].Entries, dictionary.Entry{
		CanonicalName: "Tyrell Quenby", Type: dictionary.EntryTypePerson,
	})
	entities, err := entitycatalogbuild.Build(d)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, e := range entities {
		if e.ID == "pierce-quenby" || e.ID == "tyrell-quenby" {
			if containsTerm(e.Terms, "Quenby") {
				t.Errorf("%s.Terms = %v, ambiguous surname %q must be skipped for both entities", e.ID, e.Terms, "Quenby")
			}
		}
	}
}

func containsTerm(terms []string, want string) bool {
	for _, t := range terms {
		if t == want {
			return true
		}
	}
	return false
}

func TestBuild_IsSortedByID(t *testing.T) {
	entities, err := entitycatalogbuild.Build(testDictionary())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for i := 1; i < len(entities); i++ {
		if entities[i-1].ID >= entities[i].ID {
			t.Errorf("entities not sorted by id: %q >= %q", entities[i-1].ID, entities[i].ID)
		}
	}
}
