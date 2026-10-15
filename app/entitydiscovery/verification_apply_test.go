package entitydiscovery_test

import (
	"reflect"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

func applyCatalog() entitydiscovery.CatalogIndex {
	return entitydiscovery.BuildCatalogIndex([]eventsignals.EntityRule{
		{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, Terms: []string{"Springfield Isotopes", "Isotopes"}},
		{ID: "springfield-tempests", CanonicalName: "Springfield Tempests", Kind: eventsignals.EntityKindTeam, Terms: []string{"Springfield Tempests", "Tempests"}},
	})
}

var teams = map[string]bool{"capital-city-capitals": true, "springfield-isotopes": true}

func prop(name string, et entityproposal.EntityType, md map[string]string, aliases ...string) entityproposal.Proposal {
	return entityproposal.Proposal{
		CanonicalName: name, NormalizedName: entityproposal.Normalize(name), EntityType: et,
		Kind: entityproposal.KindNewEntity, Metadata: md, Aliases: aliases, Confidence: 0.6,
		Status:  entityproposal.StatusPending,
		Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "t"}},
	}
}

func TestApply_RejectMarlinsAuditable(t *testing.T) {
	p := prop("Springfield Mackerels", entityproposal.EntityTypeTeam, map[string]string{"team": "Mackerels"})
	p.WebSources = []entityproposal.WebSource{{Domain: "baseball.example.com"}}
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionReject, Reason: "MLB team",
		Metadata: map[string]string{"sport": "baseball", "league": "MLB"},
	}, applyCatalog(), teams)
	if got.Status != entityproposal.StatusRejected || got.DecisionReason != entityproposal.ReasonAutoRejectedNonNFL {
		t.Fatalf("status/reason = %q/%q", got.Status, got.DecisionReason)
	}
	if len(got.Sources) != 1 || len(got.WebSources) != 1 || got.Metadata["team"] != "Mackerels" ||
		got.Metadata["league"] != "MLB" || got.Metadata["sport"] != "baseball" || got.Metadata["reason"] != "MLB team" {
		t.Fatalf("evidence not kept: %+v", got)
	}
}

func TestApply_RejectSpringfieldTempests(t *testing.T) {
	p := prop("Springfield Tempests", entityproposal.EntityTypeTeam, nil)
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionReject, Metadata: map[string]string{"league": "NCAA"},
	}, applyCatalog(), teams)
	if got.Status != entityproposal.StatusRejected || got.DecisionReason != entityproposal.ReasonAutoRejectedNonNFL {
		t.Fatalf("got %+v", got)
	}
}

func TestApply_UncertainLeavesProposalUnchanged(t *testing.T) {
	p := prop("Springfield", entityproposal.EntityTypeTeam, map[string]string{"a": "b"}, "Springfield")
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{Disposition: weblookup.DispositionUncertain, Confidence: 0.99}, applyCatalog(), teams)
	if !reflect.DeepEqual(got, p) || got.Status != entityproposal.StatusPending {
		t.Fatalf("uncertain changed proposal: %+v", got)
	}
}

func TestApply_KeepEnrichesMallory(t *testing.T) {
	p := prop("Patrick Ostrander", entityproposal.EntityTypePlayer, map[string]string{"position": ""})
	ws := []entityproposal.WebSource{{Domain: "league.example.com", URL: "u"}}
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Confidence: 0.97, WebSources: ws,
		Metadata: map[string]string{"position": "QB", "team": "Capital City Capitals", "team_id": "capital-city-capitals", "sport": "football", "league": "NFL"},
	}, applyCatalog(), teams)
	if got.Metadata["position"] != "QB" || got.Metadata["team_id"] != "capital-city-capitals" || got.Metadata["league"] != "NFL" {
		t.Fatalf("metadata = %+v", got.Metadata)
	}
	if got.Confidence != 0.97 || !reflect.DeepEqual(got.WebSources, ws) || got.Status != entityproposal.StatusPending {
		t.Fatalf("got %+v", got)
	}
}

func TestApply_KeepCorrectsTypeAndName(t *testing.T) {
	p := prop("fennow", entityproposal.EntityTypePlayer, map[string]string{})
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, CanonicalName: "Brian Fennow", EntityType: entityproposal.EntityTypeCoach,
		Metadata: map[string]string{"role": "defensive coordinator"}, Confidence: 0.9,
	}, applyCatalog(), teams)
	if got.EntityType != entityproposal.EntityTypeCoach || got.CanonicalName != "Brian Fennow" || got.NormalizedName != "brian fennow" || got.Metadata["role"] != "defensive coordinator" {
		t.Fatalf("got %+v", got)
	}
}

