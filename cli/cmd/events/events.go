// cli/cmd/events/events.go

// Package events provides the `events` CLI command group for event rule
// discovery: the standalone review of the `editorial cycle` step and the
// read-only clustering gate and impact reports and the new-type brief.
package events

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteeventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
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
			repo := sqliteeventruleproposal.NewRepository(db)
			w := cmd.OutOrStdout()
			n := 0
			for _, st := range []eventruleproposal.Status{eventruleproposal.StatusPending, eventruleproposal.StatusApproved} {
				ps, err := repo.ListByStatus(cmd.Context(), st)
				if err != nil {
					return fmt.Errorf("events type list: %w", err)
				}
				for _, p := range ps {
					if p.Kind != eventruleproposal.KindType {
						continue
					}
					n++
					fmt.Fprintf(w, "%s [%s] %s: %s (evidence %s)\n", p.ID, p.Status, p.TypeName, p.Description, strings.Join(p.EvidenceIDs, ", "))
				}
			}
			if n == 0 {
				fmt.Fprintln(w, "No pending or approved event type proposals.")
			}
			return nil
		},
	})
	decide := func(verb, past string, apply func(eventruleproposal.Proposal, time.Time) (eventruleproposal.Proposal, error)) *cobra.Command {
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
				repo := sqliteeventruleproposal.NewRepository(db)
				p, err := repo.Get(cmd.Context(), args[0])
				if err != nil {
					return fmt.Errorf("events type %s: %w", verb, err)
				}
				if p.Kind != eventruleproposal.KindType {
					return fmt.Errorf("events type %s: proposal %s is a %s proposal, not a type proposal; phrases are decided in `events review`", verb, p.ID, p.Kind)
				}
				decided, err := apply(p, time.Now().UTC())
				if err != nil {
					return fmt.Errorf("events type %s: %w", verb, err)
				}
				if err := repo.Save(cmd.Context(), decided); err != nil {
					return fmt.Errorf("events type %s: %w", verb, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Event type proposal %s (%s) %s.\n", decided.ID, decided.TypeName, past)
				return nil
			},
		}
	}
	typeCmd.AddCommand(decide("approve", "approved", eventruleproposal.Proposal.Approve))
	typeCmd.AddCommand(decide("reject", "rejected", eventruleproposal.Proposal.Reject))
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
	cmd.Flags().StringVar(&opts.DatasetPath, "dataset", DefaultDatasetPath, "gold clustering dataset scored by the apply gate")
	cmd.Flags().IntVar(&opts.SampleSize, "sample-size", defaultSampleSize, "generic articles sampled for discovery")
	cmd.Flags().IntVar(&opts.SinceDays, "since-days", defaultSinceDays, "sample window in days")
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
			path := GeneratedPath(cfg)
			rules, err := ReadApplied(path)
			if err != nil {
				return fmt.Errorf("events gate: %w", err)
			}
			res, err := RunGate(cmd.Context(), cfg, datasetPath, rules)
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
	cmd.Flags().StringVar(&datasetPath, "dataset", DefaultDatasetPath, "gold clustering dataset")
	return cmd
}

