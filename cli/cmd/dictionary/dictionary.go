package dictionary

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionarymining"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarywiring"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
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
			_, issues, err := dictionarywiring.Validate(args[0])
			if err != nil {
				return err
			}
			for _, issue := range issues {
				cmd.Println(issue.String())
			}
			if dictionarywiring.HasErrors(issues) {
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
			r, err := dictionarywiring.Inspect(args[0])
			if err != nil {
				return err
			}
			cmd.Printf("version: %s\nname: %s\nlanguage: %s\n", r.Version, r.Name, r.Language)
			cmd.Printf("categories: %d\n", len(r.Categories))
			for _, cat := range r.Categories {
				cmd.Printf("  %s: weight=%.1f strong_override=%v entries=%d aliases=%d\n",
					cat.Name, cat.Weight, cat.StrongOverride, cat.Entries, cat.Aliases)
			}
			cmd.Printf("total entries: %d\ntotal aliases: %d\n", r.TotalEntries, r.TotalAliases)
			cmd.Printf("thresholds: positive_high=%.1f positive_medium=%.1f negative_high=%.1f title_multiplier=%.1f max_count_per_term=%d\n",
				r.Decision.PositiveThresholdHigh, r.Decision.PositiveThresholdMedium, r.Decision.NegativeThresholdHigh,
				r.Decision.TitleWeightMultiplier, r.Decision.MaxCountPerTerm)
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
	deps := dictionarywiring.MinedTermReview(db, cfg, cmd.OutOrStderr())

	sel, err := deps.Mine(cmd.Context(), sessionID)
	for _, w := range sel.Warnings {
		cmd.Printf("%s\n", w)
	}
	if err != nil {
		var setupErr *dictionarywiring.MineSetupError
		if errors.As(err, &setupErr) {
			return 0, false, fmt.Errorf("dictionary update: %w", err)
		}
		return 0, false, err
	}
	targetSession, mined := sel.Session, sel.Candidates

	cmd.Printf("session %s: %d candidate term(s) mined\n", targetSession, len(mined))
	for _, c := range mined {
		cmd.Printf("  %-30s support=%d guess=%s\n", c.Term, c.SupportCount, c.CategoryGuess)
	}

	if dryRun {
		cmd.Println("dry run: no candidates persisted, dictionary not modified")
		return 0, false, nil
	}

	prepared, err := dictionarymining.Offer(cmd.Context(), deps, sel)
	if err != nil {
		return 0, false, err
	}
	categoryNames := make(map[string]bool, len(prepared.Categories))
	for _, name := range prepared.Categories {
		categoryNames[name] = true
	}

	if scanner == nil {
		scanner = bufio.NewScanner(cmd.InOrStdin())
	}
	for _, c := range prepared.Offered {
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
			approvedCount, err = writeDictionaryIfApproved(cmd, deps, targetSession)
			return approvedCount, true, err
		case "a":
			category := c.CategoryGuess
			if category == "" || !categoryNames[category] {
				category, err = promptForCategory(cmd, scanner, prepared.Categories)
				if err != nil {
					return 0, false, err
				}
			}
			if err := dictionarymining.Decide(cmd.Context(), deps, targetSession, c.Term, dictionarymining.ActionApprove, category); err != nil {
				return 0, false, err
			}
		case "r":
			if err := dictionarymining.Decide(cmd.Context(), deps, targetSession, c.Term, dictionarymining.ActionReject, ""); err != nil {
				return 0, false, err
			}
		case "c":
			category, err := promptForCategory(cmd, scanner, prepared.Categories)
			if err != nil {
				return 0, false, err
			}
			if err := dictionarymining.Decide(cmd.Context(), deps, targetSession, c.Term, dictionarymining.ActionCategorize, category); err != nil {
				return 0, false, err
			}
		default:
			cmd.Printf("unknown action %q — use one of [a r c q]\n", action)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, false, fmt.Errorf("dictionary update: read input: %w", err)
	}

	approvedCount, err = writeDictionaryIfApproved(cmd, deps, targetSession)
	return approvedCount, paused, err
}

// promptForCategory lists every category (sorted) and accepts either its
// number or its name, retrying invalid selections.
func promptForCategory(cmd *cobra.Command, scanner *bufio.Scanner, categories []string) (string, error) {
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
		if slices.Contains(categories, category) {
			return category, nil
		}
		cmd.Printf("unknown category %q — must be one of the dictionary's existing categories\n", category)
	}
}

// writeDictionaryIfApproved writes the session's approved terms to the
// dictionary (dictionarymining.Write) and prints the outcome. It returns the
// number of approved terms whether or not any was new to the dictionary.
func writeDictionaryIfApproved(cmd *cobra.Command, deps dictionarymining.ReviewDeps, sessionID string) (int, error) {
	res, err := dictionarymining.Write(cmd.Context(), deps, sessionID)
	if err != nil {
		return res.Approved, err
	}
	switch {
	case res.Approved == 0:
		cmd.Println("no candidates approved — dictionary not modified")
	case res.Added == 0:
		cmd.Println("no new candidates to write — dictionary not modified")
	default:
		cmd.Printf("dictionary updated: %d entr%s added, version %s -> %s\n",
			res.Added, pluralSuffix(res.Added), res.FromVersion, res.ToVersion)
	}
	return res.Approved, nil
}

func pluralSuffix(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
