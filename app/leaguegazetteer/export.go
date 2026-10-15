// app/leaguegazetteer/export.go
package leaguegazetteer

import (
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// exportTeamStatuses are the roster status codes for which the roster team is
// the player's current team.
var exportTeamStatuses = map[string]bool{"ACT": true, "DEV": true, "RES": true, "INA": true, "EXE": true}

// IdentityKey is a person's name reduced to what identifies him across
// sources: normalised (accents, case, punctuation) and without a trailing
// generational suffix, so "Ollie Penhallow II" and "Ollie Penhallow" give the same key.
func IdentityKey(name string) string {
	base, _ := splitSuffix(NormalizeText(name))
	return eventsignals.Slug(base)
}

// ExportRules builds league-scope eventsignals rules for every roster player.
//
//   - ID is "league-<slug(full name)>-<slug(gsis id)>" so homonyms stay distinct.
//   - Terms are the raw full name and the football-name variant.
//   - A surname becomes a ConditionalTerm only when it is unique league-wide,
//     has at least four letters and is not a configured common word; it needs
//     one of the player's team nicknames/aliases in the same article.
//   - taken holds team catalog ids and IdentityKey values of their names and
//     terms; a player whose slug or IdentityKey is taken is skipped, so a league
//     rule can never shadow or double count a Isotopes entity.
//   - teamIDByAbbr maps a roster team abbreviation to a catalog team id; a
//     missing entry leaves TeamID empty and the affiliation unknown.
func (i *Index) ExportRules(teamIDByAbbr map[string]string, taken map[string]bool) []eventsignals.EntityRule {
	out := make([]eventsignals.EntityRule, 0, len(i.players))
	for idx, p := range i.players {
		full := strings.TrimSpace(p.FullName)
		if full == "" {
			continue
		}
		slug := eventsignals.Slug(full)
		if slug == "" || taken[slug] || taken["league-"+slug] || taken[IdentityKey(full)] {
			continue
		}
		rule := eventsignals.EntityRule{
			ID:            "league-" + slug + "-" + eventsignals.Slug(p.GSISID),
			CanonicalName: full,
			Kind:          eventsignals.EntityKindPlayer,
			Terms:         []string{full},
			Position:      p.Position,
			Scope:         eventsignals.ScopeLeague,
		}
		if p.FootballName != "" && p.LastName != "" {
			if alt := strings.TrimSpace(p.FootballName + " " + p.LastName); !strings.EqualFold(alt, full) {
				rule.Terms = append(rule.Terms, alt)
			}
		}
		rule.AffiliationStatus = eventsignals.AffiliationUnknown
		if exportTeamStatuses[p.Status] {
			if id := teamIDByAbbr[p.TeamAbbr]; id != "" {
				rule.TeamID, rule.AffiliationStatus = id, eventsignals.AffiliationTeam
			}
		}
		if sur := i.exportSurname(idx); sur != "" {
			if ctx := i.exportContext(p.TeamAbbr); len(ctx) > 0 {
				rule.ConditionalTerms = []eventsignals.ConditionalTerm{{Term: sur, RequireAny: ctx}}
			}
		}
		terms := append([]string(nil), rule.Terms...)
		for _, ct := range rule.ConditionalTerms {
			terms = append(terms, ct.Term)
		}
		rule.Prefilter = eventsignals.PrefilterWords(terms...)
		out = append(out, rule)
	}
	return out
}

// exportSurname returns the raw surname to use as a conditional alias, or "".
func (i *Index) exportSurname(idx int) string {
	p := i.players[idx]
	raw := strings.Fields(strings.TrimSpace(p.LastName))
	if len(raw) > 1 && nameSuffixes[strings.ToLower(strings.Trim(raw[len(raw)-1], "."))] {
		raw = raw[:len(raw)-1]
	}
	sur := strings.Join(raw, " ")
	if sur == "" {
		fields := strings.Fields(p.FullName)
		if len(fields) > 1 {
			sur = fields[len(fields)-1]
		}
	}
	norm := NormalizeText(sur)
	if len([]rune(norm)) < 4 || i.common[norm] || len(i.byLast[norm]) != 1 {
		return ""
	}
	return sur
}

// exportContext returns the corroboration words for a team (explicit
// nicknames and aliases only, never derived singulars), sorted for stability.
func (i *Index) exportContext(abbr string) []string {
	terms := append([]string(nil), i.teams[abbr].terms...)
	sort.Strings(terms)
	return terms
}
