package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
)

// Publisher publishes an accepted article to the outside world (Telegram),
// returning the destination's message ID on success for
// publication_attempts.external_message_id.
type Publisher interface {
	Publish(ctx context.Context, a article.Article) (messageID string, err error)
}

// PermanentError wraps a Publish error that will never succeed on retry
// (e.g. the destination rejected the article as malformed). PublishOutbox
// discards the outbox item immediately instead of scheduling a backoff
// retry when it sees this. Any other error from Publish is treated as
// temporary.
type PermanentError struct {
	Err error
}

// Error implements the error interface.
func (e *PermanentError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped error to errors.Is/errors.As.
func (e *PermanentError) Unwrap() error { return e.Err }

// RateLimitedError wraps a Publish error caused by the destination's own
// rate limiting (e.g. Telegram's HTTP 429), carrying how long the caller
// was told to wait before retrying. PublishOutbox uses RetryAfter as a
// floor for its next retry delay, on top of (not instead of) its own
// exponential backoff schedule — whichever is later wins. This lives in
// the out package (not in the telegram adapter) so PublishOutbox can
// recognize it via errors.As without importing a concrete adapter,
// preserving the hexagonal boundary.
type RateLimitedError struct {
	Err        error
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *RateLimitedError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped error to errors.Is/errors.As.
func (e *RateLimitedError) Unwrap() error { return e.Err }

// AmbiguousError wraps a Publish error where whether Telegram actually
// received and processed the sendMessage request cannot be determined
// from the response alone — a network timeout, a dropped connection, or
// a response that could not be read/parsed after the request was sent —
// as opposed to a confirmed outcome (nil error, or PermanentError/
// RateLimitedError/any other error produced only after successfully
// parsing Telegram's own JSON response). PublishOutbox must never treat
// this the same as a confirmed temporary failure: retrying immediately
// risks a duplicate post if the original request actually succeeded
// server-side. See ADR 0030.
type AmbiguousError struct {
	Err error
}

// Error implements the error interface.
func (e *AmbiguousError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped error to errors.Is/errors.As.
func (e *AmbiguousError) Unwrap() error { return e.Err }