// withAmbiguousTokens installs the host's ambiguous team tokens for one
// test (the library default is empty).
func withAmbiguousTokens(t *testing.T) {
	t.Helper()
	prev := entitydiscovery.AmbiguousTokens
	entitydiscovery.AmbiguousTokens = map[string][]string{
		"storms":      {"springfield tempests"},
		"tempests":    {"springfield tempests", "pinecrest tempests"},
		"springfield": {},
	}
	t.Cleanup(func() { entitydiscovery.AmbiguousTokens = prev })
}

func TestApply_AmbiguousTokensEmptyByDefault(t *testing.T) {
	if len(entitydiscovery.AmbiguousTokens) != 0 {
		t.Fatalf("AmbiguousTokens = %v, want empty by default", entitydiscovery.AmbiguousTokens)
	}
	p := prop("Jory Ravenscroft", entityproposal.EntityTypePlayer, nil, "Storms", "JD")
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Confidence: 0.9}, applyCatalog(), teams)
	if !reflect.DeepEqual(got.Aliases, []string{"Storms", "JD"}) {
		t.Fatalf("aliases = %v, want both kept without host tokens", got.Aliases)
	}
}

func TestApply_KeepScrubsCrossTeamAliases(t *testing.T) {
	withAmbiguousTokens(t)
	p := prop("Jory Ravenscroft", entityproposal.EntityTypePlayer, nil, "Springfield", "Storms", "Tempests", "Springfield Isotopes", "JD")
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Confidence: 0.9}, applyCatalog(), teams)
	if !reflect.DeepEqual(got.Aliases, []string{"JD"}) {
		t.Fatalf("aliases = %v, want [JD]", got.Aliases)
	}
}

func TestApply_KeepOwnCatalogAliasSurvives(t *testing.T) {
	p := prop("Springfield Isotopes", entityproposal.EntityTypeTeam, nil, "Isotopes")
	p.TargetEntityID = "springfield-isotopes"
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Confidence: 0.9}, applyCatalog(), teams)
	if !reflect.DeepEqual(got.Aliases, []string{"Isotopes"}) {
		t.Fatalf("aliases = %v", got.Aliases)
	}
}

func TestApply_UnknownTeamIDDroppedTeamNameKept(t *testing.T) {
	// An unresolvable team keeps its name (so approval can refuse it
	// instead of defaulting to the Isotopes dictionary) but never an
	// invented team_id.
	p := prop("X Y", entityproposal.EntityTypePlayer, nil)
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Metadata: map[string]string{"team": "Nowhere", "team_id": "nowhere", "position": "WR"},
	}, applyCatalog(), teams)
	if _, ok := got.Metadata["team_id"]; ok {
		t.Fatalf("team_id kept: %+v", got.Metadata)
	}
	if got.Metadata["team"] != "Nowhere" || got.Metadata["position"] != "WR" {
		t.Fatalf("metadata = %+v", got.Metadata)
	}
}

func leagueCatalog() entitydiscovery.CatalogIndex {
	return entitydiscovery.BuildCatalogIndex([]eventsignals.EntityRule{
		{ID: "springfield-isotopes", CanonicalName: "Springfield Isotopes", Kind: eventsignals.EntityKindTeam, Terms: []string{"Springfield Isotopes", "Isotopes"}},
		{ID: "capital-city-capitals", CanonicalName: "Capital City Capitals", Kind: eventsignals.EntityKindTeam, Terms: []string{"Capital City Capitals", "Capitals"}},
		{ID: "pierce-quenby", CanonicalName: "Pierce Quenby", Kind: eventsignals.EntityKindPlayer, Terms: []string{"Pierce Quenby"}},
	})
}

// The LLM has to guess team_id; a wrong guess must be repaired from the
// team name instead of dropping the team.
func TestApply_TeamIDResolvedFromTeamName(t *testing.T) {
	cat := leagueCatalog()
	for _, tc := range []struct {
		name, teamID, team, wantID string
	}{
		{"CAP guess", "CAP", "Capital City Capitals", "capital-city-capitals"},
		{"capitals guess", "capitals", "Capital City Capitals", "capital-city-capitals"},
		{"missing id, short name", "", "Capitals", "capital-city-capitals"},
		{"valid id untouched", "springfield-isotopes", "Capital City Capitals", "springfield-isotopes"},
		{"non-team term never resolves", "pp", "Pierce Quenby", ""},
		{"unknown team", "nope", "Nowhere FC", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := prop("Patrick Ostrander", entityproposal.EntityTypePlayer, nil)
			got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
				Disposition: weblookup.DispositionKeep, Confidence: 0.9,
				Metadata: map[string]string{"team": tc.team, "team_id": tc.teamID},
			}, cat, cat.TeamIDs())
			if got.Metadata["team_id"] != tc.wantID {
				t.Fatalf("team_id = %q, want %q (%+v)", got.Metadata["team_id"], tc.wantID, got.Metadata)
			}
			if got.Metadata["team"] != tc.team {
				t.Fatalf("team = %q, want %q kept", got.Metadata["team"], tc.team)
			}
		})
	}
}

