// app/entitydiscovery/league_stage.go
package entitydiscovery

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// LeagueVerificationVersion identifies the gazetteer stage's decision logic; a
// bump re-verifies rows verified by an older version.
const LeagueVerificationVersion = "league-gazetteer-v1"

// LeagueStats counts LeagueStage outcomes.
type LeagueStats struct {
	ResolvedFullName, ResolvedSurname, ResolvedTypo, ResolvedCorrection int
	Suggested, Ambiguous, Noise, NotNFLTeam                             int
	ChooserCalls, ChooserAccepted, ChooserGated, ChooserErrors          int
}

// rosterTeamStatuses are the roster status codes for which the listed team is
// the player's current team. CUT/RET/unknown codes are deliberately absent.
var rosterTeamStatuses = map[string]bool{"ACT": true, "DEV": true, "RES": true, "INA": true, "EXE": true}

// LeagueStage decides player and team proposals from the league gazetteer.
type LeagueStage struct {
	Index       *leaguegazetteer.Index
	Catalog     CatalogIndex
	Texts       ArticleTextLoader       // nil = no article context
	Chooser     appout.CandidateChooser // nil = chooser off
	MaxArticles int                     // 0 = 3
	// MaxCandidates caps the candidates sent to the chooser (0 = no extra cap
	// beyond the gazetteer's own limit of 8).
	MaxCandidates int
	teamIDs       map[string]string // team abbr -> catalog team id

	mu    sync.Mutex
	stats LeagueStats
}

// NewLeagueStage binds the index to the entity catalog. It fails when any team
// of the rules file does not resolve in the catalog, so a typo in the rules
// can never silently reject a real team.
func NewLeagueStage(idx *leaguegazetteer.Index, catalog CatalogIndex) (*LeagueStage, error) {
	ids := map[string]string{}
	for _, abbr := range idx.TeamAbbrs() {
		id, ok := catalog.TeamIDByName(entityproposal.Normalize(idx.TeamName(abbr)))
		if !ok {
			return nil, fmt.Errorf("entitydiscovery: league rules team %s (%q) is not in the entity catalog", abbr, idx.TeamName(abbr))
		}
		ids[abbr] = id
	}
	// The reverse direction too: a catalog team with no rules entry would have
	// its informal names rejected as not_an_nfl_team.
	coveredID, coveredName := map[string]bool{}, map[string]bool{}
	for abbr, id := range ids {
		coveredID[id] = true
		coveredName[entityproposal.Normalize(idx.TeamName(abbr))] = true
	}
	for id := range catalog.TeamIDs() {
		if coveredID[id] {
			continue
		}
		// Single-word entries ("nfl", "cap") are league pseudo-teams and
		// aliases; every real full team name must be covered by id or name.
		name := entityproposal.Normalize(catalog.TeamCanonicalName(id))
		if len(strings.Fields(name)) > 1 && !coveredName[name] {
			return nil, fmt.Errorf("entitydiscovery: catalog team %q (%s) has no entry in the league rules", id, name)
		}
	}
	return &LeagueStage{Index: idx, Catalog: catalog, teamIDs: ids}, nil
}

// Stats returns a snapshot of the counters.
func (s *LeagueStage) Stats() LeagueStats { s.mu.Lock(); defer s.mu.Unlock(); return s.stats }

func (s *LeagueStage) bump(f func(*LeagueStats)) { s.mu.Lock(); f(&s.stats); s.mu.Unlock() }

func (s *LeagueStage) maxArticles() int {
	if s.MaxArticles > 0 {
		return s.MaxArticles
	}
	return 3
}

func leagueProvenance(id string) Provenance {
	return Provenance{Provider: "league-gazetteer", ProviderID: id, Version: LeagueVerificationVersion}
}

// Verify returns (result, true) when the stage decided (keep or reject). With
// false, result.Suggestions may carry hints for the later stages and the
// proposal.
func (s *LeagueStage) Verify(ctx context.Context, cand VerifyCandidate) (VerifyResult, bool) {
	// Teams are resolved before the noise check: two-letter abbreviations such
	// as "HC" or "LA" are legitimate team mentions that the letter-count rule
	// would otherwise discard.
	if cand.Type == entityproposal.EntityTypeTeam {
		return s.verifyTeam(cand)
	}
	if noisy, why := leaguegazetteer.IsNoise(cand.Name, s.Index.MinLetters()); noisy {
		return s.rejectNoise(why), true
	}
	if cand.Type == entityproposal.EntityTypePlayer {
		return s.verifyPlayer(ctx, cand)
	}
	return VerifyResult{}, false
}

