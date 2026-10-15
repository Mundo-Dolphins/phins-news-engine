// cli/cmd/entities/wikidata.go
package entities

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitycatalog"
)

func newWikidataCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "wikidata", Short: "Wikidata verification tooling"}
	cmd.AddCommand(newWikidataEvaluateCommand())
	return cmd
}

// loadCatalogIndex loads the manual and generated catalogs into one index.
func loadCatalogIndex(manualPath, generatedPath string) (entitydiscovery.CatalogIndex, error) {
	manualRules, err := entitycatalog.LoadOne(manualPath)
	if err != nil {
		return entitydiscovery.CatalogIndex{}, err
	}
	generatedRules, err := entitycatalog.LoadOne(generatedPath)
	if err != nil {
		return entitydiscovery.CatalogIndex{}, err
	}
	all := make([]eventsignals.EntityRule, 0, len(manualRules)+len(generatedRules))
	all = append(all, manualRules...)
	all = append(all, generatedRules...)
	return entitydiscovery.BuildCatalogIndex(all), nil
}

func validateEvaluateFlags(format, status, kind, entityType string) error {
	switch format {
	case "text", "json":
	default:
		return fmt.Errorf("--format must be text or json (got %q)", format)
	}
	switch status {
	case "pending", "approved", "rejected", "all":
	default:
		return fmt.Errorf("--status must be one of pending, approved, rejected, all (got %q)", status)
	}
	switch entityproposal.Kind(kind) {
	case "", entityproposal.KindNewEntity, entityproposal.KindNewAlias, entityproposal.KindPossibleDuplicate:
	default:
		return fmt.Errorf("--kind must be one of new_entity, new_alias, possible_duplicate (got %q)", kind)
	}
	switch entityproposal.EntityType(entityType) {
	case "", entityproposal.EntityTypePlayer, entityproposal.EntityTypeCoach, entityproposal.EntityTypeExecutive, entityproposal.EntityTypeTeam:
	default:
		return fmt.Errorf("--type must be one of player, coach, executive, team (got %q)", entityType)
	}
	return nil
}

// loadReviewed reads a CSV of proposal_id,verdict (correct|wrong). An empty
// path means no reviewed sample.
func loadReviewed(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path) // #nosec G304 -- path is an explicit operator-provided CLI input path.
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = 2
	out := map[string]string{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reviewed file %s: %w", path, err)
		}
		id, verdict := strings.TrimSpace(rec[0]), strings.ToLower(strings.TrimSpace(rec[1]))
		if id == "proposal_id" {
			continue
		}
		if verdict != "correct" && verdict != "wrong" {
			return nil, fmt.Errorf("reviewed file %s: verdict for %q must be correct or wrong (got %q)", path, id, rec[1])
		}
		out[id] = verdict
	}
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
			if err := validateEvaluateFlags(format, status, kind, entityType); err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			reviewed, err := loadReviewed(reviewedPath)
			if err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			db, cfg, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			defer db.Close()
			catalogIdx, err := loadCatalogIndex(manualPath, generatedPath)
			if err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			verifier, extras, err := buildChainVerifier(cmd, db, cfg, nil, catalogIdx, true, false, false)
			if err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			assessor, ok := verifier.(entitydiscovery.Assessor)
			if !ok {
				return fmt.Errorf("entities wikidata evaluate: verifier does not support assessment")
			}
			st := status
			if st == "all" {
				st = ""
			}
			repo := sqliteentityproposal.NewEntityProposalRepository(db)
			rep, err := entitydiscovery.Evaluate(cmd.Context(), repo, assessor, catalogIdx, entitydiscovery.EvalOptions{
				Limit: limit, Status: st, Kind: kind, EntityType: entityType,
				Sample: sample, Seed: seed, Reviewed: reviewed,
			})
			if err != nil {
				return fmt.Errorf("entities wikidata evaluate: %w", err)
			}
			rep.CacheHits = extras().CacheHits
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
