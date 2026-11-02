# ADR 0032: Message payload on the outbox

## Status

Accepted

## Context

`out.Publisher.Publish(ctx, article)` builds the message from the article
(title, link). A host handler can need a post with its own text and images
that do not come from the article, for example an injury report image on
Bluesky ([ADR 0031](0031-multi-destination-outbox.md)).

## Decision

**Payload contract.** `publishing.MessagePayload` is stored as JSON in
`publication_outbox.payload`:

| Field | Meaning |
|---|---|
| `text` | Message body. The destination validates its length. |
| `images` | Ordered `{path, mime, alt}`. `path` is non-empty and relative to the media store, `mime` is `image/*`, `alt` is optional (a destination may require it). |
| `link` | Optional absolute http(s) URL. |
| `kind` | Free-form label for the host's handler. |

A payload with neither `text` nor `images` is **empty**: the publisher
derives the message from the article, as before. `'{}'`, the empty string
and the legacy Telegram shape `{"title","canonical_url"}` all decode to the
empty payload; unknown fields are ignored.

**Port.** `out.PayloadPublisher` is an optional extension of `Publisher`
with `PublishPayload(ctx, article, payload)`. `PublishOutbox` calls it when
the destination's publisher implements it, passing the decoded payload
(possibly empty, in which case it must behave like `Publish`); otherwise it
calls `Publish`. The Telegram publisher does not implement it, so Telegram
is unchanged. `PermanentError`, `RateLimitedError` and `AmbiguousError`
([ADR 0030](0030-outbox-delivery-guarantee.md)) apply to `PublishPayload`
exactly as to `Publish`. A payload that cannot be decoded is a permanent
failure, because a retry cannot repair it, and the publisher is not called.

**Validation at enqueue.** `EnqueueDestination` (and so `Enqueue`) rejects a
payload that is not valid JSON, has an empty, absolute or escaping image
path, a non-image MIME type, a non-http(s) link, or a link/kind with no text
or image. Text length and image count or size remain the destination's to
enforce.

## Consequences

Rows enqueued before this change keep working untouched. Hosts enqueue
payload rows through `EnqueueDestination` with
`MessagePayload.Marshal()`. Resolving `path` against the media store is the
publisher's job; the engine only guarantees it is relative.
