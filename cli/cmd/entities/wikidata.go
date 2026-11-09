// cli/cmd/entities/wikidata.go
package entities

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

func newWikidataCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "wikidata", Short: "Wikidata verification tooling"}
	cmd.AddCommand(newWikidataEvaluateCommand())
	return cmd
}

// validateFormat checks --format; the other evaluate flags are validated by
// entitywiring.
func validateFormat(format string) error {
	switch format {
	case "text", "json":
		return nil
	}
	return fmt.Errorf("--format must be text or json (got %q)", format)
}

func newWikidataEvaluateCommand() *cobra.Command {
	var limit, sample int
	var seed int64
	var status, kind, entityType, format, reviewedPath string
	var manualPath, generatedPath string

	cmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Benchmark Wikidata verification on stored proposals (read-only; never changes proposals, never calls the LLM fallback)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := entitywiring.WikidataEvalFlags{
				Limit: limit, Sample: sample, Seed: seed, Status: status, Kind: kind, EntityType: entityType,
				ReviewedPath: reviewedPath, ManualPath: manualPath, GeneratedPath: generatedPath,
			}
			if err := validateFormat(format); err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			if err := entitywiring.ValidateWikidataEvalFlags(f); err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			db, cfg, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			defer db.Close()
			rep, err := entitywiring.EvaluateWikidata(cmd.Context(), db, cfg, f, cliLoggers(cmd))
			if err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			if format == "json" {
				return rep.WriteJSON(cmd.OutOrStdout())
			}
			return rep.WriteText(cmd.OutOrStdout())
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum proposals to evaluate (0 = no cap; ignored with --sample)")
	cmd.Flags().IntVar(&sample, "sample", 0, "evaluate a deterministic random sample of this size (e.g. 200-500 for the quality gate)")
	cmd.Flags().Int64Var(&seed, "seed", 1, "seed for --sample")
	cmd.Flags().StringVar(&status, "status", "pending", "proposal status to evaluate: pending, approved, rejected, all")
	cmd.Flags().StringVar(&kind, "kind", "", "only evaluate this proposal kind (new_entity, new_alias, possible_duplicate)")
	cmd.Flags().StringVar(&entityType, "type", "", "only evaluate this entity type (player, coach, executive, team)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	cmd.Flags().StringVar(&reviewedPath, "reviewed", "", "CSV of proposal_id,verdict (correct|wrong) from manual review; adds the observed false-resolution rate")
	cmd.Flags().StringVar(&manualPath, "manual", "configs/entity_catalog_manual.yml", "path to the manual entity catalog YAML file")
	cmd.Flags().StringVar(&generatedPath, "generated", "configs/entity_catalog_generated.yml", "path to the generated entity catalog YAML file")
	return cmd
}
