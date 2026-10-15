package editorial

import (
	"encoding/json"
	"sort"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/editorialveto"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infraeditorialveto "github.com/Mundo-Dolphins/phins-news-engine/infra/editorialveto"
)

type vetoImpactReport struct {
	Dataset   string              `json:"dataset"`
	Total     int                 `json:"total"`
	Blocked   int                 `json:"blocked"`
	Reduction float64             `json:"reduction"`
	ByReason  map[string]int      `json:"by_reason_code"`
	Examples  map[string][]string `json:"examples_by_reason_code"`
}

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
			report := evaluateVetoImpact(dataset.Examples, vetoCfg, datasetPath)
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

func evaluateVetoImpact(examples []editorialtrain.TrainingExample, cfg editorialveto.Config, dataset string) vetoImpactReport {
	report := vetoImpactReport{Dataset: dataset, Total: len(examples), ByReason: map[string]int{}, Examples: map[string][]string{}}
	for _, example := range examples {
		matches := editorialveto.EvaluateAll(example.Title+" "+example.Body, cfg)
		if len(matches) == 0 {
			continue
		}
		report.Blocked++
		// Blocked is unique-article reduction. Per-rule counts include every
		// matching rule so operators can see the independent impact of each
		// rule; online processing still uses the first-match-wins result.
		for _, match := range matches {
			code := match.ReasonCode
			report.ByReason[code]++
			if len(report.Examples[code]) < 5 {
				report.Examples[code] = append(report.Examples[code], example.ArticleID)
			}
		}
	}
	if report.Total > 0 {
		report.Reduction = float64(report.Blocked) / float64(report.Total)
	}
	return report
}
