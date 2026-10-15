// Package espnscoreboard polls ESPN's public NFL scoreboard endpoint for
// one team's currently live game and maps it to the shared
// matchstate.State contract that publishing.LivePolicy reads from
// article metadata.
package espnscoreboard

import (
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
)

// staleFinalAfter bounds how long after kickoff a "final" status is
// still trusted. ESPN's weekly scoreboard keeps returning a completed
// event with state=post/completed=true for the rest of the game week
// (until the schedule rolls over Tue/Wed) — not just briefly at the
// final whistle. A real NFL game plus its immediate postgame wrap-up
// fits comfortably inside 6 hours from kickoff, even with overtime;
// anything ESPN still reports as "final" beyond that is stale
// schedule-week noise, not a genuinely current result.
const staleFinalAfter = 6 * time.Hour

type scoreboardResponse struct {
	Events []scoreboardEvent `json:"events"`
}

type scoreboardEvent struct {
	ID           string        `json:"id"`
	Date         string        `json:"date"`
	Status       eventStatus   `json:"status"`
	Competitions []competition `json:"competitions"`
}

type eventStatus struct {
	Period       int        `json:"period"`
	DisplayClock string     `json:"displayClock"`
	Type         statusType `json:"type"`
}

type statusType struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Completed bool   `json:"completed"`
}

type competition struct {
	Competitors []competitor `json:"competitors"`
}

type competitor struct {
	HomeAway string  `json:"homeAway"`
	Team     teamRef `json:"team"`
}

type teamRef struct {
	Abbreviation string `json:"abbreviation"`
	DisplayName  string `json:"displayName"`
}

// findTeamEvent returns the single event in resp naming teamAbbreviation
// as a competitor. ok=false on zero or more than one match — this bot
// fails closed rather than guess which game is "the" game.
func findTeamEvent(resp scoreboardResponse, teamAbbreviation string) (scoreboardEvent, bool) {
	want := strings.ToUpper(strings.TrimSpace(teamAbbreviation))
	var match scoreboardEvent
	found := 0
	for _, ev := range resp.Events {
		for _, comp := range ev.Competitions {
			for _, c := range comp.Competitors {
				if strings.ToUpper(c.Team.Abbreviation) == want {
					match = ev
					found++
					break
				}
			}
		}
	}
	if found != 1 {
		return scoreboardEvent{}, false
	}
	return match, true
}

// mapEvent converts one ESPN scoreboard event into a matchstate.State.
// ok=false on any status this bot doesn't recognize (overtime, an
// unexpected competitor count, an unmapped state string) — fails closed
// rather than stamp a guessed phase onto article metadata. now is used
// to guard against ESPN's stale "final" status (see staleFinalAfter).
func mapEvent(ev scoreboardEvent, now time.Time) (matchstate.State, bool) {
	home, away, ok := homeAwayTeams(ev)
	if !ok {
		return matchstate.State{}, false
	}
	identity := matchstate.Identity{MatchID: ev.ID, HomeTeam: home, AwayTeam: away}

	if ev.Status.Type.Completed {
		if !finalIsFresh(ev, now) {
			return matchstate.State{}, false
		}
		return matchstate.State{Identity: identity, Phase: matchstate.Final}, true
	}
	switch ev.Status.Type.State {
	case "pre":
		return matchstate.State{Identity: identity, Phase: matchstate.Pregame}, true
	case "post":
		if !finalIsFresh(ev, now) {
			return matchstate.State{}, false
		}
		return matchstate.State{Identity: identity, Phase: matchstate.Final}, true
	case "in":
		phase, ok := inProgressPhase(ev.Status)
		if !ok {
			return matchstate.State{}, false
		}
		return matchstate.State{Identity: identity, Phase: phase}, true
	default:
		return matchstate.State{}, false
	}
}

// finalIsFresh reports whether ev's kickoff time (ev.Date) is recent
// enough for a "final" status to be trusted as the current game's
// result, rather than ESPN's stale schedule-week noise. Fails closed
// (false) if ev.Date doesn't parse.
func finalIsFresh(ev scoreboardEvent, now time.Time) bool {
	kickoff, err := time.Parse(time.RFC3339, ev.Date)
	if err != nil {
		return false
	}
	return now.Sub(kickoff) <= staleFinalAfter
}

func homeAwayTeams(ev scoreboardEvent) (home, away string, ok bool) {
	if len(ev.Competitions) != 1 {
		return "", "", false
	}
	for _, c := range ev.Competitions[0].Competitors {
		switch c.HomeAway {
		case "home":
			home = c.Team.DisplayName
		case "away":
			away = c.Team.DisplayName
		}
	}
	if home == "" || away == "" {
		return "", "", false
	}
	return home, away, true
}

// inProgressPhase maps an in-progress event's period/clock/status-name
// into a Phase. Kickoff is a best-effort tag for "period 1, clock hasn't
// moved yet" — Q1 is the correct and equally valid fallback once the
// clock has started, per the design spec.
func inProgressPhase(status eventStatus) (matchstate.Phase, bool) {
	if strings.EqualFold(status.Type.Name, "STATUS_HALFTIME") {
		return matchstate.Halftime, true
	}
	switch status.Period {
	case 1:
		if status.DisplayClock == "15:00" {
			return matchstate.Kickoff, true
		}
		return matchstate.Q1, true
	case 2:
		return matchstate.Q2, true
	case 3:
		return matchstate.Q3, true
	case 4:
		return matchstate.Q4, true
	default:
		return "", false
	}
}
