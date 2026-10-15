// app/entitydiscovery/chain_verifier_league_test.go
package entitydiscovery_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

type countingVerifier struct{ calls int }

func (c *countingVerifier) Verify(context.Context, entitydiscovery.VerifyCandidate) entitydiscovery.VerifyResult {
	c.calls++
	return entitydiscovery.VerifyResult{Disposition: weblookup.DispositionUncertain}
}

func TestChainVerifier_LeagueDecidesBeforeAnyOtherStage(t *testing.T) {
	fb := &countingVerifier{}
	chain := &entitydiscovery.ChainVerifier{League: leagueStage(t, leaguePlayers()), Fallback: fb, Catalog: realCatalog(t)}
	res := chain.Verify(context.Background(), cand("Avery Placeholder", entityproposal.EntityTypePlayer))
	if !res.Resolved || res.Provenance.Provider != "league-gazetteer" || fb.calls != 0 {
		t.Fatalf("Verify() = %+v, fallback calls %d; want resolved by the gazetteer with no fallback call", res, fb.calls)
	}
}

func TestChainVerifier_SuggestionsSurviveWhenLaterStagesAreInconclusive(t *testing.T) {
	fb := &countingVerifier{} // always uncertain
	chain := &entitydiscovery.ChainVerifier{League: leagueStage(t, leaguePlayers()), Fallback: fb, Catalog: realCatalog(t)}
	res := chain.Verify(context.Background(), cand("Falls", entityproposal.EntityTypePlayer))
	if res.Resolved || fb.calls != 1 {
		t.Fatalf("Verify() = %+v, fallback calls %d; want the chain to continue to the fallback", res, fb.calls)
	}
	if res.Suggestions["suggested_entity"] != "Jalen Falls" {
		t.Fatalf("suggestions = %v; the gazetteer's hint must be attached to the unresolved result", res.Suggestions)
	}
}

func TestChainVerifier_StatsExposeTheLeagueCounters(t *testing.T) {
	chain := &entitydiscovery.ChainVerifier{League: leagueStage(t, leaguePlayers()), Catalog: realCatalog(t)}
	chain.Verify(context.Background(), cand("Avery Placeholder", entityproposal.EntityTypePlayer))
	chain.Verify(context.Background(), cand("1examplefan99", entityproposal.EntityTypePlayer))
	if st := chain.ChainStats(); st.League.ResolvedFullName != 1 || st.League.Noise != 1 {
		t.Fatalf("League stats = %+v", st.League)
	}
}

type recordingLogger struct{ infos []string }

func (l *recordingLogger) Debug(string, ...any) {}
func (l *recordingLogger) Info(msg string, kv ...any) {
	l.infos = append(l.infos, msg+" "+fmt.Sprint(kv...))
}
func (l *recordingLogger) Warn(string, ...any)         {}
func (l *recordingLogger) Error(string, error, ...any) {}

func TestChainVerifier_LogsEveryClosedSetRejectWithItsName(t *testing.T) {
	log := &recordingLogger{}
	chain := &entitydiscovery.ChainVerifier{League: leagueStage(t, leaguePlayers()), Catalog: realCatalog(t), Logger: log}
	chain.Verify(context.Background(), cand("Southmoor", entityproposal.EntityTypeTeam))
	chain.Verify(context.Background(), cand("1examplefan99", entityproposal.EntityTypePlayer))
	chain.Verify(context.Background(), cand("Avery Placeholder", entityproposal.EntityTypePlayer)) // a keep: not logged as a reject
	joined := strings.Join(log.infos, "\n")
	for _, want := range []string{"Southmoor", "not_an_nfl_team", "1examplefan99", "auto_rejected_noise"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Avery Placeholder") {
		t.Errorf("a keep must not be logged as a closed-set reject:\n%s", joined)
	}
}

type rejectingVerifier struct{}

func (rejectingVerifier) Verify(context.Context, entitydiscovery.VerifyCandidate) entitydiscovery.VerifyResult {
	return entitydiscovery.VerifyResult{
		Disposition: weblookup.DispositionReject, Resolved: true, Reason: "another league",
		Provenance: entitydiscovery.Provenance{Provider: "wikidata"},
	}
}

func TestChainVerifier_ARosterPlayerIsNeverRejectedByLaterStages(t *testing.T) {
	springfield, _ := realCatalog(t).TeamIDByName(entityproposal.Normalize("Springfield Isotopes"))
	chain := &entitydiscovery.ChainVerifier{League: leagueStage(t, leaguePlayers()), Fallback: rejectingVerifier{}, Catalog: realCatalog(t)}

	// The candidate carries a stale team, so the gazetteer abstains; a later stage then says "not NFL".
	c := cand("Avery Placeholder", entityproposal.EntityTypePlayer)
	c.Metadata = map[string]string{"team_id": springfield}
	res := chain.Verify(context.Background(), c)
	if res.Disposition == weblookup.DispositionReject || res.Resolved {
		t.Fatalf("Verify() = %+v; a player present in the current roster must not be rejected by a later stage", res)
	}
	if res.Suggestions["suggested_entity"] != "Avery Placeholder" {
		t.Fatalf("suggestions = %v; the roster match must be recorded for the reviewer", res.Suggestions)
	}

	// A name that is not in the roster is still rejected by the later stage.
	out := chain.Verify(context.Background(), cand("Tom Outsider", entityproposal.EntityTypePlayer))
	if out.Disposition != weblookup.DispositionReject {
		t.Fatalf("Verify(Tom Outsider) = %+v; a non-roster name must keep the later stage's reject", out)
	}
}
