package entitydiscovery

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

// Fixture builders duplicated from domain/entityidentity/identity_test.go
// (unexported there).

func item(prop, qid string) knowledge.Statement {
	return knowledge.Statement{
		Property: prop, Rank: knowledge.RankNormal,
		Value: knowledge.Value{Kind: knowledge.KindEntity, EntityID: qid},
	}
}

func ent(id, label string, aliases []string, stmts ...knowledge.Statement) knowledge.Entity {
	return knowledge.Entity{ID: id, Label: label, Aliases: aliases, Statements: stmts}
}

func candsOf(es ...knowledge.Entity) []knowledge.Candidate {
	out := make([]knowledge.Candidate, 0, len(es))
	for _, e := range es {
		out = append(out, knowledge.Candidate{ID: e.ID, Label: e.Label, Aliases: e.Aliases})
	}
	return out
}

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
	qQB       = "Q622747"
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

// fixtureChain builds a ChainVerifier whose search returns hits and whose
// Get can serve hits plus extra (team) entities. The real resolver runs.
func fixtureChain(hits []knowledge.Entity, extra ...knowledge.Entity) (*ChainVerifier, *fakeFallback) {
	k := &fakeKnowledge{entities: map[string]knowledge.Entity{}, cands: candsOf(hits...)}
	for _, e := range append(append([]knowledge.Entity{}, hits...), extra...) {
		k.entities[e.ID] = e
	}
	fb := &fakeFallback{res: VerifyResult{Disposition: weblookup.DispositionUncertain}}
	return &ChainVerifier{
		Knowledge: k, Fallback: fb, Catalog: BuildCatalogIndex(nil),
		Now: func() time.Time { return time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC) },
	}, fb
}

func TestChainFixture_PatrickMalloryEnriched(t *testing.T) {
	started := member("Q910000007")
	started.ValidFrom = &time.Time{}
	*started.ValidFrom = time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC)
	c, fb := fixtureChain([]knowledge.Entity{ostrander(started)}, nflTeams()...)
	c.Catalog = BuildCatalogIndex([]eventsignals.EntityRule{
		{ID: "capital-city-capitals", CanonicalName: "Capital City Capitals", Kind: eventsignals.EntityKindTeam, Terms: []string{"Capital City Capitals"}},
	})
	vr := c.Verify(context.Background(), VerifyCandidate{Name: "Patrick Ostrander", Type: entityproposal.EntityTypePlayer})
	if vr.Disposition != weblookup.DispositionKeep || fb.calls != 0 {
		t.Fatalf("vr=%+v fallback=%d", vr, fb.calls)
	}
	if vr.Metadata["affiliation_status"] != "team" || vr.Metadata["team_id"] != "capital-city-capitals" {
		t.Fatalf("affiliation metadata = %+v", vr.Metadata)
	}
	if vr.Metadata["league"] != "NFL" || vr.Metadata["provider_id"] != "Q910000016" {
		t.Fatalf("metadata = %+v", vr.Metadata)
	}
}

func TestChainFixture_MarlinsRejected(t *testing.T) {
	c, fb := fixtureChain([]knowledge.Entity{mackerels}, nflTeams()...)
	vr := c.Verify(context.Background(), VerifyCandidate{Name: "Springfield Mackerels", Type: entityproposal.EntityTypeTeam})
	if vr.Disposition != weblookup.DispositionReject || fb.calls != 0 {
		t.Fatalf("vr=%+v fallback=%d", vr, fb.calls)
	}
}

// Mirrors TestResolve_NonFootballTeams in entityidentity: the Tempests
// football entity resolves to verified_non_nfl (NCAA), i.e. Reject.
func TestChainFixture_SpringfieldTempestsRejectedPerPolicy(t *testing.T) {
	for _, name := range []string{"Springfield Tempests football"} {
		c, fb := fixtureChain([]knowledge.Entity{tempests}, nflTeams()...)
		vr := c.Verify(context.Background(), VerifyCandidate{Name: name, Type: entityproposal.EntityTypeTeam})
		if vr.Disposition != weblookup.DispositionReject || fb.calls != 0 {
			t.Fatalf("%q: vr=%+v fallback=%d", name, vr, fb.calls)
		}
		if vr.Metadata["league"] != "NCAA" {
			t.Errorf("%q: metadata = %+v", name, vr.Metadata)
		}
	}
}

func TestChainFixture_BareSpringfieldAmbiguous(t *testing.T) {
	c, fb := fixtureChain([]knowledge.Entity{springfieldCity, tempests, isotopes, mackerels, heat}, nflTeams()...)
	vr := c.Verify(context.Background(), VerifyCandidate{Name: "Springfield", Type: entityproposal.EntityTypeTeam})
	if vr.Resolved || fb.calls != 1 {
		t.Fatalf("vr=%+v fallback=%d", vr, fb.calls)
	}
}

func TestChainFixture_AliasCollisionNotPersisted(t *testing.T) {
	c, _ := fixtureChain([]knowledge.Entity{ostrander(member("Q910000007"))}, nflTeams()...)
	vr := c.Verify(context.Background(), VerifyCandidate{Name: "Patrick Ostrander", Type: entityproposal.EntityTypePlayer})
	catalog := BuildCatalogIndex([]eventsignals.EntityRule{{
		ID: "x", CanonicalName: "Other Person", Kind: eventsignals.EntityKindPlayer,
		Terms: []string{"Other Person", "Ostrander"},
	}})
	p := entityproposal.Proposal{
		CanonicalName: "Patrick Ostrander", NormalizedName: entityproposal.Normalize("Patrick Ostrander"),
		EntityType: entityproposal.EntityTypePlayer, Kind: entityproposal.KindNewEntity,
		Aliases: []string{"Ostrander"}, Status: entityproposal.StatusPending,
	}
	out := ApplyVerification(p, vr, catalog, nil)
	for _, a := range out.Aliases {
		if a == "Ostrander" {
			t.Fatalf("colliding alias persisted: %v", out.Aliases)
		}
	}
}
