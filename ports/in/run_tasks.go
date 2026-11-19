package in

import "context"

// RunTasks runs one pass of the background task runners.
type RunTasks interface {
	Run(ctx context.Context) (RunTasksResult, error)
}

// RunTasksResult summarizes one pass.
type RunTasksResult struct {
	// Ran counts runner calls.
	Ran int
	// Errors counts runner errors, invalid outcomes and persistence failures.
	Errors int
	// Panics counts recovered panics.
	Panics int
	// Timeouts counts runner calls that overran their deadline.
	Timeouts int
	// Enqueued counts publication rows added.
	Enqueued int
}
