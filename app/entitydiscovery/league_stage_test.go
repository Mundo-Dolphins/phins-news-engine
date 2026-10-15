// app/entitydiscovery/league_stage_test.go
package entitydiscovery_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/leaguerules"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
	"github.com/Mundo-Dolphins/phins-news-engine/testsupport/repofs"
)

func realCatalog(t *testing.T) entitydiscovery.CatalogIndex {
	t.Helper()
	rules, err := entitycatalog.LoadOne(repofs.ExampleConfig(t, "entity_catalog_manual.yml"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return entitydiscovery.BuildCatalogIndex(rules)
}

type fakeTexts struct {
	texts []entitydiscovery.ArticleText
	err   error
}

func (f fakeTexts) Texts(context.Context, []string) ([]entitydiscovery.ArticleText, error) {
	return f.texts, f.err
}

func leagueStage(t *testing.T, players []leaguegazetteer.Player) *entitydiscovery.LeagueStage {
	t.Helper()
	rules, err := leaguerules.Load(repofs.ExampleConfig(t, "league_alias_rules.yml"))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := leaguegazetteer.New(players, rules)
	if err != nil {
		t.Fatal(err)
	}
	st, err := entitydiscovery.NewLeagueStage(idx, realCatalog(t))
	if err != nil {
		t.Fatalf("NewLeagueStage() error = %v", err)
	}
	return st
}

func lp(full, first, last, team, pos, status, gsis string) leaguegazetteer.Player {
	return leaguegazetteer.Player{FullName: full, FirstName: first, LastName: last, TeamAbbr: team, Position: pos, Status: status, GSISID: gsis}
}

func leaguePlayers() []leaguegazetteer.Player {
	return []leaguegazetteer.Player{
		lp("Jalen Falls", "Jalen", "Falls", "SHE", "QB", "ACT", "g-falls"),
		lp("Nick Kicker", "Nick", "Kicker", "CAP", "K", "CUT", "g-kicker"),
		lp("Avery Placeholder", "Avery", "Placeholder", "SHE", "LB", "ACT", "g-placeholder"),
	}
}

func cand(name string, typ entityproposal.EntityType) entitydiscovery.VerifyCandidate {
	return entitydiscovery.VerifyCandidate{Name: name, Type: typ, Sources: []entityproposal.Source{{ArticleID: "a1", ArticleTitle: "t"}}}
}

func TestNewLeagueStage_FailsWhenARulesTeamIsMissingFromTheCatalog(t *testing.T) {
	rules, _ := leaguerules.Load(repofs.ExampleConfig(t, "league_alias_rules.yml"))
	rules.Teams[0].Name = "Atlantis Krakens"
	idx, err := leaguegazetteer.New(nil, rules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entitydiscovery.NewLeagueStage(idx, realCatalog(t)); err == nil {
		t.Fatal("NewLeagueStage() error = nil, want a start-up error naming the unresolved team")
	}
}

func TestNewLeagueStage_FailsWhenTheRulesLeaveOutACatalogTeamOrAreEmpty(t *testing.T) {
	rules, _ := leaguerules.Load(repofs.ExampleConfig(t, "league_alias_rules.yml"))
	short := rules
	short.Teams = rules.Teams[:len(rules.Teams)-1] // one team has no rules entry: it would be rejected as not_an_nfl_team
	for name, r := range map[string]leaguegazetteer.Rules{"one team missing": short, "no teams at all": {}} {
		idx, err := leaguegazetteer.New(nil, r)
		if err != nil {
			t.Fatalf("%s: New() error = %v", name, err)
		}
		if _, err := entitydiscovery.NewLeagueStage(idx, realCatalog(t)); err == nil {
			t.Errorf("%s: NewLeagueStage() error = nil, want a start-up error", name)
		}
	}
}

func TestLeagueStage_AllRulesTeamsResolveInTheRealCatalog(t *testing.T) {
	_ = leagueStage(t, nil) // NewLeagueStage fails on any unresolved team
}

func TestLeagueStage_PlayerResolvedByFullNameKeepsWithTeam(t *testing.T) {
	st := leagueStage(t, leaguePlayers())
	res, decided := st.Verify(context.Background(), cand("Avery Placeholder", entityproposal.EntityTypePlayer))
	if !decided || res.Disposition != weblookup.DispositionKeep || !res.Resolved {
		t.Fatalf("Verify() = %+v, %v; want a decided keep", res, decided)
	}
	md := res.Metadata
	if md["league"] != "NFL" || md["sport"] != "american_football" || md["position"] != "LB" || md["affiliation_status"] != "team" ||
		md["team"] != "Shelbyville Sharks" || md["team_id"] == "" {
		t.Fatalf("metadata = %v", md)
	}
	if res.Provenance.Provider != "league-gazetteer" || res.Provenance.ProviderID != "g-placeholder" || res.Provenance.Version != entitydiscovery.LeagueVerificationVersion {
		t.Fatalf("provenance = %+v", res.Provenance)
	}
}

func TestLeagueStage_CutPlayerIsKeepWithUnknownAffiliation(t *testing.T) {
	res, decided := leagueStage(t, leaguePlayers()).Verify(context.Background(), cand("Nick Kicker", entityproposal.EntityTypePlayer))
	if !decided || res.Metadata["affiliation_status"] != "unknown" || res.Metadata["team_id"] != "" {
		t.Fatalf("Verify() = %+v, %v; want keep with unknown affiliation", res, decided)
	}
}

func TestLeagueStage_ConflictingCandidateTeamIsNotDecided(t *testing.T) {
	st := leagueStage(t, leaguePlayers())
	springfield, _ := realCatalog(t).TeamIDByName(entityproposal.Normalize("Springfield Isotopes"))
	c := cand("Avery Placeholder", entityproposal.EntityTypePlayer)
	c.Metadata = map[string]string{"team_id": springfield}
	if _, decided := st.Verify(context.Background(), c); decided {
		t.Fatal("a candidate whose team_id conflicts with the roster must not be decided")
	}
}

func TestLeagueStage_UniqueSurnameNeedsCorroborationFromTheArticle(t *testing.T) {
	st := leagueStage(t, leaguePlayers())
	c := cand("Falls", entityproposal.EntityTypePlayer)

	st.Texts = fakeTexts{texts: []entitydiscovery.ArticleText{{ID: "a1", Title: "Sharks win", Summary: "Falls threw two touchdowns"}}}
	if res, decided := st.Verify(context.Background(), c); !decided || res.Provenance.ProviderID != "g-falls" {
		t.Fatalf("with a Sharks article: %+v, %v; want a decided keep for Falls", res, decided)
	}

	st.Texts = fakeTexts{texts: []entitydiscovery.ArticleText{{ID: "a1", Title: "Weather", Summary: "Falls when it rains"}}}
	res, decided := st.Verify(context.Background(), c)
	if decided {
		t.Fatal("without corroboration the surname must stay a suggestion")
	}
	if res.Suggestions["suggested_entity"] != "Jalen Falls" || res.Suggestions["suggested_team"] != "SHE" || res.Suggestions["suggested_basis"] != "unique_surname" {
		t.Fatalf("suggestions = %v", res.Suggestions)
	}
}

func TestLeagueStage_NoiseIsRejectedWithItsOwnReason(t *testing.T) {
	res, decided := leagueStage(t, leaguePlayers()).Verify(context.Background(), cand("1examplefan99", entityproposal.EntityTypePlayer))
	if !decided || res.Disposition != weblookup.DispositionReject || res.DecisionReason != entityproposal.ReasonAutoRejectedNoise {
		t.Fatalf("Verify() = %+v, %v; want a noise reject", res, decided)
	}
}

func TestLeagueStage_TeamsFollowTheClosedSet(t *testing.T) {
	st := leagueStage(t, nil)
	if res, decided := st.Verify(context.Background(), cand("Northern", entityproposal.EntityTypeTeam)); !decided || res.DecisionReason != entityproposal.ReasonNotAnNFLTeam {
		t.Fatalf("Northern = %+v, %v; want a not_an_nfl_team reject", res, decided)
	}
	for _, name := range []string{"Springfield", "Shelbyville", "Capital City", "OGD"} {
		if _, decided := st.Verify(context.Background(), cand(name, entityproposal.EntityTypeTeam)); decided {
			t.Errorf("%q must never be decided (it can be a league team)", name)
		}
	}
	// A two-letter abbreviation is a legitimate team mention, not noise.
	if res, decided := st.Verify(context.Background(), cand("NH", entityproposal.EntityTypeTeam)); decided || res.Suggestions["suggested_entity"] != "North Haverbrook Hornets" {
		t.Errorf("HC = %+v, %v; want a team_alias suggestion", res, decided)
	}
	// A team "name" that is only a handle is noise, not a closed-set rejection.
	if res, decided := st.Verify(context.Background(), cand("@sportsfan", entityproposal.EntityTypeTeam)); !decided || res.DecisionReason != entityproposal.ReasonAutoRejectedNoise {
		t.Errorf("@sportsfan = %+v, %v; want a noise reject", res, decided)
	}
	res, decided := st.Verify(context.Background(), cand("Topes", entityproposal.EntityTypeTeam))
	if decided || res.Suggestions["suggested_entity"] != "Springfield Isotopes" || res.Suggestions["suggested_basis"] != "team_alias" {
		t.Fatalf("Topes = %+v, %v; want a team_alias suggestion", res, decided)
	}
}

func TestLeagueStage_NeverRejectsARosterPlayerAsNoiseOrTeam(t *testing.T) {
	st := leagueStage(t, leaguePlayers())
	for _, name := range []string{"Jalen Falls", "Nick Kicker", "Avery Placeholder"} {
		if res, _ := st.Verify(context.Background(), cand(name, entityproposal.EntityTypePlayer)); res.Disposition == weblookup.DispositionReject {
			t.Errorf("%q was rejected: %+v", name, res)
		}
	}
}

func TestLeagueStage_CoachesAndExecutivesAreNotDecided(t *testing.T) {
	if _, decided := leagueStage(t, leaguePlayers()).Verify(context.Background(), cand("Avery Placeholder", entityproposal.EntityTypeCoach)); decided {
		t.Fatal("only players and teams are decided from the roster")
	}
}

type fakeChooser struct {
	res   appout.ChooseCandidateResult
	err   error
	calls int
	last  appout.ChooseCandidateRequest
}

func (f *fakeChooser) ChooseCandidate(_ context.Context, req appout.ChooseCandidateRequest) (appout.ChooseCandidateResult, error) {
	f.calls++
	f.last = req
	return f.res, f.err
}

func ambiguousPlayers() []leaguegazetteer.Player {
	return []leaguegazetteer.Player{
		lp("Zorin Mockford", "Zorin", "Mockford", "SPR", "S", "ACT", "g-za"),
		lp("Liam Mockford", "Liam", "Mockford", "SPR", "LB", "ACT", "g-la"),
		lp("Evan Mockford", "Evan", "Mockford", "NH", "DT", "ACT", "g-ea"),
	}
}

func chooserStage(t *testing.T, ch *fakeChooser, text entitydiscovery.ArticleText) *entitydiscovery.LeagueStage {
	t.Helper()
	st := leagueStage(t, ambiguousPlayers())
	st.Chooser = ch
	st.Texts = fakeTexts{texts: []entitydiscovery.ArticleText{text}}
	return st
}

func TestLeagueStage_ChooserAcceptedOnlyWhenHighAndCorroborated(t *testing.T) {
	article := entitydiscovery.ArticleText{ID: "a1", Title: "Isotopes snap counts", Summary: "Safeties Stubbs and Mockford played"}
	c := cand("Mockford", entityproposal.EntityTypePlayer)

	cases := []struct {
		name    string
		res     appout.ChooseCandidateResult
		err     error
		decided bool
	}{
		{"high and corroborated", appout.ChooseCandidateResult{ChoiceID: "0", Confidence: "high", Evidence: "Safeties ... Mockford"}, nil, true},
		{"medium confidence", appout.ChooseCandidateResult{ChoiceID: "0", Confidence: "medium"}, nil, false},
		{"abstains", appout.ChooseCandidateResult{}, nil, false},
		{"id outside the list", appout.ChooseCandidateResult{ChoiceID: "9", Confidence: "high"}, nil, false},
		{"not a number", appout.ChooseCandidateResult{ChoiceID: "zorin", Confidence: "high"}, nil, false},
		{"transport error", appout.ChooseCandidateResult{}, errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := &fakeChooser{res: tc.res, err: tc.err}
			st := chooserStage(t, ch, article)
			res, decided := st.Verify(context.Background(), c)
			if decided != tc.decided {
				t.Fatalf("decided = %v, want %v (%+v)", decided, tc.decided, res)
			}
			if !decided && res.Suggestions["suggested_candidates"] == "" {
				t.Fatalf("an undecided ambiguous mention must still carry its candidates: %+v", res)
			}
			if decided && res.Provenance.ProviderID == "" {
				t.Fatalf("a decided result must name the chosen player: %+v", res)
			}
		})
	}
}

func TestLeagueStage_ChooserNeverAcceptsAnUncorroboratedPick(t *testing.T) {
	ch := &fakeChooser{res: appout.ChooseCandidateResult{ChoiceID: "0", Confidence: "high", Evidence: "x"}}
	st := chooserStage(t, ch, entitydiscovery.ArticleText{ID: "a1", Title: "Weather report", Summary: "Mockford scored in the rain"})
	if _, decided := st.Verify(context.Background(), cand("Mockford", entityproposal.EntityTypePlayer)); decided {
		t.Fatal("a high-confidence pick without deterministic corroboration must be gated")
	}
	if got := st.Stats(); got.ChooserCalls != 1 || got.ChooserGated != 1 || got.ChooserAccepted != 0 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestLeagueStage_ChooserIsSkippedWithoutArticlesOrWhenDisabled(t *testing.T) {
	ch := &fakeChooser{res: appout.ChooseCandidateResult{ChoiceID: "0", Confidence: "high"}}
	st := leagueStage(t, ambiguousPlayers())
	st.Chooser = ch // no Texts loader and no candidate texts -> no article
	if _, decided := st.Verify(context.Background(), cand("Mockford", entityproposal.EntityTypePlayer)); decided || ch.calls != 0 {
		t.Fatalf("decided=%v calls=%d; with no article the chooser must not be called", decided, ch.calls)
	}
	st.Chooser = nil
	st.Texts = fakeTexts{texts: []entitydiscovery.ArticleText{{ID: "a1", Title: "Isotopes", Summary: "Mockford"}}}
	if _, decided := st.Verify(context.Background(), cand("Mockford", entityproposal.EntityTypePlayer)); decided {
		t.Fatal("with the chooser disabled an ambiguous mention is only a suggestion")
	}
}

func TestLeagueStage_ChooserReceivesOrderedBoundedCandidates(t *testing.T) {
	ch := &fakeChooser{}
	st := chooserStage(t, ch, entitydiscovery.ArticleText{ID: "a1", Title: "Hornets news", Summary: "Mockford sacked"})
	st.Verify(context.Background(), cand("Mockford", entityproposal.EntityTypePlayer))
	if ch.calls != 1 || len(ch.last.Candidates) != 3 || ch.last.Candidates[0].Team != "NH" || ch.last.Candidates[0].ID != "0" || ch.last.Mention != "Mockford" || len(ch.last.Articles) != 1 {
		t.Fatalf("request = %+v (calls %d); want 3 candidates with the Hornets player first and the article", ch.last, ch.calls)
	}
}

func TestLeagueStage_ChooserCandidateListHonoursMaxCandidates(t *testing.T) {
	ch := &fakeChooser{}
	st := chooserStage(t, ch, entitydiscovery.ArticleText{ID: "a1", Title: "Hornets news", Summary: "Mockford sacked"})
	st.MaxCandidates = 2
	st.Verify(context.Background(), cand("Mockford", entityproposal.EntityTypePlayer))
	if len(ch.last.Candidates) != 2 {
		t.Fatalf("candidates sent = %d, want the configured cap of 2", len(ch.last.Candidates))
	}
}
