# ADR 0040: Bluesky threads (multi-part payloads)

## Status

Accepted

## Context

A payload ([ADR 0032](0032-message-payload.md)) described one post. Some
messages do not fit in 300 graphemes and are better sent as a thread, one
outbox row that must never end up duplicated or half-forgotten
([ADR 0030](0030-outbox-delivery-guarantee.md)).

## Decision

**Payload.** `publishing.MessagePayload.Continuations` holds the extra parts
(each with text and optional images). `Parts()` returns the head followed by
the continuations; a payload with no continuations is a one-part message and
behaves as before. `publishing.PostRef` is a post's `uri` and `cid`.

**Port.** `out.ThreadPublisher` is an optional extension of
`out.PayloadPublisher`: `PublishThread` sends the parts in order, each as a
reply to the previous one, starting from the parts already posted. A
destination whose publisher does not implement it cannot send a multi-part
row: `PublishOutbox` marks the row dead with a `PermanentError`
("destination does not support threads") without calling the publisher.

**Progress.** The `thread_progress` column (migration 0048) stores the posted
parts as JSON (`PostRef` per part). `RecordThreadPart` writes it after every
part, so a retry resumes with `ResumeThread` after the last recorded part
instead of starting over. The message ID of the row is the first part's URI.

**Bluesky.** `PublishThread` builds every part (limits, images, facets) before
any network call, so a bad part fails the row with nothing posted. Each reply
carries `reply.root` (first part) and `reply.parent` (previous part), both
with `uri` and `cid`. `createRecord` now requires a `cid` in the 200 reply;
without it the reply is ambiguous, since the next part could not be linked.

**Reconciliation.** If a part's send is ambiguous, `FindSent` looks for that
part: part 0 as in [ADR 0035](0035-bluesky-publisher.md), later parts in the
account's `posts_with_replies` feed by identical text and `reply.parent` equal
to the previous part's URI. Found with parts remaining resumes the thread;
found with none remaining finalises the row; not found follows the normal
negative path (retryable when the feed reaches past the cutoff, else
quarantine).

**Alerts.** A dead or quarantined thread row alerts the operator with
"posted parts: N/M, last: <uri>" and the text of the parts not posted, so the
rest can be sent by hand.

## Consequences

- A thread is at most once per part, with the same false-negative caveat as
  [ADR 0035](0035-bluesky-publisher.md) (AppView indexing lag).
- A failed thread is never rolled back: posted parts stay posted and the
  alert says where it stopped.
- Destinations without `ThreadPublisher` reject multi-part rows instead of
  sending only the head.
