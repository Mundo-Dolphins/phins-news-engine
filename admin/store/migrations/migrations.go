// Package migrations embeds the SQL migration files applied to the admin
// panel's own SQLite database (admin.db).
package migrations

import "embed"

// FS holds the admin.db migrations at its root, as infra/sqlite.Migrate expects.
//
//go:embed *.sql
var FS embed.FS
