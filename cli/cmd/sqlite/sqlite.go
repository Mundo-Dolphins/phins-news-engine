package sqlitecmd

import (
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	sqliteadapter "github.com/Mundo-Dolphins/phins-news-engine/adapters/outbound/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

func NewCommand() *cobra.Command {
	sqliteCmd := &cobra.Command{
		Use:   "sqlite",
		Short: "SQLite inspection and maintenance",
	}
	sqliteCmd.AddCommand(newInspectCommand())
	sqliteCmd.AddCommand(newIntegrityCommand())
	sqliteCmd.AddCommand(newMigrateCommand())
	sqliteCmd.AddCommand(newMergeLocalCommand())
	return sqliteCmd
}

// newMergeLocalCommand carries the tools-owned tables (labels, review
// sessions, registries) from a previous local database into the current
// one — the same merge `editorial cycle`'s sync runs, exposed
// so the plain investigation sync (scripts/promote-local-sqlite.sh) stops
// discarding locally-made labels into a dated backup.
func newMergeLocalCommand() *cobra.Command {
	var from, dbPath string
	cmd := &cobra.Command{
		Use:   "merge-local",
		Short: "Carry labels, review sessions and registries forward from a previous local database, then migrate",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The root command is reused across invocations (tests); reset
			// flag state so a previous --db never leaks into the next run.
			defer func() { from, dbPath = "", "" }()
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}
			target := dbPath
			if target == "" {
				target = cfg.SQLite.Path
			}
			db, err := infrasqlite.Open(target, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()

			warnings, err := infrasqlite.MergeLocalOwnedTables(cmd.Context(), db, from)
			if err != nil {
				return err
			}
			printMergeWarningSummary(cmd, warnings)
			if err := infrasqlite.Migrate(cmd.Context(), db, migrations.FS); err != nil {
				return err
			}
			cmd.Printf("merged local-owned tables from %s into %s\n", from, target)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "previous local database to carry labels and registries forward from (required)")
	cmd.Flags().StringVar(&dbPath, "db", "", "database to merge into (default: sqlite.path from config)")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}

func newInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect",
		Short: "Open the operational database, apply pending migrations, and print row counts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()

			ctx := cmd.Context()
			if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
				return err
			}

			articles := sqliteadapter.NewArticleRepository(db)
			articleCount, err := articles.Count(ctx)
			if err != nil {
				return err
			}

			sources := sqliteadapter.NewSourceRepository(db)
			sourceList, err := sources.List(ctx)
			if err != nil {
				return err
			}

			var outboxPending, outboxLeased, openClusters int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_outbox WHERE status = 'pending'`).Scan(&outboxPending); err != nil {
				return err
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM publication_outbox WHERE status = 'leased'`).Scan(&outboxLeased); err != nil {
				return err
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM event_clusters WHERE status = 'open'`).Scan(&openClusters); err != nil {
				return err
			}

			cmd.Printf("database: %s\n", cfg.SQLite.Path)
			cmd.Printf("articles: %d\n", articleCount)
			cmd.Printf("sources: %d\n", len(sourceList))
			cmd.Printf("outbox pending: %d\n", outboxPending)
			cmd.Printf("outbox leased: %d\n", outboxLeased)
			cmd.Printf("open clusters: %d\n", openClusters)
			return nil
		},
	}
}

func newIntegrityCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "integrity",
		Short: "Run PRAGMA integrity_check and PRAGMA foreign_key_check against the operational database",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()

			ctx := cmd.Context()
			if err := infrasqlite.Migrate(ctx, db, migrations.FS); err != nil {
				return err
			}
			if err := infrasqlite.Integrity(ctx, db); err != nil {
				return err
			}

			cmd.Println("integrity check: ok")
			return nil
		},
	}
}

func newMigrateCommand() *cobra.Command {
	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Inspect schema migrations",
	}
	migrateCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show which migrations are applied and which are pending, without applying anything",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(shared.ConfigPath)
			if err != nil {
				return err
			}

			db, err := infrasqlite.Open(cfg.SQLite.Path, cfg.SQLite.BusyTimeout())
			if err != nil {
				return err
			}
			defer db.Close()

			statuses, err := infrasqlite.Status(cmd.Context(), db, migrations.FS)
			if err != nil {
				return err
			}

			for _, s := range statuses {
				state := "pending"
				if s.Applied {
					state = "applied"
				}
				cmd.Printf("%s: %s\n", s.Version, state)
			}
			return nil
		},
	})
	return migrateCmd
}

// printMergeWarningSummary counts MergeLocalOwnedTables' foreign-key
// warnings per (table, referenced table) instead of printing one line per
// row: labels routinely outlive their purged article row (retention), so
// per-row output would run to thousands of expected lines. Warnings are
// "<table> row (rowid=…) references missing <ref> row".
func printMergeWarningSummary(cmd *cobra.Command, warnings []string) {
	counts := map[string]int{}
	var keys []string
	for _, w := range warnings {
		fields := strings.Fields(w)
		key := w
		for i, f := range fields {
			if f == "missing" && i+1 < len(fields) {
				key = fields[0] + "|" + fields[i+1]
				break
			}
		}
		if counts[key] == 0 {
			keys = append(keys, key)
		}
		counts[key]++
	}
	sort.Strings(keys)
	for _, k := range keys {
		table, ref, ok := strings.Cut(k, "|")
		if !ok {
			cmd.Printf("warning: %s\n", k)
			continue
		}
		cmd.Printf("note: %d %s row(s) reference purged %s (expected after retention; kept as history)\n", counts[k], table, ref)
	}
}
