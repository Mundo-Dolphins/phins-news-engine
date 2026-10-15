package dictionaryupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
)

type ReviewOutcome string

const (
	ReviewOutcomeAccepted ReviewOutcome = "accepted"
	ReviewOutcomeRejected ReviewOutcome = "rejected"
	ReviewOutcomeSkipped  ReviewOutcome = "skipped"
)

type ReviewResult struct {
	Proposal Proposal
	Outcome  ReviewOutcome
}

var ErrReviewAborted = errors.New("dictionaryupdate: review aborted by user")

// SaveProgress is called after every individual decision (including each
// one produced by accept-all/reject-all) so a session can be persisted
// incrementally — a crash loses at most the proposal currently in flight.
type SaveProgress func(cursor int, result ReviewResult) error

// ReviewProposals walks every proposal through port starting at startAt.
// ActionAcceptAllOfType/ActionRejectAllOfType apply to every later
// proposal sharing that Kind without consulting port again for them.
// Returns ErrReviewAborted (with whatever results were produced so far)
// on ActionAbort, and stops early without error on ActionQuitAndSave.
func ReviewProposals(ctx context.Context, proposals []Proposal, startAt int, port ReviewPort, save SaveProgress) ([]ReviewResult, error) {
	results := make([]ReviewResult, 0, len(proposals))
	blanket := make(map[ProposalKind]Action)

	i := startAt
	for i < len(proposals) {
		p := proposals[i]

		if b, ok := blanket[p.Kind]; ok {
			var err error
			results, err = recordAndAdvance(results, &i, save, ReviewResult{Proposal: p, Outcome: outcomeForBlanket(b)})
			if err != nil {
				return results, err
			}
			continue
		}

		action, edited, err := port.Next(ctx, p, ReviewState{Index: i, Total: len(proposals)})
		if err != nil {
			return results, fmt.Errorf("dictionaryupdate: review proposal %d: %w", i, err)
		}

		switch action {
		case ActionAccept:
			results, err = recordAndAdvance(results, &i, save, ReviewResult{Proposal: p, Outcome: ReviewOutcomeAccepted})
			if err != nil {
				return results, err
			}
		case ActionEdit:
			if edited == nil {
				return results, fmt.Errorf("dictionaryupdate: ActionEdit returned with no edited proposal")
			}
			results, err = recordAndAdvance(results, &i, save, ReviewResult{Proposal: *edited, Outcome: ReviewOutcomeAccepted})
			if err != nil {
				return results, err
			}
		case ActionReject:
			results, err = recordAndAdvance(results, &i, save, ReviewResult{Proposal: p, Outcome: ReviewOutcomeRejected})
			if err != nil {
				return results, err
			}
		case ActionSkip:
			results, err = recordAndAdvance(results, &i, save, ReviewResult{Proposal: p, Outcome: ReviewOutcomeSkipped})
			if err != nil {
				return results, err
			}
		case ActionAcceptAllOfType:
			blanket[p.Kind] = ActionAccept
		case ActionRejectAllOfType:
			blanket[p.Kind] = ActionReject
		case ActionBack:
			// A back action must never regress the cursor below this
			// invocation's own starting point: for a resumed session
			// (startAt > 0), proposals before startAt were already
			// decided and persisted in a prior invocation, and this
			// invocation's local results slice holds no entry for them
			// to pop. When i == startAt, back is a no-op: the loop
			// simply re-prompts for the same proposal, exactly like the
			// ActionViewSource/ActionHelp/... "no state change" cases.
			if i > startAt {
				i--
				if len(results) > 0 {
					results = results[:len(results)-1]
				}
			}
		case ActionQuitAndSave:
			return results, nil
		case ActionAbort:
			return results, ErrReviewAborted
		case ActionViewSource, ActionViewCurrentEntry, ActionViewConflicts, ActionHelp:
			// No state change: the ReviewPort itself renders these and
			// re-prompts for the same index on the next loop pass.
		default:
			return results, fmt.Errorf("dictionaryupdate: unknown review action %q", action)
		}
	}
	return results, nil
}

