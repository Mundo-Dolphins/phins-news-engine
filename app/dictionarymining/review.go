package dictionarymining

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// Decisions accepted by Decide.
const (
	ActionApprove    = "approve"
	ActionReject     = "reject"
	ActionCategorize = "categorize"
)

// Selection is one mining pass over a review session: the mined terms, the
// dictionary and filter they were mined against, and the non-fatal warnings
// about optional inputs that could not be used.
type Selection struct {
	Session    string
	Candidates []Candidate
	Warnings   []string
	Dictionary dictionary.Dictionary
	Filter     FilterConfig
}

// ReviewDeps are the collaborators of the mined-term review (Prepare, Offer,
// Decide, Write).
type ReviewDeps struct {
	// Mine selects the terms of sessionID ("" = the latest completed session)
	// without persisting anything. A failure may come with the warnings
	// gathered before it.
	Mine       func(ctx context.Context, sessionID string) (Selection, error)
	Candidates out.DictionaryCandidateRepository
	// LoadDictionary reads the current dictionary file; SaveDictionary
	// replaces it.
	LoadDictionary func() (dictionary.Dictionary, error)
	SaveDictionary func(dictionary.Dictionary) error
	// IgnoreTerm appends a rejected term to the mining policy's
	// ignored_terms, reporting whether it wrote; PolicyPath names that file
	// in the notices.
	IgnoreTerm func(term string) (added bool, err error)
	PolicyPath string
	Clock      out.Clock
	// Out receives the notices of a rejection; nil discards them.
	Out io.Writer
}

// Prepared is a session's mined-term review, ready for decisions.
type Prepared struct {
	Session  string
	Warnings []string
	// MinedCount is how many terms this mining pass produced.
	MinedCount int
	// Offered are the pending terms still produced by the current mining and
	// allowed by the policy, in review order.
	Offered []out.DictionaryCandidate
	// Categories are the dictionary's category names, sorted.
	Categories []string
}

// WriteResult reports what Write did. Added is 0, and the versions empty, when
// the dictionary was not modified.
type WriteResult struct {
	Approved               int
	Added                  int
	FromVersion, ToVersion string
}

// Prepare mines sessionID ("" = the latest completed session), saves the mined
// terms as pending (a term already saved keeps its decision) and returns the
// terms to review. On a mining failure the returned Prepared still carries the
// warnings gathered.
func Prepare(ctx context.Context, d ReviewDeps, sessionID string) (Prepared, error) {
	sel, err := d.Mine(ctx, sessionID)
	if err != nil {
		return Prepared{Session: sel.Session, Warnings: sel.Warnings}, err
	}
	return Offer(ctx, d, sel)
}

// Offer saves sel's terms as pending and returns the terms to review: on a
// resume, only pending terms the current mining still produces and the policy
// still allows, so fragments an earlier extraction persisted are not offered.
func Offer(ctx context.Context, d ReviewDeps, sel Selection) (Prepared, error) {
	p := Prepared{Session: sel.Session, Warnings: sel.Warnings, MinedCount: len(sel.Candidates)}
	current := make(map[string]bool, len(sel.Candidates))
	for _, c := range sel.Candidates {
		current[strings.ToLower(c.Term)] = true
		if err := d.Candidates.Save(ctx, out.DictionaryCandidate{
			Term: c.Term, SessionID: sel.Session, CategoryGuess: c.CategoryGuess,
			SupportCount: c.SupportCount, FirstSeenArticle: c.FirstSeenArticleID, Status: "pending",
		}); err != nil {
			return p, err
		}
	}
	existing, err := d.Candidates.ListBySession(ctx, sel.Session)
	if err != nil {
		return p, err
	}
	for _, c := range existing {
		if c.Status == "pending" && current[strings.ToLower(c.Term)] && sel.Filter.Allows(c.Term, c.SupportCount) {
			p.Offered = append(p.Offered, c)
		}
	}
	for _, cat := range sel.Dictionary.Categories {
		p.Categories = append(p.Categories, cat.Name)
	}
	sort.Strings(p.Categories)
	return p, nil
}

// Decide records the decision on one mined term of sessionID:
//
//   - approve: into category, or into the term's guess when category is
//     empty; the category must be one of the dictionary's.
//   - categorize: approve into category, which is required.
//   - reject: the term is also appended to the mining policy's ignored_terms
//     so no later run proposes it again; failing to do so is reported on
//     d.Out as a warning, not returned.
//
// The dictionary itself is only written by Write.
func Decide(ctx context.Context, d ReviewDeps, sessionID, term, action, category string) error {
	c, err := findCandidate(ctx, d, sessionID, term)
	if err != nil {
		return err
	}
	switch action {
	case ActionApprove, ActionCategorize:
		if action == ActionApprove && category == "" {
			category = c.CategoryGuess
		}
		if category == "" {
			return fmt.Errorf("dictionarymining: %q needs a category", term)
		}
		if err := checkCategory(d, category); err != nil {
			return err
		}
		return d.Candidates.UpdateDecision(ctx, c.Term, "approved", category, d.Clock.Now())
	case ActionReject:
		if err := d.Candidates.UpdateDecision(ctx, c.Term, "rejected", c.CategoryGuess, d.Clock.Now()); err != nil {
			return err
		}
		// Also remember it in the mining policy file, which outlives the
		// database and is shared between machines.
		w := d.Out
		if w == nil {
			w = io.Discard
		}
		if added, addErr := d.IgnoreTerm(c.Term); addErr != nil {
			fmt.Fprintf(w, "WARNING: rejected term not added to %s: %v\n", d.PolicyPath, addErr)
		} else if added {
			fmt.Fprintf(w, "  added %q to ignored_terms in %s\n", strings.ToLower(c.Term), d.PolicyPath)
		}
		return nil
	default:
		return fmt.Errorf("dictionarymining: unknown action %q (want approve, reject or categorize)", action)
	}
}

