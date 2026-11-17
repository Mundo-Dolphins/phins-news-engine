// app/eventrulecycle/known.go
package eventrulecycle

import (
	"sort"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// KnownTypeNames returns the sorted, de-duplicated union of t's event type
// names (generic included) and action names. Proposed event types that
// collide with one of these are already covered by the taxonomy. A name can
// be both a type and an action ("quote"), hence the de-duplication.
func KnownTypeNames(t eventsignals.Taxonomy) []string {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, et := range t.EventTypeNames() {
		add(string(et))
	}
	for _, a := range t.ActionPriority() {
		add(string(a))
	}
	sort.Strings(names)
	return names
}
