package dictionary

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionarymining"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clock"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryminingfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/nflverse"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func NewCommand() *cobra.Command {
	dictionaryCmd := &cobra.Command{Use: "dictionary", Short: "Dictionary maintenance"}
	dictionaryCmd.AddCommand(newValidateCommand())
	dictionaryCmd.AddCommand(newInspectCommand())
	dictionaryCmd.AddCommand(newMineCandidatesCommand())
	dictionaryCmd.AddCommand(newDiffCommand())
	dictionaryCmd.AddCommand(newHistoryCommand())
	dictionaryCmd.AddCommand(newRollbackCommand())
	dictionaryCmd.AddCommand(newRegressionCheckCommand())
	return dictionaryCmd
}

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a dictionary YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := dictionaryfile.Load(args[0])
			if err != nil {
				return err
			}
			issues := dictionary.Validate(d)
			hasError := false
			for _, issue := range issues {
				cmd.Println(issue.String())
				if issue.Severity == dictionary.SeverityError {
					hasError = true
				}
			}
			if hasError {
				return fmt.Errorf("dictionary validate: %s failed validation", args[0])
			}
			cmd.Printf("dictionary OK: %s (%d issue(s) reported)\n", args[0], len(issues))
			return nil
		},
	}
}

func newInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <path>",
		Short: "Print summary statistics for a dictionary YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := dictionaryfile.Load(args[0])
			if err != nil {
				return err
			}
			cmd.Printf("version: %s\nname: %s\nlanguage: %s\n", d.Version, d.Name, d.Language)
			cmd.Printf("categories: %d\n", len(d.Categories))
			totalEntries, totalAliases := 0, 0
			for _, cat := range d.Categories {
				aliases := 0
				for _, e := range cat.Entries {
					aliases += len(e.Aliases)
				}
				totalEntries += len(cat.Entries)
				totalAliases += aliases
				cmd.Printf("  %s: weight=%.1f strong_override=%v entries=%d aliases=%d\n",
					cat.Name, cat.Weight, cat.StrongOverride, len(cat.Entries), aliases)
			}
			cmd.Printf("total entries: %d\ntotal aliases: %d\n", totalEntries, totalAliases)
			cmd.Printf("thresholds: positive_high=%.1f positive_medium=%.1f negative_high=%.1f title_multiplier=%.1f max_count_per_term=%d\n",
				d.Decision.PositiveThresholdHigh, d.Decision.PositiveThresholdMedium, d.Decision.NegativeThresholdHigh,
				d.Decision.TitleWeightMultiplier, d.Decision.MaxCountPerTerm)
			return nil
		},
	}
}

func newMineCandidatesCommand() *cobra.Command {
	var sessionID string
	var dryRun bool
	updateCmd := &cobra.Command{
		Use:   "mine-candidates",
		Short: "Mine dictionary-candidate terms from a completed editorial review session",
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() { sessionID = ""; dryRun = false }()
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			if _, _, err := RunUpdateFlow(cmd, cfg, db, sessionID, dryRun, nil); err != nil {
				return err
			}
			return nil
		},
	}
	updateCmd.Flags().StringVar(&sessionID, "session", "", "review session to mine candidates from (default: the most recently completed session)")
	updateCmd.Flags().BoolVar(&dryRun, "dry-run", false, "list mined candidates without persisting them or entering the approval loop")
	return updateCmd
}

