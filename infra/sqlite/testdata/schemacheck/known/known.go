// Package known is a minimal, self-contained migrations.FS fixture for
// schema_check_test.go — it does not use the real migrations package
// so tests stay independent of the real migration set's size and history.
package known

import "embed"

//go:embed *.sql
var FS embed.FS
