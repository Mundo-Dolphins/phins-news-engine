// app/eventrulecycle/known.go
package eventrulecycle

import (
	"sort"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

// KnownTypeNames returns the sorted, de-duplicated union of every EventType
// and Action name. Proposed event types that collide with one of these are
// already covered by the closed taxonomy. "quote" is both a type and an
// action, hence the de-duplication.
func KnownTypeNames() []string {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, et := range eventsignals.AllEventTypes() {
		add(string(et))
	}
	for _, a := range eventsignals.AllActions() {
		add(string(a))
	}
	sort.Strings(names)
	return names
}
