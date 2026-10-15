// app/entitydiscovery/league_evaluate.go
package entitydiscovery

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/app/leaguegazetteer"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

// TierScore counts how many ground-truth mentions landed in a tier and how many
// of those were right (for "not_found": correct abstentions).
type TierScore struct{ Total, Correct int }

// LeagueEvalReport is the read-only quality report of the league gazetteer.
type LeagueEvalReport struct {
	GroundTruth int
	ByTier      map[string]TierScore // T1, T2a, T2b, correction, suggested, ambiguous, not_found
	// OutsideRoster counts ground-truth answers that are not in the roster (the
	// stage must abstain); OutsideRosterHits counts those it wrongly Resolved.
	OutsideRoster     int
	OutsideRosterHits int
	FalseFriends      []string // "mention -> wrong player (truth)" for wrong Resolved outcomes
}

func normNoSuffix(s string) string {
	n := entityproposal.Normalize(s)
	f := strings.Fields(n)
	if len(f) > 1 {
		switch f[len(f)-1] {
		case "jr", "sr", "ii", "iii", "iv", "v":
			return strings.Join(f[:len(f)-1], " ")
		}
	}
	return n
}

func tierOf(r leaguegazetteer.Resolution) string {
	switch r.Status {
	case leaguegazetteer.Resolved:
		switch r.Kind {
		case leaguegazetteer.KindUniqueSurname:
			return "T2a"
		case leaguegazetteer.KindTypo:
			return "T2b"
		case leaguegazetteer.KindCorrection:
			return "correction"
		}
		return "T1"
	case leaguegazetteer.Suggested:
		return "suggested"
	case leaguegazetteer.Ambiguous:
		return "ambiguous"
	}
	return "not_found"
}

// EvaluateLeague replays the gazetteer on a ground truth drawn from the
// pending backlog: single-word aliases of pending NFL player proposals that
// already carry a multi-word canonical name. It never writes.
func EvaluateLeague(ctx context.Context, repo ProposalLister, stage *LeagueStage) (LeagueEvalReport, error) {
	rep := LeagueEvalReport{ByTier: map[string]TierScore{}}
	pending, err := repo.List(ctx, string(entityproposal.EntityTypePlayer), string(entityproposal.StatusPending))
	if err != nil {
		return rep, fmt.Errorf("entitydiscovery: list pending players: %w", err)
	}
	for _, p := range pending {
		if !strings.EqualFold(strings.TrimSpace(p.Metadata["league"]), "NFL") || len(strings.Fields(p.CanonicalName)) < 2 {
			continue
		}
		for _, alias := range p.Aliases {
			alias = strings.TrimSpace(alias)
			if len(strings.Fields(alias)) != 1 || len([]rune(alias)) < 3 {
				continue
			}
			rep.GroundTruth++
			lctx, _ := stage.context(ctx, VerifyCandidate{Sources: p.Sources})
			got := stage.Index.ResolvePlayer(alias, lctx)
			truthIn := stage.Index.ResolvePlayer(p.CanonicalName, leaguegazetteer.Context{})
			// A suffix-only difference ("Pellard Jr." vs roster "Pellard") is the same person.
			inRoster := (truthIn.Kind == leaguegazetteer.KindFullName && (truthIn.Status == leaguegazetteer.Resolved || truthIn.Status == leaguegazetteer.Ambiguous)) ||
				(truthIn.Kind == leaguegazetteer.KindSuffix && truthIn.Status == leaguegazetteer.Suggested)

			tier := tierOf(got)
			sc := rep.ByTier[tier]
			sc.Total++
			switch {
			case tier == "not_found":
				if !inRoster {
					sc.Correct++
				}
			case inRoster && len(got.Candidates) > 0 && normNoSuffix(got.Candidates[0].FullName) == normNoSuffix(p.CanonicalName):
				sc.Correct++
			}
			rep.ByTier[tier] = sc

			if !inRoster {
				rep.OutsideRoster++
				if got.Status == leaguegazetteer.Resolved {
					rep.OutsideRosterHits++
				}
			}
			if got.Status == leaguegazetteer.Resolved && len(got.Candidates) > 0 &&
				(!inRoster || normNoSuffix(got.Candidates[0].FullName) != normNoSuffix(p.CanonicalName)) {
				rep.FalseFriends = append(rep.FalseFriends, fmt.Sprintf("%s -> %s (truth: %s)", alias, got.Candidates[0].FullName, p.CanonicalName))
			}
		}
	}
	return rep, nil
}

// Text renders the report for the CLI.
func (r LeagueEvalReport) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ground truth: %d single-word aliases of verified NFL players\n", r.GroundTruth)
	tiers := make([]string, 0, len(r.ByTier))
	for k := range r.ByTier {
		tiers = append(tiers, k)
	}
	sort.Strings(tiers)
	for _, k := range tiers {
		s := r.ByTier[k]
		fmt.Fprintf(&b, "  %s: %d/%d correct\n", k, s.Correct, s.Total)
	}
	fmt.Fprintf(&b, "outside the roster: %d/%d wrongly resolved (target 0; check each listed name for a nickname variant of the same person)\n", r.OutsideRosterHits, r.OutsideRoster)
	for _, f := range r.FalseFriends {
		fmt.Fprintf(&b, "  false friend: %s\n", f)
	}
	return b.String()
}
