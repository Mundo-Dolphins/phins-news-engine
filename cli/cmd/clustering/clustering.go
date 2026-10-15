package clustering

import (
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
)

func NewCommand() *cobra.Command {
	clusteringCmd := &cobra.Command{Use: "clustering", Short: "Event clustering tooling"}
	clusteringCmd.AddCommand(newEvaluateCommand())
	clusteringCmd.AddCommand(newRulesCommand())
	return clusteringCmd
}

func newEvaluateCommand() *cobra.Command {
	var datasetPath string
	var generatedRulesPath string
	evaluateCmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Score a labeled dataset of event groups against the configured clustering rules",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}

			rulesFile, err := clusteringrules.LoadWithEntityCatalog(
				cfg.Clustering.RulesPath, cfg.Clustering.EntityCatalog.ManualPath, cfg.Clustering.EntityCatalog.GeneratedPath,
			)
			if err != nil {
				return err
			}

			generatedPath := cfg.Clustering.GeneratedRulesPath
			if generatedRulesPath != "" {
				generatedPath = generatedRulesPath
			}
			rulesFile, genWarnings, err := clusteringrules.LoadWithGenerated(rulesFile, generatedPath)
			if err != nil {
				return err
			}
			for _, w := range genWarnings {
				cmd.PrintErrln("warning: " + w)
			}

			dataset, err := clustereval.LoadDataset(datasetPath)
			if err != nil {
				return err
			}

			// now is derived from the dataset itself (its latest
			// published_at) rather than the wall clock, so a given dataset
			// always scores identically regardless of when this command
			// runs — the "freshness"/"original content" representative-
			// ranking signals (clustering.RepresentativeConfig) both read
			// now, and this evaluator's whole point is reproducible,
			// deterministic offline scoring (see clustereval.Evaluate's doc
			// comment on the pure-function/no-wall-clock design it mirrors
			// from clustering.DecideJoin/RankRepresentative themselves).
			now := clustereval.LatestPublishedAt(dataset)

			report, err := clustereval.Evaluate(
				dataset,
				rulesFile.ToEventSignalsRules(),
				rulesFile.ToClusteringConfig(),
				rulesFile.ToRepresentativeConfig(),
				now,
			)
			if err != nil {
				return err
			}

			printClusterEvalReport(cmd, report)
			return nil
		},
	}
	evaluateCmd.Flags().StringVar(&datasetPath, "dataset", "testdata/clustering_groups.yml", "path to the labeled clustering evaluation dataset")
	evaluateCmd.Flags().StringVar(&generatedRulesPath, "generated-rules", "", "path to a generated event rules file; overrides clustering.generated_rules_path")
	return evaluateCmd
}

// printClusterEvalReport prints report in a stable, deterministic format
// (report.Assignments is already in Evaluate's fixed published_at/ID
// processing order — no map iteration, so no extra sorting is needed here)
// suitable for CLI golden-style test assertions, mirroring "dedup
// evaluate"'s cmd.Printf/cmd.Println style.
func printClusterEvalReport(cmd *cobra.Command, report clustereval.Report) {
	cmd.Printf("articles: %d total, %d scored, %d ambiguous-excluded\n",
		report.ArticlesTotal, report.ArticlesScored, report.AmbiguousExpected)
	cmd.Printf("pairwise: precision=%.3f recall=%.3f f1=%.3f (tp=%d fp=%d fn=%d tn=%d)\n",
		report.Pairwise.Precision(), report.Pairwise.Recall(), report.Pairwise.F1(),
		report.Pairwise.TP, report.Pairwise.FP, report.Pairwise.FN, report.Pairwise.TN)
	cmd.Printf("purity: %.3f\n", report.Purity)
	cmd.Printf("over_grouped_clusters: %d\n", report.OverGroupedClusters)
	cmd.Printf("fragmented_clusters: %d\n", report.FragmentedClusters)
	cmd.Printf("representative_accuracy: %.3f (%d/%d correct)\n",
		report.RepresentativeAccuracy, report.RepresentativeCorrect, report.RepresentativeTotal)
	cmd.Printf("ambiguous: expected=%d correctly_flagged=%d unexpected=%d\n",
		report.AmbiguousExpected, report.AmbiguousCorrectlyFlagged, report.UnexpectedAmbiguous)
	if report.RoleTotal > 0 {
		roleAccuracy := float64(report.RoleCorrect) / float64(report.RoleTotal)
		cmd.Printf("member_role_accuracy: %.3f (%d/%d correct)\n", roleAccuracy, report.RoleCorrect, report.RoleTotal)
	}
	cmd.Println("assignments (gold group x predicted cluster):")
	for _, a := range report.Assignments {
		excluded := ""
		if a.Excluded {
			excluded = " excluded=ambiguous"
		}
		cmd.Printf("  %s: gold=%s predicted=%s action=%s role=%s%s\n",
			a.ArticleID, a.GoldGroupID, a.PredictedClusterID, a.Action, a.MemberRole, excluded)
	}
}

func newRulesCommand() *cobra.Command {
	rulesCmd := &cobra.Command{Use: "rules", Short: "Clustering rules file maintenance"}
	rulesCmd.AddCommand(newRulesValidateCommand())
	return rulesCmd
}

func newRulesValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a clustering rules YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rf, err := clusteringrules.Load(args[0])
			if err != nil {
				return err
			}
			cmd.Printf("clustering rules OK: %s\n", args[0])
			cmd.Printf("  version=%s name=%s entities=%d\n", rf.Version, rf.Name, len(rf.Entities))
			return nil
		},
	}
}