const (
	maxBriefExamples  = 5
	maxImpactExamples = 5
	// impactListLimit bounds the generic articles re-extracted by `impact`.
	impactListLimit = 20000
)

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
			p, err := sqliteeventruleproposal.NewRepository(db).Get(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("events brief: %w", err)
			}
			if p.Kind != eventruleproposal.KindType {
				return fmt.Errorf("events brief: proposal %s is a %s proposal, not a type proposal", p.ID, p.Kind)
			}
			if p.Status != eventruleproposal.StatusPending && p.Status != eventruleproposal.StatusApproved {
				return fmt.Errorf("events brief: proposal %s is %s; only pending or approved type proposals have a brief", p.ID, p.Status)
			}
			articles := sqliteadapter.NewArticleRepository(db)
			var examples []string
			for _, id := range p.EvidenceIDs {
				if len(examples) >= maxBriefExamples {
					break
				}
				a, err := articles.FindByID(cmd.Context(), id)
				if err != nil || a.Title == "" {
					continue // evidence may have been pruned
				}
				examples = append(examples, a.Title)
			}
			fmt.Fprint(cmd.OutOrStdout(), eventrulecycle.Brief(p, examples))
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
			if cfg.Clustering.RulesPath == "" {
				return fmt.Errorf("events impact: clustering.rules_path is empty")
			}
			if days <= 0 {
				return fmt.Errorf("events impact: --days must be positive")
			}
			base, err := handWrittenRules(cfg)
			if err != nil {
				return fmt.Errorf("events impact: %w", err)
			}
			path := GeneratedPath(cfg)
			merged, warnings, err := clusteringrules.LoadWithGenerated(base, path)
			if err != nil {
				return fmt.Errorf("events impact: %w", err)
			}
			for _, w := range warnings {
				cmd.PrintErrln("warning: " + w)
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			now := time.Now().UTC()
			samples, err := sqliteadapter.NewGenericArticleLister(db).ListGeneric(cmd.Context(), now.AddDate(0, 0, -days), impactListLimit)
			if err != nil {
				return fmt.Errorf("events impact: %w", err)
			}
			typed, err := sqliteadapter.NewGenericArticleLister(db).ListTyped(cmd.Context(), now.AddDate(0, 0, -days), impactListLimit)
			if err != nil {
				return fmt.Errorf("events impact: %w", err)
			}
			beforeRules, afterRules := base.ToEventSignalsRules(), merged.ToEventSignalsRules()
			moved := map[eventsignals.EventType][]string{}
			changed := 0
			for _, s := range samples {
				was := eventsignals.Extract(s.Article, beforeRules, now).EventType
				is := eventsignals.Extract(s.Article, afterRules, now).EventType
				if was == is {
					continue
				}
				changed++
				moved[is] = append(moved[is], s.Article.Title)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "generated rules: %s\n", path)
			if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
				fmt.Fprintf(w, "generated rules file not found: %s (nothing to compare)\n", path)
			}
			if len(samples) >= impactListLimit {
				fmt.Fprintf(w, "WARNING: sample truncated at %d generic articles; use a smaller --days for a complete report\n", impactListLimit)
			}
			fmt.Fprintf(w, "last %d day(s): %d generic article(s), %d would change type\n", days, len(samples), changed)
			types := make([]string, 0, len(moved))
			for t := range moved {
				types = append(types, string(t))
			}
			sort.Strings(types)
			for _, t := range types {
				titles := moved[eventsignals.EventType(t)]
				fmt.Fprintf(w, "\n%s: %d\n", t, len(titles))
				for _, title := range titles[:min(len(titles), maxImpactExamples)] {
					fmt.Fprintf(w, "  - %s\n", title)
				}
			}
			// Typed control: an already-typed article whose type changes is a
			// regression the checker should have caught.
			retyped := map[string][]string{}
			retypedN := 0
			for _, s := range typed {
				was := eventsignals.Extract(s.Article, beforeRules, now).EventType
				is := eventsignals.Extract(s.Article, afterRules, now).EventType
				if was == is {
					continue
				}
				retypedN++
				key := string(was) + " -> " + string(is)
				retyped[key] = append(retyped[key], s.Article.Title)
			}
			if len(typed) >= impactListLimit {
				fmt.Fprintf(w, "\nWARNING: typed sample truncated at %d articles; use a smaller --days for a complete report\n", impactListLimit)
			}
			fmt.Fprintf(w, "\nlast %d day(s): %d typed article(s), %d would change type\n", days, len(typed), retypedN)
			moves := make([]string, 0, len(retyped))
			for k := range retyped {
				moves = append(moves, k)
			}
			sort.Strings(moves)
			for _, k := range moves {
				titles := retyped[k]
				fmt.Fprintf(w, "\n%s: %d\n", k, len(titles))
				for _, title := range titles[:min(len(titles), maxImpactExamples)] {
					fmt.Fprintf(w, "  - %s\n", title)
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 14, "look-back window in days")
	return cmd
}