// recordAndAdvance appends r to results, advances the cursor *i past the
// proposal it was just decided for, and persists the decision via save
// (if set) — the single place that performs this record+advance+save
// triple, shared by every decisive action (accept/edit/reject/skip, and
// each blanket-expanded decision from accept-all/reject-all).
func recordAndAdvance(results []ReviewResult, i *int, save SaveProgress, r ReviewResult) ([]ReviewResult, error) {
	results = append(results, r)
	*i++
	if err := saveIfSet(save, *i, r); err != nil {
		return results, err
	}
	return results, nil
}

func outcomeForBlanket(a Action) ReviewOutcome {
	if a == ActionAccept {
		return ReviewOutcomeAccepted
	}
	return ReviewOutcomeRejected
}

func saveIfSet(save SaveProgress, cursor int, r ReviewResult) error {
	if save == nil {
		return nil
	}
	if err := save(cursor, r); err != nil {
		return fmt.Errorf("dictionaryupdate: save progress: %w", err)
	}
	return nil
}

type entryLocation struct{ category, entry int }

// ApplyDecisions builds a candidate Dictionary from current plus every
// accepted ReviewResult. Rejected and skipped results are ignored.
// Conflict/NeedsReview proposals are defensively never applied even if
// somehow marked accepted upstream.
func ApplyDecisions(current dictionary.Dictionary, results []ReviewResult) (dictionary.Dictionary, int) {
	candidate := current
	candidate.Categories = make([]dictionary.Category, len(current.Categories))
	copy(candidate.Categories, current.Categories)
	for i := range candidate.Categories {
		entries := make([]dictionary.Entry, len(candidate.Categories[i].Entries))
		copy(entries, candidate.Categories[i].Entries)
		candidate.Categories[i].Entries = entries
	}

	categoryIndex := make(map[string]int, len(candidate.Categories))
	for i, c := range candidate.Categories {
		categoryIndex[c.Name] = i
	}
	entryIndex := make(map[string]entryLocation)
	for ci, c := range candidate.Categories {
		for ei, e := range c.Entries {
			entryIndex[CanonicalKey(e.CanonicalName)] = entryLocation{ci, ei}
		}
	}

	changeCount := 0
	for _, r := range results {
		if r.Outcome != ReviewOutcomeAccepted {
			continue
		}
		p := r.Proposal
		switch p.Kind {
		case ProposalConflict, ProposalNeedsReview, ProposalNoChange:
			continue

		case ProposalAddEntity:
			if p.Observed == nil {
				continue
			}
			ci, ok := categoryIndex[p.Category]
			if !ok {
				candidate.Categories = append(candidate.Categories, dictionary.Category{Name: p.Category, Weight: 1})
				ci = len(candidate.Categories) - 1
				categoryIndex[p.Category] = ci
			}
			e := dictionary.Entry{
				CanonicalName: p.Observed.DisplayName,
				Type:          dictionary.EntryTypePerson,
				Role:          p.Observed.Role,
				Position:      p.Observed.Position,
				Number:        p.Observed.Number,
				RosterStatus:  p.Observed.RosterStatus,
				Source:        p.Observed.Source,
				UpdatedAt:     p.Observed.ObservedAt,
				Confidence:    0.8,
			}
			candidate.Categories[ci].Entries = append(candidate.Categories[ci].Entries, e)
			entryIndex[p.CanonicalKey] = entryLocation{ci, len(candidate.Categories[ci].Entries) - 1}
			changeCount++

		case ProposalReactivateEntity, ProposalUpdateRole, ProposalUpdatePosition, ProposalUpdateNumber, ProposalUpdateRosterStatus:
			loc, ok := entryIndex[p.CanonicalKey]
			if !ok || p.Observed == nil {
				continue
			}
			e := &candidate.Categories[loc.category].Entries[loc.entry]
			switch p.Kind {
			case ProposalReactivateEntity:
				e.ActiveUntil = nil
			case ProposalUpdateRole:
				e.Role = p.Observed.Role
			case ProposalUpdatePosition:
				e.Position = p.Observed.Position
			case ProposalUpdateNumber:
				e.Number = p.Observed.Number
			case ProposalUpdateRosterStatus:
				e.RosterStatus = p.Observed.RosterStatus
			}
			changeCount++

		case ProposalDeactivateEntity:
			loc, ok := entryIndex[p.CanonicalKey]
			if !ok {
				continue
			}
			now := time.Now().UTC()
			candidate.Categories[loc.category].Entries[loc.entry].ActiveUntil = &now
			changeCount++

		case ProposalAddAlias:
			loc, ok := entryIndex[p.CanonicalKey]
			if !ok || p.AliasProposal == nil {
				continue
			}
			e := &candidate.Categories[loc.category].Entries[loc.entry]
			e.Aliases = append(e.Aliases, p.AliasProposal.Value)
			changeCount++

		case ProposalMarkAliasAmbiguous:
			// Ambiguous aliases go into Notes, never Aliases — putting
			// them in Aliases would feed the matcher directly, defeating
			// the entire point of flagging them; they need contextual
			// review in the relevance classifier instead.
			loc, ok := entryIndex[p.CanonicalKey]
			if !ok || p.AliasProposal == nil {
				continue
			}
			e := &candidate.Categories[loc.category].Entries[loc.entry]
			e.Notes = appendNote(e.Notes, fmt.Sprintf("ambiguous alias candidate: %q (needs contextual confirmation)", p.AliasProposal.Value))
			changeCount++

		default:
			// ProposalRemoveAlias/ProposalUpdateSource/ProposalUpdateConfidence
			// are not currently produced by Compare and have no apply
			// behavior defined yet; explicitly a no-op rather than
			// silently falling through, so a future proposal kind is
			// never applied by accident.
			continue
		}
	}

	return candidate, changeCount
}

