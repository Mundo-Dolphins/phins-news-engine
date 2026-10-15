package sourceid

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/config"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
)

func NewCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "sourceid",
		Short: "Inspect the platform-namespaced source_id migration",
	}
	c.AddCommand(newMigrateReportCommand())
	return c
}

// sourceIDMapping is one distinct old_source_id -> new_source_id
// transformation migration 0021 would apply to one table.
type sourceIDMapping struct {
	Table       string
	OldSourceID string
	NewSourceID string
	RowCount    int
	// Unparseable is true when NewSourceID fails source.ParseID -- i.e.
	// migration 0021 would write a source_id that the running binary's own
	// read paths (scanArticleRow, source_repository.go, etc.) then hard-
	// reject. 0021 has no down-migration, so this is the pre-flight signal
	// an operator needs before running it for real: a legacy value that
	// doesn't match its platform's current normalization rule (e.g. a
	// YouTube channel ID an old buggy adapter lowercased, a Bluesky
	// curated feed name that doesn't match "feed-<slug>"), or a
	// source_type/platform with no entry in id.go's platformRules at all
	// (e.g. "other").
	Unparseable bool
	ParseError  string
}

// sourceIDReport is buildSourceIDMigrateReport's result.
type sourceIDReport struct {
	AlreadyApplied bool
	Mappings       []sourceIDMapping
}

// UnparseableCount returns how many of r's mappings would produce a
// new_source_id that fails source.ParseID -- the pre-flight warning count
// an operator (or an automated gate) should check before running migration
// 0021 for real.
func (r sourceIDReport) UnparseableCount() int {
	n := 0
	for _, m := range r.Mappings {
		if m.Unparseable {
			n++
		}
	}
	return n
}

// buildSourceIDMigrateReport computes, read-only, the exact mapping
// migration 0021_source_id_namespace.sql would apply, without running it.
// It deliberately never calls infrasqlite.Migrate -- that would
// unconditionally apply every pending migration, including 0021 itself,
// defeating the point of a pre-migration report. If 0021 has already run
// (recorded in schema_migrations), it reports that fact instead of
// re-deriving a (now no-op) mapping.
func buildSourceIDMigrateReport(ctx context.Context, db *sql.DB) (sourceIDReport, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='schema_migrations'
	`).Scan(&count)
	if err != nil {
		return sourceIDReport{}, fmt.Errorf("sourceid: check schema_migrations exists: %w", err)
	}
	if count == 0 {
		return sourceIDReport{}, fmt.Errorf("sourceid: database has no schema_migrations table — run the service once to initialize it first")
	}

	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM schema_migrations WHERE version = '0021_source_id_namespace.sql'
	`).Scan(&count); err != nil {
		return sourceIDReport{}, fmt.Errorf("sourceid: check migration status: %w", err)
	}
	if count > 0 {
		return sourceIDReport{AlreadyApplied: true}, nil
	}

	queries := []struct {
		table string
		query string
	}{
		{"articles", `SELECT source_id, source_type || ':' || source_id, COUNT(*) FROM articles GROUP BY source_id, source_type`},
		{"editorial_examples", `SELECT source_id, source_type || ':' || source_id, COUNT(*) FROM editorial_examples GROUP BY source_id, source_type`},
		// The identifier half is this row's OWN source_id (spd.source_id /
		// ecp.source_id below), not the joined article's current
		// source_id -- these two queries must mirror migration 0021's
		// UPDATE statements for these tables exactly (see that file's
		// comments on this point).
		{"source_policy_decisions", `
			SELECT spd.source_id, a.source_type || ':' || spd.source_id, COUNT(*)
			FROM source_policy_decisions spd JOIN articles a ON a.id = spd.article_id
			GROUP BY spd.source_id, a.source_type`},
		{"event_cluster_publications", `
			SELECT ecp.source_id, a.source_type || ':' || ecp.source_id, COUNT(*)
			FROM event_cluster_publications ecp JOIN articles a ON a.id = ecp.article_id
			GROUP BY ecp.source_id, a.source_type`},
		{"seen_items", `SELECT source_id, platform || ':' || source_id, COUNT(*) FROM seen_items GROUP BY source_id, platform`},
	}

	var mappings []sourceIDMapping
	for _, q := range queries {
		mapped, err := queryMappings(ctx, db, q.table, q.query)
		if err != nil {
			return sourceIDReport{}, err
		}
		mappings = append(mappings, mapped...)
	}
	return sourceIDReport{Mappings: mappings}, nil
}

// queryMappings runs query (one of buildSourceIDMigrateReport's per-table
// mapping queries) and scans every row into a sourceIDMapping for table,
// flagging any new_source_id that wouldn't round-trip through
// source.ParseID.
func queryMappings(ctx context.Context, db *sql.DB, table, query string) ([]sourceIDMapping, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("sourceid: query %s: %w", table, err)
	}
	defer rows.Close()

	var mappings []sourceIDMapping
	for rows.Next() {
		var m sourceIDMapping
		m.Table = table
		if err := rows.Scan(&m.OldSourceID, &m.NewSourceID, &m.RowCount); err != nil {
			return nil, fmt.Errorf("sourceid: scan %s row: %w", table, err)
		}
		if _, err := source.ParseID(m.NewSourceID); err != nil {
			m.Unparseable = true
			m.ParseError = err.Error()
		}
		mappings = append(mappings, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sourceid: iterate %s: %w", table, err)
	}
	return mappings, nil
}

func newMigrateReportCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate-report",
		Short: "Print exactly what migration 0021 would change, without applying it",
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

			report, err := buildSourceIDMigrateReport(cmd.Context(), db)
			if err != nil {
				return err
			}
			if report.AlreadyApplied {
				cmd.Println("migration 0021_source_id_namespace.sql has already been applied — nothing to report")
				return nil
			}
			if len(report.Mappings) == 0 {
				cmd.Println("no source_id rows found in any affected table — migration would be a no-op")
				return nil
			}
			for _, m := range report.Mappings {
				if m.Unparseable {
					continue
				}
				cmd.Printf("%s: %q -> %q (%d row(s))\n", m.Table, m.OldSourceID, m.NewSourceID, m.RowCount)
			}

			unparseableCount := report.UnparseableCount()
			if unparseableCount > 0 {
				cmd.Println()
				cmd.Println("=== WARNING: unparseable new_source_id values (migration 0021 would produce these, but this binary's own read paths would then reject them) ===")
				for _, m := range report.Mappings {
					if !m.Unparseable {
						continue
					}
					cmd.Printf("WARNING: %s: %q -> %q (%d row(s)) — %s\n", m.Table, m.OldSourceID, m.NewSourceID, m.RowCount, m.ParseError)
				}
				return fmt.Errorf("sourceid: %d mapping(s) would produce a source_id that fails source.ParseID — migration 0021 has no down-migration; fix the underlying data (or platformRules) before running it, see the WARNING lines above", unparseableCount)
			}
			return nil
		},
	}
}
