package entityidentity

import (
	"reflect"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func year(y int) *time.Time {
	t := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	return &t
}

// --- fixture builders (normalized knowledge shapes, no network) ---

func item(prop, qid string) knowledge.Statement {
	return knowledge.Statement{
		Property: prop, Rank: knowledge.RankNormal,
		Value: knowledge.Value{Kind: knowledge.KindEntity, EntityID: qid},
	}
}

func ranked(s knowledge.Statement, r knowledge.Rank) knowledge.Statement {
	s.Rank = r
	return s
}

func span(s knowledge.Statement, from, to *time.Time) knowledge.Statement {
	s.ValidFrom, s.ValidTo = from, to
	return s
}

func ent(id, label string, aliases []string, stmts ...knowledge.Statement) knowledge.Entity {
	return knowledge.Entity{ID: id, Label: label, Aliases: aliases, Statements: stmts}
}

func world(es ...knowledge.Entity) map[string]knowledge.Entity {
	m := map[string]knowledge.Entity{}
	for _, e := range es {
		m[e.ID] = e
	}
	return m
}

func candsOf(es ...knowledge.Entity) []knowledge.Candidate {
	out := make([]knowledge.Candidate, 0, len(es))
	for _, e := range es {
		out = append(out, knowledge.Candidate{ID: e.ID, Label: e.Label, Aliases: e.Aliases})
	}
	return out
}

type fakeCatalog map[string]CatalogEntry

func (c fakeCatalog) Lookup(n string) (CatalogEntry, bool) { e, ok := c[n]; return e, ok }

var catalog = fakeCatalog{
	"springfield isotopes":  {ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: entityproposal.EntityTypeTeam},
	"capital city capitals": {ID: "capital-city-capitals", CanonicalName: "Capital City Capitals", Kind: entityproposal.EntityTypeTeam},
	"brockway badgers":      {ID: "brockway-badgers", CanonicalName: "Brockway Badgers", Kind: entityproposal.EntityTypeTeam},
	"the topes":             {ID: "other-thing", CanonicalName: "Other Thing", Kind: entityproposal.EntityTypeTeam},
	"ezo quarrington":       {ID: "ezo", CanonicalName: "Ezo Quarrington", Kind: entityproposal.EntityTypePlayer},
}

// real Wikidata QIDs used by the vocabulary
const (
	qNFL      = "Q1215884"
	qMLB      = "Q1163715"
	qNBA      = "Q155223"
	qNHL      = "Q1215892"
	qFBS      = "Q19673326"
	qFootball = "Q41323"
	qBaseball = "Q5369"
	qBasket   = "Q5372"
	qHockey   = "Q41466"
	qHuman    = "Q5"
	qFBPlayer = "Q19204627"
	qFBCoach  = "Q42331263"
	qNFLHC    = "Q141192267"
	qQB       = "Q622747"
	qLB       = "Q528145"
	qFBTeam   = "Q17156793"
)

var (
	isotopes = ent("Q910000006", "Springfield Isotopes", []string{"Isotopes", "Springfield"},
		item("P31", qFBTeam), item("P641", qFootball), item("P118", qNFL))
	capitals = ent("Q910000007", "Capital City Capitals", []string{"Capitals"},
		item("P31", qFBTeam), item("P641", qFootball), item("P118", qNFL))
	badgers = ent("Q910000009", "Brockway Badgers", []string{"Badgers"},
		item("P31", qFBTeam), item("P641", qFootball), item("P118", qNFL))
	mackerels = ent("Q910000011", "Springfield Mackerels", []string{"Mackerels"},
		item("P31", "Q13027888"), item("P641", qBaseball), item("P118", qMLB))
	tempests = ent("Q910000014", "Springfield Tempests football", []string{"Springfield Tempests", "Springfield"},
		item("P31", qFBTeam), item("P31", "Q18558301"), item("P641", qFootball), item("P118", qFBS))
	heat = ent("Q910000003", "Springfield Blaze", []string{"Blaze"},
		item("P31", "Q13393265"), item("P641", qBasket), item("P118", qNBA))
	pumas = ent("Q910000004", "Seaboard Pumas", nil,
		item("P31", "Q4498974"), item("P641", qHockey), item("P118", qNHL))
	texasTech = ent("Q910000015", "Western Tech Red Rovers football", nil,
		item("P31", qFBTeam), item("P641", qFootball), item("P118", qFBS))
	springfieldCity = ent("Q910000001", "Springfield", nil, item("P31", "Q910000012"))
)

func nflTeams() []knowledge.Entity {
	return []knowledge.Entity{isotopes, capitals, badgers, mackerels, tempests, heat, pumas, texasTech}
}

func ostrander(extra ...knowledge.Statement) knowledge.Entity {
	st := []knowledge.Statement{
		item("P31", qHuman), item("P106", qFBPlayer), item("P641", qFootball), item("P641", qBaseball),
		item("P413", qQB),
	}
	st = append(st, extra...)
	return ent("Q910000016", "Patrick Ostrander", nil, st...)
}

func member(team string) knowledge.Statement { return item("P54", team) }

func resolve(name string, kind entityproposal.EntityType, cands []knowledge.Candidate, es []knowledge.Entity, ctx ...string) Result {
	return Resolve(Input{
		Name: name, ExpectedKind: kind, Catalog: catalog, ContextEntityIDs: ctx,
		Candidates: cands, Entities: world(append(es, nflTeams()...)...), Now: now,
	})
}

func wantStatus(t *testing.T, r Result, s Status) {
	t.Helper()
	if r.Status != s {
		t.Fatalf("status = %q (%s), want %q; ranking=%+v", r.Status, r.Reason, s, r.Ranking)
	}
}

func wantMeta(t *testing.T, r Result, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if r.Metadata[k] != v {
			t.Errorf("metadata[%s] = %q, want %q (all=%v)", k, r.Metadata[k], v, r.Metadata)
		}
	}
}

