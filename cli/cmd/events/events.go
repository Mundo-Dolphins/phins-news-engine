// cli/cmd/events/events.go

// Package events provides the `events` CLI command group for event rule
// discovery: the standalone review of the `editorial cycle` step and the
// read-only clustering gate and impact reports and the new-type brief.
package events

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/eventrules"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

// NewCommand returns the `events` command group.
func NewCommand() *cobra.Command {
	eventsCmd := &cobra.Command{Use: "events", Short: "Event rule discovery: review proposed trigger phrases and check the clustering gate"}
	eventsCmd.AddCommand(newReviewCommand())
	eventsCmd.AddCommand(newGateCommand())
	eventsCmd.AddCommand(newBriefCommand())
	eventsCmd.AddCommand(newImpactCommand())
	eventsCmd.AddCommand(newTypeCommand())
	return eventsCmd
}

// openDB opens and migrates the operational database of cfg.
func openDB(cmd *cobra.Command, cfg *config.Config) (*sql.DB, error) {
	db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
	if err != nil {
		return nil, err
	}
	if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// newTypeCommand groups the decisions on new event type proposals, which
// the cycle lists but never applies: a type needs a code change.
func newTypeCommand() *cobra.Command {
	typeCmd := &cobra.Command{Use: "type", Short: "List, approve or reject new event type proposals (applying one is a code change, see `events brief`)"}
	typeCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the pending and approved event type proposals with their ids",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			db, err := openDB(cmd, cfg)
			if err != nil {
				return err
			}
			defer db.Close()
			ps, err := eventrules.ListTypes(cmd.Context(), db)
			if err != nil {
				return fmt.Errorf("events type list: %w", err)
			}
			w := cmd.OutOrStdout()
			for _, p := range ps {
				fmt.Fprintf(w, "%s [%s] %s: %s (evidence %s)\n", p.ID, p.Status, p.TypeName, p.Description, strings.Join(p.EvidenceIDs, ", "))
			}
			if len(ps) == 0 {
				fmt.Fprintln(w, "No pending or approved event type proposals.")
			}
			return nil
		},
	})
	decide := func(verb, past string, approve bool) *cobra.Command {
		return &cobra.Command{
			Use:   verb + " <proposal-id>",
			Short: "Mark an event type proposal as " + past + " (nothing is applied; an approved type is implemented through `events brief`)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load(shared.ConfigPath)
				if err != nil {
					return err
				}
				db, err := openDB(cmd, cfg)
				if err != nil {
					return err
				}
				defer db.Close()
				decided, err := eventrules.DecideType(cmd.Context(), db, args[0], approve, time.Now().UTC())
				if err != nil {
					return fmt.Errorf("events type %s: %w", verb, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Event type proposal %s (%s) %s.\n", decided.ID, decided.TypeName, past)
				return nil
			},
		}
	}
	typeCmd.AddCommand(decide("approve", "approved", true))
	typeCmd.AddCommand(decide("reject", "rejected", false))
	return typeCmd
}

