package entitydiscovery_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func sampleRules() []eventsignals.EntityRule {
	return []eventsignals.EntityRule{
		{ID: "jeff-harrowby", CanonicalName: "Jeff Harrowby", Kind: eventsignals.EntityKindCoach, Terms: []string{"Jeff Harrowby"}},
		{ID: "pierce-quenby", CanonicalName: "Pierce Quenby", Kind: eventsignals.EntityKindPlayer, Terms: []string{"Pierce Quenby", "Quenby"}},
		{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, Terms: []string{"Springfield Isotopes", "Isotopes"}},
		{ID: "afc-east", CanonicalName: "AFC East", Kind: eventsignals.EntityKindCompetition, Terms: []string{"AFC East"}},
	}
}

func TestBuildCatalogIndex_LookupMatchesKnownCanonicalNameAndAlias(t *testing.T) {
	idx := entitydiscovery.BuildCatalogIndex(sampleRules())

	if ce, ok := idx.Lookup(entityproposal.Normalize("Jeff Harrowby")); !ok || ce.ID != "jeff-harrowby" || ce.Type != entityproposal.EntityTypeCoach {
		t.Fatalf("Lookup(Jeff Harrowby) = %+v, %v, want jeff-harrowby/coach", ce, ok)
	}
	if ce, ok := idx.Lookup(entityproposal.Normalize("Quenby")); !ok || ce.ID != "pierce-quenby" {
		t.Fatalf("Lookup(Quenby) = %+v, %v, want pierce-quenby (an explicit term)", ce, ok)
	}
}

func TestBuildCatalogIndex_LookupMissesUnknownName(t *testing.T) {
	idx := entitydiscovery.BuildCatalogIndex(sampleRules())
	if _, ok := idx.Lookup(entityproposal.Normalize("Jai'Rene' Mossbank")); ok {
		t.Fatal("Lookup(Jai'Rene' Mossbank) = true, want false (not in the catalog)")
	}
}

func TestBuildCatalogIndex_SkipsCompetitions(t *testing.T) {
	idx := entitydiscovery.BuildCatalogIndex(sampleRules())
	if _, ok := idx.Lookup(entityproposal.Normalize("AFC East")); ok {
		t.Fatal("Lookup(AFC East) = true, want false — competitions are out of scope for this feature")
	}
}

func TestBuildCatalogIndex_SurnameOwnerRequiresUniqueSurname(t *testing.T) {
	idx := entitydiscovery.BuildCatalogIndex(sampleRules())
	// "Harrowby" is unique among sampleRules' canonical names.
	if ce, ok := idx.SurnameOwner("harrowby"); !ok || ce.ID != "jeff-harrowby" {
		t.Fatalf("SurnameOwner(harrowby) = %+v, %v, want jeff-harrowby", ce, ok)
	}
}

func TestBuildCatalogIndex_SurnameOwnerAmbiguousWhenSharedByTwoEntities(t *testing.T) {
	rules := append(sampleRules(), eventsignals.EntityRule{ID: "billy-harrowby", CanonicalName: "Billy Harrowby", Kind: eventsignals.EntityKindPlayer, Terms: []string{"Billy Harrowby"}})
	idx := entitydiscovery.BuildCatalogIndex(rules)
	if _, ok := idx.SurnameOwner("harrowby"); ok {
		t.Fatal("SurnameOwner(harrowby) = true, want false once two catalog entities share that surname")
	}
}

func TestCatalogIndex_TeamIDsReturnsOnlyTeams(t *testing.T) {
	ids := entitydiscovery.BuildCatalogIndex(sampleRules()).TeamIDs()
	if len(ids) != 1 || !ids["springfield-isotopes"] {
		t.Fatalf("TeamIDs() = %v, want only springfield-isotopes", ids)
	}
}

func TestCatalogIndex_TeamIDsIncludesTeamWithCollidingOrNoTerms(t *testing.T) {
	rules := append(sampleRules(),
		eventsignals.EntityRule{ID: "springfield-tempests", CanonicalName: "Springfield Tempests", Kind: eventsignals.EntityKindTeam, Terms: []string{"Isotopes"}},
		eventsignals.EntityRule{ID: "no-terms", CanonicalName: "No Terms FC", Kind: eventsignals.EntityKindTeam},
	)
	ids := entitydiscovery.BuildCatalogIndex(rules).TeamIDs()
	for _, want := range []string{"springfield-isotopes", "springfield-tempests", "no-terms"} {
		if !ids[want] {
			t.Errorf("TeamIDs() = %v, missing %s", ids, want)
		}
	}
}
