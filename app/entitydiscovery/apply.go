package entitydiscovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
)

// CatalogPaths names the two catalog files ApplyApproval may write to:
// the team dictionary (player/coach/executive entries and their
// aliases) and the manual entity catalog (team entries and their
// aliases) — see infra/entitycatalog's package doc.
type CatalogPaths struct {
	DictionaryPath    string
	ManualCatalogPath string
	// GeneratedCatalogPath, when set, is the generated entity catalog the
	// runtime loads together with the manual one; it is only read, to
	// refuse ids/names that would collide at load time. A missing file is
	// treated as empty.
	GeneratedCatalogPath string
}

// ApplyApproval converts an approved proposal into a real catalog-file
// change. It performs no database I/O — the caller (the `entities
// approve` CLI command) is responsible for only calling this once, and
// for recording the resulting status change itself. p.Kind must already
// be KindNewEntity or KindNewAlias — a KindPossibleDuplicate proposal
// must be resolved to one of those by the caller first (via
// --merge-into/--as-new), since ApplyApproval has no policy for which
// one an ambiguous proposal should become.
func ApplyApproval(p entityproposal.Proposal, paths CatalogPaths, now time.Time) error {
	switch p.Kind {
	case entityproposal.KindNewEntity:
		if p.EntityType == entityproposal.EntityTypeTeam {
			return applyNewTeam(p, paths.ManualCatalogPath)
		}
		return applyNewPersonEntity(p, paths, now)
	case entityproposal.KindNewAlias:
		return applyNewAlias(p, paths, now)
	default:
		return fmt.Errorf("entitydiscovery: proposal kind %q cannot be applied directly (resolve via --merge-into/--as-new first)", p.Kind)
	}
}

func categoryForType(t entityproposal.EntityType) (string, error) {
	switch t {
	case entityproposal.EntityTypePlayer:
		return "active_roster_core", nil
	case entityproposal.EntityTypeCoach:
		return "coaching_staff", nil
	case entityproposal.EntityTypeExecutive:
		return "front_office", nil
	default:
		return "", fmt.Errorf("entitydiscovery: entity type %q has no dictionary category", t)
	}
}

func backupDictionaryFile(path string, previous dictionary.Dictionary, now time.Time) error {
	backupPath := fmt.Sprintf("%s.bak-%s", path, now.UTC().Format("20060102T150405Z"))
	return dictionaryfile.Save(backupPath, previous)
}

func applyNewPersonEntity(p entityproposal.Proposal, paths CatalogPaths, now time.Time) error {
	category, err := categoryForType(p.EntityType)
	if err != nil {
		return err
	}
	dictPath := paths.DictionaryPath
	teamID, team := p.Metadata["team_id"], p.Metadata["team"]
	status, err := eventsignals.ParseAffiliationStatus(p.Metadata[MetadataAffiliationStatus])
	if err != nil {
		return fmt.Errorf("entitydiscovery: %q: %w", p.CanonicalName, err)
	}
	if err := eventsignals.ValidateAffiliation(eventsignals.EntityKind(p.EntityType), status, teamID, team); err != nil {
		return fmt.Errorf("entitydiscovery: %q: %w", p.CanonicalName, err)
	}
	if status == eventsignals.AffiliationUnspecified && (teamID != "" || team != "") {
		// Legacy proposal: team data without an explicit status.
		status = eventsignals.AffiliationTeam
	}
	switch status {
	case eventsignals.AffiliationFreeAgent:
		manual, generated, err := loadLeagueCatalogs(paths)
		if err != nil {
			return err
		}
		return applyNewLeaguePerson(p, status, manual, generated, paths.ManualCatalogPath)
	case eventsignals.AffiliationTeam:
		manual, generated, err := loadLeagueCatalogs(paths)
		if err != nil {
			return err
		}
		primary := primaryTeamID(manual)
		switch {
		case teamID == "":
			// A team the verifier could not resolve to a catalog id must
			// never default to the team dictionary.
			if primary == "" || teamIDForName(manual, team) != primary {
				return fmt.Errorf("entitydiscovery: %q has team %q but no team_id — set a team_id or reject this proposal", p.CanonicalName, team)
			}
		case teamID != primary:
			return applyNewLeaguePerson(p, status, manual, generated, paths.ManualCatalogPath)
		}
	default:
		// Unknown or undeclared affiliation is never evidence of Isotopes
		// membership: leave the proposal pending for an operator.
		return fmt.Errorf("entitydiscovery: %q has no established affiliation — resolve its team (or free agency) before approving, or reject this proposal", p.CanonicalName)
	}
	current, err := dictionaryfile.Load(dictPath)
	if err != nil {
		return fmt.Errorf("entitydiscovery: load %s: %w", dictPath, err)
	}

	key := dictionaryupdate.CanonicalKey(p.CanonicalName)
	for _, cat := range current.Categories {
		for _, e := range cat.Entries {
			if dictionaryupdate.CanonicalKey(e.CanonicalName) == key {
				return fmt.Errorf("entitydiscovery: %q already exists in category %q — reject this proposal or resolve manually", p.CanonicalName, cat.Name)
			}
		}
	}

	candidate := current
	candidate.Categories = make([]dictionary.Category, len(current.Categories))
	copy(candidate.Categories, current.Categories)

	entry := dictionary.Entry{
		CanonicalName: p.CanonicalName,
		Aliases:       p.Aliases,
		Type:          dictionary.EntryTypePerson,
		Position:      p.Metadata["position"],
		Source:        "entities discover (LLM)",
		UpdatedAt:     now,
		Confidence:    0.5,
	}
	idx := -1
	for i, cat := range candidate.Categories {
		if cat.Name == category {
			idx = i
			break
		}
	}
	if idx == -1 {
		candidate.Categories = append(candidate.Categories, dictionary.Category{Name: category, Weight: 1, Entries: []dictionary.Entry{entry}})
	} else {
		entries := make([]dictionary.Entry, len(candidate.Categories[idx].Entries), len(candidate.Categories[idx].Entries)+1)
		copy(entries, candidate.Categories[idx].Entries)
		candidate.Categories[idx].Entries = append(entries, entry)
	}

	_, err = dictionaryupdate.WriteCandidate(current, candidate, 1, []string{"entities discover"}, "", now,
		func(previous dictionary.Dictionary) error { return backupDictionaryFile(dictPath, previous, now) },
		func(d dictionary.Dictionary) error { return dictionaryfile.Save(dictPath, d) },
	)
	return err
}

