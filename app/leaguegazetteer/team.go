// app/leaguegazetteer/team.go
package leaguegazetteer

import (
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

// ResolveTeam resolves a team mention against the closed set of 32 teams.
func (i *Index) ResolveTeam(mention string) TeamResolution {
	raw := strings.TrimSpace(mention)
	n := entityproposal.Normalize(raw)
	if n == "" {
		return TeamResolution{Status: TeamNotNFL}
	}
	if raw == strings.ToUpper(raw) {
		if abbrs := i.upperAbbr[raw]; len(abbrs) == 1 {
			return i.teamResult(abbrs[0])
		} else if len(abbrs) > 1 {
			return TeamResolution{Status: TeamUndecided}
		}
		if i.allAbbrs[n] { // an abbreviation that is not an accepted alias ("LA")
			return TeamResolution{Status: TeamUndecided}
		}
	}
	if abbrs := i.teamAlias[n]; len(abbrs) == 1 {
		return i.teamResult(abbrs[0])
	} else if len(abbrs) > 1 {
		return TeamResolution{Status: TeamUndecided}
	}
	if i.cityOnly[n] || i.mentionsNFLTerm(n) {
		return TeamResolution{Status: TeamUndecided}
	}
	return TeamResolution{Status: TeamNotNFL}
}

// mentionsNFLTerm reports whether a team mention contains something from the
// NFL vocabulary: a team nickname or alias, a distinctive city word, an
// abbreviation, a configured undecided term, a compound that embeds a nickname
// ("SprIsotopes", "MSCondors"), or a truncated city ("Cobble", "Ogdenvil").
// Such a mention may be an NFL team in an informal form, so the closed-set
// rule never rejects it.
func (i *Index) mentionsNFLTerm(n string) bool {
	for _, tk := range strings.Fields(n) {
		if i.cityTokens[tk] || i.allAbbrs[tk] || len(i.teamAlias[tk]) > 0 {
			return true
		}
		if len(tk) >= 4 {
			for city := range i.cityTokens {
				if strings.HasPrefix(city, tk) { // "cobble" -> "cobbleton"
					return true
				}
			}
		}
		if len(tk) >= 8 {
			for alias := range i.teamAlias {
				if len(alias) >= 6 && !strings.Contains(alias, " ") && strings.Contains(tk, alias) { // "sprisotopes"
					return true
				}
			}
		}
	}
	if len(n) >= 6 {
		for city := range i.cityOnly {
			if strings.HasPrefix(city, n) { // "ogdenvil" -> "ogdenville"
				return true
			}
		}
	}
	padded := " " + n + " "
	for _, u := range i.undecided {
		if strings.Contains(padded, " "+u+" ") {
			return true
		}
	}
	return false
}

func (i *Index) teamResult(abbr string) TeamResolution {
	return TeamResolution{Status: TeamResolved, Abbr: abbr, Name: i.teams[abbr].rule.Name}
}
