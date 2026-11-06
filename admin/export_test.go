package admin

import (
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
)

// SetSchemaRecheck overrides the blocked-guard recheck interval for a test.
func SetSchemaRecheck(d time.Duration) (restore func()) {
	old := schemaRecheck
	schemaRecheck = d
	return func() { schemaRecheck = old }
}

// RegistryFor lists the names of the actions the built-in registry offers in
// mode.
func RegistryFor(mode action.Mode) []string {
	var names []string
	for _, a := range registry.For(mode) {
		names = append(names, a.Name)
	}
	return names
}
