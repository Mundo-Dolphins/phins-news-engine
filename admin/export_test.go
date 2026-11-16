package admin

import (
	"slices"
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

// RegistryGroupsFor maps the name of each action the built-in registry
// offers in mode to its group.
func RegistryGroupsFor(mode action.Mode) map[string]string {
	groups := map[string]string{}
	for _, a := range registry.For(mode) {
		groups[a.Name] = a.Group
	}
	return groups
}

// RegistryLLMActions lists the names of the registered actions marked
// UsesLLM, in either mode.
func RegistryLLMActions() []string {
	var names []string
	for _, mode := range []action.Mode{action.ModeLocal, action.ModeProduction} {
		for _, a := range registry.For(mode) {
			if a.UsesLLM && !slices.Contains(names, a.Name) {
				names = append(names, a.Name)
			}
		}
	}
	slices.Sort(names)
	return names
}
