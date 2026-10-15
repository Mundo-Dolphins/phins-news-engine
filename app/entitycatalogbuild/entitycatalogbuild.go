// Package entitycatalogbuild filters a dictionary.Dictionary down to
// Isotopes players, coaches, and front-office personnel, and converts
// each into an eventsignals.EntityRule — the business logic behind the
// `entities generate` CLI command (see cli/cmd/
// entities). This is the only application-layer code that reads
// dictionary.Dictionary for entity-extraction purposes; runtime code
// (eventsignals, clustering, entitycatalog) never does.
package entitycatalogbuild

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// categoryKinds maps the dictionary.yml category name Build reads
// entities from to the EntityKind those entities become. Any category not
// listed here (alumni, rivals, nfl_people_context, abbreviations, and
// every negative/context/ambiguous category) is intentionally excluded:
// those exist in the dictionary for relevance-classification ambiguity
// gating, not as clustering subjects — including them risks false-
// positive entity matches on people who aren't the covered team's own roster.
var categoryKinds = map[string]eventsignals.EntityKind{
	"active_roster_core": eventsignals.EntityKindPlayer,
	"coaching_staff":     eventsignals.EntityKindCoach,
	"front_office":       eventsignals.EntityKindExecutive,
}

// Build filters d's categories (see categoryKinds) and converts each
// dictionary.Entry into an eventsignals.EntityRule: id is
// eventsignals.Slug(entry.CanonicalName), terms are the canonical name
// plus every alias verbatim (no new alias vetting — the dictionary
// curator has already vetted these for the relevance classifier), and
// position is carried through informationally. Output is sorted by id
// for a stable, reviewable diff.
func Build(d dictionary.Dictionary) ([]eventsignals.EntityRule, error) {
	var out []eventsignals.EntityRule
	seen := make(map[string]string)

	for _, cat := range d.Categories {
		kind, ok := categoryKinds[cat.Name]
		if !ok {
			continue
		}
		for _, entry := range cat.Entries {
			if entry.CanonicalName == "" {
				continue
			}
			if !strings.Contains(entry.CanonicalName, " ") {
				continue
			}
			id := eventsignals.Slug(entry.CanonicalName)
			if id == "" {
				return nil, fmt.Errorf("entitycatalogbuild: entity %q produced an empty id", entry.CanonicalName)
			}
			if prev, dup := seen[id]; dup {
				return nil, fmt.Errorf("entitycatalogbuild: id %q collides for %q and %q", id, prev, entry.CanonicalName)
			}
			seen[id] = entry.CanonicalName

			terms := append([]string{entry.CanonicalName}, entry.Aliases...)
			out = append(out, eventsignals.EntityRule{
				ID:            id,
				CanonicalName: entry.CanonicalName,
				Kind:          kind,
				Terms:         terms,
				Position:      entry.Position,
			})
		}
	}

	// Synthesize a last-name alias for every multi-word canonical name, so
	// common surname-only headlines ("Wickersham returns to practice") still
	// resolve. Collision-checked across every entity's full term set: an
	// ambiguous surname (shared by more than one entity) is skipped for
	// both rather than guessed, per the "false negatives over false
	// positives" preference the source ticket states. Team alumni,
	// division rivals, and generic NFL personalities are NOT in this
	// catalog (see categoryKinds' doc comment), so the collision universe
	// is only this build's own ~150 team-personnel entries — small
	// enough that a real collision is rare and safe to just skip.
	//
	// The candidate last names are collected in a first pass and only
	// assigned in a second pass (rather than assigning greedily as each
	// entity is visited) so that when two different entities want the same
	// surname, the alias is skipped for both regardless of which entity
	// happens to be visited first — a single-pass, assign-as-you-go
	// version would let whichever entity is processed first claim the
	// surname before the second entity's request ever creates the
	// collision, silently reintroducing the exact wrong-entity risk this
	// guard exists to prevent.
	termOwner := make(map[string]string, len(out)*2)
	for _, e := range out {
		for _, t := range e.Terms {
			termOwner[strings.ToLower(t)] = e.ID
		}
	}
	type surnameCandidate struct {
		idx      int
		lastName string
	}
	candidatesByKey := make(map[string][]surnameCandidate)
	for i := range out {
		parts := strings.Fields(out[i].CanonicalName)
		if len(parts) < 2 {
			continue
		}
		lastName := parts[len(parts)-1]
		key := strings.ToLower(lastName)
		if _, exists := termOwner[key]; exists {
			continue // already a term of some entity — nothing to synthesize
		}
		candidatesByKey[key] = append(candidatesByKey[key], surnameCandidate{idx: i, lastName: lastName})
	}
	for _, cands := range candidatesByKey {
		if len(cands) != 1 {
			continue // ambiguous surname, wanted by more than one entity — skip for all of them
		}
		c := cands[0]
		out[c.idx].Terms = append(out[c.idx].Terms, c.lastName)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
