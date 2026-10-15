// app/leaguegazetteer/player.go
package leaguegazetteer

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

const maxCandidates = 8

func notFound(reason string) Resolution { return Resolution{Status: NotFound, Reason: reason} }

// ResolvePlayer resolves a mention to roster players. See the tier table in
// the league gazetteer spec; ctx supplies the corroboration text.
func (i *Index) ResolvePlayer(mention string, ctx Context) Resolution {
	n := entityproposal.Normalize(mention)
	if n == "" {
		return notFound("empty mention")
	}
	if g, ok := i.corr[n]; ok {
		if g == "none" {
			return notFound("manual correction: not a roster player")
		}
		return Resolution{Status: Resolved, Kind: KindCorrection, Candidates: []Player{i.players[i.byGSIS[g]]}}
	}
	if tokens := strings.Fields(n); len(tokens) > 1 {
		return i.resolveMulti(n, tokens)
	}
	return i.resolveSingle(n, ctx)
}

func (i *Index) pick(idxs []int) []Player {
	out := make([]Player, 0, len(idxs))
	for _, ix := range idxs {
		out = append(out, i.players[ix])
	}
	return out
}

// namesEqual reports whether player ix is known under exactly the name n
// (full name or football-name alias, suffix kept).
func (i *Index) namesEqual(ix int, n string) bool {
	for _, got := range i.exactKeys(ix) {
		if got == n {
			return true
		}
	}
	return false
}

func (i *Index) exactKeys(ix int) []string {
	pl := i.players[ix]
	keys := []string{i.norm[ix]}
	if pl.FootballName != "" && pl.LastName != "" {
		keys = append(keys, entityproposal.Normalize(pl.FootballName+" "+pl.LastName))
	}
	return keys
}

func (i *Index) resolveMulti(n string, tokens []string) Resolution {
	base, hasSuffix := splitSuffix(n)
	var idxs []int
	if hasSuffix {
		idxs = i.exact[n]
		if len(idxs) == 0 {
			// "Garrick Mossvale II" vs a roster that omits the suffix: only a hint.
			if s := i.stripped[base]; len(s) == 1 && !i.hasSuffixName(s[0]) {
				return Resolution{Status: Suggested, Kind: KindSuffix, Candidates: i.pick(s), Reason: "roster name has no suffix"}
			}
		}
	} else {
		idxs = i.stripped[n]
	}
	switch {
	case len(idxs) == 1:
		if !i.namesEqual(idxs[0], n) {
			return Resolution{Status: Suggested, Kind: KindSuffix, Candidates: i.pick(idxs), Reason: "mention lacks the roster suffix"}
		}
		return Resolution{Status: Resolved, Kind: KindFullName, Candidates: i.pick(idxs)}
	case len(idxs) > 1:
		return Resolution{Status: Ambiguous, Kind: KindFullName, Candidates: i.pick(idxs[:min(len(idxs), maxCandidates)]), Reason: "several players share the name"}
	}
	if r, ok := i.typo(n, base); ok {
		return r
	}
	if r, ok := i.initials(base); ok {
		return r
	}
	return notFound("no roster player matches")
}

func (i *Index) hasSuffixName(ix int) bool {
	_, has := splitSuffix(i.norm[ix])
	return has
}

// suffixOf returns the generational suffix of a normalised name ("" if none).
func suffixOf(n string) string {
	f := strings.Fields(n)
	if len(f) > 1 && nameSuffixes[f[len(f)-1]] {
		return f[len(f)-1]
	}
	return ""
}

// typo finds the single player whose suffix-stripped name is within two edits
// and whose suffix equals the mention's (a Sr. is never a typo of a Jr.). One
// edit that is not a truncation is a decided correction; a truncated last name
// ("chris bel" for "orrin belk") or two edits on a long name is only a
// suggestion, and two edits on a short name is not a typo at all.
func (i *Index) typo(n, base string) (Resolution, bool) {
	for _, tk := range strings.Fields(base) {
		if utf8.RuneCountInString(tk) < 3 {
			return Resolution{}, false
		}
	}
	tokCount := len(strings.Fields(base))
	mentionSuffix := suffixOf(n)
	type hit struct{ ix, d int }
	var hits []hit
	for ix := range i.players {
		cand, _ := splitSuffix(i.norm[ix])
		if cand == "" || cand[0] != base[0] || len(strings.Fields(cand)) != tokCount || suffixOf(i.norm[ix]) != mentionSuffix {
			continue
		}
		if d := editDistance(base, cand); d >= 1 && d <= 2 {
			hits = append(hits, hit{ix, d})
		}
	}
	if len(hits) != 1 {
		return Resolution{}, false
	}
	h := hits[0]
	cand, _ := splitSuffix(i.norm[h.ix])
	switch {
	case h.d == 1 && !truncatedLastToken(base, cand):
		return Resolution{Status: Resolved, Kind: KindTypo, Candidates: i.pick([]int{h.ix}), Reason: "one edit from the roster name"}, true
	case h.d == 1 || utf8.RuneCountInString(base) >= 10:
		return Resolution{Status: Suggested, Kind: KindTypo, Candidates: i.pick([]int{h.ix}), Reason: "close to the roster name but not decisive"}, true
	}
	return Resolution{}, false
}