// RunUpdateFlow executes the interactive approve/reject loop for dictionary mining.
// It loads candidates from a review session, presents them for approval/rejection,
// and writes the updated dictionary based on approved candidates. approvedCount is
// the number of candidates left with status "approved" for sessionID once the flow
// exits (0 on a dry run or if nothing was approved), used by callers such as
// `editorial cycle` that report on how much this step contributed.
// paused signals quit or EOF before the review finished. A supplied scanner
// preserves input buffered by the preceding article review.
func RunUpdateFlow(cmd *cobra.Command, cfg *config.Config, db *sql.DB, sessionID string, dryRun bool, scanner *bufio.Scanner) (approvedCount int, paused bool, err error) {
	sessions := sqliteadapter.NewEditorialCycleSessionRepository(db)
	reviewDecisions := sqliteadapter.NewReviewDecisionRepository(db)
	articles := sqliteadapter.NewArticleRepository(db)
	candidatesRepo := sqliteadapter.NewDictionaryCandidateRepository(db)
	realClock := clock.Real{}

	targetSession := sessionID
	if targetSession == "" {
		s, err := sessions.GetLatestCompleted(cmd.Context())
		if err != nil {
			return 0, false, fmt.Errorf("dictionary update: no completed session found and --session not given: %w", err)
		}
		targetSession = s.ID
	}

	dict, err := dictionaryfile.Load(cfg.Dictionary.Path)
	if err != nil {
		return 0, false, err
	}
	matcher, err := dictionary.Prepare(dict)
	if err != nil {
		return 0, false, fmt.Errorf("dictionary update: prepare matcher: %w", err)
	}

	filter, err := dictionaryMiningFilter(cfg)
	if err != nil {
		return 0, false, err
	}
	catalogPaths := cfg.Clustering.EntityCatalog
	if catalogPaths.ManualPath != "" || catalogPaths.GeneratedPath != "" {
		entities, err := entitycatalog.Load(catalogPaths.ManualPath, catalogPaths.GeneratedPath)
		if err != nil {
			return 0, false, fmt.Errorf("dictionary update: load entity catalog: %w", err)
		}
		for _, entity := range entities {
			filter.KnownEntityTerms = append(filter.KnownEntityTerms, entity.CanonicalName)
			filter.KnownEntityTerms = append(filter.KnownEntityTerms, entity.Terms...)
		}
	}
	if p := catalogPaths.League.RosterPath; p != "" {
		if f, openErr := os.Open(p); openErr == nil { // #nosec G304 -- operator-configured roster path
			players, parseErr := nflverse.ParseRoster(f)
			_ = f.Close()
			if parseErr == nil {
				filter.KnownNames, filter.KnownSurnames = dictionaryminingfile.NamesFromRoster(players)
			} else {
				cmd.Printf("WARNING: nflverse roster %s unreadable (%v); people it covers are not filtered out\n", p, parseErr)
			}
		} else {
			cmd.Printf("WARNING: nflverse roster %s not found; people it covers are not filtered out\n", p)
		}
	}
	if p := cfg.DictionaryMining.EnglishWordsPath; p != "" {
		if words, wordsErr := dictionaryminingfile.LoadEnglishWords(p); wordsErr == nil {
			filter.EnglishWords = words
		} else {
			cmd.Printf("WARNING: english word list unavailable (%v); coined words (nicknames, slang) are not mined\n", wordsErr)
		}
	}
	rejectedTerms, err := candidatesRepo.ListRejectedTerms(cmd.Context())
	if err != nil {
		return 0, false, err
	}
	filter.IgnoredTerms = append(filter.IgnoredTerms, rejectedTerms...)
	mined, err := dictionarymining.Select(cmd.Context(), dictionarymining.Deps{
		ReviewDecisions: reviewDecisions, Articles: articles,
	}, targetSession, matcher, cfg.Normalization.Text.Policy(), filter)
	if err != nil {
		return 0, false, err
	}

	cmd.Printf("session %s: %d candidate term(s) mined\n", targetSession, len(mined))
	for _, c := range mined {
		cmd.Printf("  %-30s support=%d guess=%s\n", c.Term, c.SupportCount, c.CategoryGuess)
	}

	if dryRun {
		cmd.Println("dry run: no candidates persisted, dictionary not modified")
		return 0, false, nil
	}

	// On resume, only offer candidates still produced by the current extraction
	// and catalog. Earlier runs may have persisted split name fragments.
	currentTerms := make(map[string]bool, len(mined))
	for _, c := range mined {
		currentTerms[strings.ToLower(c.Term)] = true
		if err := candidatesRepo.Save(cmd.Context(), out.DictionaryCandidate{
			Term: c.Term, SessionID: targetSession, CategoryGuess: c.CategoryGuess,
			SupportCount: c.SupportCount, FirstSeenArticle: c.FirstSeenArticleID, Status: "pending",
		}); err != nil {
			return 0, false, err
		}
	}

	existing, err := candidatesRepo.ListBySession(cmd.Context(), targetSession)
	if err != nil {
		return 0, false, err
	}

	categoryNames := make(map[string]bool, len(dict.Categories))
	for _, cat := range dict.Categories {
		categoryNames[cat.Name] = true
	}

	if scanner == nil {
		scanner = bufio.NewScanner(cmd.InOrStdin())
	}
	for _, c := range existing {
		if c.Status != "pending" || !currentTerms[strings.ToLower(c.Term)] || !filter.Allows(c.Term, c.SupportCount) {
			continue
		}
		cmd.Printf("----------------------------------------------------------------\n")
		cmd.Printf("Term:      %s\n", c.Term)
		cmd.Printf("Support:   %d (first seen: %s)\n", c.SupportCount, c.FirstSeenArticle)
		cmd.Printf("Guess:     %s\n", c.CategoryGuess)
		cmd.Printf("[a]pprove [r]eject [c]ategorize [q]uit-and-save-progress\n> ")

		if !scanner.Scan() {
			paused = true
			break
		}
		action := strings.TrimSpace(scanner.Text())
		switch action {
		case "q":
			cmd.Println("progress saved — resume with `dictionary mine-candidates --session " + targetSession + "`")
			approvedCount, err = writeDictionaryIfApproved(cmd, cfg.Dictionary.Path, candidatesRepo, targetSession, realClock)
			return approvedCount, true, err
		case "a":
			category := c.CategoryGuess
			if category == "" || !categoryNames[category] {
				category, err = promptForCategory(cmd, scanner, categoryNames)
				if err != nil {
					return 0, false, err
				}
			}
			if err := candidatesRepo.UpdateDecision(cmd.Context(), c.Term, "approved", category, realClock.Now()); err != nil {
				return 0, false, err
			}
		case "r":
			if err := candidatesRepo.UpdateDecision(cmd.Context(), c.Term, "rejected", c.CategoryGuess, realClock.Now()); err != nil {
				return 0, false, err
			}
			// Also remember it in the mining policy file, which outlives the
			// database and is shared between machines.
			if added, addErr := dictionaryminingfile.AddIgnoredTerm(cfg.DictionaryMining.Path, c.Term); addErr != nil {
				cmd.Printf("WARNING: rejected term not added to %s: %v\n", cfg.DictionaryMining.Path, addErr)
			} else if added {
				cmd.Printf("  added %q to ignored_terms in %s\n", strings.ToLower(c.Term), cfg.DictionaryMining.Path)
			}
		case "c":
			category, err := promptForCategory(cmd, scanner, categoryNames)
			if err != nil {
				return 0, false, err
			}
			if err := candidatesRepo.UpdateDecision(cmd.Context(), c.Term, "approved", category, realClock.Now()); err != nil {
				return 0, false, err
			}
		default:
			cmd.Printf("unknown action %q — use one of [a r c q]\n", action)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, false, fmt.Errorf("dictionary update: read input: %w", err)
	}

	approvedCount, err = writeDictionaryIfApproved(cmd, cfg.Dictionary.Path, candidatesRepo, targetSession, realClock)
	return approvedCount, paused, err
}

func dictionaryMiningFilter(cfg *config.Config) (dictionarymining.FilterConfig, error) {
	if cfg.DictionaryMining.Path == "" {
		return dictionarymining.FilterConfig{}, fmt.Errorf("dictionary update: dictionary_mining.path is required")
	}
	filter, err := dictionaryminingfile.Load(cfg.DictionaryMining.Path)
	if err != nil {
		return dictionarymining.FilterConfig{}, fmt.Errorf("dictionary update: load mining policy: %w", err)
	}
	return filter, nil
}

// promptForCategory lists every category in stable order and accepts
// either its number or its name, retrying invalid selections.
func promptForCategory(cmd *cobra.Command, scanner *bufio.Scanner, categoryNames map[string]bool) (string, error) {
	categories := make([]string, 0, len(categoryNames))
	for name := range categoryNames {
		categories = append(categories, name)
	}
	sort.Strings(categories)
	cmd.Println("Categories:")
	for i, name := range categories {
		cmd.Printf("  %d. %s\n", i+1, name)
	}
	for {
		cmd.Print("category [number or name]: ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", fmt.Errorf("dictionary update: read category input: %w", err)
			}
			return "", fmt.Errorf("dictionary update: no category provided before input ended")
		}
		category := strings.TrimSpace(scanner.Text())
		if number, err := strconv.Atoi(category); err == nil && number >= 1 && number <= len(categories) {
			return categories[number-1], nil
		}
		if categoryNames[category] {
			return category, nil
		}
		cmd.Printf("unknown category %q — must be one of the dictionary's existing categories\n", category)
	}
}

