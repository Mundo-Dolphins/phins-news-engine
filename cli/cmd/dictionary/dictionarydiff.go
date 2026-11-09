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
				return dictionarywiring.RenderDiffText(cmd.OutOrStdout(), proposals)
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

func renderDiffJSON(w io.Writer, proposals []dictionaryupdate.Proposal) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(proposals)
}

func renderDiffMarkdown(w io.Writer, proposals []dictionaryupdate.Proposal) error {
	sections, unchanged := dictionarywiring.DiffSections(proposals)
	for _, section := range sections {
		fmt.Fprintf(w, "## %s (%d)\n\n", section.Title, len(section.Proposals))
		for _, p := range section.Proposals {
			fmt.Fprintf(w, "- **%s**: %s\n", p.CanonicalKey, p.Detail)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "_Unchanged: %d_\n", unchanged)
	return nil
}
