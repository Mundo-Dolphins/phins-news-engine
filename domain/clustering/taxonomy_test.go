package clustering_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// syntheticTaxonomy declares neutral event types and signals so these tests
// prove clustering reads behaviour from the taxonomy, not from type names.
func syntheticTaxonomy() eventsignals.Taxonomy {
	return eventsignals.Taxonomy{
		EventTypes: []eventsignals.EventTypeDef{
			{Name: "alpha", Flags: []eventsignals.TypeFlag{eventsignals.FlagTeamLevelNeedsOpponent}},
			{Name: "beta", Flags: []eventsignals.TypeFlag{eventsignals.FlagReferential}},
			{Name: "delta"},
			{Name: "epsilon", MemberRole: "analysis"},
			{Name: "eta", MemberRole: "correction"},
		},
		Signals: []eventsignals.SignalDef{
			{Name: "gamma", EventType: "delta", Precedence: eventsignals.PrecedenceAfterAction, Flags: []eventsignals.SignalFlag{eventsignals.FlagSpeculative}},
			{Name: "theta", EventType: "delta", Precedence: eventsignals.PrecedenceAfterAction},
			{Name: "zeta", EventType: "eta", Precedence: eventsignals.PrecedenceBeforeAction},
		},
	}
}

func syntheticConfig() clustering.Config {
	cfg := testConfig()
	cfg.Taxonomy = syntheticTaxonomy()
	return cfg
}

func teamLevelRep(articleID, sourceID string, eventType eventsignals.EventType) eventsignals.Representation {
	rep := baseRepresentation(articleID, sourceID, "Team", eventType, "act-a", fixedNow)
	rep.PrimaryTeam = "Team"
	rep.Opponent = "Rivals"
	return rep
}

func TestCompare_TeamLevelNeedsOpponentFlag_GrantsIdentity(t *testing.T) {
	cfg := syntheticConfig()

	flagged := clustering.Compare(teamLevelRep("in", "s1", "alpha"),
		clustering.Candidate{ArticleID: "c", ClusterID: "k", Representation: teamLevelRep("c", "s2", "alpha")}, cfg)
	if flagged.Relation == clustering.RelationDifferent {
		t.Fatalf("flagged team-level pair: Relation = %q (evidence %v), want a join relation", flagged.Relation, flagged.Evidence)
	}

	plain := clustering.Compare(teamLevelRep("in", "s1", "delta"),
		clustering.Candidate{ArticleID: "c", ClusterID: "k", Representation: teamLevelRep("c", "s2", "delta")}, cfg)
	if plain.Relation != clustering.RelationDifferent {
		t.Fatalf("unflagged team-level pair: Relation = %q, want %q", plain.Relation, clustering.RelationDifferent)
	}
}

func TestCompare_ReferentialFlag_CompatibleButNoIdentityByType(t *testing.T) {
	cfg := syntheticConfig()

	// Compatible with any type: beta vs delta still scores an event type match.
	incoming := baseRepresentation("in", "s1", "Pat Doe", "beta", "act-a", fixedNow)
	cand := baseRepresentation("c", "s2", "Pat Doe", "delta", "act-a", fixedNow)
	c := clustering.Compare(incoming, clustering.Candidate{ArticleID: "c", ClusterID: "k", Representation: cand}, cfg)
	if !c.EventTypeMatch {
		t.Fatalf("beta vs delta: EventTypeMatch = false (evidence %v), want true", c.Evidence)
	}

	// A shared referential type with different actions is not an identity.
	incoming = baseRepresentation("in", "s1", "Pat Doe", "beta", "act-a", fixedNow)
	cand = baseRepresentation("c", "s2", "Pat Doe", "beta", "act-b", fixedNow)
	c = clustering.Compare(incoming, clustering.Candidate{ArticleID: "c", ClusterID: "k", Representation: cand}, cfg)
	if c.Relation != clustering.RelationDifferent || len(c.Evidence) == 0 ||
		!strings.HasPrefix(string(c.Evidence[0]), "insufficient event identity") {
		t.Fatalf("beta/beta different actions: Relation = %q evidence %v, want insufficient identity", c.Relation, c.Evidence)
	}

	// Control: a shared non-referential type with different actions is.
	incoming = baseRepresentation("in", "s1", "Pat Doe", "delta", "act-a", fixedNow)
	cand = baseRepresentation("c", "s2", "Pat Doe", "delta", "act-b", fixedNow)
	c = clustering.Compare(incoming, clustering.Candidate{ArticleID: "c", ClusterID: "k", Representation: cand}, cfg)
	if c.Relation == clustering.RelationDifferent {
		t.Fatalf("delta/delta different actions: Relation = %q evidence %v, want a join relation", c.Relation, c.Evidence)
	}
}

func TestRankRepresentative_SpeculativeSignalHalvesVerifiability(t *testing.T) {
	cfg := clustering.RepresentativeConfig{VerifiabilityWeight: 1, Taxonomy: syntheticTaxonomy()}
	rep := func(signal eventsignals.ContentSignal) clustering.RepresentativeCandidate {
		return clustering.RepresentativeCandidate{
			ArticleID: string(signal),
			Representation: eventsignals.Representation{
				Opponent: "Rivals", Competition: "League", PrimaryTeam: "Team",
				Entities: []string{"Pat Doe", "Lee Roe"}, ContentSignal: signal,
			},
			Editorial: neutralEditorial,
		}
	}

	scores := clustering.RankRepresentative([]clustering.RepresentativeCandidate{rep("gamma"), rep("theta")}, cfg, fixedNow)
	byID := map[string]float64{}
	for _, s := range scores {
		byID[s.ArticleID] = s.Score
	}
	if byID["theta"] != 1 || byID["gamma"] != 0.5 {
		t.Fatalf("scores = %v, want theta 1 and gamma 0.5", byID)
	}
}

func TestDecideJoin_MemberRole_SignalCorrectionBeatsTypeAnalysis(t *testing.T) {
	cfg := syntheticConfig()
	cand := clustering.Candidate{
		ArticleID: "c", ClusterID: "k",
		Representation: baseRepresentation("c", "s2", "Pat Doe", "epsilon", "act-a", fixedNow),
	}

	incoming := baseRepresentation("in", "s1", "Pat Doe", "epsilon", "act-a", fixedNow)
	if d := clustering.DecideJoin(incoming, []clustering.Candidate{cand}, cfg, fixedNow); d.MemberRole != clustering.MemberRoleAnalysis {
		t.Fatalf("type role only: MemberRole = %q (action %q), want %q", d.MemberRole, d.Action, clustering.MemberRoleAnalysis)
	}

	incoming.ContentSignal = "zeta"
	if d := clustering.DecideJoin(incoming, []clustering.Candidate{cand}, cfg, fixedNow); d.MemberRole != clustering.MemberRoleCorrection {
		t.Fatalf("type analysis + signal correction: MemberRole = %q (action %q), want %q", d.MemberRole, d.Action, clustering.MemberRoleCorrection)
	}
}
