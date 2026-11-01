# ADR 0031: Multi-destination outbox

## Status

Accepted

## Context

The transactional outbox ([ADR 0007](0007-transactional-outbox.md)) was
Telegram-only and `publication_outbox.article_id` was `UNIQUE`: one row per
article. A host now wants to deliver some articles to a second destination
(Bluesky) as well, with the same crash-safety, retry and quarantine rules
([ADR 0030](0030-outbox-delivery-guarantee.md)) and without changing what
Telegram receives.

## Decision

**One row per article and destination.** `publication_outbox.destination`
is `telegram` (default) or `bluesky`, constrained by a CHECK, and the
publication key is `UNIQUE (article_id, destination)`. Migration 0046
rebuilds the table the way 0023 did (ids preserved, every index and the
`lane` and `expires_at` columns kept); existing rows become `telegram`.
Adding a destination means another rebuild to widen the CHECK, and a new
constant in `domain/publishing.KnownDestinations`.

**Enqueueing.** `Enqueue` and `EnqueueLane` still mean Telegram. The
optional `out.DestinationOutboxRepository.EnqueueDestination` enqueues for
any destination; it is idempotent per `(article, destination)`. The engine's
own pipeline only enqueues Telegram rows: a host enqueues other destinations
itself through the SQLite repository.

**Routing.** `usecase.PublishOutbox` holds a map from destination to
`out.Publisher`. The constructor's publisher is Telegram; hosts add the
others with `RegisterPublisher`, or through `engine.Options.Publishers`.
Registering an unknown destination, a nil publisher, a duplicate, or a
replacement for Telegram is an error. At startup `ValidateWiring` fails if
the outbox holds unfinished rows for a destination with no publisher, and a
claimed row with no publisher fails the run loudly instead of being skipped.
Retry, backoff, lease, ambiguity and quarantine apply to each row on its
own: a failure on one destination never changes another's row.

**What stays Telegram-only.**

- The article lifecycle (`publishing`, `published`, `discarded`), the
  event-cluster gate and the source-policy cooldown follow the Telegram
  delivery. A non-Telegram row records only its own outbox state, attempt and
  an audit event named `publish_destination_*`; its failure, expiry or repair
  never discards the article or releases a gate.
- The editorial publication policy (burst, same-topic, quiet hours) governs
  Telegram sends, and its counters count Telegram attempts only.
- A Telegram `429` defers the remaining Telegram rows of the batch; rows for
  other destinations keep draining.
- Reconciliation uses the Telegram user session. For another destination a
  `sent_unconfirmed` row with a recorded message ID is finalised from that
  local evidence; one without is quarantined for `outbox repair`. The channel
  history is never searched on its behalf, and nothing is blindly resent.
- Operator views (`outbox status`, `backlog`, `expire`) and the supersession
  of live-lane rows cover Telegram rows only. Per-destination operator views
  are future work.

**Observability.** Every outbox log line carries `destination`, a
per-destination summary line follows each run, and
`PublishOutboxResult.ByDestination` carries the counters.

## Consequences

An article can now have several outbox rows; code that assumed one row per
article must filter by destination. The rebuild takes one transaction over
the whole table, which is fast for the production size (hundreds of MB, the
outbox is a small fraction of it) and is recorded in `schema_migrations`, so
it runs once. Rolling back means deleting non-Telegram rows and rebuilding
with `UNIQUE(article_id)`.