// A team keep must name a specific, new team: a made-up team_id cannot
// bypass the generic-name guard and an existing team is never proposed
// again as new_entity (finding 5).
func TestApply_TeamKeepGuards(t *testing.T) {
	cat := leagueCatalog()
	for _, tc := range []struct {
		name    string
		p       entityproposal.Proposal
		md      map[string]string
		applied bool
	}{
		{"generic name with made-up team_id", prop("Springfield", entityproposal.EntityTypeTeam, nil), map[string]string{"team_id": "spr", "league": "NFL"}, false},
		{"generic name without team_id", prop("Springfield", entityproposal.EntityTypeTeam, nil), map[string]string{"league": "NFL"}, false},
		{"existing team via team_id", prop("Springfield", entityproposal.EntityTypeTeam, nil), map[string]string{"team_id": "springfield-isotopes"}, false},
		{"existing team via team name", prop("CAP", entityproposal.EntityTypeTeam, nil), map[string]string{"team": "Capital City Capitals"}, false},
		{"specific new team", prop("Brockway Badgers", entityproposal.EntityTypeTeam, nil), map[string]string{"league": "NFL"}, true},
		{"alias proposal for the same team", func() entityproposal.Proposal {
			p := prop("Springfield Isotopes", entityproposal.EntityTypeTeam, nil, "Topes")
			p.Kind, p.TargetEntityID = entityproposal.KindNewAlias, "springfield-isotopes"
			return p
		}(), map[string]string{"team_id": "springfield-isotopes"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := entitydiscovery.ApplyVerification(tc.p, entitydiscovery.VerifyResult{
				Disposition: weblookup.DispositionKeep, Confidence: 0.99, Metadata: tc.md,
			}, cat, cat.TeamIDs())
			if applied := !reflect.DeepEqual(got, tc.p); applied != tc.applied {
				t.Fatalf("applied = %v, want %v: %+v", applied, tc.applied, got)
			}
		})
	}
}

// A rename keeps the pre-verification surface form as an alias (finding 3).
func TestApply_KeepRenameKeepsOldNameAsAlias(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     []string
	}{
		{"Ostrander", "Patrick Ostrander", []string{"Ostrander"}},
		{"fennow", "Brian Fennow", []string{"fennow"}},
		{"patrick ostrander", "Patrick Ostrander", nil},
	} {
		p := prop(tc.from, entityproposal.EntityTypePlayer, nil)
		got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
			Disposition: weblookup.DispositionKeep, CanonicalName: tc.to, Confidence: 0.9,
		}, applyCatalog(), teams)
		if len(got.Aliases) != len(tc.want) || (len(tc.want) > 0 && !reflect.DeepEqual(got.Aliases, tc.want)) {
			t.Fatalf("%s -> %s aliases = %v, want %v", tc.from, tc.to, got.Aliases, tc.want)
		}
	}
}

func TestApply_IdempotentAndNoInputMutation(t *testing.T) {
	p := prop("Jory Ravenscroft", entityproposal.EntityTypePlayer, map[string]string{"x": "y"}, "Springfield", "JD")
	orig := prop("Jory Ravenscroft", entityproposal.EntityTypePlayer, map[string]string{"x": "y"}, "Springfield", "JD")
	vr := entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Confidence: 0.9, Metadata: map[string]string{"team_id": "springfield-isotopes", "team": "Springfield Isotopes"}}
	once := entitydiscovery.ApplyVerification(p, vr, applyCatalog(), teams)
	twice := entitydiscovery.ApplyVerification(once, vr, applyCatalog(), teams)
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("not idempotent:\n%+v\n%+v", once, twice)
	}
	if !reflect.DeepEqual(p, orig) {
		t.Fatalf("input mutated: %+v", p)
	}
	rej := entitydiscovery.VerifyResult{Disposition: weblookup.DispositionReject}
	r1 := entitydiscovery.ApplyVerification(p, rej, applyCatalog(), teams)
	if r2 := entitydiscovery.ApplyVerification(r1, rej, applyCatalog(), teams); !reflect.DeepEqual(r1, r2) || !reflect.DeepEqual(p, orig) {
		t.Fatal("reject not idempotent or mutated input")
	}
}

