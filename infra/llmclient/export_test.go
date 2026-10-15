package llmclient

import (
	"context"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// SetSleepForTest replaces the retry wait and returns a restore function.
func SetSleepForTest(fn func(ctx context.Context, d time.Duration) error) (restore func()) {
	prev := sleepFn
	sleepFn = fn
	return func() { sleepFn = prev }
}

// BoundedRetryWait exposes the retry-wait calculation to tests.
func BoundedRetryWait(requested time.Duration) time.Duration { return boundedRetryWait(requested) }

// BuildVerifyPromptForTest exposes buildVerifyPrompt to the external tests.
func BuildVerifyPromptForTest(req appout.VerifyCandidateRequest) string {
	return buildVerifyPrompt(req)
}

// BuildChoosePromptForTest exposes buildChoosePrompt to the external tests.
func BuildChoosePromptForTest(req appout.ChooseCandidateRequest) string {
	return buildChoosePrompt(req)
}

// TransportTimeoutForTest exposes the HTTP client timeout built for cfg.
func TransportTimeoutForTest(cfg Config) time.Duration { return newTransport(cfg).httpClient.Timeout }
