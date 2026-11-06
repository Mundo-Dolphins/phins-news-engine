// Package sqliteactions holds the admin actions over the operational SQLite
// database. They mirror `sqlite inspect`, `sqlite integrity` and
// `sqlite migrate status`; unlike the CLI they never apply migrations.
package sqliteactions

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/app/dbinspect"
	infrasqlite "github.com/Mundo-Dolphins/phins-news-engine/infra/sqlite"
	"github.com/Mundo-Dolphins/phins-news-engine/migrations"
)

const group = "sqlite"

// Actions returns the sqlite actions: sqlite.inspect, sqlite.integrity and
// sqlite.migrate_status.
func Actions() []action.Action {
	both := []action.Mode{action.ModeLocal, action.ModeProduction}
	return []action.Action{
		{
			Name: "sqlite.inspect", Group: group, Title: "Inspect database",
			Description: "Reports the database path and row counts: articles, sources, pending and leased outbox rows, open clusters.",
			Kind:        action.KindRead, Modes: both, Run: runInspect,
		},
		{
			Name: "sqlite.integrity", Group: group, Title: "Integrity check",
			Description: "Runs PRAGMA integrity_check and PRAGMA foreign_key_check; a failure fails the job with the PRAGMA output. Scans the whole file.",
			Kind:        action.KindRead, Modes: both, LongRunning: true, Run: runIntegrity,
		},
		{
			Name: "sqlite.migrate_status", Group: group, Title: "Migration status",
			Description: "Shows which schema migrations are applied and which are pending, without applying anything.",
			Kind:        action.KindRead, Modes: both, Run: runMigrateStatus,
		},
	}
}

func runInspect(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	c, err := dbinspect.Inspect(ctx, env.DB)
	if err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "object", Object: map[string]any{
		"database": env.Config.SQLite.Path, "articles": c.Articles, "sources": c.Sources,
		"outbox_pending": c.OutboxPending, "outbox_leased": c.OutboxLeased, "open_clusters": c.OpenClusters,
	}}, nil
}

func runIntegrity(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	if err := infrasqlite.Integrity(ctx, env.DB); err != nil {
		return action.Result{}, err
	}
	return action.Result{Render: "text", Text: "integrity check: ok"}, nil
}

func runMigrateStatus(ctx context.Context, env action.Env, _ any) (action.Result, error) {
	statuses, err := infrasqlite.Status(ctx, env.DB, migrations.FS)
	if err != nil {
		return action.Result{}, err
	}
	rows := make([]map[string]any, 0, len(statuses))
	for _, s := range statuses {
		state := "pending"
		if s.Applied {
			state = "applied"
		}
		rows = append(rows, map[string]any{"version": s.Version, "state": state})
	}
	return action.Result{
		Render:  "table",
		Columns: []action.Column{{Key: "version", Label: "Version"}, {Key: "state", Label: "State"}},
		Rows:    rows,
	}, nil
}
