// app/eventrulecycle/brief.go
package eventrulecycle

import (
	"fmt"
	"strings"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

// Brief renders the implementation brief for an approved new-type proposal:
// everything the owner needs to open the configuration PR. It is pure.
func Brief(p eventruleproposal.Proposal, examples []string) string {
	var b strings.Builder
	name := strings.TrimSpace(p.TypeName)
	slug := eventruleproposal.Slug(name)
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
	fmt.Fprintf(&b, "- Event type and action name: `%s`\n\n", slug)
	b.WriteString("## Implementation checklist\n\n")
	b.WriteString("An event type is derived from an action or content signal, so a new type needs a new action. The taxonomy is configuration, not Go code: open a separate reviewed PR touching the hand-written rules file (`configs/event_clustering_rules.yml`, `clustering.rules_path`) and the gold dataset:\n\n")
	b.WriteString("- [ ] `taxonomy.event_types`: add the type with its `description`, any `flags` (`referential`, `update_framing`, `team_level_needs_opponent`) and its `member_role` if it has one.\n")
	b.WriteString("- [ ] `taxonomy.actions`: add the action with its `event_type`, and `body_fallback` only if it should also be detected from body text.\n")
	b.WriteString("- [ ] `temporal_windows`: add the type's entry (the loader requires one).\n")
	b.WriteString("- [ ] `action_terms`: add the phrases that detect the action.\n")
	b.WriteString("- [ ] `testdata/clustering_groups.yml`: add gold groups for the new type; config validation and `make ci` must pass.\n")
	return b.String()
}
