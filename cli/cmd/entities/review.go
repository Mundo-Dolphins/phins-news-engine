package entities

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sqliteentityproposal "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqliteentityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/app/entitydiscovery"
	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// newReviewCommand provides a deliberately small terminal review loop for
// pending proposals. It keeps the proposal ID internal to the selected item,
// avoiding error-prone copy/paste into separate approve/reject commands.
func newReviewCommand() *cobra.Command {
	var dictionaryPath, manualPath, generatedPath string

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Interactively approve, reject, or skip pending entity proposals",
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, _, err := openDatabase(cmd)
			if err != nil {
				return fmt.Errorf("entities review: %w", err)
			}
			defer db.Close()

			repo := sqliteentityproposal.NewEntityProposalRepository(db)
			proposals, err := repo.List(cmd.Context(), "", string(entityproposal.StatusPending))
			if err != nil {
				return fmt.Errorf("entities review: %w", err)
			}
			sort.SliceStable(proposals, func(i, j int) bool {
				return entityproposal.Score(proposals[i]) > entityproposal.Score(proposals[j])
			})
			if len(proposals) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No pending entity proposals.")
				return nil
			}

			in := bufio.NewScanner(cmd.InOrStdin())
			approved, rejected, skipped := 0, 0, 0
			for i, proposal := range proposals {
				printProposalForReview(cmd, i+1, len(proposals), proposal)
				for {
					prompt := "Action: [a]pprove, [r]eject, [s]kip, [q]uit: "
					if proposal.Kind == entityproposal.KindPossibleDuplicate {
						prompt = "Action: approve as [n]ew entity, [r]eject, [s]kip, [q]uit: "
					}
					fmt.Fprint(cmd.OutOrStdout(), prompt)
					if !in.Scan() {
						fmt.Fprintf(cmd.OutOrStdout(), "\nReview ended. approved: %d, rejected: %d, skipped: %d\n", approved, rejected, skipped)
						return nil
					}
					action := strings.ToLower(strings.TrimSpace(in.Text()))
					switch action {
					case "a", "approve", "n", "new":
						if proposal.Kind == entityproposal.KindPossibleDuplicate && action != "n" && action != "new" {
							fmt.Fprintln(cmd.OutOrStdout(), "This is a possible duplicate. Choose n to approve it as a new entity, or skip and use `entities approve --merge-into` to create an alias.")
							continue
						}
						resolved := proposal
						if proposal.Kind == entityproposal.KindPossibleDuplicate {
							resolved.Kind = entityproposal.KindNewEntity
						}
						paths := entitydiscovery.CatalogPaths{DictionaryPath: dictionaryPath, ManualCatalogPath: manualPath, GeneratedCatalogPath: generatedPath}
						if err := entitydiscovery.ApplyApproval(resolved, paths, time.Now().UTC()); err != nil {
							fmt.Fprintf(cmd.OutOrStdout(), "Cannot approve %s: %v\nThe proposal remains pending; choose reject, skip, or another action.\n", proposal.CanonicalName, err)
							continue
						}
						if err := repo.UpdateStatus(cmd.Context(), proposal.ID, entityproposal.StatusApproved, time.Now().UTC()); err != nil {
							return fmt.Errorf("entities review: approve %s: %w", proposal.ID, err)
						}
						fmt.Fprintf(cmd.OutOrStdout(), "Approved %s.\n", proposal.CanonicalName)
						approved++
						goto nextProposal
					case "r", "reject":
						if err := repo.UpdateStatus(cmd.Context(), proposal.ID, entityproposal.StatusRejected, time.Now().UTC()); err != nil {
							return fmt.Errorf("entities review: reject %s: %w", proposal.ID, err)
						}
						fmt.Fprintf(cmd.OutOrStdout(), "Rejected %s.\n", proposal.CanonicalName)
						rejected++
						goto nextProposal
					case "s", "skip", "":
						skipped++
						goto nextProposal
					case "q", "quit":
						fmt.Fprintf(cmd.OutOrStdout(), "Review ended. approved: %d, rejected: %d, skipped: %d\n", approved, rejected, skipped)
						return nil
					default:
						fmt.Fprintln(cmd.OutOrStdout(), "Unknown action. Choose a, r, s, or q.")
					}
				}
			nextProposal:
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Review complete. approved: %d, rejected: %d, skipped: %d\n", approved, rejected, skipped)
			return nil
		},
	}
	cmd.Flags().StringVar(&dictionaryPath, "dictionary", shared.DefaultDictionaryPath, "path to the team dictionary YAML file")
	cmd.Flags().StringVar(&manualPath, "manual", "configs/entity_catalog_manual.yml", "path to the manual entity catalog YAML file")
	cmd.Flags().StringVar(&generatedPath, "generated", "configs/entity_catalog_generated.yml", "path to the generated entity catalog YAML file (read-only duplicate check; a missing file is ignored)")
	return cmd
}

