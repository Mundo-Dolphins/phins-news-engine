package dictionaryupdate

import (
	"fmt"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

type ProposalKind string

const (
	ProposalAddEntity          ProposalKind = "add_entity"
	ProposalReactivateEntity   ProposalKind = "reactivate_entity"
	ProposalDeactivateEntity   ProposalKind = "deactivate_entity"
	ProposalUpdateRole         ProposalKind = "update_role"
	ProposalUpdatePosition     ProposalKind = "update_position"
	ProposalUpdateNumber       ProposalKind = "update_number"
	ProposalUpdateRosterStatus ProposalKind = "update_roster_status"
	ProposalAddAlias           ProposalKind = "add_alias"
	ProposalRemoveAlias        ProposalKind = "remove_alias"
	ProposalMarkAliasAmbiguous ProposalKind = "mark_alias_ambiguous"
	ProposalUpdateSource       ProposalKind = "update_source"
	ProposalUpdateConfidence   ProposalKind = "update_confidence"
	ProposalNoChange           ProposalKind = "no_change"
	ProposalConflict           ProposalKind = "conflict"
	ProposalNeedsReview        ProposalKind = "needs_review"
)

type Proposal struct {
	Kind          ProposalKind
	CanonicalKey  string
	Category      string
	CurrentEntry  *dictionary.Entry
	Observed      *SourceEntity
	AliasProposal *AliasCandidate // set only when Kind is AddAlias or MarkAliasAmbiguous: the one specific candidate this proposal is about, since Observed.Aliases may hold several. Populated by Task 13's aliasProposals and consumed by Task 19's ApplyDecisions so accepting one alias proposal never applies every candidate on the entity.
	Detail        string
	ConflictWith  []string
}

type AbsenceRecord struct {
	CanonicalKey        string
	Source              SourceKind
	FirstAbsentAt       time.Time
	LastCheckedAt       time.Time
	ConsecutiveAbsences int
}

type AbsenceLog struct {
	Records map[string]AbsenceRecord
}

func absenceKey(canonicalKey string, source SourceKind) string {
	return canonicalKey + "|" + string(source)
}

func (l AbsenceLog) Get(canonicalKey string, source SourceKind) (AbsenceRecord, bool) {
	if l.Records == nil {
		return AbsenceRecord{}, false
	}
	r, ok := l.Records[absenceKey(canonicalKey, source)]
	return r, ok
}

type DeactivationPolicy struct {
	MinimumSuccessfulAbsences int
	MinimumElapsedDays        int
}

func categoryForKind(kind SourceKind) string {
	switch kind {
	case SourceKindRoster:
		return "active_roster_core"
	case SourceKindCoaches:
		return "coaching_staff"
	case SourceKindFrontOffice:
		return "front_office"
	default:
		return ""
	}
}

func sourceKindForCategory(category string) (SourceKind, bool) {
	switch category {
	case "active_roster_core":
		return SourceKindRoster, true
	case "coaching_staff":
		return SourceKindCoaches, true
	case "front_office":
		return SourceKindFrontOffice, true
	default:
		return "", false
	}
}

type currentEntry struct {
	category string
	entry    dictionary.Entry
}

// buildAliasOwners maps a normalized alias to the canonical key of the
// current-dictionary entry that owns it, across every category.
func buildAliasOwners(current dictionary.Dictionary) map[string]string {
	owners := make(map[string]string)
	for _, cat := range current.Categories {
		for _, e := range cat.Entries {
			key := CanonicalKey(e.CanonicalName)
			for _, a := range e.Aliases {
				owners[CanonicalKey(a)] = key
			}
		}
	}
	return owners
}

// aliasProposals compares an observed entity's alias candidates against
// the entity's already-known aliases (empty for a brand-new entity) and
// every alias currently owned dictionary-wide. A candidate already owned
// by a *different* entity is always a Conflict, never silently merged.
func aliasProposals(obs SourceEntity, currentAliases []string, aliasOwners map[string]string, category string) []Proposal {
	existing := make(map[string]bool, len(currentAliases))
	for _, a := range currentAliases {
		existing[CanonicalKey(a)] = true
	}

	var out []Proposal
	for _, cand := range obs.Aliases {
		normKey := CanonicalKey(cand.Value)
		if normKey == "" || normKey == obs.CanonicalKey || existing[normKey] {
			continue
		}
		c := cand
		if owner, owned := aliasOwners[normKey]; owned && owner != obs.CanonicalKey {
			out = append(out, Proposal{
				Kind: ProposalConflict, CanonicalKey: obs.CanonicalKey, Category: category,
				Detail:       fmt.Sprintf("alias %q is already associated with entity %q", cand.Value, owner),
				ConflictWith: []string{owner},
			})
			continue
		}
		if cand.Class == AliasClassAmbiguous {
			out = append(out, Proposal{
				Kind: ProposalMarkAliasAmbiguous, CanonicalKey: obs.CanonicalKey, Category: category,
				AliasProposal: &c,
				Detail:        fmt.Sprintf("candidate alias %q is ambiguous (bare first/last name) and needs contextual confirmation", cand.Value),
			})
			continue
		}
		out = append(out, Proposal{
			Kind: ProposalAddAlias, CanonicalKey: obs.CanonicalKey, Category: category,
			AliasProposal: &c,
			Detail:        fmt.Sprintf("candidate alias %q (%s)", cand.Value, cand.Class),
		})
	}
	return out
}

// Compare produces proposals describing every difference between the
// current dictionary and a batch of freshly observed source entities. It
// never mutates current and never silently applies anything — callers
// decide what to accept.
func Compare(current dictionary.Dictionary, observed []SourceEntity, absences AbsenceLog, policy DeactivationPolicy, now time.Time) []Proposal {
	byKey := make(map[string]currentEntry)
	for _, cat := range current.Categories {
		for _, e := range cat.Entries {
			byKey[CanonicalKey(e.CanonicalName)] = currentEntry{category: cat.Name, entry: e}
		}
	}

	aliasOwners := buildAliasOwners(current)

	observedByKey := make(map[string][]SourceEntity)
	for _, obs := range observed {
		observedByKey[obs.CanonicalKey] = append(observedByKey[obs.CanonicalKey], obs)
	}

	var proposals []Proposal
	seen := make(map[string]bool)

	for key, group := range observedByKey {
		if len(group) > 1 {
			proposals = append(proposals, Proposal{
				Kind: ProposalConflict, CanonicalKey: key,
				Detail: fmt.Sprintf("%d observed entities share canonical key %q", len(group), key),
			})
			seen[key] = true
		}
	}

	for _, obs := range observed {
		if seen[obs.CanonicalKey] {
			continue
		}
		seen[obs.CanonicalKey] = true

		cur, exists := byKey[obs.CanonicalKey]
		if !exists {
			o := obs
			proposals = append(proposals, Proposal{
				Kind: ProposalAddEntity, CanonicalKey: obs.CanonicalKey, Category: categoryForKind(obs.Kind),
				Observed: &o, Detail: fmt.Sprintf("new entity observed on %s", obs.Kind),
			})
			proposals = append(proposals, aliasProposals(obs, nil, aliasOwners, categoryForKind(obs.Kind))...)
			continue
		}

		e := cur.entry
		anyChange := false

		if e.ActiveUntil != nil {
			o, ce := obs, e
			proposals = append(proposals, Proposal{
				Kind: ProposalReactivateEntity, CanonicalKey: obs.CanonicalKey, Category: cur.category,
				CurrentEntry: &ce, Observed: &o, Detail: "entity observed again after being marked inactive",
			})
			anyChange = true
		}
		if obs.Role != "" && obs.Role != e.Role {
			o, ce := obs, e
			proposals = append(proposals, Proposal{
				Kind: ProposalUpdateRole, CanonicalKey: obs.CanonicalKey, Category: cur.category,
				CurrentEntry: &ce, Observed: &o, Detail: fmt.Sprintf("role %q -> %q", e.Role, obs.Role),
			})
			anyChange = true
		}
		if obs.Position != "" && obs.Position != e.Position {
			o, ce := obs, e
			proposals = append(proposals, Proposal{
				Kind: ProposalUpdatePosition, CanonicalKey: obs.CanonicalKey, Category: cur.category,
				CurrentEntry: &ce, Observed: &o, Detail: fmt.Sprintf("position %q -> %q", e.Position, obs.Position),
			})
			anyChange = true
		}
		if obs.Number != "" && obs.Number != e.Number {
			o, ce := obs, e
			proposals = append(proposals, Proposal{
				Kind: ProposalUpdateNumber, CanonicalKey: obs.CanonicalKey, Category: cur.category,
				CurrentEntry: &ce, Observed: &o, Detail: fmt.Sprintf("number %q -> %q", e.Number, obs.Number),
			})
			anyChange = true
		}
		if obs.RosterStatus != "" && obs.RosterStatus != e.RosterStatus {
			o, ce := obs, e
			proposals = append(proposals, Proposal{
				Kind: ProposalUpdateRosterStatus, CanonicalKey: obs.CanonicalKey, Category: cur.category,
				CurrentEntry: &ce, Observed: &o, Detail: fmt.Sprintf("roster_status %q -> %q", e.RosterStatus, obs.RosterStatus),
			})
			anyChange = true
		}
		if aliasProps := aliasProposals(obs, e.Aliases, aliasOwners, cur.category); len(aliasProps) > 0 {
			proposals = append(proposals, aliasProps...)
			anyChange = true
		}

		if !anyChange {
			ce := e
			proposals = append(proposals, Proposal{Kind: ProposalNoChange, CanonicalKey: obs.CanonicalKey, Category: cur.category, CurrentEntry: &ce})
		}
	}

	for key, cur := range byKey {
		if seen[key] || cur.entry.ActiveUntil != nil {
			continue
		}
		kind, ok := sourceKindForCategory(cur.category)
		if !ok {
			continue // not a source-tracked category (e.g. official_names) -- absence is meaningless
		}
		rec, hadRecord := absences.Get(key, kind)
		consecutive := 1
		firstAbsent := now
		if hadRecord {
			consecutive = rec.ConsecutiveAbsences + 1
			firstAbsent = rec.FirstAbsentAt
		}
		elapsedDays := int(now.Sub(firstAbsent).Hours() / 24)

		ce := cur.entry
		if consecutive >= policy.MinimumSuccessfulAbsences && elapsedDays >= policy.MinimumElapsedDays {
			proposals = append(proposals, Proposal{
				Kind: ProposalDeactivateEntity, CanonicalKey: key, Category: cur.category, CurrentEntry: &ce,
				Detail: fmt.Sprintf("absent for %d consecutive successful run(s) over %d day(s)", consecutive, elapsedDays),
			})
		} else {
			proposals = append(proposals, Proposal{
				Kind: ProposalNeedsReview, CanonicalKey: key, Category: cur.category, CurrentEntry: &ce,
				Detail: fmt.Sprintf("absent %d consecutive run(s), not yet eligible for deactivation (need %d over %d days)", consecutive, policy.MinimumSuccessfulAbsences, policy.MinimumElapsedDays),
			})
		}
	}

	sort.Slice(proposals, func(i, j int) bool {
		if proposals[i].Category != proposals[j].Category {
			return proposals[i].Category < proposals[j].Category
		}
		if proposals[i].CanonicalKey != proposals[j].CanonicalKey {
			return proposals[i].CanonicalKey < proposals[j].CanonicalKey
		}
		return proposals[i].Kind < proposals[j].Kind
	})
	return proposals
}
