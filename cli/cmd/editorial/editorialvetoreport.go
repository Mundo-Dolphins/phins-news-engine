package editorial

import (
	"encoding/json"
	"sort"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialmetrics"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infraeditorialveto "github.com/Mundo-Dolphins/phins-news-engine/infra/editorialveto"
)

func newVetoReportCommand() *cobra.Command {
	var datasetPath, vetoPath string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "veto-report",
		Short: "Dry-run deterministic editorial vetoes against an exported dataset",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			if vetoPath == "" {
				vetoPath = cfg.Editorial.VetoPath
			}
			vetoCfg, err := infraeditorialveto.Load(vetoPath)
			if err != nil {
				return err
			}
			dataset, err := editorialtrain.LoadDataset(datasetPath)
			if err != nil {
				return err
			}
			report := editorialmetrics.VetoImpact(dataset.Examples, vetoCfg, datasetPath)
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			cmd.Printf("dataset=%s total=%d blocked=%d reduction=%.3f\n", report.Dataset, report.Total, report.Blocked, report.Reduction)
			codes := make([]string, 0, len(report.ByReason))
			for code := range report.ByReason {
				codes = append(codes, code)
			}
			sort.Strings(codes)
			for _, code := range codes {
				cmd.Printf("  reason_code=%s blocked=%d examples=%v\n", code, report.ByReason[code], report.Examples[code])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&datasetPath, "dataset", "testdata/editorial_seed_dataset.yml", "exported editorial dataset (YAML); never reads live SQLite")
	cmd.Flags().StringVar(&vetoPath, "veto-config", "", "deterministic veto config (defaults to editorial.veto_path)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	return cmd
}
