package entities

import (
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	sqliteclassifierproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteclassifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/classifierdictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/classifierproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
)

func newClassifierDictionaryCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "classifier-dictionary", Short: "Mine, review, and report classifier dictionary proposals"}
	cmd.AddCommand(newClassifierAnalyzeCommand())
	cmd.AddCommand(newClassifierListCommand())
	cmd.AddCommand(newClassifierShowCommand())
	cmd.AddCommand(newClassifierApproveCommand())
	cmd.AddCommand(newClassifierRejectCommand())
	cmd.AddCommand(newClassifierMetricsCommand())
	return cmd
}

func newClassifierAnalyzeCommand() *cobra.Command {
	var since string
	var all bool
	var force bool
	var limit int
	var minOccurrences int
	var analyzerVersion string
	var dictionaryPath string
	var positiveCategory string
	var negativeCategory string

	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Create pending classifier dictionary proposals from stored article evidence",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary analyze: %w", err)
			}
			defer db.Close()

			sinceTime, err := resolveSince(since, all)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary analyze: %w", err)
			}
			if all {
				limit = 10_000_000
			}

			dict, err := dictionaryfile.Load(dictionaryPath)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary analyze: %w", err)
			}
			for _, issue := range dictionary.Validate(dict) {
				if issue.Severity == dictionary.SeverityError {
					return fmt.Errorf("entities classifier-dictionary analyze: %s failed validation: %s", dictionaryPath, issue)
				}
			}

			articlesRepo := sqliteadapter.NewArticleRepository(db)
			proposalsRepo := sqliteclassifierproposal.NewClassifierProposalRepository(db)
			result, err := classifierdictionary.Analyze(cmd.Context(), articlesRepo, proposalsRepo, dict, classifierdictionary.AnalyzeOptions{
				Since: sinceTime, Limit: limit, Force: force, AnalyzerVersion: analyzerVersion,
				MinOccurrences: minOccurrences, PositiveCategory: positiveCategory, NegativeCategory: negativeCategory,
			}, time.Now().UTC())
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary analyze: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "articles considered: %d\n", result.ArticlesConsidered)
			fmt.Fprintf(cmd.OutOrStdout(), "article proposals created/updated: %d\n", result.ArticleProposalsUpserted)
			fmt.Fprintf(cmd.OutOrStdout(), "unknown-entity proposals created/updated: %d\n", result.UnknownProposalsUpserted)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "only consider articles fetched since this duration (24h, 7d, 30d) or RFC3339 date; default: no lower bound")
	cmd.Flags().BoolVar(&all, "all", false, "consider every stored article, with no --limit cap")
	cmd.Flags().BoolVar(&force, "force", false, "reprocess articles already marked processed for this analyzer version")
	cmd.Flags().IntVar(&limit, "limit", 500, "maximum number of articles to consider in this run (ignored when --all is set)")
	cmd.Flags().IntVar(&minOccurrences, "min-occurrences", 2, "minimum repeated evidence count before a proposal is created")
	cmd.Flags().StringVar(&analyzerVersion, "analyzer-version", "classifier-dictionary-v1", "version tag for incremental analysis")
	cmd.Flags().StringVar(&dictionaryPath, "dictionary", shared.DefaultDictionaryPath, "path to the team dictionary YAML file")
	cmd.Flags().StringVar(&positiveCategory, "positive-category", "nfl_people_context", "dictionary category for positive classifier term proposals")
	cmd.Flags().StringVar(&negativeCategory, "negative-category", "negative_other_sports_terms", "dictionary category for negative classifier term proposals")
	return cmd
}

func newClassifierListCommand() *cobra.Command {
	var kindFilter string
	var statusFilter string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending classifier dictionary proposals",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary list: %w", err)
			}
			defer db.Close()
			repo := sqliteclassifierproposal.NewClassifierProposalRepository(db)
			proposals, err := repo.List(cmd.Context(), kindFilter, statusFilter)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary list: %w", err)
			}
			sort.SliceStable(proposals, func(i, j int) bool {
				return classifierproposal.Score(proposals[i]) > classifierproposal.Score(proposals[j])
			})
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tKIND\tCATEGORY\tVALUE\tOCCURRENCES\tCONFIDENCE\tSTATUS")
			for _, p := range proposals {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%.2f\t%s\n", p.ID, p.Kind, p.Category, p.Value, p.Occurrences, p.Confidence, p.Status)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&kindFilter, "kind", "", "filter by proposal kind")
	cmd.Flags().StringVar(&statusFilter, "status", "pending", "filter by status: pending|approved|rejected (pass \"\" for all)")
	return cmd
}

func newClassifierShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one classifier dictionary proposal's detail and evidence",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary show: %w", err)
			}
			defer db.Close()
			repo := sqliteclassifierproposal.NewClassifierProposalRepository(db)
			p, err := repo.Get(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary show: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Value: %s\nKind: %s\nCategory: %s\nOperation: %s\n", p.Value, p.Kind, p.Category, p.Operation)
			if p.TargetEntry != "" {
				fmt.Fprintf(out, "Target entry: %s\n", p.TargetEntry)
			}
			fmt.Fprintf(out, "\nOccurrences: %d\nConfidence: %.2f\nStatus: %s\nFirst seen: %s\nLast seen: %s\n",
				p.Occurrences, p.Confidence, p.Status, p.FirstSeen.Format("2006-01-02"), p.LastSeen.Format("2006-01-02"))
			if len(p.Metadata) > 0 {
				fmt.Fprintln(out, "\nMetadata:")
				keys := make([]string, 0, len(p.Metadata))
				for k := range p.Metadata {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(out, "- %s: %s\n", k, p.Metadata[k])
				}
			}
			fmt.Fprintln(out, "\nEvidence:")
			for _, s := range p.Sources {
				fmt.Fprintf(out, "- %s %s\n", s.ArticleID, s.ArticleTitle)
			}
			return nil
		},
	}
	return cmd
}

func newClassifierApproveCommand() *cobra.Command {
	var dictionaryPath string
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve a pending classifier dictionary proposal and write it to the dictionary",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary approve: %w", err)
			}
			defer db.Close()
			repo := sqliteclassifierproposal.NewClassifierProposalRepository(db)
			p, err := repo.Get(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary approve: %w", err)
			}
			if p.Status != classifierproposal.StatusPending {
				return fmt.Errorf("entities classifier-dictionary approve: proposal %s is already %s", p.ID, p.Status)
			}
			now := time.Now().UTC()
			if err := classifierdictionary.ApplyApproval(p, dictionaryPath, now); err != nil {
				return fmt.Errorf("entities classifier-dictionary approve: %w", err)
			}
			if err := repo.UpdateStatus(cmd.Context(), p.ID, classifierproposal.StatusApproved, now); err != nil {
				return fmt.Errorf("entities classifier-dictionary approve: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "approved %s (%s)\n", p.Value, p.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&dictionaryPath, "dictionary", shared.DefaultDictionaryPath, "path to the team dictionary YAML file")
	return cmd
}

func newClassifierRejectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reject <id>",
		Short: "Reject a pending classifier dictionary proposal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary reject: %w", err)
			}
			defer db.Close()
			repo := sqliteclassifierproposal.NewClassifierProposalRepository(db)
			p, err := repo.Get(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary reject: %w", err)
			}
			if p.Status != classifierproposal.StatusPending {
				return fmt.Errorf("entities classifier-dictionary reject: proposal %s is already %s", p.ID, p.Status)
			}
			if err := repo.UpdateStatus(cmd.Context(), p.ID, classifierproposal.StatusRejected, time.Now().UTC()); err != nil {
				return fmt.Errorf("entities classifier-dictionary reject: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rejected %s (%s)\n", p.Value, p.ID)
			return nil
		},
	}
	return cmd
}

func newClassifierMetricsCommand() *cobra.Command {
	var since string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Report catalog analysis and proposal metrics",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary metrics: %w", err)
			}
			defer db.Close()
			duration, err := time.ParseDuration(since)
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary metrics: invalid --since %q: %w", since, err)
			}
			repo := sqliteclassifierproposal.NewClassifierProposalRepository(db)
			report, err := repo.Metrics(cmd.Context(), time.Now().UTC().Add(-duration))
			if err != nil {
				return fmt.Errorf("entities classifier-dictionary metrics: %w", err)
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "catalog_analysis_runs_total=%d\n", report.AnalysisRunsTotal)
			fmt.Fprintf(cmd.OutOrStdout(), "catalog_proposals_total=%d\n", report.ProposalsTotal)
			fmt.Fprintf(cmd.OutOrStdout(), "catalog_proposals_approved_total=%d\n", report.ProposalsApprovedTotal)
			fmt.Fprintf(cmd.OutOrStdout(), "catalog_proposals_rejected_total=%d\n", report.ProposalsRejectedTotal)
			fmt.Fprintf(cmd.OutOrStdout(), "entity_extraction_total=%d\n", report.EntityExtractionTotal)
			fmt.Fprintf(cmd.OutOrStdout(), "entity_extraction_unknown_total=%d\n", report.EntityExtractionUnknownTotal)
			fmt.Fprintf(cmd.OutOrStdout(), "unknown_entity_count=%d\n", report.UnknownEntityCount)
			return nil
		},
	}
	cmd.Flags().StringVar(&since, "since", "168h", "how far back to aggregate, as a Go duration")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON instead of text")
	return cmd
}
