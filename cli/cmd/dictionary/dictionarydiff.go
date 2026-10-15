package dictionary

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/app/regressioncheck"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysnapshot"
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

			var current dictionary.Dictionary
			var observed []dictionaryupdate.SourceEntity

			switch {
			case from != "":
				// Historical mode: diff a recorded version's on-disk artifacts
				// against either another recorded version (--to) or the current
				// dictionary on disk (default). Reads only file artifacts, so no
				// database access is needed here.
				fromArtifacts, err := dictionaryhistory.ReadVersion(dictionaryHistoryDir, from)
				if err != nil {
					return fmt.Errorf("dictionary diff: %w", err)
				}
				current = fromArtifacts.Dictionary

				if to != "" {
					toArtifacts, err := dictionaryhistory.ReadVersion(dictionaryHistoryDir, to)
					if err != nil {
						return fmt.Errorf("dictionary diff: %w", err)
					}
					observed = SourceEntitiesFromDictionary(toArtifacts.Dictionary)
				} else {
					dictPath := dictPathFlag
					if dictPath == "" {
						cfg, err := config.Load(shared.ConfigPath)
						if err != nil {
							return fmt.Errorf("dictionary diff: %w", err)
						}
						dictPath = cfg.Dictionary.Path
					}
					currentOnDisk, err := dictionaryfile.Load(dictPath)
					if err != nil {
						return fmt.Errorf("dictionary diff: %w", err)
					}
					observed = SourceEntitiesFromDictionary(currentOnDisk)
				}

			case to != "":
				// --to only makes sense alongside --from (historical mode);
				// on its own it would be silently ignored, so reject it.
				return fmt.Errorf("dictionary diff: --to requires --from")

			case fromFile != "" || toFile != "":
				if fromFile == "" || toFile == "" {
					return fmt.Errorf("dictionary diff: --from-file and --to-file must be given together")
				}
				fromDict, err := dictionaryfile.Load(fromFile)
				if err != nil {
					return fmt.Errorf("dictionary diff: %w", err)
				}
				current = fromDict
				toDict, err := dictionaryfile.Load(toFile)
				if err != nil {
					return fmt.Errorf("dictionary diff: %w", err)
				}
				observed = SourceEntitiesFromDictionary(toDict)

			default:
				dictPath := dictPathFlag
				if dictPath == "" {
					cfg, err := config.Load(shared.ConfigPath)
					if err != nil {
						return fmt.Errorf("dictionary diff: %w", err)
					}
					dictPath = cfg.Dictionary.Path
				}
				loaded, err := dictionaryfile.Load(dictPath)
				if err != nil {
					return fmt.Errorf("dictionary diff: %w", err)
				}
				current = loaded

				if snapshotPath != "" {
					snap, err := dictionarysnapshot.Read(snapshotPath)
					if err != nil {
						return fmt.Errorf("dictionary diff: %w", err)
					}
					observed = snap.Entities
				} else {
					dir := snapshotDir
					if dir == "" {
						dir = "data/dictionary/snapshots"
					}
					paths, err := latestSnapshotPathsBySource(dir)
					if err != nil {
						return fmt.Errorf("dictionary diff: %w", err)
					}
					// Iterate sources in a fixed order so the observed
					// slice (and therefore Compare's output for
					// same-priority ties) is deterministic across runs.
					sources := make([]dictionaryupdate.SourceKind, 0, len(paths))
					for source := range paths {
						sources = append(sources, source)
					}
					sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
					for _, source := range sources {
						snap, err := dictionarysnapshot.Read(paths[source])
						if err != nil {
							return fmt.Errorf("dictionary diff: %w", err)
						}
						observed = append(observed, snap.Entities...)
					}
				}
			}

			policy := dictionaryupdate.DeactivationPolicy{MinimumSuccessfulAbsences: 2, MinimumElapsedDays: 3}
			proposals := dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, policy, time.Now())

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

// latestSnapshotPathsBySource returns the most recent snapshot file per
// source kind found in dir. Snapshot filenames are fetched-<source>-
// <YYYYMMDD-HHMMSS>.json; since none of the three SourceKind values
// contain a hyphen, splitting on the first hyphen after stripping the
// fetched-/.json wrapper reliably separates source from timestamp, and
// lexicographic sort within a single source's filename group still
// equals chronological order (only the timestamp suffix varies).
//
// The host's dictionary fetcher writes one file per source per run, so a snapshot
// directory normally holds several current files at once (one per
// source) rather than a single "latest" file across all sources — the
// diff's default mode must merge each source's own latest snapshot,
// not pick whichever single file happens to sort last overall.
func latestSnapshotPathsBySource(dir string) (map[dictionaryupdate.SourceKind]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read snapshot dir %s: %w", dir, err)
	}
	bySource := make(map[dictionaryupdate.SourceKind][]string)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "fetched-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		trimmed := strings.TrimSuffix(strings.TrimPrefix(name, "fetched-"), ".json")
		parts := strings.SplitN(trimmed, "-", 2)
		if len(parts) != 2 {
			continue
		}
		source := dictionaryupdate.SourceKind(parts[0])
		bySource[source] = append(bySource[source], name)
	}
	if len(bySource) == 0 {
		return nil, fmt.Errorf("no snapshot files found in %s", dir)
	}
	latest := make(map[dictionaryupdate.SourceKind]string, len(bySource))
	for source, names := range bySource {
		sort.Strings(names)
		latest[source] = filepath.Join(dir, names[len(names)-1])
	}
	return latest, nil
}

func SourceEntitiesFromDictionary(d dictionary.Dictionary) []dictionaryupdate.SourceEntity {
	kindForCategory := map[string]dictionaryupdate.SourceKind{
		"active_roster_core": dictionaryupdate.SourceKindRoster,
		"coaching_staff":     dictionaryupdate.SourceKindCoaches,
		"front_office":       dictionaryupdate.SourceKindFrontOffice,
	}
	var out []dictionaryupdate.SourceEntity
	for _, cat := range d.Categories {
		kind, ok := kindForCategory[cat.Name]
		if !ok {
			continue
		}
		for _, e := range cat.Entries {
			out = append(out, dictionaryupdate.SourceEntity{
				DisplayName:  e.CanonicalName,
				CanonicalKey: dictionaryupdate.CanonicalKey(e.CanonicalName),
				Kind:         kind,
				Role:         e.Role,
				Position:     e.Position,
				Number:       e.Number,
				RosterStatus: e.RosterStatus,
			})
		}
	}
	return out
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
