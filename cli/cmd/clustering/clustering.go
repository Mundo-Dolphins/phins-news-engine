package clustering

import (
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/clustereval"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/evalwiring"
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

			report, warnings, err := evalwiring.Clustering(cfg, datasetPath, generatedRulesPath)
			for _, w := range warnings {
				cmd.PrintErrln("warning: " + w)
			}
			if err != nil {
				return err
			}

			printClusterEvalReport(cmd, report)
			return nil
		},
	}
	evaluateCmd.Flags().StringVar(&datasetPath, "dataset", evalwiring.DefaultClusteringDataset, "path to the labeled clustering evaluation dataset")
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
