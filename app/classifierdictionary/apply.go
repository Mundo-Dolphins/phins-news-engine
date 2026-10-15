package classifierdictionary

import (
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
)

func ApplyApproval(p classifierproposal.Proposal, dictPath string, now time.Time) error {
	current, err := dictionaryfile.Load(dictPath)
	if err != nil {
		return fmt.Errorf("classifierdictionary: load %s: %w", dictPath, err)
	}
	candidate, changes, err := applyToDictionary(current, p, now)
	if err != nil {
		return err
	}
	if changes == 0 {
		return fmt.Errorf("classifierdictionary: proposal %s produced no dictionary changes", p.ID)
	}
	_, err = dictionaryupdate.WriteCandidate(current, candidate, changes, []string{"classifier dictionary proposals"}, "classifier-proposal:"+p.ID, now,
		func(previous dictionary.Dictionary) error { return backupDictionaryFile(dictPath, previous, now) },
		func(d dictionary.Dictionary) error { return dictionaryfile.Save(dictPath, d) },
	)
	return err
}

func applyToDictionary(current dictionary.Dictionary, p classifierproposal.Proposal, now time.Time) (dictionary.Dictionary, int, error) {
	switch p.Kind {
	case classifierproposal.KindTerm, classifierproposal.KindNegativeTerm, classifierproposal.KindContextualTerm:
		return addEntry(current, p, now)
	case classifierproposal.KindAlias:
		return addAlias(current, p)
	case classifierproposal.KindTermPromotion, classifierproposal.KindTermRemoval, classifierproposal.KindWeightChange:
		return dictionary.Dictionary{}, 0, fmt.Errorf("classifierdictionary: proposal kind %q is reviewable but not automatically applicable yet", p.Kind)
	default:
		return dictionary.Dictionary{}, 0, fmt.Errorf("classifierdictionary: unknown proposal kind %q", p.Kind)
	}
}

func addEntry(current dictionary.Dictionary, p classifierproposal.Proposal, now time.Time) (dictionary.Dictionary, int, error) {
	if p.Category == "" {
		return dictionary.Dictionary{}, 0, fmt.Errorf("classifierdictionary: proposal %s has no category", p.ID)
	}
	if p.Value == "" {
		return dictionary.Dictionary{}, 0, fmt.Errorf("classifierdictionary: proposal %s has no value", p.ID)
	}
	matcher, err := dictionary.Prepare(current)
	if err != nil {
		return dictionary.Dictionary{}, 0, fmt.Errorf("classifierdictionary: prepare dictionary: %w", err)
	}
	if len(matcher.Find(p.Value)) > 0 {
		return current, 0, nil
	}

	candidate := cloneDictionary(current)
	entry := dictionary.Entry{
		CanonicalName: p.Value,
		Type:          entryTypeForCategory(p.Category, p.Kind),
		Source:        "classifier_dictionary_proposals",
		UpdatedAt:     now,
		Confidence:    p.Confidence,
		Notes:         fmt.Sprintf("proposal_id=%s occurrences=%d", p.ID, p.Occurrences),
	}
	for i := range candidate.Categories {
		if candidate.Categories[i].Name == p.Category {
			candidate.Categories[i].Entries = append(candidate.Categories[i].Entries, entry)
			return candidate, 1, nil
		}
	}
	candidate.Categories = append(candidate.Categories, dictionary.Category{Name: p.Category, Weight: 1, Entries: []dictionary.Entry{entry}})
	return candidate, 1, nil
}

func addAlias(current dictionary.Dictionary, p classifierproposal.Proposal) (dictionary.Dictionary, int, error) {
	target := p.TargetEntry
	if target == "" {
		target = p.Metadata["target_entry"]
	}
	if target == "" {
		return dictionary.Dictionary{}, 0, fmt.Errorf("classifierdictionary: alias proposal %s has no target entry", p.ID)
	}
	candidate := cloneDictionary(current)
	targetKey := dictionaryupdate.CanonicalKey(target)
	aliasKey := classifierproposal.Normalize(p.Value)
	for ci := range candidate.Categories {
		for ei := range candidate.Categories[ci].Entries {
			e := &candidate.Categories[ci].Entries[ei]
			if dictionaryupdate.CanonicalKey(e.CanonicalName) != targetKey {
				continue
			}
			for _, existing := range e.Aliases {
				if classifierproposal.Normalize(existing) == aliasKey {
					return current, 0, nil
				}
			}
			e.Aliases = append(e.Aliases, p.Value)
			return candidate, 1, nil
		}
	}
	return dictionary.Dictionary{}, 0, fmt.Errorf("classifierdictionary: target entry %q not found", target)
}

func cloneDictionary(current dictionary.Dictionary) dictionary.Dictionary {
	candidate := current
	candidate.Categories = make([]dictionary.Category, len(current.Categories))
	copy(candidate.Categories, current.Categories)
	for i := range candidate.Categories {
		entries := make([]dictionary.Entry, len(candidate.Categories[i].Entries))
		copy(entries, candidate.Categories[i].Entries)
		candidate.Categories[i].Entries = entries
	}
	return candidate
}

func entryTypeForCategory(category string, kind classifierproposal.Kind) dictionary.EntryType {
	if kind == classifierproposal.KindNegativeTerm || stringsHasPrefix(category, "negative_") {
		return dictionary.EntryTypeNegative
	}
	switch category {
	case "active_roster_core", "nfl_people_context":
		return dictionary.EntryTypePerson
	case "coaching_staff":
		return dictionary.EntryTypeCoach
	case "front_office":
		return dictionary.EntryTypeFrontOffice
	case "owners":
		return dictionary.EntryTypeOwner
	case "rivals":
		return dictionary.EntryTypeRival
	case "ambiguous_team_terms":
		return dictionary.EntryTypeAmbiguous
	default:
		return dictionary.EntryTypeNFLContext
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func backupDictionaryFile(path string, previous dictionary.Dictionary, now time.Time) error {
	backupPath := fmt.Sprintf("%s.bak-%s", path, now.UTC().Format("20060102T150405Z"))
	return dictionaryfile.Save(backupPath, previous)
}
