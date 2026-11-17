// domain/eventsignals/legacy_equivalence_test.go
package eventsignals_test

import (
	"reflect"
	"testing"

	es "github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/eventsignals/eventsignalstest"
)

// The legacy* values below are verbatim copies of the hard-coded taxonomy
// eventsignals.go carried before the taxonomy moved into a Taxonomy value.
// They pin eventsignalstest.Taxonomy() to the exact pre-refactor behaviour.

var legacyActionPriority = []es.Action{
	es.ActionPracticeSquad, es.ActionDemote,
	es.ActionSign, es.ActionRelease, es.ActionTrade, es.ActionWaive, es.ActionClaim, es.ActionExtend, es.ActionContract,
	es.ActionInjuredReserve, es.ActionInjure, es.ActionReturn, es.ActionPractice, es.ActionActivate,
	es.ActionPromote, es.ActionRoster, es.ActionDepthChart,
	es.ActionTryout, es.ActionGameStatus,
	es.ActionDraft, es.ActionPreview, es.ActionResult,
	es.ActionQuote, es.ActionReport, es.ActionConfirm, es.ActionCorrect,
}

var legacyBodyFallbackActionPriority = []es.Action{
	es.ActionInjuredReserve,
	es.ActionPracticeSquad,
	es.ActionPromote,
	es.ActionDemote,
	es.ActionActivate,
	es.ActionWaive,
	es.ActionClaim,
	es.ActionRelease,
	es.ActionTrade,
	es.ActionSign,
	es.ActionRoster,
	es.ActionInjure,
}

var legacyContentSignalPriority = []es.ContentSignal{
	es.ContentSignalCorrection, es.ContentSignalConfirmation, es.ContentSignalRumor,
	es.ContentSignalAnalysis, es.ContentSignalReaction, es.ContentSignalQuote,
}

func legacyClassify(action es.Action, signal es.ContentSignal) es.EventType {
	switch signal {
	case es.ContentSignalCorrection:
		return es.EventTypeCorrection
	case es.ContentSignalConfirmation:
		return es.EventTypeConfirmation
	case es.ContentSignalRumor:
		return es.EventTypeRumor
	}

	switch action {
	case es.ActionSign, es.ActionRelease, es.ActionTrade, es.ActionWaive, es.ActionClaim, es.ActionExtend, es.ActionContract,
		es.ActionPracticeSquad, es.ActionPromote, es.ActionDemote, es.ActionRoster:
		return es.EventTypeTransaction
	case es.ActionInjure, es.ActionInjuredReserve, es.ActionReturn, es.ActionActivate, es.ActionGameStatus:
		return es.EventTypeInjury
	case es.ActionPractice, es.ActionTryout, es.ActionDepthChart:
		return es.EventTypePractice
	case es.ActionDraft:
		return es.EventTypeDraft
	case es.ActionPreview, es.ActionResult:
		return es.EventTypeGame
	case es.ActionQuote:
		return es.EventTypeQuote
	case es.ActionReport:
		return es.EventTypeRumor
	case es.ActionConfirm:
		return es.EventTypeConfirmation
	case es.ActionCorrect:
		return es.EventTypeCorrection
	}

	switch signal {
	case es.ContentSignalQuote:
		return es.EventTypeQuote
	case es.ContentSignalAnalysis:
		return es.EventTypeAnalysis
	case es.ContentSignalReaction:
		return es.EventTypeReaction
	}

	return es.EventTypeGeneric
}

func TestLegacyEquivalence(t *testing.T) {
	tax := eventsignalstest.Taxonomy()

	actions := append([]es.Action{""}, legacyActionPriority...)
	signals := append([]es.ContentSignal{""}, legacyContentSignalPriority...)
	for _, a := range actions {
		for _, s := range signals {
			if got, want := tax.Classify(a, s), legacyClassify(a, s); got != want {
				t.Errorf("Classify(%q, %q) = %q, legacy %q", a, s, got, want)
			}
		}
	}

	if got := tax.ActionPriority(); !reflect.DeepEqual(got, legacyActionPriority) {
		t.Errorf("ActionPriority = %v, legacy %v", got, legacyActionPriority)
	}
	if got := tax.BodyFallbackPriority(); !reflect.DeepEqual(got, legacyBodyFallbackActionPriority) {
		t.Errorf("BodyFallbackPriority = %v, legacy %v", got, legacyBodyFallbackActionPriority)
	}
	if got := tax.SignalPriority(); !reflect.DeepEqual(got, legacyContentSignalPriority) {
		t.Errorf("SignalPriority = %v, legacy %v", got, legacyContentSignalPriority)
	}
}