func TestApply_GenericSpringfieldDroppedFromVerifiedIsotopes(t *testing.T) {
	withAmbiguousTokens(t)
	p := prop("Springfield Isotopes", entityproposal.EntityTypeTeam, nil, "Springfield", "Isos")
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Confidence: 0.9}, applyCatalog(), teams)
	if !reflect.DeepEqual(got.Aliases, []string{"Isos"}) {
		t.Fatalf("aliases = %v, want [Isos]", got.Aliases)
	}
}

func TestApply_AliasDroppedOnlyByTargetMismatch(t *testing.T) {
	// "Isotopes" is not in the token map; it is dropped solely because its
	// catalog owner (springfield-isotopes) differs from the proposal's target.
	p := prop("Springfield Isotopes", entityproposal.EntityTypeTeam, nil, "Isotopes", "Isos")
	p.TargetEntityID = "some-other-id"
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{Disposition: weblookup.DispositionKeep, Confidence: 0.9}, applyCatalog(), teams)
	if !reflect.DeepEqual(got.Aliases, []string{"Isos"}) {
		t.Fatalf("aliases = %v, want [Isos]", got.Aliases)
	}
}

func keepPlayer(p entityproposal.Proposal, md map[string]string) entityproposal.Proposal {
	return entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Confidence: 0.9, Metadata: md,
	}, applyCatalog(), teams)
}

func TestApply_KeepVerifiedFreeAgentHasNoTeam(t *testing.T) {
	p := prop("Example Player", entityproposal.EntityTypePlayer, map[string]string{"team": "Capital City Capitals", "team_id": "capital-city-capitals"})
	got := keepPlayer(p, map[string]string{"position": "WR", "league": "NFL", "sport": "football", "affiliation_status": "free_agent"})
	if got.Metadata["affiliation_status"] != "free_agent" || got.Metadata["team"] != "" || got.Metadata["team_id"] != "" || got.Metadata["position"] != "WR" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
	got.Kind, got.Confidence = entityproposal.KindNewAlias, 0.95
	if entitydiscovery.ShouldVerify(got, 0.85) {
		t.Fatal("verified free agent must not be re-verified")
	}
}

func TestApply_FreeAgentWithTeamEvidenceBecomesUnknown(t *testing.T) {
	p := prop("Example Player", entityproposal.EntityTypePlayer, nil)
	got := keepPlayer(p, map[string]string{"position": "WR", "affiliation_status": "free_agent", "team_id": "capital-city-capitals"})
	if got.Metadata["affiliation_status"] != "unknown" || got.Metadata["team_id"] != "" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
}

func TestApply_FreeAgentOnNonPlayerDropped(t *testing.T) {
	p := prop("Some Coach", entityproposal.EntityTypeCoach, nil)
	got := keepPlayer(p, map[string]string{"role": "HC", "affiliation_status": "free_agent"})
	if got.Metadata["affiliation_status"] != "" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
}

func TestApply_TeamResolvedMarksTeamAndSupersedesStaleStatus(t *testing.T) {
	p := prop("Example Player", entityproposal.EntityTypePlayer, map[string]string{"affiliation_status": "free_agent"})
	got := keepPlayer(p, map[string]string{"team_id": "capital-city-capitals", "team": "Capital City Capitals"})
	if got.Metadata["affiliation_status"] != "team" || got.Metadata["team_id"] != "capital-city-capitals" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
}

func TestApply_UnresolvableTeamNeverBecomesTeamStatus(t *testing.T) {
	p := prop("Example Player", entityproposal.EntityTypePlayer, nil)
	got := keepPlayer(p, map[string]string{"team": "Nowhere FC", "team_id": "nowhere-fc", "affiliation_status": "team"})
	if got.Metadata["affiliation_status"] == "team" || got.Metadata["team_id"] != "" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
}

func TestApply_InvalidStatusDropped(t *testing.T) {
	p := prop("Example Player", entityproposal.EntityTypePlayer, nil)
	got := keepPlayer(p, map[string]string{"position": "WR", "affiliation_status": "retired"})
	if _, ok := got.Metadata["affiliation_status"]; ok {
		t.Fatalf("metadata = %v", got.Metadata)
	}
}

