package publishing

import (
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/matchstate"
)

// PublicationLane separates ordinary editorial traffic from structured live
// game milestones. The zero value is intentionally normal for compatibility.
type PublicationLane string

const (
	LaneNormal PublicationLane = "normal"
	LaneLive   PublicationLane = "live"
)

func (l PublicationLane) Valid() bool { return l == "" || l == LaneNormal || l == LaneLive }
func (l PublicationLane) Normalized() PublicationLane {
	if l == LaneLive {
		return LaneLive
	}
	return LaneNormal
}

// LivePolicy is the explicit admission contract for the live lane.
type LivePolicy struct {
	Enabled         bool
	SourceAllowlist map[string]bool
	Milestones      map[string]bool
}

// LaneFor admits live only when every structured boundary is satisfied.
func (p LivePolicy) LaneFor(sourceID string, metadata map[string]string) PublicationLane {
	if !p.Enabled || !p.SourceAllowlist[strings.TrimSpace(sourceID)] {
		return LaneNormal
	}
	state, ok := matchstate.FromMetadata(metadata)
	if !ok || state.Phase == matchstate.Pregame {
		return LaneNormal
	}
	milestone := strings.ToLower(strings.TrimSpace(metadata["match_milestone"]))
	if milestone == "" && state.Phase != matchstate.Pregame {
		// Every non-pregame Phase constant's own string value already IS
		// its milestone name (kickoff/q1/q2/halftime/q3/q4/final) — no
		// per-phase switch needed. An explicit match_milestone (e.g. a
		// team-specific "touchdown_<team>" tag) still takes priority above.
		milestone = string(state.Phase)
	}
	if !p.Milestones[milestone] {
		return LaneNormal
	}
	return LaneLive
}

func ValidateLane(l PublicationLane) error {
	if !l.Valid() {
		return fmt.Errorf("publication lane must be %q or %q, got %q", LaneNormal, LaneLive, l)
	}
	return nil
}