func wantNoMeta(t *testing.T, r Result, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := r.Metadata[k]; ok {
			t.Errorf("metadata[%s] = %q, want absent", k, v)
		}
	}
}

// --- NFL player ---

func TestResolve_NFLPlayerWithPositionAndCurrentTeam(t *testing.T) {
	m := ostrander(
		span(member("Q910000015"), year(2014), year(2017)),
		span(member("Q910000007"), year(2017), nil),
	)
	surname := ent("Q1", "Ostrander", nil, item("P31", "Q910000002"))
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(surname, m), []knowledge.Entity{surname, m})
	wantStatus(t, r, StatusVerifiedNFL)
	if r.EntityType != entityproposal.EntityTypePlayer || r.CanonicalName != "Patrick Ostrander" {
		t.Errorf("identity = %s/%s", r.EntityType, r.CanonicalName)
	}
	wantMeta(t, r, map[string]string{
		"provider": "wikidata", "provider_id": "Q910000016", "sport": "american_football",
		"league": "NFL", "position": "QB", "team": "Capital City Capitals", "team_id": "capital-city-capitals",
	})
}

func TestResolve_EndedMembershipsLeaveTeamEmptyButKeepIdentity(t *testing.T) {
	m := ostrander(
		span(member("Q910000015"), year(2014), year(2017)),
		span(member("Q910000007"), year(2017), year(2024)),
	)
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
	wantStatus(t, r, StatusVerifiedNFL)
	wantNoMeta(t, r, "team", "team_id")
	wantMeta(t, r, map[string]string{"provider_id": "Q910000016", "position": "QB", "league": "NFL"})
}

func TestResolve_CurrentTeamPrefersOpenStatementOverEnded(t *testing.T) {
	m := ostrander(
		span(member("Q910000007"), year(2017), year(2021)),
		span(member("Q910000009"), year(2021), nil),
	)
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
	wantMeta(t, r, map[string]string{"team": "Brockway Badgers", "team_id": "brockway-badgers"})
}