func (s *LeagueStage) rejectNoise(why string) VerifyResult {
	s.bump(func(st *LeagueStats) { st.Noise++ })
	return VerifyResult{
		Disposition: weblookup.DispositionReject, Resolved: true, Reason: "noise: " + why,
		DecisionReason: entityproposal.ReasonAutoRejectedNoise, Provenance: leagueProvenance(""),
	}
}

func (s *LeagueStage) verifyTeam(cand VerifyCandidate) (VerifyResult, bool) {
	r := s.Index.ResolveTeam(cand.Name)
	switch r.Status {
	case leaguegazetteer.TeamNotNFL:
		if noisy, why := leaguegazetteer.IsNoise(cand.Name, s.Index.MinLetters()); noisy {
			return s.rejectNoise(why), true
		}
		s.bump(func(st *LeagueStats) { st.NotNFLTeam++ })
		return VerifyResult{
			Disposition: weblookup.DispositionReject, Resolved: true, Reason: "matches none of the 32 NFL teams",
			DecisionReason: entityproposal.ReasonNotAnNFLTeam, Provenance: leagueProvenance(""),
		}, true
	case leaguegazetteer.TeamResolved:
		s.bump(func(st *LeagueStats) { st.Suggested++ })
		return VerifyResult{Suggestions: map[string]string{
			"suggested_entity": r.Name, "suggested_team": r.Abbr, "suggested_basis": "team_alias",
		}}, false
	}
	return VerifyResult{}, false
}

// context builds the corroboration text from the candidate's source articles.
func (s *LeagueStage) context(ctx context.Context, cand VerifyCandidate) (leaguegazetteer.Context, []ArticleText) {
	texts := cand.Texts
	if len(texts) == 0 && s.Texts != nil && len(cand.Sources) > 0 {
		ids := make([]string, 0, len(cand.Sources))
		for _, src := range cand.Sources {
			ids = append(ids, src.ArticleID)
		}
		if got, err := s.Texts.Texts(ctx, ids); err == nil {
			texts = got
		}
	}
	if len(texts) > s.maxArticles() {
		texts = texts[:s.maxArticles()]
	}
	parts := make([]string, 0, len(texts)*2)
	for _, t := range texts {
		parts = append(parts, t.Title, t.Summary)
	}
	return leaguegazetteer.Context{Text: leaguegazetteer.NormalizeText(parts...)}, texts
}

func (s *LeagueStage) verifyPlayer(ctx context.Context, cand VerifyCandidate) (VerifyResult, bool) {
	lctx, texts := s.context(ctx, cand)
	r := s.Index.ResolvePlayer(cand.Name, lctx)
	switch r.Status {
	case leaguegazetteer.Resolved:
		if res, ok := s.keep(cand, r.Candidates[0], r.Kind); ok {
			return res, true
		}
		return VerifyResult{}, false
	case leaguegazetteer.Suggested:
		s.bump(func(st *LeagueStats) { st.Suggested++ })
		return VerifyResult{Suggestions: s.suggest(r.Candidates, string(r.Kind))}, false
	case leaguegazetteer.Ambiguous:
		s.bump(func(st *LeagueStats) { st.Ambiguous++ })
		if res, ok := s.choose(ctx, cand, r, lctx, texts); ok {
			return res, true
		}
		return VerifyResult{Suggestions: s.suggest(r.Candidates, string(r.Kind))}, false
	}
	return VerifyResult{}, false
}

// RosterPlayer returns the roster player whose full name is exactly cand's
// (suffix rules included), if there is one unambiguous match. The chain uses it
// to stop a later stage from rejecting someone who is on the current roster.
func (s *LeagueStage) RosterPlayer(cand VerifyCandidate) (leaguegazetteer.Player, bool) {
	if cand.Type != entityproposal.EntityTypePlayer {
		return leaguegazetteer.Player{}, false
	}
	r := s.Index.ResolvePlayer(cand.Name, leaguegazetteer.Context{})
	if r.Status == leaguegazetteer.Resolved && r.Kind == leaguegazetteer.KindFullName && len(r.Candidates) == 1 {
		return r.Candidates[0], true
	}
	return leaguegazetteer.Player{}, false
}

