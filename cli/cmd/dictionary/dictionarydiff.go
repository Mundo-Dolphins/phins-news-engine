package dictionary

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/app/regressioncheck"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarywiring"
)

func newDiffCommand() *cobra.Command {
	var snapshotPath, snapshotDir, dictPathFlag, fromFile, toFile, from, to, format string

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show proposed changes between the current dictionary and a snapshot or file",
		RunE: func(cmd *cobra.Command, args []string) error {
			defer func() {
				snapshotPath = ""
				snapshotDir = ""
				dictPathFlag = ""
				fromFile = ""
				toFile = ""
				from = ""
				to = ""
				format = "text"
			}()

			dictPath := dictPathFlag
			// The configured dictionary path is only needed (and the config
			// only loaded) in the modes that read the dictionary on disk.
			needsDict := (from != "" && to == "") || (from == "" && to == "" && fromFile == "" && toFile == "")
			if needsDict && dictPath == "" {
				cfg, err := config.Load(shared.ConfigPath)
				if err != nil {
					return fmt.Errorf("dictionary diff: %w", err)
				}
				dictPath = cfg.Dictionary.Path
			}
			proposals, err := dictionarywiring.Diff(dictionarywiring.DiffOptions{
				DictionaryPath: dictPath, SnapshotPath: snapshotPath, SnapshotDir: snapshotDir,
				FromFile: fromFile, ToFile: toFile, From: from, To: to,
			}, time.Now())
			if err != nil {
				return fmt.Errorf("dictionary diff: %w", err)
			}

			switch format {
			case "json":
				return renderDiffJSON(cmd.OutOrStdout(), proposals)
			case "markdown":
				return renderDiffMarkdown(cmd.OutOrStdout(), proposals)
			case "text", "":
				return RenderDiffText(cmd.OutOrStdout(), proposals)
			default:
				return fmt.Errorf("dictionary diff: unknown --format %q (want text, json, or markdown)", format)
			}
		},
	}

	cmd.Flags().StringVar(&snapshotPath, "snapshot", "", "specific snapshot file to diff against (default: latest snapshot per source in --snapshot-dir)")
	cmd.Flags().StringVar(&snapshotDir, "snapshot-dir", "", "directory to search for the latest snapshot per source when --snapshot is not given (default data/dictionary/snapshots)")
	cmd.Flags().StringVar(&dictPathFlag, "dictionary", "", "current dictionary file to diff from (default: dictionary.path from config)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "explicit dictionary file to diff from")
	cmd.Flags().StringVar(&toFile, "to-file", "", "explicit dictionary file to diff to")
	cmd.Flags().StringVar(&from, "from", "", "historical version to diff from (from data/dictionary/history)")
	cmd.Flags().StringVar(&to, "to", "", "historical version to diff to (requires --from; default: current dictionary on disk)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, or markdown")
	return cmd
}

// SourceEntitiesFromDictionary delegates to dictionarywiring; it stays here
// until rollback moves out of the CLI package.
func SourceEntitiesFromDictionary(d dictionary.Dictionary) []dictionaryupdate.SourceEntity {
	return dictionarywiring.SourceEntitiesFromDictionary(d)
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

func groupProposals(proposals []dictionaryupdate.Proposal) (map[dictionaryupdate.ProposalKind][]dictionaryupdate.Proposal, int) {
	grouped := make(map[dictionaryupdate.ProposalKind][]dictionaryupdate.Proposal)
	unchanged := 0
	for _, p := range proposals {
		if p.Kind == dictionaryupdate.ProposalNoChange {
			unchanged++
			continue
		}
		grouped[p.Kind] = append(grouped[p.Kind], p)
	}
	return grouped, unchanged
}

// PrintRegressionReport prints the regression-check flip summary shared by
// `dictionary update` and `dictionary rollback`: report-only, printed after a
// successful write, never blocking. Both commands print nothing when
// report.FlipCount is 0.
func PrintRegressionReport(w io.Writer, report regressioncheck.Report) {
	if report.FlipCount == 0 {
		return
	}
	fmt.Fprintf(w, "regression-check: %d/%d cases flipped — review recommended\n", report.FlipCount, report.TotalCases)
	for _, f := range report.Flips {
		fmt.Fprintf(w, "  %s: %s\n", f.CaseName, f.Direction)
	}
}

func RenderDiffText(w io.Writer, proposals []dictionaryupdate.Proposal) error {
	grouped, unchanged := groupProposals(proposals)
	for _, section := range diffSectionOrder {
		var items []dictionaryupdate.Proposal
		for _, k := range section.kinds {
			items = append(items, grouped[k]...)
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s (%d):\n", section.title, len(items))
		for _, p := range items {
			fmt.Fprintf(w, "  - %s: %s\n", p.CanonicalKey, p.Detail)
		}
	}
	fmt.Fprintf(w, "Unchanged: %d\n", unchanged)
	return nil
}

func renderDiffJSON(w io.Writer, proposals []dictionaryupdate.Proposal) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(proposals)
}

func renderDiffMarkdown(w io.Writer, proposals []dictionaryupdate.Proposal) error {
	grouped, unchanged := groupProposals(proposals)
	for _, section := range diffSectionOrder {
		var items []dictionaryupdate.Proposal
		for _, k := range section.kinds {
			items = append(items, grouped[k]...)
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(w, "## %s (%d)\n\n", section.title, len(items))
		for _, p := range items {
			fmt.Fprintf(w, "- **%s**: %s\n", p.CanonicalKey, p.Detail)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "_Unchanged: %d_\n", unchanged)
	return nil
}
