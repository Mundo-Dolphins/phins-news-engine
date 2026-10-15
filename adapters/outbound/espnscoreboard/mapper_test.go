package espnscoreboard

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
)

// kickoffTime is the fixed kickoff baked into isotopesChiefsEvent's
// fixtures. testNow is a few minutes after it, well within the
// freshness window used by every existing test.
var kickoffTime = time.Date(2026, 9, 27, 17, 0, 0, 0, time.UTC)

var testNow = kickoffTime.Add(10 * time.Minute)

func isotopesChiefsEvent(state, statusName string, period int, displayClock string, completed bool) scoreboardEvent {
	return scoreboardEvent{
		ID:   "401671000",
		Date: kickoffTime.Format(time.RFC3339),
		Status: eventStatus{
			Period:       period,
			DisplayClock: displayClock,
			Type:         statusType{Name: statusName, State: state, Completed: completed},
		},
		Competitions: []competition{{
			Competitors: []competitor{
				{HomeAway: "home", Team: teamRef{Abbreviation: "SPR", DisplayName: "Springfield Isotopes"}},
				{HomeAway: "away", Team: teamRef{Abbreviation: "CAP", DisplayName: "Capital City Capitals"}},
			},
		}},
	}
}

func TestMapEvent_Pregame(t *testing.T) {
	ev := isotopesChiefsEvent("pre", "STATUS_SCHEDULED", 0, "15:00", false)
	state, ok := mapEvent(ev, testNow)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if state.Phase != matchstate.Pregame {
		t.Fatalf("Phase = %q, want pregame", state.Phase)
	}
	if state.MatchID != "401671000" || state.HomeTeam != "Springfield Isotopes" || state.AwayTeam != "Capital City Capitals" {
		t.Fatalf("Identity = %+v, want the seeded SPR/CAP identity", state.Identity)
	}
}

func TestMapEvent_KickoffVsOrdinaryQ1(t *testing.T) {
	kickoff, ok := mapEvent(isotopesChiefsEvent("in", "STATUS_IN_PROGRESS", 1, "15:00", false), testNow)
	if !ok || kickoff.Phase != matchstate.Kickoff {
		t.Fatalf("Phase = %q, ok=%v, want kickoff/true (clock still at 15:00)", kickoff.Phase, ok)
	}
	midQ1, ok := mapEvent(isotopesChiefsEvent("in", "STATUS_IN_PROGRESS", 1, "09:12", false), testNow)
	if !ok || midQ1.Phase != matchstate.Q1 {
		t.Fatalf("Phase = %q, ok=%v, want q1/true (clock has run)", midQ1.Phase, ok)
	}
}

func TestMapEvent_Quarters2Through4(t *testing.T) {
	cases := []struct {
		period int
		want   matchstate.Phase
	}{
		{2, matchstate.Q2},
		{3, matchstate.Q3},
		{4, matchstate.Q4},
	}
	for _, tc := range cases {
		state, ok := mapEvent(isotopesChiefsEvent("in", "STATUS_IN_PROGRESS", tc.period, "09:12", false), testNow)
		if !ok || state.Phase != tc.want {
			t.Errorf("period %d: Phase = %q, ok=%v, want %q/true", tc.period, state.Phase, ok, tc.want)
		}
	}
}

func TestMapEvent_Halftime(t *testing.T) {
	state, ok := mapEvent(isotopesChiefsEvent("in", "STATUS_HALFTIME", 2, "00:00", false), testNow)
	if !ok || state.Phase != matchstate.Halftime {
		t.Fatalf("Phase = %q, ok=%v, want halftime/true", state.Phase, ok)
	}
}

func TestMapEvent_FinalByStateOrByCompletedFlag(t *testing.T) {
	byState, ok := mapEvent(isotopesChiefsEvent("post", "STATUS_FINAL", 4, "00:00", false), testNow)
	if !ok || byState.Phase != matchstate.Final {
		t.Fatalf("Phase = %q, ok=%v, want final/true (state=post)", byState.Phase, ok)
	}
	byCompleted, ok := mapEvent(isotopesChiefsEvent("in", "STATUS_FINAL", 4, "00:00", true), testNow)
	if !ok || byCompleted.Phase != matchstate.Final {
		t.Fatalf("Phase = %q, ok=%v, want final/true (completed=true overrides state)", byCompleted.Phase, ok)
	}
}

