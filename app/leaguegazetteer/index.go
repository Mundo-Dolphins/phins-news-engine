// app/leaguegazetteer/index.go
package leaguegazetteer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "v": true}

// genericCityWords are connector words of city names that say nothing about a
// team on their own ("new", "san", "city"...).
var genericCityWords = map[string]bool{"new": true, "san": true, "los": true, "las": true, "city": true, "bay": true, "green": true}

// splitSuffix returns n without a trailing generational suffix and whether one was present.
func splitSuffix(n string) (string, bool) {
	f := strings.Fields(n)
	if len(f) > 1 && nameSuffixes[f[len(f)-1]] {
		return strings.Join(f[:len(f)-1], " "), true
	}
	return n, false
}

// NormalizeText normalises and joins text fragments for use as Context.Text.
func NormalizeText(parts ...string) string {
	return entityproposal.Normalize(strings.Join(parts, " "))
}

type teamInfo struct {
	rule  TeamRule
	terms []string // normalised nicknames/aliases, used for corroboration
}

// Index resolves mentions against a roster and the rules. Safe for concurrent
// reads after New returns.
type Index struct {
	players    []Player
	norm       []string         // normalised full name per player (suffix kept)
	exact      map[string][]int // normalised full name or football-name alias -> players
	stripped   map[string][]int // same keys with the suffix removed
	byLast     map[string][]int // normalised suffix-stripped last name -> players
	byGSIS     map[string]int
	teams      map[string]teamInfo
	teamAlias  map[string][]string // normalised alias -> team abbrs
	upperAbbr  map[string][]string // capitals-only abbreviation -> team abbrs
	allAbbrs   map[string]bool     // normalised team abbreviation -> true
	cityOnly   map[string]bool     // normalised city/region part of a team name
	cityTokens map[string]bool     // distinctive words of those city/region parts
	undecided  []string            // normalised UndecidedTerms
	common     map[string]bool
	corr       map[string]string
	minLetters int
}

// New builds the index. It rejects invalid rules (duplicate team, alias claimed
// by two teams, empty names, a correction pointing at an unknown player).
func New(players []Player, rules Rules) (*Index, error) {
	i := &Index{
		players: players, norm: make([]string, len(players)),
		exact: map[string][]int{}, stripped: map[string][]int{}, byLast: map[string][]int{}, byGSIS: map[string]int{},
		teams: map[string]teamInfo{}, teamAlias: map[string][]string{}, upperAbbr: map[string][]string{},
		allAbbrs: map[string]bool{}, cityOnly: map[string]bool{}, cityTokens: map[string]bool{}, common: map[string]bool{}, corr: map[string]string{},
		minLetters: rules.NoiseMinLetters,
	}
	if i.minLetters <= 0 {
		i.minLetters = 3
	}
	for _, t := range rules.Teams {
		if strings.TrimSpace(t.Abbr) == "" || strings.TrimSpace(t.Name) == "" {
			return nil, fmt.Errorf("leaguegazetteer: team %+v needs an abbr and a name", t)
		}
		if _, dup := i.teams[t.Abbr]; dup {
			return nil, fmt.Errorf("leaguegazetteer: duplicate team %q", t.Abbr)
		}
		nameN := entityproposal.Normalize(t.Name)
		words := strings.Fields(nameN)
		if len(words) == 0 {
			return nil, fmt.Errorf("leaguegazetteer: team %q has a name with no letters or digits", t.Abbr)
		}
		nick := words[len(words)-1]
		info := teamInfo{rule: t}
		seen := map[string]bool{}
		forms := []string{t.Name, nick}
		singular := ""
		if len(nick) >= 5 && strings.HasSuffix(nick, "s") {
			singular = strings.TrimSuffix(nick, "s") // "comet", "88er": resolves a team mention, never corroborates
			forms = append(forms, singular)
		}
		for _, a := range append(forms, t.Aliases...) {
			n := entityproposal.Normalize(a)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			for _, owner := range i.teamAlias[n] {
				if owner != t.Abbr {
					return nil, fmt.Errorf("leaguegazetteer: alias %q is claimed by %s and %s", a, owner, t.Abbr)
				}
			}
			i.teamAlias[n] = append(i.teamAlias[n], t.Abbr)
			if n != nameN && n != singular {
				info.terms = append(info.terms, n)
			}
		}
		if len(words) > 1 {
			i.cityOnly[strings.Join(words[:len(words)-1], " ")] = true
			for _, w := range words[:len(words)-1] {
				if !genericCityWords[w] {
					i.cityTokens[w] = true
				}
			}
		}
		for _, u := range t.UpperAbbrs {
			i.upperAbbr[u] = append(i.upperAbbr[u], t.Abbr)
		}
		i.allAbbrs[entityproposal.Normalize(t.Abbr)] = true
		i.teams[t.Abbr] = info
	}
	for _, u := range rules.UndecidedTerms {
		if n := entityproposal.Normalize(u); n != "" {
			i.undecided = append(i.undecided, n)
		}
	}
	for _, w := range rules.CommonWords {
		i.common[entityproposal.Normalize(w)] = true
	}
	for idx, pl := range players {
		n := entityproposal.Normalize(pl.FullName)
		i.norm[idx] = n
		if pl.GSISID != "" {
			i.byGSIS[pl.GSISID] = idx
		}
		keys := map[string]bool{n: n != ""}
		if pl.FootballName != "" && pl.LastName != "" {
			keys[entityproposal.Normalize(pl.FootballName+" "+pl.LastName)] = true
		}
		for k := range keys {
			if k == "" {
				continue
			}
			i.exact[k] = append(i.exact[k], idx)
			s, _ := splitSuffix(k)
			i.stripped[s] = append(i.stripped[s], idx)
		}
		last := entityproposal.Normalize(pl.LastName)
		if last == "" {
			f := strings.Fields(n)
			if len(f) > 0 {
				last = f[len(f)-1]
				if nameSuffixes[last] && len(f) > 1 {
					last = f[len(f)-2]
				}
			}
		}
		if s, _ := splitSuffix(last); s != "" {
			i.byLast[s] = append(i.byLast[s], idx)
		}
	}
	for _, c := range rules.Corrections {
		m := entityproposal.Normalize(c.Mention)
		if m == "" {
			return nil, fmt.Errorf("leaguegazetteer: correction with an empty mention")
		}
		if c.GSISID != "none" {
			if _, ok := i.byGSIS[c.GSISID]; !ok {
				return nil, fmt.Errorf("leaguegazetteer: correction %q points at unknown player %q", c.Mention, c.GSISID)
			}
		}
		i.corr[m] = c.GSISID
	}
	return i, nil
}

// TeamName returns the configured name of a team abbreviation ("" if unknown).
func (i *Index) TeamName(abbr string) string { return i.teams[abbr].rule.Name }

// TeamAbbrs lists every configured team abbreviation, sorted.
func (i *Index) TeamAbbrs() []string {
	out := make([]string, 0, len(i.teams))
	for a := range i.teams {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// MinLetters is the effective noise threshold.
func (i *Index) MinLetters() int { return i.minLetters }

// PlayerByGSIS looks a player up by id.
func (i *Index) PlayerByGSIS(id string) (Player, bool) {
	idx, ok := i.byGSIS[id]
	if !ok {
		return Player{}, false
	}
	return i.players[idx], true
}