func TestResolve_DeprecatedMembershipIsIgnored(t *testing.T) {
	m := ostrander(
		ranked(span(member("Q910000007"), year(2017), nil), knowledge.RankDeprecated),
		span(member("Q910000009"), year(2021), nil),
	)
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
	wantMeta(t, r, map[string]string{"team": "Brockway Badgers"})

	only := ostrander(ranked(span(member("Q910000007"), year(2017), nil), knowledge.RankDeprecated))
	r = resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(only), []knowledge.Entity{only})
	wantNoMeta(t, r, "team", "team_id")
}

func TestResolve_ConflictingCurrentMembershipsLeaveTeamEmpty(t *testing.T) {
	m := ostrander(
		span(member("Q910000007"), year(2017), nil),
		span(member("Q910000009"), year(2021), nil),
	)
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
	wantStatus(t, r, StatusVerifiedNFL)
	wantNoMeta(t, r, "team", "team_id")
}

func TestResolve_PreferredRankBreaksConflict(t *testing.T) {
	m := ostrander(
		span(member("Q910000007"), year(2017), nil),
		ranked(span(member("Q910000009"), year(2021), nil), knowledge.RankPreferred),
	)
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
	wantMeta(t, r, map[string]string{"team": "Brockway Badgers"})
}

func TestResolve_UndatedOrFutureMembershipIsNotCurrent(t *testing.T) {
	undated := ostrander(member("Q910000007"))
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(undated), []knowledge.Entity{undated})
	wantStatus(t, r, StatusVerifiedNFL)
	wantNoMeta(t, r, "team")

	future := ostrander(span(member("Q910000007"), year(2030), nil))
	r = resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(future), []knowledge.Entity{future})
	wantNoMeta(t, r, "team")
}

func TestResolve_ConflictingPositionsLeavePositionEmpty(t *testing.T) {
	m := ostrander(span(member("Q910000007"), year(2017), nil), item("P413", qLB))
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
	wantStatus(t, r, StatusVerifiedNFL)
	wantNoMeta(t, r, "position")
}

func TestResolve_ResponseOrderIsNotEvidenceOfRecency(t *testing.T) {
	a := span(member("Q910000007"), year(2017), year(2021))
	b := span(member("Q910000009"), year(2021), nil)
	m1 := ostrander(a, b)
	m2 := ostrander(b, a)
	r1 := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m1), []knowledge.Entity{m1})
	r2 := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m2), []knowledge.Entity{m2})
	if !reflect.DeepEqual(r1.Metadata, r2.Metadata) {
		t.Fatalf("statement order changed result: %v vs %v", r1.Metadata, r2.Metadata)
	}
}

func TestResolve_DeterministicAcrossMapOrdering(t *testing.T) {
	m := ostrander(span(member("Q910000007"), year(2017), nil))
	first := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
	for i := 0; i < 50; i++ {
		got := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, first, got)
		}
	}
}

func TestResolve_FootballPlayerWithoutTeamEvidenceIsAmbiguous(t *testing.T) {
	m := ostrander()
	r := resolve("Patrick Ostrander", entityproposal.EntityTypePlayer, candsOf(m), []knowledge.Entity{m})
	wantStatus(t, r, StatusAmbiguous)
	wantMeta(t, r, map[string]string{"provider_id": "Q910000016"})
}

func TestResolve_UnfetchedTeamEntitiesAreNotGuessed(t *testing.T) {
	m := ostrander(span(member("Q999"), year(2017), nil))
	r := Resolve(Input{
		Name: "Patrick Ostrander", ExpectedKind: entityproposal.EntityTypePlayer, Catalog: catalog,
		Candidates: candsOf(m), Entities: world(m), Now: now,
	})
	wantStatus(t, r, StatusAmbiguous)
}

