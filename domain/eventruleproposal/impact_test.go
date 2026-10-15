// domain/eventruleproposal/impact_test.go
package eventruleproposal

import (
	"testing"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
)

func TestImpact_CountsGenericMovedAndRetyped(t *testing.T) {
	rules := eventsignals.Rules{ActionTerms: map[eventsignals.Action][]string{eventsignals.ActionSign: {"signed"}}}
	sample := []Sample{
		{Article: article.Article{ID: "1", Title: "Isotopes inks a deal with veteran"}, ClusterID: "c1"},
		{Article: article.Article{ID: "2", Title: "Isotopes signed a veteran"}, ClusterID: "c2"},
		{Article: article.Article{ID: "3", Title: "Weather in Springfield"}, ClusterID: "c3"},
	}
	p := Proposal{Kind: KindPhrase, TargetKind: TargetAction, Target: "sign", Phrase: "inks a deal"}
	got := Impact(rules, p, sample, time.Unix(0, 0))
	if got.Moved[eventsignals.EventTypeTransaction] != 1 || got.Retyped != 0 || got.Matches != 1 {
		t.Fatalf("%+v", got)
	}
	if got.DistinctClusters != 1 || len(got.Examples) != 1 {
		t.Fatalf("%+v", got)
	}
	if len(rules.ActionTerms[eventsignals.ActionSign]) != 1 {
		t.Fatal("base mutated")
	}
}

func TestImpact_RetypedWhenBaselineAlreadyTyped(t *testing.T) {
	rules := eventsignals.Rules{ActionTerms: map[eventsignals.Action][]string{eventsignals.ActionSign: {"signed"}}}
	sample := []Sample{{Article: article.Article{ID: "1", Title: "Isotopes signed a veteran"}, ClusterID: "c1"}}
	// A rumor cue overrides the transaction type of an already-typed article.
	p := Proposal{Kind: KindPhrase, TargetKind: TargetSignal, Target: "rumor", Phrase: "signed a veteran"}
	got := Impact(rules, p, sample, time.Unix(0, 0))
	if got.Retyped != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestImpact_TypeKindIsNoop(t *testing.T) {
	p := Proposal{Kind: KindType, Phrase: "x"}
	got := Impact(eventsignals.Rules{}, p, []Sample{{Article: article.Article{Title: "x"}}}, time.Unix(0, 0))
	if got.Matches != 0 || got.Retyped != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestImpact_ReportsPhraseAlreadyInAnyBucket(t *testing.T) {
	rules := eventsignals.Rules{
		ActionTerms: map[eventsignals.Action][]string{eventsignals.ActionSign: {"Inks A Deal"}},
		ContentCues: map[eventsignals.ContentSignal][]string{eventsignals.ContentSignalRumor: {"per sources"}},
	}
	sample := []Sample{{Article: article.Article{ID: "1", Title: "x"}}}
	for _, tc := range []struct {
		p    Proposal
		want string
	}{
		{Proposal{Kind: KindPhrase, TargetKind: TargetSignal, Target: "rumor", Phrase: " inks a deal"}, "action_terms.sign"},
		{Proposal{Kind: KindPhrase, TargetKind: TargetAction, Target: "trade", Phrase: "per sources"}, "content_cues.rumor"},
		{Proposal{Kind: KindPhrase, TargetKind: TargetAction, Target: "trade", Phrase: "ships off"}, ""},
	} {
		if got := Impact(rules, tc.p, sample, time.Unix(0, 0)).InVocabulary; got != tc.want {
			t.Errorf("%q: InVocabulary = %q, want %q", tc.p.Phrase, got, tc.want)
		}
	}
}
