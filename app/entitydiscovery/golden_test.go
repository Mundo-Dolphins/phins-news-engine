package entitydiscovery

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/knowledge"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/weblookup"
)

type goldenFile struct {
	Entities []struct {
		ID         string     `json:"id"`
		Label      string     `json:"label"`
		Aliases    []string   `json:"aliases"`
		Statements [][]string `json:"statements"`
	} `json:"entities"`
	Cases []struct {
		Name   string   `json:"name"`
		Type   string   `json:"type"`
		Hits   []string `json:"hits"`
		Expect struct {
			Decisive    bool   `json:"decisive"`
			Disposition string `json:"disposition"`
			Status      string `json:"status"`
			ProviderID  string `json:"provider_id"`
			League      string `json:"league"`
			Position    string `json:"position"`
		} `json:"expect"`
	} `json:"cases"`
}

func TestGoldenWikidataFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/wikidata_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	all := map[string]knowledge.Entity{}
	for _, e := range append(nflTeams(), springfieldCity) {
		all[e.ID] = e
	}
	for _, e := range g.Entities {
		var st []knowledge.Statement
		for _, s := range e.Statements {
			st = append(st, item(s[0], s[1]))
		}
		all[e.ID] = ent(e.ID, e.Label, e.Aliases, st...)
	}
	for _, tc := range g.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var hits []knowledge.Entity
			for _, id := range tc.Hits {
				e, ok := all[id]
				if !ok {
					t.Fatalf("fixture references unknown entity %s", id)
				}
				hits = append(hits, e)
			}
			c, fb := fixtureChain(hits, nflTeams()...)
			a := c.Assess(context.Background(), VerifyCandidate{Name: tc.Name, Type: entityproposal.EntityType(tc.Type)})
			if a.Decisive != tc.Expect.Decisive || fb.calls != 0 {
				t.Fatalf("assessment = %+v fallback=%d", a, fb.calls)
			}
			if tc.Expect.Status != "" && a.Status != tc.Expect.Status {
				t.Errorf("status = %q, want %q", a.Status, tc.Expect.Status)
			}
			if !tc.Expect.Decisive {
				return
			}
			want := weblookup.DispositionKeep
			if tc.Expect.Disposition == "reject" {
				want = weblookup.DispositionReject
			}
			md := a.Result.Metadata
			if a.Result.Disposition != want {
				t.Errorf("disposition = %v, want %v", a.Result.Disposition, want)
			}
			if tc.Expect.ProviderID != "" && md["provider_id"] != tc.Expect.ProviderID {
				t.Errorf("provider_id = %q, want %q", md["provider_id"], tc.Expect.ProviderID)
			}
			if tc.Expect.League != "" && md["league"] != tc.Expect.League {
				t.Errorf("league = %q, want %q", md["league"], tc.Expect.League)
			}
			if tc.Expect.Position != "" && md["position"] != tc.Expect.Position {
				t.Errorf("position = %q, want %q", md["position"], tc.Expect.Position)
			}
		})
	}
}

// Da'Rel (ASCII apostrophe) and Da’Rel (typographic) must resolve to the
// same Wikidata identity.
func TestGoldenDeVonApostropheEquivalence(t *testing.T) {
	ashcombe := ent("Q900000001", "Da'Rel Ashcombe", []string{"Da’Rel Ashcombe"},
		item("P31", qHuman), item("P106", qFBPlayer), item("P641", qFootball), item("P413", "Q912985"), item("P54", "Q910000006"))
	var ids []string
	for _, name := range []string{"Da'Rel Ashcombe", "Da’Rel Ashcombe"} {
		c, _ := fixtureChain([]knowledge.Entity{ashcombe}, nflTeams()...)
		a := c.Assess(context.Background(), VerifyCandidate{Name: name, Type: entityproposal.EntityTypePlayer})
		if !a.Decisive {
			t.Fatalf("%q not decisive: %+v", name, a)
		}
		ids = append(ids, a.Result.Provenance.ProviderID)
	}
	if ids[0] != ids[1] || ids[0] != "Q900000001" {
		t.Fatalf("identities differ: %v", ids)
	}
}
