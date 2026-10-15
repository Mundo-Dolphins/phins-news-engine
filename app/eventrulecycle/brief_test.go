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
		"24h", "ActionContractExtension",
		"domain/eventsignals/eventsignals.go", "EventType", "Action",
		"infra/clusteringrules/loader.go", "knownEventTypes", "knownActions",
		"configs/event_clustering_rules.yml", "temporal_windows",
		"classifyEventType", "compatibility", "tests",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief lacks %q:\n%s", want, got)
		}
	}
}

func TestBrief_NoExamplesAndOddName(t *testing.T) {
	got := eventrulecycle.Brief(eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "pro-bowl / all_star!"}, nil)
	if !strings.Contains(got, "ActionProBowlAllStar") {
		t.Errorf("action name not sanitized:\n%s", got)
	}
	if !strings.Contains(got, "no example") {
		t.Errorf("empty examples not stated:\n%s", got)
	}
}
