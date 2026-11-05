package admin

import "time"

// SetSchemaRecheck overrides the blocked-guard recheck interval for a test.
func SetSchemaRecheck(d time.Duration) (restore func()) {
	old := schemaRecheck
	schemaRecheck = d
	return func() { schemaRecheck = old }
}
