package clustering

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals/eventsignalstest"
)

// League-scope mentions must not raise verifiability: they are recorded only,
// and may never influence which article represents a cluster.
func TestVerifiability_IgnoresLeagueMentions(t *testing.T) {
	isotopes := eventsignals.EntityMention{ID: "ezo", Name: "Ezo Quarrington", Scope: ""}
	league := eventsignals.EntityMention{ID: "league-josh-wrenfield", Name: "Josh Wrenfield", Scope: eventsignals.ScopeLeague}

	without := eventsignals.Representation{Entities: []string{"Ezo Quarrington"}, EntityMentions: []eventsignals.EntityMention{isotopes}}
	with := eventsignals.Representation{Entities: []string{"Ezo Quarrington", "Josh Wrenfield"}, EntityMentions: []eventsignals.EntityMention{isotopes, league}}
	if a, b := verifiability(without, eventsignalstest.Taxonomy()), verifiability(with, eventsignalstest.Taxonomy()); a != b {
		t.Fatalf("verifiability changed with a league mention: %v -> %v", a, b)
	}

	// Representations stored before mentions existed only have Entities.
	legacy := eventsignals.Representation{Entities: []string{"A", "B"}}
	if verifiability(legacy, eventsignalstest.Taxonomy()) <= verifiability(eventsignals.Representation{Entities: []string{"A"}}, eventsignalstest.Taxonomy()) {
		t.Fatal("legacy representations (no mentions) must still count len(Entities) >= 2")
	}
}
