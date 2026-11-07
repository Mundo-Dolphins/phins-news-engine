package out

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

// SentLookup describes a send whose outcome is unknown (ADR 0030): what the
// publisher was asked to post and when the engine started the send.
type SentLookup struct {
	Article article.Article
	// Payload is the outbox row's decoded payload (possibly empty, in which
	// case the post was derived from Article).
	Payload publishing.MessagePayload
	// SentAt is when the engine marked the row as sent.
	SentAt time.Time
	// ClockSkew is the tolerance between the engine's clock and the
	// destination's.
	ClockSkew time.Duration
	// ThreadProgress holds the thread parts already posted, in order (empty
	// for a single-post payload).
	ThreadProgress []publishing.PostRef
}

// DestinationReconciler is the optional extension of Publisher for
// destinations whose own history the publisher can read (Bluesky: the
// account's author feed). PublishOutbox.Reconcile uses it to decide whether
// an AmbiguousError send actually landed, before any resend.
//
// FindSent returns (ref, true, true, nil) on a confident match (ref.URI is the
// message ID the original Publish would have returned),
// (PostRef{}, false, true, nil) when the history reached back past
// SentAt-ClockSkew and holds no match, and (PostRef{}, false, false, nil) when the
// history was too shallow to conclude anything. An error means the lookup
// itself failed; the caller quarantines the row.
type DestinationReconciler interface {
	FindSent(ctx context.Context, lookup SentLookup) (ref publishing.PostRef, matched, covered bool, err error)
}
