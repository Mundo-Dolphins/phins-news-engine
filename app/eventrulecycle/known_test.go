// app/eventrulecycle/known_test.go
package eventrulecycle_test

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals/eventsignalstest"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestKnownTypeNames(t *testing.T) {
	got := eventrulecycle.KnownTypeNames(eventsignalstest.Taxonomy())
	if !sort.StringsAreSorted(got) {
		t.Errorf("not sorted: %v", got)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("duplicate %q", n)
		}
		seen[n] = true
	}
	for _, want := range []string{"game", "injury", "injured-reserve", "generic", "quote"} {
		if !seen[want] {
			t.Errorf("missing %q", want)
		}
	}
}

// kindNames returns the names of ks, in order.
func kindNames(ks []appout.KnownEventKind) []string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, k.Name)
	}
	return out
}

// deltaTaxonomy is the reference taxonomy plus a type delta detected by an
// action delta.
func deltaTaxonomy() eventsignals.Taxonomy {
	tax := eventsignalstest.Taxonomy()
	tax.EventTypes = append(tax.EventTypes, eventsignals.EventTypeDef{Name: "delta", Description: "A delta move."})
	tax.Actions = append(tax.Actions, eventsignals.ActionDef{Name: "delta", EventType: "delta"})
	return tax
}

func TestKnownTypeNames_FollowsTheTaxonomy(t *testing.T) {
	if got := eventrulecycle.KnownTypeNames(deltaTaxonomy()); !slices.Contains(got, "delta") {
		t.Errorf("known = %v, want delta", got)
	}
	if got := eventrulecycle.KnownTypeNames(eventsignals.Taxonomy{}); !slices.Equal(got, []string{"generic"}) {
		t.Errorf("empty taxonomy known = %v, want [generic]", got)
	}
}

func TestKnownTarget_FollowsTheTaxonomy(t *testing.T) {
	if tk, ok := eventrulecycle.KnownTarget("action", "delta", deltaTaxonomy()); !ok || tk != eventruleproposal.TargetAction {
		t.Errorf("KnownTarget(action, delta) = %q, %v", tk, ok)
	}
	if _, ok := eventrulecycle.KnownTarget("action", "delta", eventsignalstest.Taxonomy()); ok {
		t.Error("delta is known without the taxonomy declaring it")
	}
	if tk, ok := eventrulecycle.KnownTarget("rumor", "rumor", eventsignalstest.Taxonomy()); !ok || tk != eventruleproposal.TargetSignal {
		t.Errorf("KnownTarget(rumor, rumor) = %q, %v", tk, ok)
	}
}

func TestDiscover_UsesTheBaseTaxonomy(t *testing.T) {
	h := newHarness(t)
	h.deps.BaseRules = func() (eventsignals.Rules, error) {
		return eventsignals.Rules{Taxonomy: deltaTaxonomy()}, nil
	}
	h.llm.topics = []appout.EventTopicProposal{{Name: "Delta", ArticleIDs: []string{"a3"}}}
	c, err := eventrulecycle.Prepare(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	r, err := eventrulecycle.Discover(context.Background(), h.deps, c, 20)
	if err != nil {
		t.Fatal(err)
	}
	if r.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1 (delta is a known type)", r.Dropped)
	}
	if !slices.Contains(h.llm.actions, "delta") {
		t.Errorf("allowed actions = %v, want delta", h.llm.actions)
	}
	if len(h.llm.known) == 0 || !slices.Contains(h.llm.known[0], appout.KnownEventKind{Name: "delta", Description: "A delta move."}) {
		t.Errorf("known = %v, want delta with its description", h.llm.known)
	}
}

func TestDiscover_KnownKindsCarryTypeDescriptionsOnly(t *testing.T) {
	h := newHarness(t)
	h.llm.topics = nil
	if _, err := h.repo.Upsert(context.Background(), eventruleproposal.Proposal{
		Kind: eventruleproposal.KindType, TypeName: "betting-odds", Description: "Odds.",
		Status: eventruleproposal.StatusPending, PromptVersion: "event-rules-v1", CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	c, err := eventrulecycle.Prepare(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventrulecycle.Discover(context.Background(), h.deps, c, 20); err != nil {
		t.Fatal(err)
	}
	if len(h.llm.known) == 0 {
		t.Fatal("no known kinds passed")
	}
	byName := map[string]string{}
	for _, k := range h.llm.known[0] {
		if _, dup := byName[k.Name]; dup {
			t.Errorf("%q listed twice", k.Name)
		}
		byName[k.Name] = k.Description
	}
	for name, want := range map[string]string{
		"game":         "A game preview or result.",
		"quote":        "Coverage built around what someone said.", // type and action: the type's description
		"sign":         "",
		"generic":      "",
		"betting-odds": "",
	} {
		if got, ok := byName[name]; !ok || got != want {
			t.Errorf("%s: description = %q (listed %v), want %q", name, got, ok, want)
		}
	}
}