func (s *LeagueStage) suggest(cands []leaguegazetteer.Player, basis string) map[string]string {
	if len(cands) == 0 {
		return nil
	}
	names := make([]string, 0, 5)
	for i, c := range cands {
		if i == 5 {
			break
		}
		names = append(names, c.FullName+" ("+c.TeamAbbr+")")
	}
	out := map[string]string{"suggested_basis": basis, "suggested_candidates": strings.Join(names, "; ")}
	if len(cands) == 1 {
		out["suggested_entity"], out["suggested_team"] = cands[0].FullName, cands[0].TeamAbbr
	}
	return out
}

// keep builds the keep result for a decided player (team only for current-team
// statuses whose abbreviation maps to a catalog team).
func (s *LeagueStage) keep(cand VerifyCandidate, p leaguegazetteer.Player, kind leaguegazetteer.MatchKind) (VerifyResult, bool) {
	teamID := ""
	if rosterTeamStatuses[p.Status] {
		teamID = s.teamIDs[p.TeamAbbr]
	}
	if want := cand.Metadata["team_id"]; want != "" && teamID != "" && want != teamID {
		return VerifyResult{}, false // a different team: probably another person
	}
	md := map[string]string{
		"league": "NFL", "sport": "american_football", "position": p.Position,
		"affiliation_status": "unknown", "roster_status": p.Status, "match_basis": string(kind),
	}
	if teamID != "" {
		md["team"], md["team_id"], md["affiliation_status"] = s.Index.TeamName(p.TeamAbbr), teamID, "team"
	}
	s.bump(func(st *LeagueStats) {
		switch kind {
		case leaguegazetteer.KindUniqueSurname:
			st.ResolvedSurname++
		case leaguegazetteer.KindTypo:
			st.ResolvedTypo++
		case leaguegazetteer.KindCorrection:
			st.ResolvedCorrection++
		case leaguegazetteer.KindChooser:
			// counted as ChooserAccepted by choose
		default:
			st.ResolvedFullName++
		}
	})
	return VerifyResult{
		Disposition: weblookup.DispositionKeep, CanonicalName: p.FullName, EntityType: entityproposal.EntityTypePlayer,
		Metadata: md, Resolved: true, Reason: "league gazetteer: " + string(kind),
		Provenance: Provenance{Provider: "league-gazetteer", ProviderID: p.GSISID, Version: LeagueVerificationVersion, Reason: "roster status " + p.Status},
	}, true
}

// choose asks the closed-set chooser and accepts its answer only through the
// deterministic gate: confidence "high" AND Index.Corroborated. Errors,
// abstentions, invalid ids and gated picks all fall back to suggestions.
func (s *LeagueStage) choose(ctx context.Context, cand VerifyCandidate, r leaguegazetteer.Resolution, lctx leaguegazetteer.Context, texts []ArticleText) (VerifyResult, bool) {
	if s.Chooser == nil || len(texts) == 0 || len(r.Candidates) == 0 {
		return VerifyResult{}, false
	}
	req := appout.ChooseCandidateRequest{Mention: cand.Name}
	for _, t := range texts {
		req.Articles = append(req.Articles, appout.EntityDiscoveryArticle{ArticleID: t.ID, Title: t.Title, Summary: t.Summary})
	}
	cands := r.Candidates
	if s.MaxCandidates > 0 && len(cands) > s.MaxCandidates {
		cands = cands[:s.MaxCandidates]
	}
	for i, p := range cands {
		req.Candidates = append(req.Candidates, appout.ChooseCandidate{ID: strconv.Itoa(i), FullName: p.FullName, Team: p.TeamAbbr, Position: p.Position, Status: p.Status})
	}
	s.bump(func(st *LeagueStats) { st.ChooserCalls++ })
	got, err := s.Chooser.ChooseCandidate(ctx, req)
	if err != nil {
		s.bump(func(st *LeagueStats) { st.ChooserErrors++ })
		return VerifyResult{}, false
	}
	n, convErr := strconv.Atoi(got.ChoiceID)
	if got.ChoiceID == "" || convErr != nil || n < 0 || n >= len(cands) {
		return VerifyResult{}, false
	}
	picked := cands[n]
	if got.Confidence != "high" || !s.Index.Corroborated(picked, lctx) {
		s.bump(func(st *LeagueStats) { st.ChooserGated++ })
		return VerifyResult{}, false
	}
	res, ok := s.keep(cand, picked, leaguegazetteer.KindChooser)
	if ok {
		s.bump(func(st *LeagueStats) { st.ChooserAccepted++ })
		res.Reason = "league chooser: " + got.Evidence
	}
	return res, ok
}
