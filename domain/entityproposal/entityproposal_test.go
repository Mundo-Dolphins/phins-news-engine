package entityproposal_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/entityproposal"
)

func TestScore_CombinesOccurrencesTitleHitsSourcesAndConfidence(t *testing.T) {
	p := entityproposal.Proposal{
		CanonicalName: "Jai'Rene' Mossbank",
		Occurrences:   3,
		Confidence:    0.9,
		Sources: []entityproposal.Source{
			{ArticleID: "a1", ArticleTitle: "Isotopes sign CB Jai'Rene' Mossbank"},
			{ArticleID: "a2", ArticleTitle: "Springfield adds a cornerback"},
		},
	}
	// occurrences(3) + 1.5*titleOccurrences(1) + 1.5*uniqueSources(2) + confidence(0.9)
	want := 3.0 + 1.5*1 + 1.5*2 + 0.9
	if got := entityproposal.Score(p); got != want {
		t.Errorf("Score() = %v, want %v", got, want)
	}
}

func TestConstants_HaveExpectedStringValues(t *testing.T) {
	if entityproposal.EntityTypePlayer != "player" || entityproposal.EntityTypeCoach != "coach" ||
		entityproposal.EntityTypeExecutive != "executive" || entityproposal.EntityTypeTeam != "team" {
		t.Fatalf("EntityType constants changed value — eventsignals.EntityKind string-cast compatibility depends on these staying identical")
	}
	if entityproposal.KindNewEntity != "new_entity" || entityproposal.KindNewAlias != "new_alias" || entityproposal.KindPossibleDuplicate != "possible_duplicate" {
		t.Fatalf("Kind constants changed value")
	}
	if entityproposal.StatusPending != "pending" || entityproposal.StatusApproved != "approved" || entityproposal.StatusRejected != "rejected" {
		t.Fatalf("Status constants changed value")
	}
}

func TestProposalSource_Constants(t *testing.T) {
	if entityproposal.ProposalSourceDiscovery != "discovery" {
		t.Fatalf("ProposalSourceDiscovery = %q, want %q", entityproposal.ProposalSourceDiscovery, "discovery")
	}
	if entityproposal.ProposalSourceOnlineFallback != "online_fallback" {
		t.Fatalf("ProposalSourceOnlineFallback = %q, want %q", entityproposal.ProposalSourceOnlineFallback, "online_fallback")
	}
}

func TestProposal_WebSourcesField(t *testing.T) {
	p := entityproposal.Proposal{
		CanonicalName: "Jai'Rene' Mossbank",
		WebSources: []entityproposal.WebSource{
			{Domain: "isotopes.example.com", URL: "https://isotopes.example.com/news/1", Title: "Isotopes Add CB Jai'Rene' Mossbank"},
		},
	}
	if len(p.WebSources) != 1 {
		t.Fatalf("WebSources = %+v, want 1 entry", p.WebSources)
	}
	if p.WebSources[0].Domain != "isotopes.example.com" {
		t.Fatalf("WebSources[0].Domain = %q, want isotopes.example.com", p.WebSources[0].Domain)
	}
}