func primaryTeamID(entries []eventsignals.EntityRule) string {
	for _, e := range entries {
		if e.Kind == eventsignals.EntityKindTeam && e.IsPrimaryTeam {
			return e.ID
		}
	}
	return ""
}

// teamIDForName returns the id of the team entry one of whose terms
// matches name (normalized), or "".
func teamIDForName(entries []eventsignals.EntityRule, name string) string {
	key := entityproposal.Normalize(name)
	if key == "" {
		return ""
	}
	for _, e := range entries {
		if e.Kind != eventsignals.EntityKindTeam {
			continue
		}
		for _, term := range append([]string{e.CanonicalName}, e.Terms...) {
			if entityproposal.Normalize(term) == key {
				return e.ID
			}
		}
	}
	return ""
}

// loadLeagueCatalogs loads the manual catalog and the optional generated
// one approval routes against.
func loadLeagueCatalogs(paths CatalogPaths) (manual, generated []eventsignals.EntityRule, err error) {
	manual, err = entitycatalog.LoadOne(paths.ManualCatalogPath)
	if err != nil {
		return nil, nil, fmt.Errorf("entitydiscovery: load %s: %w", paths.ManualCatalogPath, err)
	}
	generated, err = loadOptionalCatalog(paths.GeneratedCatalogPath)
	return manual, generated, err
}

// loadOptionalCatalog loads a read-only catalog file; an empty path or a
// missing file yields no entries.
func loadOptionalCatalog(path string) ([]eventsignals.EntityRule, error) {
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	entries, err := entitycatalog.LoadOne(path)
	if err != nil {
		return nil, fmt.Errorf("entitydiscovery: load %s: %w", path, err)
	}
	return entries, nil
}

