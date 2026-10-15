package entitydiscovery

import (
	"context"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityidentity"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

// Wikidata can only say "team" or "unknown"; the trusted
// fallback is the only way to promote unknown to free_agent or a team.

var trustedSrc = []entityproposal.WebSource{{Domain: "league.example.com", URL: "https://league.example.com/x", Title: "t"}}

func unknownAffiliationResolve(entityidentity.Input) entityidentity.Result {
	return entityidentity.Result{
		Status: entityidentity.StatusVerifiedNFL, Reason: "nfl_membership", CanonicalName: "Pat Test",
		EntityType: entityproposal.EntityTypePlayer,
		Metadata: map[string]string{
			"provider": "wikidata", "provider_id": "Q1", "league": "NFL", "sport": "american_football",
			"affiliation_status": "unknown",
		},
		AffiliationReason: entityidentity.ReasonAffNoCurrent,
	}
}

func affiliationChain(fb *fakeFallback) *ChainVerifier {
	c := newChain(&fakeKnowledge{cands: nil}, fb, entityidentity.StatusVerifiedNFL)
	c.Resolve = unknownAffiliationResolve
	return c
}

func keepWith(md map[string]string, sources []entityproposal.WebSource) VerifyResult {
	return VerifyResult{
		Disposition: weblookup.DispositionKeep, Resolved: true, EntityType: entityproposal.EntityTypePlayer,
		CanonicalName: "Someone Else", Metadata: md, WebSources: sources,
	}
}

func assertAffiliation(t *testing.T, vr VerifyResult, status, team, teamID string) {
	t.Helper()
	md := vr.Metadata
	if md["affiliation_status"] != status || md["team"] != team || md["team_id"] != teamID {
		t.Fatalf("affiliation = %q/%q/%q, want %q/%q/%q", md["affiliation_status"], md["team"], md["team_id"], status, team, teamID)
	}
}

func TestChainAffiliation_TrustedFreeAgentPromotesUnknown(t *testing.T) {
	fb := &fakeFallback{res: keepWith(map[string]string{"affiliation_status": "free_agent"}, trustedSrc)}
	vr := affiliationChain(fb).Verify(context.Background(), cand)
	assertAffiliation(t, vr, "free_agent", "", "")
	if fb.calls != 1 || vr.Provenance.Provider != "wikidata" || vr.CanonicalName != "Pat Test" {
		t.Fatalf("calls=%d provenance=%+v name=%q", fb.calls, vr.Provenance, vr.CanonicalName)
	}
	if len(vr.WebSources) != 1 {
		t.Errorf("trusted evidence not kept: %+v", vr.WebSources)
	}
}

func TestChainAffiliation_TrustedTeamPromotesUnknown(t *testing.T) {
	fb := &fakeFallback{res: keepWith(map[string]string{
		"affiliation_status": "team", "team": "Capital City Capitals", "team_id": "capital-city-capitals",
	}, trustedSrc)}
	vr := affiliationChain(fb).Verify(context.Background(), cand)
	assertAffiliation(t, vr, "team", "Capital City Capitals", "capital-city-capitals")
}

func TestChainAffiliation_NoTrustedEvidenceStaysUnknown(t *testing.T) {
	for name, res := range map[string]VerifyResult{
		"fallback uncertain": {Disposition: weblookup.DispositionUncertain, Reason: "no trusted evidence"},
		"llm claim, uncertain": {
			Disposition: weblookup.DispositionUncertain,
			Metadata:    map[string]string{"affiliation_status": "free_agent"},
		},
		"llm claim, resolved without sources": keepWith(map[string]string{"affiliation_status": "free_agent"}, nil),
		"unresolved keep":                     {Disposition: weblookup.DispositionKeep, Metadata: map[string]string{"affiliation_status": "free_agent"}, WebSources: trustedSrc},
	} {
		t.Run(name, func(t *testing.T) {
			fb := &fakeFallback{res: res}
			vr := affiliationChain(fb).Verify(context.Background(), cand)
			assertAffiliation(t, vr, "unknown", "", "")
			if vr.Metadata["affiliation_status"] == "free_agent" {
				t.Fatal("free_agent persisted without trusted evidence")
			}
		})
	}
}

func TestChainAffiliation_TeamFromWikidataSkipsFallback(t *testing.T) {
	fb := &fakeFallback{res: keepWith(map[string]string{"affiliation_status": "free_agent"}, trustedSrc)}
	c := affiliationChain(fb)
	c.Resolve = func(in entityidentity.Input) entityidentity.Result {
		r := unknownAffiliationResolve(in)
		r.Metadata["affiliation_status"], r.Metadata["team"], r.Metadata["team_id"] = "team", "Springfield Isotopes", "springfield-isotopes"
		return r
	}
	vr := c.Verify(context.Background(), cand)
	assertAffiliation(t, vr, "team", "Springfield Isotopes", "springfield-isotopes")
	if fb.calls != 0 {
		t.Fatalf("fallback called %d times for a resolved team", fb.calls)
	}
}

func TestChainAffiliation_FallbackDisabledStaysUnknown(t *testing.T) {
	vr := affiliationChain(nil).Verify(context.Background(), cand)
	assertAffiliation(t, vr, "unknown", "", "")
}

func TestChainAffiliation_FallbackTeamWithoutIDIsNotTeam(t *testing.T) {
	fb := &fakeFallback{res: keepWith(map[string]string{"affiliation_status": "team", "team": "Somewhere FC"}, trustedSrc)}
	vr := affiliationChain(fb).Verify(context.Background(), cand)
	assertAffiliation(t, vr, "unknown", "", "")
}

func TestChainAffiliation_UnknownReasonReachesProvenance(t *testing.T) {
	vr := affiliationChain(nil).Verify(context.Background(), cand)
	want := "nfl_membership; wikidata_affiliation=no_current_membership"
	if vr.Provenance.Reason != want {
		t.Fatalf("provenance reason = %q, want %q", vr.Provenance.Reason, want)
	}
}
