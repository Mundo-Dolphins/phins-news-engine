package model

import (
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/editorialmodel"
)

func NewValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate-model [directory]",
		Short: "Verify a candidate or promoted editorial-classifier model directory's integrity",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := ""
			if len(args) == 1 {
				dir = args[0]
			} else {
				cfg, err := config.Load(shared.ConfigPath)
				if err != nil {
					return err
				}
				dir = cfg.Model.Path
			}

			manifest, schema, metricsJSON, err := editorialmodel.Inspect(dir)
			if err != nil {
				return err
			}

			cmd.Printf("directory: %s\n", dir)
			cmd.Printf("version: %s\n", manifest.Version)
			cmd.Printf("algorithm: %s\n", manifest.Algorithm)
			cmd.Printf("status: %s\n", manifest.Status)
			cmd.Printf("feature_schema_version: %s (%d features)\n", schema.Version, len(schema.Features))
			cmd.Printf("thresholds: reject=%.2f accept=%.2f\n", manifest.Thresholds.Reject, manifest.Thresholds.Accept)
			cmd.Printf("dataset_hash: %s\n", manifest.DatasetHash)
			cmd.Printf("metrics: %s\n", metricsJSON)
			return nil
		},
	}
}