// truncatedLastToken reports whether the mention's last word is a strict prefix
// of the candidate's last word with the same preceding words.
func truncatedLastToken(mention, cand string) bool {
	m, c := strings.Fields(mention), strings.Fields(cand)
	if len(m) != len(c) || len(m) == 0 {
		return false
	}
	for k := 0; k < len(m)-1; k++ {
		if m[k] != c[k] {
			return false
		}
	}
	return len(m[len(m)-1]) < len(c[len(c)-1]) && strings.HasPrefix(c[len(c)-1], m[len(m)-1])
}

// initials finds the single player with the same last name whose first name
// starts with the mention's first token ("ad mossgrove" -> "adair mossgrove").
func (i *Index) initials(base string) (Resolution, bool) {
	f := strings.Fields(base)
	if len(f) != 2 {
		return Resolution{}, false
	}
	var hits []int
	for _, ix := range i.byLast[f[1]] {
		first := entityproposal.Normalize(i.players[ix].FirstName)
		if first != "" && first != f[0] && strings.HasPrefix(first, f[0]) {
			hits = append(hits, ix)
		}
	}
	if len(hits) == 1 {
		return Resolution{Status: Suggested, Kind: KindInitials, Candidates: i.pick(hits), Reason: "first name starts with the mention"}, true
	}
	return Resolution{}, false
}

func (i *Index) resolveSingle(tok string, ctx Context) Resolution {
	if utf8.RuneCountInString(tok) < 4 {
		return notFound("too short to be a surname alias")
	}
	if i.common[tok] {
		return notFound("common word")
	}
	idxs := i.byLast[tok]
	switch len(idxs) {
	case 0:
		return notFound("not a roster surname")
	case 1:
		pl := i.players[idxs[0]]
		if i.Corroborated(pl, ctx) {
			return Resolution{Status: Resolved, Kind: KindUniqueSurname, Candidates: []Player{pl}}
		}
		return Resolution{Status: Suggested, Kind: KindUniqueSurname, Candidates: []Player{pl}, Reason: "unique surname without corroboration"}
	}
	cands := i.pick(idxs)
	sort.SliceStable(cands, func(a, b int) bool {
		return i.teamMentioned(cands[a], ctx) && !i.teamMentioned(cands[b], ctx)
	})
	if len(cands) > maxCandidates {
		cands = cands[:maxCandidates]
	}
	return Resolution{Status: Ambiguous, Kind: KindSurnameShared, Candidates: cands, Reason: "surname shared by several players"}
}

func (i *Index) teamMentioned(p Player, ctx Context) bool {
	text := " " + ctx.Text + " "
	for _, t := range i.teams[p.TeamAbbr].terms {
		if strings.Contains(text, " "+t+" ") {
			return true
		}
	}
	return false
}

// Corroborated reports whether ctx independently supports p: the player's
// full name, or one of his team's nicknames/aliases, appears as whole words.
func (i *Index) Corroborated(p Player, ctx Context) bool {
	if ctx.Text == "" {
		return false
	}
	if n := entityproposal.Normalize(p.FullName); n != "" && strings.Contains(" "+ctx.Text+" ", " "+n+" ") {
		return true
	}
	return i.teamMentioned(p, ctx)
}

// CandidateTeamTerms exposes a team's normalised nicknames (used by the stage
// to order chooser candidates and by tests).
func (i *Index) CandidateTeamTerms(abbr string) []string {
	return append([]string(nil), i.teams[abbr].terms...)
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > 2 || d < -2 {
		return 3
	}
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for x := 1; x <= len(ra); x++ {
		cur := make([]int, len(rb)+1)
		cur[0] = x
		for y := 1; y <= len(rb); y++ {
			cost := 1
			if ra[x-1] == rb[y-1] {
				cost = 0
			}
			cur[y] = min(prev[y]+1, min(cur[y-1]+1, prev[y-1]+cost))
		}
		prev = cur
	}
	return prev[len(rb)]
}
