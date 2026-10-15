package entityidentity

import (
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
)

// valuesOf returns the sorted, de-duplicated item IDs of the non-deprecated
// statements for prop. Statement order never matters.
func valuesOf(e knowledge.Entity, prop string) []string {
	set := map[string]bool{}
	for _, st := range e.Statements {
		if st.Property != prop || st.Rank == knowledge.RankDeprecated || st.Value.Kind != knowledge.KindEntity || st.Value.EntityID == "" {
			continue
		}
		set[st.Value.EntityID] = true
	}
	return sortedKeys(set)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ReferencedIDs lists the items an entity's score depends on (the teams of
// its sports-team memberships), so the caller can fetch them in one batch
// before calling Resolve. It is sorted and de-duplicated.
func ReferencedIDs(e knowledge.Entity) []string { return valuesOf(e, propMemberOf) }

// profile is the project-level reading of one Wikidata entity.
type profile struct {
	isPerson   bool
	isTeam     bool
	kinds      map[entityproposal.EntityType]bool // person roles
	football   bool                               // football-related (any level)
	sports     []string                           // sorted sport slugs
	leagues    []string                           // sorted league labels
	nflTeam    bool                               // team in the NFL
	nflOcc     bool                               // occupation names the NFL itself
	occupation map[string]bool
}

func classify(e knowledge.Entity) profile {
	p := profile{kinds: map[entityproposal.EntityType]bool{}, occupation: map[string]bool{}}
	instance := valuesOf(e, propInstanceOf)
	for _, id := range instance {
		if id == itemHuman {
			p.isPerson = true
		}
	}
	sports := map[string]bool{}
	for _, id := range valuesOf(e, propSport) {
		if s, ok := sportByItem[id]; ok {
			sports[s] = true
		}
	}
	leagues := map[string]bool{}
	hasLeague := false
	for _, id := range valuesOf(e, propLeague) {
		hasLeague = true
		if l, ok := leagueByItem[id]; ok {
			leagues[l] = true
			if id == itemNFL {
				p.nflTeam = true
			}
		}
	}
	if p.isPerson {
		for _, id := range valuesOf(e, propOccupation) {
			p.occupation[id] = true
			if s, ok := playerOccupationSport[id]; ok {
				p.kinds[entityproposal.EntityTypePlayer] = true
				sports[s] = true
			}
			if coachOccupations[id] {
				p.kinds[entityproposal.EntityTypeCoach] = true
			}
			if executiveOccupations[id] {
				p.kinds[entityproposal.EntityTypeExecutive] = true
			}
			if footballOccupations[id] {
				sports[sportAmericanFootball] = true
			}
			if nflOnlyOccupations[id] {
				p.nflOcc = true
			}
		}
	} else {
		for _, id := range instance {
			if teamClasses[id] {
				p.isTeam = true
			}
		}
		if hasLeague {
			p.isTeam = true
		}
	}
	p.football = sports[sportAmericanFootball]
	p.sports = sortedKeys(sports)
	p.leagues = sortedKeys(leagues)
	return p
}

// entityType picks the person role to report: the expected one when the
// entity holds it, otherwise player > coach > executive.
func (p profile) entityType(expected entityproposal.EntityType) entityproposal.EntityType {
	switch {
	case p.isTeam:
		return entityproposal.EntityTypeTeam
	case !p.isPerson:
		return ""
	case p.kinds[expected]:
		return expected
	case p.kinds[entityproposal.EntityTypePlayer]:
		return entityproposal.EntityTypePlayer
	case p.kinds[entityproposal.EntityTypeCoach]:
		return entityproposal.EntityTypeCoach
	case p.kinds[entityproposal.EntityTypeExecutive]:
		return entityproposal.EntityTypeExecutive
	}
	return ""
}

// teamSport is the single sport slug of a team, or "" when unknown or
// conflicting.
func (p profile) teamSport() string {
	if len(p.sports) == 1 {
		return p.sports[0]
	}
	return ""
}

func firstLeague(p profile) string {
	if len(p.leagues) == 0 {
		return ""
	}
	for _, l := range p.leagues {
		if l != leagueNFL {
			return l
		}
	}
	return p.leagues[0]
}

// Bounded diagnostics for an unresolved player affiliation (Result.
// AffiliationReason). They explain affiliation_status=unknown without
// becoming statuses themselves.
const (
	ReasonAffNoCurrent   = "wikidata_affiliation=no_current_membership"
	ReasonAffHistorical  = "wikidata_affiliation=historical_only"
	ReasonAffConflicting = "wikidata_affiliation=conflicting_current_memberships"
	ReasonAffUnmapped    = "wikidata_affiliation=unmapped_team"
)

// currentMembership returns the item ID of the entity's single
// current-looking sports team (P54), or "" when there is none or the
// evidence conflicts. See membership.
func currentMembership(e knowledge.Entity, now time.Time) string {
	tid, _ := membership(e, now)
	return tid
}

// membership reads P54 as temporal evidence. Deprecated, ended and
// not-yet-started statements are ignored (Statement.ValidAt), as are
// contradictory windows (start after end); a preferred-rank statement is
// Wikidata's own "current" marker and overrides normal ones; otherwise
// only statements with a known start count, because an undated open
// membership says nothing about recency. Response order is never
// consulted. When no team is returned, reason says why.
func membership(e knowledge.Entity, now time.Time) (string, string) {
	preferred := map[string]bool{}
	dated := map[string]bool{}
	historical := false
	for _, st := range e.Statements {
		if st.Property != propMemberOf || st.Rank == knowledge.RankDeprecated || st.Value.Kind != knowledge.KindEntity || st.Value.EntityID == "" {
			continue
		}
		if st.ValidFrom != nil && st.ValidTo != nil && st.ValidFrom.After(*st.ValidTo) {
			continue // contradictory window: malformed, never evidence
		}
		if !st.ValidAt(now) {
			if st.ValidTo != nil && !now.Before(*st.ValidTo) {
				historical = true
			}
			continue
		}
		switch {
		case st.Rank == knowledge.RankPreferred:
			preferred[st.Value.EntityID] = true
		case st.ValidFrom != nil:
			dated[st.Value.EntityID] = true
		}
	}
	pool := dated
	if len(preferred) > 0 {
		pool = preferred
	}
	switch {
	case len(pool) == 1:
		return sortedKeys(pool)[0], ""
	case len(pool) > 1:
		return "", ReasonAffConflicting
	case historical:
		return "", ReasonAffHistorical
	}
	return "", ReasonAffNoCurrent
}

// position returns the single football position abbreviation (P413), or
// "" when absent or conflicting. A preferred-rank position wins.
func position(e knowledge.Entity) string {
	preferred := map[string]bool{}
	all := map[string]bool{}
	for _, st := range e.Statements {
		if st.Property != propPosition || st.Rank == knowledge.RankDeprecated || st.Value.Kind != knowledge.KindEntity {
			continue
		}
		abbr, ok := positionByItem[st.Value.EntityID]
		if !ok {
			continue
		}
		all[abbr] = true
		if st.Rank == knowledge.RankPreferred {
			preferred[abbr] = true
		}
	}
	pool := all
	if len(preferred) > 0 {
		pool = preferred
	}
	if len(pool) != 1 {
		return ""
	}
	return sortedKeys(pool)[0]
}

// currentHeadCoachTeam finds the single NFL team entity that names id as
// its current head coach (P286 on the team). Only current-looking
// statements count, with the same rules as currentMembership.
func currentHeadCoachTeam(id string, ents map[string]knowledge.Entity, now time.Time) string {
	found := map[string]bool{}
	for teamID, t := range ents {
		if !classify(t).nflTeam {
			continue
		}
		for _, st := range t.Statements {
			if st.Property != propHeadCoach || st.Value.EntityID != id || !st.ValidAt(now) {
				continue
			}
			if st.Rank == knowledge.RankPreferred || st.ValidFrom != nil {
				found[teamID] = true
			}
		}
	}
	if len(found) != 1 {
		return ""
	}
	return sortedKeys(found)[0]
}

// namesAsHeadCoach reports whether any NFL team entity lists id as a head
// coach in any non-deprecated statement, current or historical.
func namesAsHeadCoach(id string, ents map[string]knowledge.Entity) bool {
	for _, t := range ents {
		if !classify(t).nflTeam {
			continue
		}
		for _, v := range valuesOf(t, propHeadCoach) {
			if v == id {
				return true
			}
		}
	}
	return false
}