func TestApplyVerification_CopiesProvenanceOnKeepAndReject(t *testing.T) {
	prov := entitydiscovery.Provenance{Provider: "wikidata", ProviderID: "Q910000016", Version: "wikidata-v1"}
	base := entityproposal.Proposal{
		CanonicalName: "Patrick Ostrander", NormalizedName: "patrick ostrander",
		EntityType: entityproposal.EntityTypePlayer, Metadata: map[string]string{},
		Status: entityproposal.StatusPending,
	}
	for _, d := range []weblookup.Disposition{weblookup.DispositionKeep, weblookup.DispositionReject} {
		got := entitydiscovery.ApplyVerification(base, entitydiscovery.VerifyResult{
			Disposition: d, Resolved: true, CanonicalName: "Patrick Ostrander",
			EntityType: entityproposal.EntityTypePlayer, Provenance: prov,
		}, applyCatalog(), nil)
		want := entityproposal.Verification{Provider: "wikidata", ProviderID: "Q910000016", Version: "wikidata-v1"}
		if got.Verification != want {
			t.Errorf("%s: verification = %+v, want %+v", d, got.Verification, want)
		}
	}
	unchanged := entitydiscovery.ApplyVerification(base, entitydiscovery.VerifyResult{Disposition: weblookup.DispositionUncertain, Provenance: prov}, applyCatalog(), nil)
	if unchanged.Verification != (entityproposal.Verification{}) {
		t.Errorf("uncertain must not record provenance: %+v", unchanged.Verification)
	}
}

// M2: a Keep without web sources (e.g. Wikidata) keeps existing evidence.
func TestApply_KeepWithoutWebSourcesKeepsExisting(t *testing.T) {
	p := prop("Patrick Ostrander", entityproposal.EntityTypePlayer, map[string]string{"position": ""})
	ws := []entityproposal.WebSource{{Domain: "league.example.com", URL: "u"}}
	p.WebSources = ws
	got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionKeep, Confidence: 0.97, Resolved: true,
		Metadata: map[string]string{"league": "NFL"},
	}, applyCatalog(), teams)
	if !reflect.DeepEqual(got.WebSources, ws) {
		t.Fatalf("WebSources = %+v, want existing kept", got.WebSources)
	}
}

func TestApplyVerification_RejectUsesTheResultsDecisionReason(t *testing.T) {
	p := entityproposal.Proposal{CanonicalName: "Northern", NormalizedName: "northern", EntityType: entityproposal.EntityTypeTeam, Status: entityproposal.StatusPending, Metadata: map[string]string{}}
	for _, tc := range []struct{ reason, want string }{
		{"", entityproposal.ReasonAutoRejectedNonNFL},
		{entityproposal.ReasonNotAnNFLTeam, entityproposal.ReasonNotAnNFLTeam},
		{entityproposal.ReasonAutoRejectedNoise, entityproposal.ReasonAutoRejectedNoise},
	} {
		got := entitydiscovery.ApplyVerification(p, entitydiscovery.VerifyResult{Disposition: weblookup.DispositionReject, Resolved: true, DecisionReason: tc.reason}, entitydiscovery.CatalogIndex{}, nil)
		if got.Status != entityproposal.StatusRejected || got.DecisionReason != tc.want {
			t.Errorf("reason %q -> status %s reason %q, want rejected/%q", tc.reason, got.Status, got.DecisionReason, tc.want)
		}
	}
}

func TestApplySuggestions_WritesAndReplacesOnlySuggestedKeys(t *testing.T) {
	p := entityproposal.Proposal{Metadata: map[string]string{"league": "NFL", "suggested_entity": "Old Name", "suggested_basis": "typo"}}
	got := entitydiscovery.ApplySuggestions(p, map[string]string{"suggested_entity": "Dorian Burns", "suggested_team": "WAV", "suggested_basis": "unique_surname"})
	if got.Metadata["suggested_entity"] != "Dorian Burns" || got.Metadata["suggested_team"] != "WAV" || got.Metadata["league"] != "NFL" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
	if p.Metadata["suggested_entity"] != "Old Name" {
		t.Fatal("ApplySuggestions must not mutate its input")
	}
	cleared := entitydiscovery.ApplySuggestions(got, nil)
	if _, ok := cleared.Metadata["suggested_entity"]; ok || cleared.Metadata["league"] != "NFL" {
		t.Fatalf("nil suggestions must clear stale suggested_* keys only, got %v", cleared.Metadata)
	}
}

func TestApplySuggestions_WorksOnAProposalWithNilMetadata(t *testing.T) {
	got := entitydiscovery.ApplySuggestions(entityproposal.Proposal{}, map[string]string{"suggested_entity": "Dorian Burns"})
	if got.Metadata["suggested_entity"] != "Dorian Burns" {
		t.Fatalf("metadata = %v", got.Metadata)
	}
}