func findCandidate(ctx context.Context, d ReviewDeps, sessionID, term string) (out.DictionaryCandidate, error) {
	all, err := d.Candidates.ListBySession(ctx, sessionID)
	if err != nil {
		return out.DictionaryCandidate{}, err
	}
	for _, c := range all {
		if c.Term == term {
			return c, nil
		}
	}
	return out.DictionaryCandidate{}, fmt.Errorf("dictionarymining: %q is not a mined term of session %s", term, sessionID)
}

func checkCategory(d ReviewDeps, category string) error {
	dict, err := d.LoadDictionary()
	if err != nil {
		return err
	}
	for _, cat := range dict.Categories {
		if cat.Name == category {
			return nil
		}
	}
	return fmt.Errorf("dictionarymining: %q is not one of the dictionary's categories", category)
}

// Write adds every term approved in sessionID to the current dictionary file
// as a new entry of its category, bumps the patch version, validates the
// result and saves it. Terms the file already covers (written by an earlier
// Write) are skipped, so a second Write with no new approval changes nothing.
// Approved counts the session's approved terms whether or not any was new.
func Write(ctx context.Context, d ReviewDeps, sessionID string) (WriteResult, error) {
	var res WriteResult
	decided, err := d.Candidates.ListBySession(ctx, sessionID)
	if err != nil {
		return res, err
	}
	var approved []out.DictionaryCandidate
	for _, c := range decided {
		if c.Status == "approved" {
			approved = append(approved, c)
		}
	}
	res.Approved = len(approved)
	if len(approved) == 0 {
		return res, nil
	}

	dict, err := d.LoadDictionary()
	if err != nil {
		return res, err
	}
	originalVersion := dict.Version

	// The candidate store has no "already written" status distinct from
	// "approved", so a term an earlier Write added is recognised by the
	// freshly loaded file itself, with the matcher mining uses to skip terms
	// the dictionary already covers.
	matcher, err := dictionary.Prepare(dict)
	if err != nil {
		return res, fmt.Errorf("dictionary update: prepare matcher: %w", err)
	}
	now := d.Clock.Now()
	added := 0
	for _, c := range approved {
		if len(matcher.Find(c.Term)) > 0 {
			continue
		}
		for i := range dict.Categories {
			if dict.Categories[i].Name != c.CategoryGuess {
				continue
			}
			dict.Categories[i].Entries = append(dict.Categories[i].Entries, dictionary.Entry{
				CanonicalName: c.Term, Type: entryTypeForCategory(c.CategoryGuess),
				Source: "dictionary_update", UpdatedAt: now, Confidence: 0.5,
			})
			added++
		}
	}
	if added == 0 {
		return res, nil
	}

	newVersion, err := dictionary.BumpPatchVersion(dict.Version)
	if err != nil {
		return res, fmt.Errorf("dictionary update: %w", err)
	}
	dict.Version = newVersion
	dict.GeneratedAt = now
	if dict.SchemaVersion >= 2 {
		dict.Checksum = dictionary.ContentHash(dict)
	}
	for _, issue := range dictionary.Validate(dict) {
		if issue.Severity == dictionary.SeverityError {
			return res, fmt.Errorf("dictionary update: modified dictionary fails validation: %s", issue.String())
		}
	}
	if err := d.SaveDictionary(dict); err != nil {
		return res, err
	}
	res.Added, res.FromVersion, res.ToVersion = added, originalVersion, newVersion
	return res, nil
}

// entryTypeForCategory guesses the EntryType of a new entry landing in
// categoryName — informational only (EntryType doesn't gate scoring;
// Category.Weight/StrongOverride/RequiresAnyOf do), so an imperfect guess has
// no effect on classification.
func entryTypeForCategory(categoryName string) dictionary.EntryType {
	switch categoryName {
	case "coaching_staff":
		return dictionary.EntryTypeCoach
	case "owners":
		return dictionary.EntryTypeOwner
	case "front_office":
		return dictionary.EntryTypeFrontOffice
	case "rivals":
		return dictionary.EntryTypeRival
	case "venue":
		return dictionary.EntryTypeVenue
	case "hashtags":
		return dictionary.EntryTypeHashtag
	case "abbreviations":
		return dictionary.EntryTypeAbbreviation
	case "official_names":
		return dictionary.EntryTypeOfficialName
	case "url_patterns":
		return dictionary.EntryTypeURLPattern
	case "ambiguous_team_terms":
		return dictionary.EntryTypeAmbiguous
	case "alumni", "active_roster_core", "nfl_people_context":
		return dictionary.EntryTypePerson
	default:
		if strings.HasPrefix(categoryName, "negative_") {
			return dictionary.EntryTypeNegative
		}
		return dictionary.EntryTypeNFLContext
	}
}
