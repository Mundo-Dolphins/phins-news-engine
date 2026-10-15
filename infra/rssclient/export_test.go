package rssclient

import (
	"context"
	"time"
)

// SetSleepForTest replaces the retry wait and returns a restore function.
func SetSleepForTest(fn func(ctx context.Context, d time.Duration) error) (restore func()) {
	prev := sleepFn
	sleepFn = fn
	return func() { sleepFn = prev }
}

// HTTPClientForTest exposes the client Fetch builds for cfg.
var HTTPClientForTest = newHTTPClient
