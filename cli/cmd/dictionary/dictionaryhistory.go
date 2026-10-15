package dictionary

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// newHistoryCommand builds `dictionary history`, which shows either
// every recorded dictionary version (newest first, per
// DictionaryHistoryRepository.List's contract) or, with --version, the
// full detail for one version: its SQLite ledger record combined with its
// on-disk artifacts (dictionary, diff, decisions, validation) written by
// `dictionary update` under dictionaryHistoryDir.
func newHistoryCommand() *cobra.Command {
	var version, format string

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show known dictionary versions and their change history",
		RunE: func(cmd *cobra.Command, _ []string) error {
			defer func() { version = ""; format = "text" }()

			switch format {
			case "text", "json", "markdown":
			default:
				return fmt.Errorf("dictionary history: unknown --format %q (want text, json, or markdown)", format)
			}

			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return fmt.Errorf("dictionary history: %w", err)
			}
			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return fmt.Errorf("dictionary history: %w", err)
			}
			defer db.Close()
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return fmt.Errorf("dictionary history: %w", err)
			}
			repo := sqlite.NewDictionaryHistoryRepository(db)

			if version != "" {
				rec, err := repo.Get(cmd.Context(), version)
				if err != nil {
					return fmt.Errorf("dictionary history: %w", err)
				}
				artifacts, err := dictionaryhistory.ReadVersion(dictionaryHistoryDir, version)
				if err != nil {
					return fmt.Errorf("dictionary history: %w", err)
				}
				return renderHistoryDetail(cmd.OutOrStdout(), rec, artifacts, format)
			}

			list, err := repo.List(cmd.Context())
			if err != nil {
				return fmt.Errorf("dictionary history: %w", err)
			}
			return renderHistoryList(cmd.OutOrStdout(), list, format)
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "show detail for a specific version")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, or markdown")
	return cmd
}

func renderHistoryList(w io.Writer, list []out.DictionaryHistoryRecord, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	case "markdown":
		fmt.Fprintln(w, "| Version | Updated at | Changes | Snapshot | Hash | Promoted |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|")
		for _, r := range list {
			fmt.Fprintf(w, "| %s | %s | %d | %s | %s | %v |\n",
				r.Version, r.CreatedAt.Format("2006-01-02 15:04"), r.ChangeCount, r.SourceSnapshotRef, r.ContentHash, r.Promoted)
		}
		return nil
	default:
		fmt.Fprintf(w, "%-10s %-20s %-8s %-30s %s\n", "Version", "Updated at", "Changes", "Snapshot", "Hash")
		for _, r := range list {
			fmt.Fprintf(w, "%-10s %-20s %-8d %-30s %s\n", r.Version, r.CreatedAt.Format("2006-01-02 15:04"), r.ChangeCount, r.SourceSnapshotRef, r.ContentHash)
		}
		return nil
	}
}

func renderHistoryDetail(w io.Writer, rec out.DictionaryHistoryRecord, a dictionaryhistory.Artifacts, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Record    out.DictionaryHistoryRecord
			Artifacts dictionaryhistory.Artifacts
		}{rec, a})
	case "markdown":
		fmt.Fprintf(w, "# Dictionary version %s\n\n", rec.Version)
		fmt.Fprintf(w, "| Field | Value |\n|---|---|\n")
		fmt.Fprintf(w, "| Hash | %s |\n| Changes | %d |\n| Snapshot | %s |\n| Operation | %s |\n| Predecessor | %s |\n| Promoted | %v |\n\n",
			rec.ContentHash, rec.ChangeCount, rec.SourceSnapshotRef, rec.OperationKind, rec.PredecessorVersion, rec.Promoted)
		fmt.Fprintf(w, "## Diff\n\n```\n%s\n```\n", a.Diff)
		return nil
	default:
		fmt.Fprintf(w, "Version: %s\nHash: %s\nChanges: %d\nSnapshot: %s\nOperation: %s\nPredecessor: %s\nPromoted: %v\n\nDiff:\n%s\n",
			rec.Version, rec.ContentHash, rec.ChangeCount, rec.SourceSnapshotRef, rec.OperationKind, rec.PredecessorVersion, rec.Promoted, a.Diff)
		return nil
	}
}
