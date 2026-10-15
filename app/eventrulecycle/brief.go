// app/eventrulecycle/brief.go
package eventrulecycle

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

// suggestedActionName derives a Go constant name such as ActionContractExtension
// from a free-text type name.
func suggestedActionName(typeName string) string {
	parts := strings.FieldsFunc(typeName, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var b strings.Builder
	b.WriteString("Action")
	for _, w := range parts {
		r := []rune(strings.ToLower(w))
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

// Brief renders the implementation brief for an approved new-type proposal:
// everything the owner needs to open the code PR. It is pure.
func Brief(p eventruleproposal.Proposal, examples []string) string {
	var b strings.Builder
	name := strings.TrimSpace(p.TypeName)
	fmt.Fprintf(&b, "# New event type: %s\n\n", name)
	fmt.Fprintf(&b, "Proposal: `%s`\n\n", p.ID)
	fmt.Fprintf(&b, "## Description\n\n%s\n\n", strings.TrimSpace(p.Description))
	b.WriteString("## Example titles\n\n")
	if len(examples) == 0 {
		b.WriteString("(no example titles could be resolved from the evidence)\n")
	}
	for _, e := range examples {
		fmt.Fprintf(&b, "- %s\n", e)
	}
	b.WriteString("\n## Suggestions\n\n")
	b.WriteString("- Temporal window: start from 24h, the default window (or the median of the `temporal_windows` already in `configs/event_clustering_rules.yml`), then tune with the clustering dataset.\n")
	fmt.Fprintf(&b, "- New `Action` constant: `%s`\n\n", suggestedActionName(name))
	b.WriteString("## Implementation checklist\n\n")
	b.WriteString("An event type is derived from an `Action` or `ContentSignal`, so a new type needs a new `Action`. Open a separate reviewed PR touching:\n\n")
	b.WriteString("- [ ] `domain/eventsignals/eventsignals.go`: add the `EventType` and the new `Action` enums.\n")
	b.WriteString("- [ ] `infra/clusteringrules/loader.go`: add them to `knownEventTypes` and `knownActions`.\n")
	b.WriteString("- [ ] `configs/event_clustering_rules.yml`: add the `temporal_windows` entry (the loader requires one) and the `action_terms` phrases.\n")
	b.WriteString("- [ ] `classifyEventType` in `eventsignals.go`: map the new `Action` to the new `EventType`, and add the compatibility rules against the existing types.\n")
	b.WriteString("- [ ] tests: extraction, loader validation, compatibility, and new groups in `testdata/clustering_groups.yml`; `make ci` must pass.\n")
	return b.String()
}
