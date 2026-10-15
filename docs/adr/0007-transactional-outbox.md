# ADR 0007: Transactional outbox for publication

## Status

Accepted

## Context

Publishing from inside the processing cycle couples it to the availability
of the destination, gives no way to retry a transient failure, and can
lose or duplicate a decision across a crash.

## Decision

When `ProcessCycle` accepts an article, it writes the
`pending_publication` status and a `publication_outbox` row in the same
transaction. `publication_outbox.article_id` is unique. A separate use
case, `PublishOutbox`, runs on its own interval:

1. It leases due rows in one atomic step, which also reclaims leases older
   than `publication.stale_claim_after_minutes`.
2. It applies the publication policy (burst, same topic, quiet hours, the
   live lane). A deferral moves `available_at`; it never drops the row.
3. It calls `Publisher.Publish` outside any database transaction.
4. It records the outcome and a `publication_attempts` row in a second,
   short transaction.

Retries use exponential backoff (`publication.base_backoff_seconds`,
`publication.max_backoff_seconds`) up to `publication.max_attempts`. An
`out.PermanentError` discards the row at once; an `out.RateLimitedError`
sets a floor on the next retry. At admission the row stores an absolute
deadline (`published_at` plus the freshness window); rows past it are
expired through an audited path (`publish_expired`) instead of sitting in
the queue.

## Consequences

- An article is never published without a durable record that it was
  decided, and the status and outbox row cannot diverge.
- Two extra writes per published article, bounded by indexed queries.
- A crash between a successful send and the outcome commit could cause a
  resend. [ADR 0030](0030-outbox-delivery-guarantee.md) narrows that
  window and handles sends whose outcome is unknown.
