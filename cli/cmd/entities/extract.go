// cli/cmd/entities/extract.go
package entities

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/entitywiring"
)

// newExtractCommand runs the production deterministic extractor
// (eventsignals.Extract) on a text you give it, with the same rules and catalog
// the service loads. It is read-only: no database, no network.
func newExtractCommand() *cobra.Command {
	var title, description, body, bodyFile string
	var rulesPath, manualPath, generatedPath, generatedRulesPath string
	var league bool
	var rosterPath, leagueRulesPath, format string

	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Run the deterministic entity extractor on a text and print what it recognises",
		Long: `Runs the same extractor the ingestion service uses (eventsignals.Extract) on the
text you pass, with the same clustering rules and entity catalog, and prints the
recognised entities, the primary entity and team, the unknown entities and the
evidence. Nothing is written and no network is used.

Give at least one of --title, --description and --body. --body-file reads the
body from a file, or from standard input when the value is "-".

By default the catalog and rules come from configs/settings.yml; --rules,
--manual and --generated override them. The generated event rules file
(clustering.generated_rules_path, or --generated-rules) is merged as the
service does at startup; when --rules, --manual and --generated are all given
the config is not read, so only --generated-rules adds it. --league also loads the league-wide
player rules from the nflverse roster (as clustering.entity_catalog.league.mode
mentions_only does in production), ignoring roster staleness; --roster and
--league-rules override their configured paths.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("entities extract: --format must be text or json (got %q)", format)
			}
			if bodyFile != "" {
				if body != "" {
					return fmt.Errorf("entities extract: pass only one of --body and --body-file")
				}
				text, err := readBody(cmd.InOrStdin(), bodyFile)
				if err != nil {
					return fmt.Errorf("entities extract: %w", err)
				}
				body = text
			}
			if strings.TrimSpace(title+description+body) == "" {
				return fmt.Errorf("entities extract: %w", entitywiring.ErrNoText)
			}

			in := entitywiring.ExtractInput{
				Title: title, Description: description, Body: body,
				RulesPath: rulesPath, ManualPath: manualPath, GeneratedPath: generatedPath,
				GeneratedRulesPath: generatedRulesPath,
				League:             league, RosterPath: rosterPath, LeagueRulesPath: leagueRulesPath,
			}
			var cfg *config.Config
			if entitywiring.NeedsConfig(in) {
				c, err := config.Load(shared.ConfigPath)
				if err != nil {
					return fmt.Errorf("entities extract: %w (or pass --rules, --manual and --generated)", err)
				}
				cfg = c
			}
			rep, genWarnings, err := entitywiring.Extract(cfg, in, time.Now(), discoveryLogger{out: cmd.ErrOrStderr(), component: "extract"})
			if err != nil {
				return fmt.Errorf("entities extract: %w", err)
			}
			for _, w := range genWarnings {
				cmd.PrintErrln("warning: " + w)
			}

			if format == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			writeExtractText(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "article title")
	cmd.Flags().StringVar(&description, "description", "", "article description / summary")
	cmd.Flags().StringVar(&body, "body", "", "article body text")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "read the body from this file (\"-\" = standard input)")
	cmd.Flags().StringVar(&rulesPath, "rules", "", "clustering rules file (default clustering.rules_path)")
	cmd.Flags().StringVar(&manualPath, "manual", "", "manual entity catalog (default clustering.entity_catalog.manual_path)")
	cmd.Flags().StringVar(&generatedPath, "generated", "", "generated entity catalog (default clustering.entity_catalog.generated_path)")
	cmd.Flags().StringVar(&generatedRulesPath, "generated-rules", "", "generated event rules file merged into the rules (default clustering.generated_rules_path when the config is loaded)")
	cmd.Flags().BoolVar(&league, "league", false, "also recognise league-wide players from the nflverse roster (mentions only)")
	cmd.Flags().StringVar(&rosterPath, "roster", "", "nflverse roster CSV for --league (default clustering.entity_catalog.league.roster_path)")
	cmd.Flags().StringVar(&leagueRulesPath, "league-rules", "", "league alias rules for --league (default clustering.entity_catalog.league.rules_path)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json (the full Representation)")
	return cmd
}

func readBody(stdin io.Reader, path string) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
		return string(b), err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- operator-provided path of a local tool, not user input
	return string(b), err
}

func writeExtractText(w io.Writer, rep eventsignals.Representation) {
	or := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}
	fmt.Fprintf(w, "Primary entity: %s\n", or(rep.PrimaryEntity))
	fmt.Fprintf(w, "Primary team: %s\n", or(rep.PrimaryTeam))
	fmt.Fprintf(w, "Opponent: %s\n", or(rep.Opponent))
	fmt.Fprintf(w, "Competition: %s\n", or(rep.Competition))
	if len(rep.SecondaryEntities) > 0 {
		fmt.Fprintf(w, "Secondary entities: %s\n", strings.Join(rep.SecondaryEntities, ", "))
	}
	fmt.Fprintln(w, "Entities (first-mention order):")
	if len(rep.EntityMentions) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, m := range rep.EntityMentions {
		scope := ""
		if m.Scope != "" {
			scope = ", " + string(m.Scope)
		}
		fmt.Fprintf(w, "  - %s (%s, %s%s)\n", m.Name, m.Kind, m.Field, scope)
	}
	fmt.Fprintln(w, "Unknown entities:")
	if len(rep.UnknownEntities) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, u := range rep.UnknownEntities {
		fmt.Fprintf(w, "  - %s (%s)\n", u.Text, u.Field)
	}
	if len(rep.Evidence) > 0 {
		fmt.Fprintln(w, "Evidence:")
		for _, e := range rep.Evidence {
			fmt.Fprintf(w, "  - %s\n", e)
		}
	}
}
