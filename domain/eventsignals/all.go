// domain/eventsignals/all.go
package eventsignals

// AllActions returns every Action in the closed taxonomy, in detection
// priority order. The slice is a fresh copy.
func AllActions() []Action {
	return append([]Action(nil), actionPriority...)
}

// AllContentSignals returns every ContentSignal in the closed taxonomy, in
// detection priority order. The slice is a fresh copy.
func AllContentSignals() []ContentSignal {
	return append([]ContentSignal(nil), contentSignalPriority...)
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
