// app/eventrulecycle/brief_test.go
package eventrulecycle_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/app/eventrulecycle"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
)

func TestBrief_ContainsNameDescriptionExamplesAndChecklist(t *testing.T) {
	p := eventruleproposal.Proposal{
		ID: "evr_1", Kind: eventruleproposal.KindType, TypeName: "contract extension",
		Description: "Players re-sign before free agency.",
	}
	got := eventrulecycle.Brief(p, []string{"Isotopes extend Ezo", "Dale gets new deal"})
	for _, want := range []string{
		"contract extension", "Players re-sign before free agency.", "evr_1",
		"- Isotopes extend Ezo", "- Dale gets new deal",
		"24h", "`contract-extension`",
		"configs/event_clustering_rules.yml", "taxonomy.event_types", "description", "flags", "member_role",
		"taxonomy.actions", "event_type", "body_fallback", "temporal_windows", "action_terms",
		"testdata/clustering_groups.yml", "make ci",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief lacks %q:\n%s", want, got)
		}
	}
}

func TestBrief_NoExamplesAndOddName(t *testing.T) {
	got := eventrulecycle.Brief(eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "pro-bowl / all_star!"}, nil)
	if !strings.Contains(got, "`pro-bowl-all-star`") {
		t.Errorf("action name not slugged:\n%s", got)
	}
	if !strings.Contains(got, "no example") {
		t.Errorf("empty examples not stated:\n%s", got)
	}
}
