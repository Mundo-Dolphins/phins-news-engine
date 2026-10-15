// app/entitydiscovery/league_evaluate_test.go
package entitydiscovery_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

type evalLister struct{ proposals []entityproposal.Proposal }

func (l evalLister) List(_ context.Context, entityType, status string) ([]entityproposal.Proposal, error) {
	var out []entityproposal.Proposal
	for _, p := range l.proposals {
		if (entityType == "" || string(p.EntityType) == entityType) && (status == "" || string(p.Status) == status) {
			out = append(out, p)
		}
	}
	return out, nil
}

func pendingNFL(canonical string, aliases ...string) entityproposal.Proposal {
	return entityproposal.Proposal{
		ID: entityproposal.Normalize(canonical), CanonicalName: canonical, NormalizedName: entityproposal.Normalize(canonical),
		EntityType: entityproposal.EntityTypePlayer, Status: entityproposal.StatusPending, Aliases: aliases,
		Metadata: map[string]string{"league": "NFL"}, Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "t"}},
	}
}

func TestEvaluateLeague_ScoresTiersAndOutsideRosterAbstentions(t *testing.T) {
	stage := leagueStage(t, leaguePlayers()) // Jalen Falls SHE, Nick Kicker CAP, Avery Placeholder SHE
	stage.Texts = fakeTexts{texts: []entitydiscovery.ArticleText{{ID: "a1", Title: "Sharks", Summary: "Falls and Placeholder"}}}
	repo := evalLister{proposals: []entityproposal.Proposal{
		pendingNFL("Jalen Falls", "Falls"),             // truth in roster, resolves by corroborated surname
		pendingNFL("Avery Placeholder", "Placeholder"), // same
		pendingNFL("Tom Outsider", "Outsider"),         // truth NOT in the roster; no roster surname -> a correct abstention
	}}
	rep, err := entitydiscovery.EvaluateLeague(context.Background(), repo, stage)
	if err != nil {
		t.Fatal(err)
	}
	if rep.GroundTruth != 3 || rep.ByTier["T2a"].Total != 2 || rep.ByTier["T2a"].Correct != 2 ||
		rep.ByTier["not_found"].Total != 1 || rep.ByTier["not_found"].Correct != 1 ||
		rep.OutsideRoster != 1 || rep.OutsideRosterHits != 0 || len(rep.FalseFriends) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	txt := rep.Text()
	for _, want := range []string{"ground truth: 3", "T2a: 2/2", "not_found: 1/1", "outside the roster: 0/1 wrongly resolved"} {
		if !strings.Contains(txt, want) {
			t.Errorf("Text() is missing %q:\n%s", want, txt)
		}
	}
}

func TestEvaluateLeague_CountsAFalseAcceptOutsideTheRoster(t *testing.T) {
	// "Falls" is really another person's alias (Dan Falls is not in the roster), but the roster has a unique, corroborated Falls.
	stage := leagueStage(t, leaguePlayers())
	stage.Texts = fakeTexts{texts: []entitydiscovery.ArticleText{{ID: "a1", Title: "Sharks", Summary: "Falls"}}}
	repo := evalLister{proposals: []entityproposal.Proposal{pendingNFL("Dan Falls", "Falls")}}
	rep, err := entitydiscovery.EvaluateLeague(context.Background(), repo, stage)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OutsideRoster != 1 || rep.OutsideRosterHits != 1 || len(rep.FalseFriends) != 1 {
		t.Fatalf("report = %+v; want the wrong resolution counted as an outside-roster false accept", rep)
	}
}

func TestEvaluateLeague_IgnoresProposalsWithoutAnNFLLeagueOrAMultiWordName(t *testing.T) {
	stage := leagueStage(t, leaguePlayers())
	other := pendingNFL("Jalen Falls", "Falls")
	other.Metadata = map[string]string{"league": "NCAA"}
	oneWord := pendingNFL("Falls", "Fall")
	rep, err := entitydiscovery.EvaluateLeague(context.Background(), evalLister{proposals: []entityproposal.Proposal{other, oneWord}}, stage)
	if err != nil || rep.GroundTruth != 0 {
		t.Fatalf("report = %+v, err = %v; want no ground truth", rep, err)
	}
}

func TestEvaluateLeague_ASuffixVariantOfARosterPlayerIsTheSamePerson(t *testing.T) {
	stage := leagueStage(t, append(leaguePlayers(), lp("Marcus Example", "Marcus", "Example", "BRK", "WR", "ACT", "g-example")))
	stage.Texts = fakeTexts{texts: []entitydiscovery.ArticleText{{ID: "a1", Title: "Badgers", Summary: "Example caught it"}}}
	repo := evalLister{proposals: []entityproposal.Proposal{pendingNFL("Marcus Example Jr.", "Example")}}
	rep, err := entitydiscovery.EvaluateLeague(context.Background(), repo, stage)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OutsideRoster != 0 || rep.OutsideRosterHits != 0 || rep.ByTier["T2a"].Correct != 1 || len(rep.FalseFriends) != 0 {
		t.Fatalf("report = %+v; the roster omitting a Jr. must not make the answer 'outside the roster'", rep)
	}
}