// entryTypeForCategory guesses an EntryType for a new entry landing in
// categoryName — informational only (EntryType doesn't gate scoring;
// Category.Weight/StrongOverride/RequiresAnyOf do), so an imperfect guess
// here has no effect on classification behavior.
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

// writeDictionaryIfApproved reloads the current on-disk dictionary
// (freshly, so it reflects anything written since RunE started loading
// it), appends every dictionary_candidate_terms row with status
// "approved" for sessionID as a new Entry in its recorded category, bumps
// the patch version, validates the result, and writes it back — a no-op
// if nothing was approved. It returns the number of candidates found with
// status "approved" for sessionID (0 if none), regardless of whether any
// of them were new enough to actually change the on-disk dictionary.
func writeDictionaryIfApproved(
	cmd *cobra.Command,
	path string,
	candidatesRepo *sqliteadapter.DictionaryCandidateRepository,
	sessionID string,
	clk out.Clock,
) (int, error) {
	decided, err := candidatesRepo.ListBySession(cmd.Context(), sessionID)
	if err != nil {
		return 0, err
	}

	var approved []out.DictionaryCandidate
	for _, c := range decided {
		if c.Status == "approved" {
			approved = append(approved, c)
		}
	}
	if len(approved) == 0 {
		cmd.Println("no candidates approved — dictionary not modified")
		return 0, nil
	}

	dict, err := dictionaryfile.Load(path)
	if err != nil {
		return len(approved), err
	}
	originalVersion := dict.Version

	// A term approved in an earlier `dictionary update` invocation against
	// this same session may already have been written to the dictionary
	// file by that invocation's own write-back (each pass through the
	// approval loop can trigger a write, on "q" as well as on completion).
	// candidatesRepo has no "already written" status distinct from
	// "approved" (see out.DictionaryCandidate — only pending/approved/
	// rejected), so re-check against the freshly loaded file itself,
	// using the same matcher-based existence check the mining heuristic
	// already uses to skip terms it already covers, rather than
	// unconditionally re-appending every approved row and risking a
	// duplicate-entry validation failure on resume.
	matcher, err := dictionary.Prepare(dict)
	if err != nil {
		return len(approved), fmt.Errorf("dictionary update: prepare matcher: %w", err)
	}

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
				Source: "dictionary_update", UpdatedAt: clk.Now(), Confidence: 0.5,
			})
			added++
		}
	}
	if added == 0 {
		cmd.Println("no new candidates to write — dictionary not modified")
		return len(approved), nil
	}

	newVersion, err := dictionary.BumpPatchVersion(dict.Version)
	if err != nil {
		return len(approved), fmt.Errorf("dictionary update: %w", err)
	}
	dict.Version = newVersion
	dict.GeneratedAt = clk.Now()
	if dict.SchemaVersion >= 2 {
		dict.Checksum = dictionary.ContentHash(dict)
	}

	if issues := dictionary.Validate(dict); len(issues) > 0 {
		for _, issue := range issues {
			if issue.Severity == dictionary.SeverityError {
				return len(approved), fmt.Errorf("dictionary update: modified dictionary fails validation: %s", issue.String())
			}
		}
	}

	if err := dictionaryfile.Save(path, dict); err != nil {
		return len(approved), err
	}
	cmd.Printf("dictionary updated: %d entr%s added, version %s -> %s\n",
		added, pluralSuffix(added), originalVersion, newVersion)
	return len(approved), nil
}

func pluralSuffix(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
