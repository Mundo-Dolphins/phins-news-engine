package dictionarywiring

import (
	"fmt"
	"io"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
)

// DiffSection is one titled group of change proposals, as the diff renderers
// print them.
type DiffSection struct {
	Title     string
	Proposals []dictionaryupdate.Proposal
}

var diffSectionOrder = []struct {
	title string
	kinds []dictionaryupdate.ProposalKind
}{
	{"New entities", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalAddEntity}},
	{"Possible departures", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalDeactivateEntity, dictionaryupdate.ProposalNeedsReview}},
	{"Reactivations", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalReactivateEntity}},
	{"Role changes", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalUpdateRole}},
	{"Position changes", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalUpdatePosition}},
	{"Number changes", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalUpdateNumber}},
	{"Roster status changes", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalUpdateRosterStatus}},
	{"New aliases", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalAddAlias}},
	{"Ambiguous aliases", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalMarkAliasAmbiguous}},
	{"Conflicts", []dictionaryupdate.ProposalKind{dictionaryupdate.ProposalConflict}},
}

// DiffSections groups proposals into the non-empty sections in their fixed
// display order and counts the no-change proposals separately.
func DiffSections(proposals []dictionaryupdate.Proposal) (sections []DiffSection, unchanged int) {
	grouped := make(map[dictionaryupdate.ProposalKind][]dictionaryupdate.Proposal)
	for _, p := range proposals {
		if p.Kind == dictionaryupdate.ProposalNoChange {
			unchanged++
			continue
		}
		grouped[p.Kind] = append(grouped[p.Kind], p)
	}
	for _, section := range diffSectionOrder {
		var items []dictionaryupdate.Proposal
		for _, k := range section.kinds {
			items = append(items, grouped[k]...)
		}
		if len(items) > 0 {
			sections = append(sections, DiffSection{Title: section.title, Proposals: items})
		}
	}
	return sections, unchanged
}

// RenderDiffText writes proposals as the plain-text diff shared by
// `dictionary diff` and `dictionary rollback`.
func RenderDiffText(w io.Writer, proposals []dictionaryupdate.Proposal) error {
	sections, unchanged := DiffSections(proposals)
	for _, section := range sections {
		fmt.Fprintf(w, "%s (%d):\n", section.Title, len(section.Proposals))
		for _, p := range section.Proposals {
			fmt.Fprintf(w, "  - %s: %s\n", p.CanonicalKey, p.Detail)
		}
	}
	fmt.Fprintf(w, "Unchanged: %d\n", unchanged)
	return nil
}