// --- non-NFL ---

func TestResolve_NonNFLTeams(t *testing.T) {
	tests := []struct {
		name   string
		e      knowledge.Entity
		sport  string
		league string
	}{
		{"Springfield Mackerels", mackerels, "baseball", "MLB"},
		{"Springfield Blaze", heat, "basketball", "NBA"},
		{"Seaboard Pumas", pumas, "ice_hockey", "NHL"},
		{"Springfield Tempests football", tempests, "american_football", "NCAA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := resolve(tt.name, entityproposal.EntityTypeTeam, candsOf(tt.e), nil)
			wantStatus(t, r, StatusVerifiedNonNFL)
			wantMeta(t, r, map[string]string{"provider": "wikidata", "provider_id": tt.e.ID, "sport": tt.sport, "league": tt.league})
		})
	}
}

func TestResolve_NonFootballPersonIsNonNFL(t *testing.T) {
	p := ent("Q500", "Rollo Quayle", nil, item("P31", qHuman), item("P106", "Q3665646"), item("P641", qBasket),
		span(member("Q910000003"), year(2019), year(2024)))
	r := resolve("Rollo Quayle", entityproposal.EntityTypePlayer, candsOf(p), []knowledge.Entity{p})
	wantStatus(t, r, StatusVerifiedNonNFL)
	wantMeta(t, r, map[string]string{"sport": "basketball"})
}

func TestResolve_CollegeOnlyFootballPlayerIsNonNFL(t *testing.T) {
	p := ent("Q501", "Some Prospect", nil, item("P31", qHuman), item("P106", qFBPlayer), item("P641", qFootball),
		span(member("Q910000014"), year(2024), nil))
	r := resolve("Some Prospect", entityproposal.EntityTypePlayer, candsOf(p), []knowledge.Entity{p})
	wantStatus(t, r, StatusVerifiedNonNFL)
	wantNoMeta(t, r, "team", "team_id")
}

// --- generic names and ranking ---

func missingCity() []knowledge.Entity { return []knowledge.Entity{springfieldCity} }

func TestResolve_GenericSpringfieldIsAmbiguous(t *testing.T) {
	cands := candsOf(springfieldCity, tempests, isotopes, mackerels, heat)
	r := resolve("Springfield", entityproposal.EntityTypeTeam, cands, missingCity())
	wantStatus(t, r, StatusAmbiguous)
	if len(r.Aliases) != 0 {
		t.Errorf("generic alias must not be proposed: %v", r.Aliases)
	}
}

func TestResolve_SpringfieldResolvedByContextEntity(t *testing.T) {
	cands := candsOf(springfieldCity, tempests, isotopes, mackerels, heat)
	r := resolve("Springfield", entityproposal.EntityTypeTeam, cands, missingCity(), "springfield-isotopes", "ezo")
	wantStatus(t, r, StatusVerifiedNFL)
	wantMeta(t, r, map[string]string{"provider_id": "Q910000006", "league": "NFL"})
	if len(r.Aliases) != 0 {
		t.Errorf("single-token alias Springfield must never be proposed: %v", r.Aliases)
	}
}

func TestResolve_SameNameDifferentSports(t *testing.T) {
	nfl := ent("Q600", "Crispin Halloway", nil, item("P31", qHuman), item("P106", qFBPlayer), item("P641", qFootball),
		item("P413", "Q2550304"), span(member("Q910000007"), year(2016), nil))
	nba := ent("Q601", "Crispin Halloway", nil, item("P31", qHuman), item("P106", "Q3665646"), item("P641", qBasket),
		span(member("Q910000003"), year(2020), nil))
	cands := candsOf(nba, nfl)
	es := []knowledge.Entity{nfl, nba}

	wantStatus(t, resolve("Crispin Halloway", entityproposal.EntityTypePlayer, cands, es), StatusAmbiguous)

	r := resolve("Crispin Halloway", entityproposal.EntityTypePlayer, cands, es, "capital-city-capitals")
	wantStatus(t, r, StatusVerifiedNFL)
	wantMeta(t, r, map[string]string{"provider_id": "Q600", "position": "DT"})
}

