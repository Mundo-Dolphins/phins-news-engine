// Package telegram implements a real out.Publisher against the Telegram
// Bot API: a hand-rolled net/http client (no SDK, matching every other
// adapter in this codebase), HTML-formatted messages, and Retry-After-aware
// rate-limit handling.
package telegram

import (
	"fmt"
	"net/http"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// defaultRetryAfter is used when Telegram returns a 429 but omits
// parameters.retry_after (should not happen per the Bot API docs, but
// cheap to guard against).
const defaultRetryAfter = 30 * time.Second

// classifyError turns a non-OK Telegram API response into a typed error:
// *out.RateLimitedError for 429 (carrying the suggested wait, from
// parameters.retry_after), *out.PermanentError for 400/403 (a malformed
// request or the bot no longer having access to the channel — retrying
// without operator intervention can never succeed), and a plain error for
// everything else (network hiccups, 5xx) — temporary, left to
// PublishOutbox's existing exponential backoff.
func classifyError(statusCode int, resp apiResponse) error {
	err := fmt.Errorf("telegram: %s (code %d, http %d)", resp.Description, resp.ErrorCode, statusCode)

	switch statusCode {
	case http.StatusTooManyRequests:
		retryAfter := time.Duration(resp.Parameters.RetryAfter) * time.Second
		if retryAfter <= 0 {
			retryAfter = defaultRetryAfter
		}
		return &appout.RateLimitedError{Err: err, RetryAfter: retryAfter}
	case http.StatusBadRequest, http.StatusForbidden:
		return &appout.PermanentError{Err: err}
	default:
		return err
	}
}
