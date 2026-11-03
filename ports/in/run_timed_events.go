package in

import "context"

// RunTimedEvents runs one pass of the timed event handlers (ADR 0038).
type RunTimedEvents interface {
	Run(ctx context.Context) (RunTimedEventsResult, error)
}

// RunTimedEventsResult summarizes one pass.
type RunTimedEventsResult struct {
	// Due counts events handlers reported as due (instant reached) and not
	// yet claimed.
	Due int
	// Ran counts Handle calls.
	Ran int
	// Matched counts Handle calls that returned at least one action.
	Matched int
	// Enqueued counts publication rows added.
	Enqueued int
	// Missed counts events found outside their grace window, recorded as
	// failed claims and alerted.
	Missed int
	// Errors counts handlers that returned an error, invalid or unknown
	// actions, and persistence failures.
	Errors int
	// Panics counts recovered panics (in Due or Handle).
	Panics int
	// Timeouts counts Handle calls that overran their deadline.
	Timeouts int
}