func TestResolve_ExactLabelBeatsWeakAlias(t *testing.T) {
	exact := ent("Q700", "Ezo Quarrington", nil, item("P31", qHuman), item("P106", qFBPlayer), item("P641", qFootball),
		span(member("Q910000006"), year(2020), nil))
	weak := ent("Q701", "Ezo Quarrington Sr", []string{"Ezo Quarrington"}, item("P31", qHuman), item("P106", qFBPlayer), item("P641", qFootball),
		span(member("Q910000006"), year(1990), year(1995)))
	r := resolve("Ezo Quarrington", entityproposal.EntityTypePlayer, candsOf(weak, exact), []knowledge.Entity{exact, weak})
	wantStatus(t, r, StatusVerifiedNFL)
	wantMeta(t, r, map[string]string{"provider_id": "Q700"})
}

func TestResolve_SearchRankIsOnlyATieBreaker(t *testing.T) {
	// A strictly worse name match that the provider ranked first must not win.
	first := ent("Q800", "Ezo Quarrington Jr", []string{"Ezo Quarrington"}, item("P31", qHuman), item("P106", qFBPlayer),
		item("P641", qFootball), span(member("Q910000006"), year(2020), nil))
	second := ent("Q801", "Ezo Quarrington", nil, item("P31", qHuman), item("P106", qFBPlayer),
		item("P641", qFootball), span(member("Q910000006"), year(2020), nil))
	r := resolve("Ezo Quarrington", entityproposal.EntityTypePlayer, candsOf(first, second), []knowledge.Entity{first, second})
	wantMeta(t, r, map[string]string{"provider_id": "Q801"})
}

func TestResolve_ExpectedKindMismatchLoses(t *testing.T) {
	team := ent("Q900", "Ezo Quarrington Foundation", nil, item("P31", qFBTeam), item("P118", qNFL))
	person := ent("Q901", "Ezo Quarrington Foundation", nil, item("P31", qHuman), item("P106", qFBPlayer), item("P641", qFootball),
		span(member("Q910000006"), year(2020), nil))
	r := resolve("Ezo Quarrington Foundation", entityproposal.EntityTypePlayer, candsOf(team, person), []knowledge.Entity{team, person})
	wantMeta(t, r, map[string]string{"provider_id": "Q901"})
}

// --- coaches ---

func TestResolve_CoachWithoutCurrentTeamData(t *testing.T) {
	fennow := ent("Q910000013", "Brian Fennow", nil, item("P31", qHuman), item("P106", qFBCoach), item("P641", qFootball),
		item("P413", qLB), span(member("Q910000006"), year(2019), nil), member("Q910000009"))
	r := resolve("Brian Fennow", entityproposal.EntityTypeCoach, candsOf(fennow), []knowledge.Entity{fennow})
	wantStatus(t, r, StatusVerifiedNFL)
	if r.EntityType != entityproposal.EntityTypeCoach {
		t.Errorf("type = %s, want coach", r.EntityType)
	}
	wantMeta(t, r, map[string]string{"provider_id": "Q910000013", "league": "NFL", "sport": "american_football"})
	wantNoMeta(t, r, "team", "team_id", "role", "position")
}

func TestResolve_CoachCurrentTeamOnlyFromTeamHeadCoachStatement(t *testing.T) {
	coach := ent("Q910000013", "Brian Fennow", nil, item("P31", qHuman), item("P106", qFBCoach), item("P641", qFootball))
	comets := ent("Q123", "Cypress Comets", nil, item("P31", qFBTeam), item("P641", qFootball), item("P118", qNFL),
		span(item("P286", "Q910000013"), year(2025), nil))
	r := resolve("Brian Fennow", entityproposal.EntityTypeCoach, candsOf(coach), []knowledge.Entity{coach, comets})
	wantStatus(t, r, StatusVerifiedNFL)
	wantMeta(t, r, map[string]string{"team": "Cypress Comets", "role": "head coach"})
}

