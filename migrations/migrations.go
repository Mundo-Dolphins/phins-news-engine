// Package migrations embeds the SQL migration files applied to the
// operational SQLite database at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
