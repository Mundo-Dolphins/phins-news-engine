// cli/cmd/events/golden_test.go
package events_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventruleproposal"
	"path/filepath"
)

// Exact output of the read commands, pinned before their logic moved to
// infra/eventrules so the move cannot change a byte.

func TestEventsType_ListGolden(t *testing.T) {
	configPath := gateFixture(t, "")
	seedGeneric(t, configPath, "Isotopes extend Ezo")
	ids := seedProposals(t, configPath,
		eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "contract extension", Description: "Re-signings.", EvidenceIDs: []string{"g0", "g1"}, Status: eventruleproposal.StatusPending, PromptVersion: "v1"},
		eventruleproposal.Proposal{Kind: eventruleproposal.KindType, TypeName: "stadium works", Description: "Construction.", Status: eventruleproposal.StatusApproved, PromptVersion: "v1"},
	)
	out, err := testsupport.Execute(t, "--config", configPath, "events", "type", "list")
	if err != nil {
		t.Fatal(err)
	}
	out = strings.NewReplacer(ids[0], "<p0>", ids[1], "<p1>").Replace(out)
	want := "<p0> [pending] contract extension: Re-signings. (evidence g0, g1)\n<p1> [approved] stadium works: Construction. (evidence )\n"
	if out != want {
		t.Fatalf("output:\n%q\nwant:\n%q", out, want)
	}
}

func TestEventsType_ListEmptyGolden(t *testing.T) {
	configPath := gateFixture(t, "")
	out, err := testsupport.Execute(t, "--config", configPath, "events", "type", "list")
	if err != nil {
		t.Fatal(err)
	}
	if want := "No pending or approved event type proposals.\n"; out != want {
		t.Fatalf("output %q, want %q", out, want)
	}
}

func TestEventsImpact_Golden(t *testing.T) {
	configPath := gateFixture(t, impactGenerated)
	seedGeneric(t, configPath, "Isotopes zzqx pens a pact with veteran", "Weather in Springfield is sunny")
	seedArticles(t, configPath, "t", "transaction", "Isotopes sign veteran linebacker amid zzqx whispers")
	out, err := testsupport.Execute(t, "--config", configPath, "events", "impact", "--days", "7")
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(filepath.Dir(configPath), "event_rules_generated.yml")
	out = strings.ReplaceAll(out, generated, "<generated>")
	want := "generated rules: <generated>\n" +
		"last 7 day(s): 2 generic article(s), 1 would change type\n" +
		"\ntransaction: 1\n  - Isotopes zzqx pens a pact with veteran\n" +
		"\nlast 7 day(s): 1 typed article(s), 1 would change type\n" +
		"\ntransaction -> rumor: 1\n  - Isotopes sign veteran linebacker amid zzqx whispers\n"
	if out != want {
		t.Fatalf("output:\n%q\nwant:\n%q", out, want)
	}
}