func printProposalForReview(cmd *cobra.Command, index, total int, p entityproposal.Proposal) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n--- Proposal %d/%d ---\n", index, total)
	fmt.Fprintf(out, "%s · %s · %s · confidence %.2f · occurrences %d\n", p.CanonicalName, p.EntityType, p.Kind, p.Confidence, p.Occurrences)
	if len(p.Aliases) > 0 {
		fmt.Fprintf(out, "Aliases: %s\n", strings.Join(p.Aliases, ", "))
	}
	if len(p.Metadata) > 0 {
		parts := make([]string, 0, len(p.Metadata))
		for key, value := range p.Metadata {
			if key == entitydiscovery.MetadataVerifyAttemptedAt {
				continue
			}
			parts = append(parts, key+"="+value)
		}
		sort.Strings(parts)
		if len(parts) > 0 {
			fmt.Fprintf(out, "Metadata: %s\n", strings.Join(parts, ", "))
		}
	}
	printVerification(out, p, true)
	for i, source := range p.Sources {
		if i == 3 {
			break
		}
		fmt.Fprintf(out, "Evidence: %s\n", source.ArticleTitle)
	}
}

// printVerification prints the verified identity lines (Role, Team, League
// and, when withPosition, Position) plus a "Web evidence" line listing the
// distinct web-source domains. Nothing is printed for absent values, so
// proposals without verification data render exactly as before.
func printVerification(out io.Writer, p entityproposal.Proposal, withPosition bool) {
	lines := []struct{ label, key string }{{"Position", "position"}, {"Role", "role"}, {"Team", "team"}, {"League", "league"}}
	for _, l := range lines {
		if l.key == "position" && !withPosition {
			continue
		}
		if v := p.Metadata[l.key]; v != "" {
			fmt.Fprintf(out, "%s: %s\n", l.label, v)
		}
	}
	if line := affiliationLine(p); line != "" {
		fmt.Fprintln(out, line)
	}
	if e := p.Metadata["suggested_entity"]; e != "" {
		fmt.Fprintf(out, "Suggested match: %s (%s), basis %s\n", e, p.Metadata["suggested_team"], p.Metadata["suggested_basis"])
	} else if c := p.Metadata["suggested_candidates"]; c != "" {
		fmt.Fprintf(out, "Candidates: %s (basis %s)\n", c, p.Metadata["suggested_basis"])
	}
	var domains []string
	seen := map[string]bool{}
	for _, ws := range p.WebSources {
		if ws.Domain != "" && !seen[ws.Domain] {
			seen[ws.Domain] = true
			domains = append(domains, ws.Domain)
		}
	}
	if len(domains) > 0 {
		fmt.Fprintf(out, "Web evidence: %s\n", strings.Join(domains, ", "))
	}
}

// affiliationLine labels a player's affiliation when no team line can say
// it: a verified free agent and an unresolved affiliation must read
// differently, never as an empty "Team:". Players with a team (or any
// non-player) get no extra line.
func affiliationLine(p entityproposal.Proposal) string {
	if p.EntityType != entityproposal.EntityTypePlayer {
		return ""
	}
	switch eventsignals.AffiliationStatus(p.Metadata[entitydiscovery.MetadataAffiliationStatus]) {
	case eventsignals.AffiliationFreeAgent:
		return "Affiliation: FREE AGENT"
	case eventsignals.AffiliationUnknown:
		return "Affiliation: UNKNOWN"
	}
	if p.Metadata["team"] == "" && p.Metadata["team_id"] == "" {
		return "Affiliation: UNKNOWN"
	}
	return ""
}