// applyNewLeaguePerson writes a non-Isotopes player/coach/executive to the
// manual entity catalog, leaving the team dictionary untouched. The
// id/name must be new in both the manual and the generated catalog
// (the runtime loads them together).
func applyNewLeaguePerson(p entityproposal.Proposal, status eventsignals.AffiliationStatus, entries, generated []eventsignals.EntityRule, manualPath string) error {
	teamID := p.Metadata["team_id"]
	if status == eventsignals.AffiliationTeam {
		teamFound := false
		for _, e := range entries {
			if e.ID == teamID && e.Kind == eventsignals.EntityKindTeam {
				teamFound = true
				break
			}
		}
		if !teamFound {
			return fmt.Errorf("entitydiscovery: team_id %q is not a team in %s", teamID, manualPath)
		}
	}
	id := eventsignals.Slug(p.CanonicalName)
	key := dictionaryupdate.CanonicalKey(p.CanonicalName)
	for _, e := range entries {
		if e.ID == id || dictionaryupdate.CanonicalKey(e.CanonicalName) == key {
			return fmt.Errorf("entitydiscovery: %q (id %q) already exists in %s — reject this proposal or resolve manually", p.CanonicalName, id, manualPath)
		}
	}
	for _, e := range generated {
		if e.ID == id || dictionaryupdate.CanonicalKey(e.CanonicalName) == key {
			return fmt.Errorf("entitydiscovery: %q (id %q) already exists in the generated entity catalog — reject this proposal or resolve manually", p.CanonicalName, id)
		}
	}
	terms := append([]string{p.CanonicalName}, p.Aliases...)
	entries = append(entries, eventsignals.EntityRule{
		ID: id, CanonicalName: p.CanonicalName, Kind: eventsignals.EntityKind(p.EntityType),
		TeamID: teamID, AffiliationStatus: status, Position: p.Metadata["position"], Role: p.Metadata["role"], Terms: terms,
	})
	return entitycatalog.WriteManual(manualPath, entries)
}

func applyNewTeam(p entityproposal.Proposal, manualPath string) error {
	entries, err := entitycatalog.LoadOne(manualPath)
	if err != nil {
		return fmt.Errorf("entitydiscovery: load %s: %w", manualPath, err)
	}
	id := eventsignals.Slug(p.CanonicalName)
	for _, e := range entries {
		if e.ID == id {
			return fmt.Errorf("entitydiscovery: team %q (id %q) already exists in %s", p.CanonicalName, id, manualPath)
		}
	}
	terms := append([]string{p.CanonicalName}, p.Aliases...)
	entries = append(entries, eventsignals.EntityRule{ID: id, CanonicalName: p.CanonicalName, Kind: eventsignals.EntityKindTeam, Terms: terms})
	return entitycatalog.WriteManual(manualPath, entries)
}

func applyNewAlias(p entityproposal.Proposal, paths CatalogPaths, now time.Time) error {
	if p.TargetEntityID == "" {
		return fmt.Errorf("entitydiscovery: alias proposal %s has no target_entity_id", p.ID)
	}

	current, err := dictionaryfile.Load(paths.DictionaryPath)
	if err != nil {
		return fmt.Errorf("entitydiscovery: load %s: %w", paths.DictionaryPath, err)
	}
	for ci, cat := range current.Categories {
		for ei, e := range cat.Entries {
			if eventsignals.Slug(e.CanonicalName) != p.TargetEntityID {
				continue
			}
			candidate := current
			candidate.Categories = make([]dictionary.Category, len(current.Categories))
			copy(candidate.Categories, current.Categories)
			entries := make([]dictionary.Entry, len(candidate.Categories[ci].Entries))
			copy(entries, candidate.Categories[ci].Entries)
			entries[ei].Aliases = mergeAliases(entries[ei].Aliases, p.Aliases)
			candidate.Categories[ci].Entries = entries

			_, err := dictionaryupdate.WriteCandidate(current, candidate, 1, []string{"entities discover"}, "", now,
				func(previous dictionary.Dictionary) error {
					return backupDictionaryFile(paths.DictionaryPath, previous, now)
				},
				func(d dictionary.Dictionary) error { return dictionaryfile.Save(paths.DictionaryPath, d) },
			)
			return err
		}
	}

	manualEntries, err := entitycatalog.LoadOne(paths.ManualCatalogPath)
	if err != nil {
		return fmt.Errorf("entitydiscovery: load %s: %w", paths.ManualCatalogPath, err)
	}
	for i, e := range manualEntries {
		if e.ID == p.TargetEntityID {
			manualEntries[i].Terms = mergeAliases(manualEntries[i].Terms, p.Aliases)
			return entitycatalog.WriteManual(paths.ManualCatalogPath, manualEntries)
		}
	}

	return fmt.Errorf("entitydiscovery: target entity %q not found in %s or %s", p.TargetEntityID, paths.DictionaryPath, paths.ManualCatalogPath)
}

func mergeAliases(existing, additions []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, a := range existing {
		seen[entityproposal.Normalize(a)] = true
	}
	out := existing
	for _, a := range additions {
		if a == "" {
			continue
		}
		if key := entityproposal.Normalize(a); !seen[key] {
			seen[key] = true
			out = append(out, a)
		}
	}
	return out
}