func TestResolve_CoachWithOnlyFootballOccupationIsAmbiguous(t *testing.T) {
	coach := ent("Q910000013", "Brian Fennow", nil, item("P31", qHuman), item("P106", qFBCoach), item("P641", qFootball))
	r := resolve("Brian Fennow", entityproposal.EntityTypeCoach, candsOf(coach), []knowledge.Entity{coach})
	wantStatus(t, r, StatusAmbiguous)
}

func TestResolve_NFLHeadCoachOccupationIsNFLEvidence(t *testing.T) {
	coach := ent("Q910000013", "Brian Fennow", nil, item("P31", qHuman), item("P106", qNFLHC), item("P641", qFootball))
	r := resolve("Brian Fennow", entityproposal.EntityTypeCoach, candsOf(coach), []knowledge.Entity{coach})
	wantStatus(t, r, StatusVerifiedNFL)
	wantNoMeta(t, r, "team")
}

// --- not found ---

func TestResolve_NotFound(t *testing.T) {
	wantStatus(t, resolve("Zzyzx Qwerty", entityproposal.EntityTypePlayer, nil, nil), StatusNotFound)

	unrelated := ent("Q42", "Douglas Adams", nil, item("P31", qHuman))
	wantStatus(t, resolve("Zzyzx Qwerty", entityproposal.EntityTypePlayer, candsOf(unrelated), []knowledge.Entity{unrelated}), StatusNotFound)
}

func TestResolve_NameOnlyPartOfLabelIsNotFoundWithoutContext(t *testing.T) {
	surname := ent("Q1", "Patrick Ostrander", nil, item("P31", qHuman))
	wantStatus(t, resolve("Ostrander", entityproposal.EntityTypePlayer, candsOf(surname), []knowledge.Entity{surname}), StatusNotFound)
}

// --- alias safety ---

func TestResolve_AliasSafety(t *testing.T) {
	topes := ent("Q910000006", "Springfield Isotopes", []string{"The Topes", "Springfield", "Isotopes"},
		item("P31", qFBTeam), item("P641", qFootball), item("P118", qNFL))
	// "The Topes" is owned by another local entity => collision, rejected.
	r := resolve("The Topes", entityproposal.EntityTypeTeam, candsOf(topes), []knowledge.Entity{topes}, "springfield-isotopes")
	for _, a := range r.Aliases {
		if a == "The Topes" {
			t.Fatalf("colliding alias accepted: %v", r.Aliases)
		}
	}
	if len(r.RejectedAliases) == 0 {
		t.Errorf("expected a recorded alias rejection, got none")
	}

	// A free multi-token observed alias for the same entity is accepted;
	// Wikidata aliases are never imported wholesale.
	r = resolve("Springfield Isotopes FC", entityproposal.EntityTypeTeam,
		[]knowledge.Candidate{{ID: "Q910000006", Label: "Springfield Isotopes", Aliases: []string{"Springfield Isotopes FC"}}},
		[]knowledge.Entity{topes}, "springfield-isotopes")
	wantStatus(t, r, StatusVerifiedNFL)
	if !reflect.DeepEqual(r.Aliases, []string{"Springfield Isotopes FC"}) {
		t.Errorf("aliases = %v, want only the observed text", r.Aliases)
	}
}

func TestReferencedIDs(t *testing.T) {
	m := ostrander(span(member("Q910000007"), year(2017), nil), member("Q910000009"), member("Q910000007"))
	got := ReferencedIDs(m)
	want := []string{"Q910000007", "Q910000009"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReferencedIDs = %v, want %v", got, want)
	}
}