func appendNote(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

// DefaultToolName is the Tool value stamped on every written dictionary
// version unless SetToolName overrides it.
const DefaultToolName = "phins-news-engine-tools dictionary update"

var toolName atomic.Pointer[string]

// SetToolName sets the Tool value WriteCandidate stamps on new dictionary
// versions; an empty name restores DefaultToolName.
func SetToolName(name string) {
	if strings.TrimSpace(name) == "" {
		toolName.Store(nil)
		return
	}
	toolName.Store(&name)
}

// ToolName returns the Tool value WriteCandidate currently stamps.
func ToolName() string {
	if p := toolName.Load(); p != nil {
		return *p
	}
	return DefaultToolName
}

// WriteCandidate validates candidate, assigns it a new version and
// schema-v2 metadata, then calls backup(current) followed by write
// (candidate) — in that order — so the outgoing version is always
// preserved before anything is overwritten. It has no direct file I/O of
// its own.
func WriteCandidate(current, candidate dictionary.Dictionary, changeCount int, sources []string, snapshotRef string, now time.Time,
	backup func(previous dictionary.Dictionary) error,
	write func(d dictionary.Dictionary) error,
) (dictionary.Dictionary, error) {
	// candidate carries over current's stale Checksum unchanged (only
	// Categories is mutated by ApplyDecisions) -- clear it before this
	// first validation pass so the schema-v2 checksum check (gated on
	// Checksum != "") doesn't compare the OLD checksum against the NEW,
	// post-ApplyDecisions content and spuriously fail on every
	// content-changing update once current is already schema-v2 with a
	// stamped checksum. The real checksum is computed fresh below and
	// verified by the second Validate pass.
	candidate.Checksum = ""
	for _, iss := range dictionary.Validate(candidate) {
		if iss.Severity == dictionary.SeverityError {
			return dictionary.Dictionary{}, fmt.Errorf("dictionaryupdate: candidate dictionary failed validation: %s", iss.String())
		}
	}

	newVersion, err := dictionary.BumpPatchVersion(current.Version)
	if err != nil {
		return dictionary.Dictionary{}, fmt.Errorf("dictionaryupdate: bump version: %w", err)
	}

	candidate.Version = newVersion
	candidate.GeneratedAt = now
	candidate.SchemaVersion = 2
	candidate.Sources = sources
	candidate.SnapshotRef = snapshotRef
	candidate.ChangeCount = changeCount
	candidate.Tool = ToolName()
	candidate.Checksum = dictionary.ContentHash(candidate)

	for _, iss := range dictionary.Validate(candidate) {
		if iss.Severity == dictionary.SeverityError {
			return dictionary.Dictionary{}, fmt.Errorf("dictionaryupdate: candidate dictionary failed post-versioning validation: %s", iss.String())
		}
	}

	if backup != nil {
		if err := backup(current); err != nil {
			return dictionary.Dictionary{}, fmt.Errorf("dictionaryupdate: backup previous version: %w", err)
		}
	}
	if err := write(candidate); err != nil {
		return dictionary.Dictionary{}, fmt.Errorf("dictionaryupdate: write candidate: %w", err)
	}
	return candidate, nil
}

// UpdateAbsenceLog advances the absence streak for every active,
// source-tracked current entry that was not present in observed this run,
// and clears the streak for any entry that reappeared. Compare only reads
// AbsenceLog; this is the step that actually moves it forward once a
// fetch+compare cycle completes.
func UpdateAbsenceLog(current dictionary.Dictionary, observed []SourceEntity, absences AbsenceLog, now time.Time) AbsenceLog {
	seen := make(map[string]bool, len(observed))
	for _, o := range observed {
		seen[o.CanonicalKey] = true
	}

	updated := AbsenceLog{Records: make(map[string]AbsenceRecord)}
	for _, cat := range current.Categories {
		kind, ok := sourceKindForCategory(cat.Name)
		if !ok {
			continue
		}
		for _, e := range cat.Entries {
			if e.ActiveUntil != nil {
				continue
			}
			key := CanonicalKey(e.CanonicalName)
			if seen[key] {
				continue
			}
			rec, had := absences.Get(key, kind)
			if had {
				rec.ConsecutiveAbsences++
				rec.LastCheckedAt = now
			} else {
				rec = AbsenceRecord{CanonicalKey: key, Source: kind, FirstAbsentAt: now, LastCheckedAt: now, ConsecutiveAbsences: 1}
			}
			updated.Records[absenceKey(key, kind)] = rec
		}
	}
	return updated
}

var recognizedSafeCategories = map[string]bool{
	"active_roster_core": true,
	"coaching_staff":     true,
	"front_office":       true,
}

// IsSafeAddition implements the narrow, explicit "safe additions" policy
// from the original requirements: brand-new (AddEntity only), a full
// display name (not initials-only — at least two space-separated parts),
// a non-empty source, a captured profile URL, a recognized category, and
// zero conflicts for that key. Every clause is required; this is
// deliberately conservative.
func IsSafeAddition(p Proposal, hasConflict bool) bool {
	if p.Kind != ProposalAddEntity || p.Observed == nil || hasConflict {
		return false
	}
	if !recognizedSafeCategories[p.Category] {
		return false
	}
	if p.Observed.Source == "" || p.Observed.ProfileURL == "" {
		return false
	}
	nameParts := 0
	for _, f := range strings.Fields(p.Observed.DisplayName) {
		if len(f) > 2 { // excludes bare initials like "T." from counting as a real name part
			nameParts++
		}
	}
	return nameParts >= 2
}