func TestMapEvent_StaleFinalFailsClosed(t *testing.T) {
	// Reproduces the production incident: a game kicked off Sunday and
	// ESPN's weekly scoreboard still reports it "final" ~17-20 hours
	// later (well past our 6-hour freshness window), because ESPN keeps
	// last week's final in the response until the schedule rolls over.
	staleNow := kickoffTime.Add(20 * time.Hour)

	byState, ok := mapEvent(isotopesChiefsEvent("post", "STATUS_FINAL", 4, "00:00", false), staleNow)
	if ok {
		t.Fatalf("ok = true for a final status 20h after kickoff, want false (stale); got %+v", byState)
	}
	byCompleted, ok := mapEvent(isotopesChiefsEvent("in", "STATUS_FINAL", 4, "00:00", true), staleNow)
	if ok {
		t.Fatalf("ok = true for completed=true 20h after kickoff, want false (stale); got %+v", byCompleted)
	}
}

func TestMapEvent_FinalWithUnparsableDateFailsClosed(t *testing.T) {
	ev := isotopesChiefsEvent("post", "STATUS_FINAL", 4, "00:00", false)
	ev.Date = "not-a-timestamp"
	if _, ok := mapEvent(ev, testNow); ok {
		t.Fatal("ok = true with an unparsable date, want false (fail closed)")
	}
}

func TestMapEvent_UnrecognizedStatusFailsClosed(t *testing.T) {
	if _, ok := mapEvent(isotopesChiefsEvent("in", "STATUS_END_PERIOD", 5, "00:00", false), testNow); ok {
		t.Fatal("ok = true for an overtime/period-5 status this bot doesn't model, want false (fail closed)")
	}
	if _, ok := mapEvent(isotopesChiefsEvent("weird", "STATUS_UNKNOWN", 1, "15:00", false), testNow); ok {
		t.Fatal("ok = true for an unrecognized state string, want false")
	}
}

func TestMapEvent_MissingHomeOrAwayFailsClosed(t *testing.T) {
	ev := scoreboardEvent{
		ID:     "401671000",
		Status: eventStatus{Period: 1, DisplayClock: "15:00", Type: statusType{State: "in"}},
		Competitions: []competition{{
			Competitors: []competitor{
				{HomeAway: "home", Team: teamRef{Abbreviation: "SPR", DisplayName: "Springfield Isotopes"}},
				// no "away" competitor
			},
		}},
	}
	if _, ok := mapEvent(ev, testNow); ok {
		t.Fatal("ok = true with no away team, want false")
	}
}

func TestFindTeamEvent_ExactlyOneMatch(t *testing.T) {
	resp := scoreboardResponse{Events: []scoreboardEvent{
		isotopesChiefsEvent("pre", "STATUS_SCHEDULED", 0, "15:00", false),
		{ID: "other-game", Competitions: []competition{{Competitors: []competitor{
			{HomeAway: "home", Team: teamRef{Abbreviation: "SHE"}},
			{HomeAway: "away", Team: teamRef{Abbreviation: "OGD"}},
		}}}},
	}}
	ev, ok := findTeamEvent(resp, "spr")
	if !ok || ev.ID != "401671000" {
		t.Fatalf("findTeamEvent = %+v, ok=%v, want the SPR event", ev, ok)
	}
}

func TestFindTeamEvent_ZeroOrMultipleMatchesFailClosed(t *testing.T) {
	if _, ok := findTeamEvent(scoreboardResponse{}, "spr"); ok {
		t.Fatal("ok = true with zero events, want false")
	}
	dup := scoreboardResponse{Events: []scoreboardEvent{
		isotopesChiefsEvent("pre", "STATUS_SCHEDULED", 0, "15:00", false),
		isotopesChiefsEvent("pre", "STATUS_SCHEDULED", 0, "15:00", false),
	}}
	if _, ok := findTeamEvent(dup, "spr"); ok {
		t.Fatal("ok = true with two events naming the same team, want false (fail closed on ambiguity)")
	}
}
