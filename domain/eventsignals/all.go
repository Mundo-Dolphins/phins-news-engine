// domain/eventsignals/all.go
package eventsignals

// AllActions returns every Action in the closed taxonomy, in detection
// priority order. The slice is a fresh copy.
func AllActions() []Action {
	return []Action{
		ActionPracticeSquad, ActionDemote,
		ActionSign, ActionRelease, ActionTrade, ActionWaive, ActionClaim, ActionExtend, ActionContract,
		ActionInjuredReserve, ActionInjure, ActionReturn, ActionPractice, ActionActivate,
		ActionPromote, ActionRoster, ActionDepthChart,
		ActionTryout, ActionGameStatus,
		ActionDraft, ActionPreview, ActionResult,
		ActionQuote, ActionReport, ActionConfirm, ActionCorrect,
	}
}

// AllContentSignals returns every ContentSignal in the closed taxonomy, in
// detection priority order. The slice is a fresh copy.
func AllContentSignals() []ContentSignal {
	return []ContentSignal{
		ContentSignalCorrection, ContentSignalConfirmation, ContentSignalRumor,
		ContentSignalAnalysis, ContentSignalReaction, ContentSignalQuote,
	}
}

// AllEventTypes returns every EventType in the closed taxonomy, with
// EventTypeGeneric last. The slice is a fresh copy.
func AllEventTypes() []EventType {
	return []EventType{
		EventTypeTransaction, EventTypeInjury, EventTypeDraft,
		EventTypeGame, EventTypePractice, EventTypeQuote,
		EventTypeRumor, EventTypeConfirmation, EventTypeCorrection,
		EventTypeAnalysis, EventTypeReaction, EventTypeGeneric,
	}
}
