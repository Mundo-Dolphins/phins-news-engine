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