func newReviewCommand() *cobra.Command {
	var opts CycleOptions
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run the event rules step of `editorial cycle` on its own: discover, review and apply (gated) trigger phrases",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if reason := CycleStepDisabledReason(cfg); reason != "" {
				return fmt.Errorf("events review: step disabled (%s)", reason)
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			res, err := RunCycleStep(cmd, cfg, db, bufio.NewScanner(cmd.InOrStdin()), opts)
			fmt.Fprintf(cmd.OutOrStdout(), "event rules: sampled=%d proposed=%d approved=%d rejected=%d skipped=%d applied=%d\n",
				res.Sampled, res.Proposed, res.Approved, res.Rejected, res.Skipped, res.Applied)
			if err != nil {
				return fmt.Errorf("events review: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.DatasetPath, "dataset", eventrules.DefaultDatasetPath, "gold clustering dataset scored by the apply gate")
	cmd.Flags().IntVar(&opts.SampleSize, "sample-size", eventrules.DefaultSampleSize, "generic articles sampled for discovery")
	cmd.Flags().IntVar(&opts.SinceDays, "since-days", eventrules.DefaultSinceDays, "sample window in days")
	return cmd
}

func newGateCommand() *cobra.Command {
	var datasetPath string
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Read-only: score the clustering dataset without and with the generated rules file and print the gate verdict",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if cfg.Clustering.RulesPath == "" {
				return fmt.Errorf("events gate: clustering.rules_path is empty")
			}
			path := eventrules.GeneratedPath(cfg)
			rules, err := eventrules.ReadApplied(path)
			if err != nil {
				return fmt.Errorf("events gate: %w", err)
			}
			res, err := eventrules.RunGate(cmd.Context(), cfg, datasetPath, rules)
			if err != nil {
				return fmt.Errorf("events gate: %w", err)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "generated rules: %s (%d phrase(s))\n", path, len(rules))
			printMetrics := func(label string, r clustereval.Report) {
				fmt.Fprintf(w, "%s: precision=%.4f recall=%.4f purity=%.4f over_grouped=%d fragmented=%d\n",
					label, r.Pairwise.Precision(), r.Pairwise.Recall(), r.Purity, r.OverGroupedClusters, r.FragmentedClusters)
			}
			printMetrics("before (hand-written)", res.Before)
			printMetrics("after (with generated)", res.After)
			fmt.Fprintf(w, "dataset: %s\n", eventrulecycle.GateExercise(res.Affected))
			if !res.OK {
				fmt.Fprintf(w, "gate: BLOCKED (%s)\n", res.Reason)
				return fmt.Errorf("events gate: blocked: %s", res.Reason)
			}
			fmt.Fprintln(w, "gate: PASS")
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", eventrules.DefaultDatasetPath, "gold clustering dataset")
	return cmd
}

// maxImpactExamples bounds the titles listed per type in the impact report.
const maxImpactExamples = 5

func newBriefCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "brief <proposal-id>",
		Short: "Print the implementation brief of a new event type proposal (pending or approved)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			brief, err := eventrules.Brief(cmd.Context(), db, args[0])
			if err != nil {
				return fmt.Errorf("events brief: %w", err)
			}
			fmt.Fprint(cmd.OutOrStdout(), brief)
			return nil
		},
	}
}

func newImpactCommand() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "impact",
		Short: "Read-only shadow report: which generic articles the generated rules file would type, and which typed ones it would change",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			rep, err := eventrules.Impact(cmd.Context(), db, cfg, days, time.Now())
			if err != nil {
				return fmt.Errorf("events impact: %w", err)
			}
			for _, w := range rep.Warnings {
				cmd.PrintErrln("warning: " + w)
			}
			printImpact(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 14, "look-back window in days")
	return cmd
}

// printImpact renders rep as the `events impact` report.
func printImpact(w io.Writer, rep eventrules.ImpactReport) {
	fmt.Fprintf(w, "generated rules: %s\n", rep.GeneratedPath)
	if rep.GeneratedMissing {
		fmt.Fprintf(w, "generated rules file not found: %s (nothing to compare)\n", rep.GeneratedPath)
	}
	if rep.GenericTruncated {
		fmt.Fprintf(w, "WARNING: sample truncated at %d generic articles; use a smaller --days for a complete report\n", rep.Generic)
	}
	fmt.Fprintf(w, "last %d day(s): %d generic article(s), %d would change type\n", rep.Days, rep.Generic, rep.Changed)
	printGroups(w, rep.Moved)
	if rep.TypedTruncated {
		fmt.Fprintf(w, "\nWARNING: typed sample truncated at %d articles; use a smaller --days for a complete report\n", rep.Typed)
	}
	fmt.Fprintf(w, "\nlast %d day(s): %d typed article(s), %d would change type\n", rep.Days, rep.Typed, rep.Retyped)
	printGroups(w, rep.RetypedBy)
}

// printGroups lists each key with its count and the first titles, keys
// sorted.
func printGroups(w io.Writer, groups map[string][]string) {
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		titles := groups[k]
		fmt.Fprintf(w, "\n%s: %d\n", k, len(titles))
		for _, title := range titles[:min(len(titles), maxImpactExamples)] {
			fmt.Fprintf(w, "  - %s\n", title)
		}
	}
}
