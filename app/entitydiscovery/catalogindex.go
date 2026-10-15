// Package entitydiscovery is the application-layer use case behind the
// offline `entities discover`/`entities approve` commands:
// building a normalized-name index over the currently-loaded
// deterministic entity catalog, classifying LLM-reported entities
// against it and against each other, aggregating occurrences into
// entityproposal.Proposal values, and converting an approved proposal
// into a real catalog-file change. It depends only on
// ports/out interfaces, never on a concrete
// adapter.
package entitydiscovery

import (
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// CatalogEntity is the minimal shape CatalogIndex needs from a matched
// eventsignals.EntityRule.
type CatalogEntity struct {
	ID            string
	CanonicalName string
	Type          entityproposal.EntityType
}

// CatalogIndex is a normalized-name lookup over the entity catalog
// loaded at the start of a discover run.
type CatalogIndex struct {
	byTerm    map[string]CatalogEntity
	bySurname map[string]CatalogEntity
	teamIDs   map[string]bool
}

// BuildCatalogIndex indexes rules by every normalized term (canonical
// name + aliases) and, separately, by normalized surname — but only for
// a surname that belongs to exactly one rule's canonical name, mirroring
// the same uniqueness rule
// app/entitycatalogbuild.Build already uses to
// synthesize its own surname aliases. Rules whose Kind has no
// entityproposal.EntityType equivalent (eventsignals.EntityKindCompetition)
// are skipped entirely.
func BuildCatalogIndex(rules []eventsignals.EntityRule) CatalogIndex {
	idx := CatalogIndex{byTerm: map[string]CatalogEntity{}, bySurname: map[string]CatalogEntity{}, teamIDs: map[string]bool{}}
	surnameCount := map[string]int{}
	surnameFirst := map[string]CatalogEntity{}

	for _, r := range rules {
		et, ok := entityTypeForKind(r.Kind)
		if !ok {
			continue
		}
		ce := CatalogEntity{ID: r.ID, CanonicalName: r.CanonicalName, Type: et}
		if et == entityproposal.EntityTypeTeam {
			idx.teamIDs[r.ID] = true
		}
		for _, term := range r.Terms {
			idx.byTerm[entityproposal.Normalize(term)] = ce
		}
		if sn := entityproposal.Surname(r.CanonicalName); sn != "" {
			surnameCount[sn]++
			if _, seen := surnameFirst[sn]; !seen {
				surnameFirst[sn] = ce
			}
		}
	}
	for sn, count := range surnameCount {
		if count == 1 {
			idx.bySurname[sn] = surnameFirst[sn]
		}
	}
	return idx
}

func entityTypeForKind(k eventsignals.EntityKind) (entityproposal.EntityType, bool) {
	switch k {
	case eventsignals.EntityKindPlayer:
		return entityproposal.EntityTypePlayer, true
	case eventsignals.EntityKindCoach:
		return entityproposal.EntityTypeCoach, true
	case eventsignals.EntityKindExecutive:
		return entityproposal.EntityTypeExecutive, true
	case eventsignals.EntityKindTeam:
		return entityproposal.EntityTypeTeam, true
	default:
		return "", false
	}
}

// Lookup reports the catalog entity owning normalizedName (already
// passed through entityproposal.Normalize), if any.
func (idx CatalogIndex) Lookup(normalizedName string) (CatalogEntity, bool) {
	ce, ok := idx.byTerm[normalizedName]
	return ce, ok
}

// SurnameOwner reports the single catalog entity whose canonical name's
// surname (already passed through entityproposal.Normalize/Surname)
// matches normalizedSurname — only when exactly one catalog entity owns
// that surname.
func (idx CatalogIndex) SurnameOwner(normalizedSurname string) (CatalogEntity, bool) {
	ce, ok := idx.bySurname[normalizedSurname]
	return ce, ok
}

// TeamIDs returns the ids of every team-type catalog entity.
func (idx CatalogIndex) TeamIDs() map[string]bool {
	ids := make(map[string]bool, len(idx.teamIDs))
	for id := range idx.teamIDs {
		ids[id] = true
	}
	return ids
}

// TeamCanonicalName returns the canonical name of the team-type catalog entity
// with the given id ("" if there is none).
func (idx CatalogIndex) TeamCanonicalName(id string) string {
	for _, ce := range idx.byTerm {
		if ce.ID == id && ce.Type == entityproposal.EntityTypeTeam {
			return ce.CanonicalName
		}
	}
	return ""
}

// TeamIDByName reports the id of the team-type catalog entity owning
// normalizedName (already passed through entityproposal.Normalize), if
// any. Non-team entities never match.
func (idx CatalogIndex) TeamIDByName(normalizedName string) (string, bool) {
	ce, ok := idx.byTerm[normalizedName]
	if !ok || ce.Type != entityproposal.EntityTypeTeam {
		return "", false
	}
	return ce.ID, true
}
