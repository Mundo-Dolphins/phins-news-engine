// Package matchstate defines the small, explicit contract used to supersede
// live game publications when a terminal result is accepted.
package matchstate

import "strings"

// Phase is the ordered state of one sporting event. The zero value is
// intentionally unknown: callers must fail closed when the source did not
// provide enough evidence.
type Phase string

const (
	Pregame  Phase = "pregame"
	Kickoff  Phase = "kickoff"
	Q1       Phase = "q1"
	Q2       Phase = "q2"
	Halftime Phase = "halftime"
	Q3       Phase = "q3"
	Q4       Phase = "q4"
	Final    Phase = "final"
)

var order = map[Phase]int{Pregame: 0, Kickoff: 1, Q1: 2, Q2: 3, Halftime: 4, Q3: 5, Q4: 6, Final: 7}

// Identity is reliable only when supplied as structured source metadata.
// Team names are retained as an additional audit/validation boundary; a
// title containing a team name is never sufficient to construct Identity.
type Identity struct {
	MatchID  string
	HomeTeam string
	AwayTeam string
}

type State struct {
	Identity
	Phase Phase
}

// FromMetadata reads the versioned metadata contract. Missing, malformed, or
// ambiguous values return ok=false. These keys are deliberately not inferred
// from title/summary text.
func FromMetadata(metadata map[string]string) (State, bool) {
	if metadata == nil {
		return State{}, false
	}
	id := strings.TrimSpace(metadata["match_id"])
	home := strings.TrimSpace(metadata["match_home_team"])
	away := strings.TrimSpace(metadata["match_away_team"])
	phase := Phase(strings.ToLower(strings.TrimSpace(metadata["match_phase"])))
	if id == "" || home == "" || away == "" || home == away || metadata["match_state_kind"] != "live" {
		return State{}, false
	}
	if _, ok := order[phase]; !ok {
		return State{}, false
	}
	return State{Identity: Identity{MatchID: id, HomeTeam: home, AwayTeam: away}, Phase: phase}, true
}

func (s State) IsTerminal() bool { return s.Phase == Final }

func (s State) Precedes(other State) bool {
	return s.MatchID != "" && s.MatchID == other.MatchID && order[s.Phase] < order[other.Phase]
}
