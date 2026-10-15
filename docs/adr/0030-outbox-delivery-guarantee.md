# ADR 0030: Outbox delivery guarantee for sends with an unknown outcome

## Status

Accepted

## Context

The transactional outbox ([ADR 0007](0007-transactional-outbox.md))
guarantees an article is enqueued once, but a send can still be repeated:
the process can crash after the destination accepted a message and before
the outcome is recorded, or a timeout can leave it unknown whether the
message arrived. The Bot API offers no idempotency key, so exactly-once
delivery is not achievable and is never claimed.

## Decision

**Outbox states.** `publication_outbox.status` is one of `pending`,
`leased`, `sent_unconfirmed`, `delivered`, `retryable`, `quarantined` or
`dead`. The unique `article_id` is the publication key.

**Evidence first.** After a confirmed success, the returned message ID is
written first, in a single statement that moves the row to
`sent_unconfirmed`. The multi-table finalisation (article status, attempt
row, cluster gate confirmation, audit event) follows. A crash between the
two is finalised on the next run from the recorded message ID, with no new
send.

**Three outcomes from the publisher.** A parsed rejection is a confirmed
failure (`out.PermanentError`, `out.RateLimitedError` or a temporary
error) and is retried normally. A parsed success carries the message ID.
Anything else (a transport error, an unreadable or unparseable response) is
an `out.AmbiguousError`: the row moves to `sent_unconfirmed` with a reason
and is never retried blindly.

**Reconciliation before any resend.** At the start of every publish pass,
`PublishOutbox.Reconcile` resolves `sent_unconfirmed` rows: a recorded
message ID finalises at once. Otherwise, if the host supplied a history
client ([ADR 0021](0021-channel-history-client-port.md)), recent channel
history is searched for the article's canonical URL. A match finalises; a
confident non-match (the history covered the send time) makes the row
`retryable`; anything inconclusive quarantines it.

**Quarantine.** A quarantined row is never retried automatically. It is
logged without secrets or article text and resolved only by an explicit,
audited decision (`usecase.RepairOutbox`: deliver, retry or discard),
which a host exposes as an operator command.

**Configuration floor.** `publication.reconciliation.clock_skew_seconds`
must be at least 60, so a typo cannot turn real matches into non-matches.

## Consequences

- Each row ends delivered (sent at most once by the engine), dead,
  retryable after a confident non-match, or quarantined and visible.
- Residual risk: a reconciliation false negative (for example a URL whose
  tracking parameters drifted) makes a delivered row retryable and resends
  it once.
- Residual risk: a crash in the tiny gap between parsing the success and the
  single-statement write leaves a leased row that is reclaimed and resent.
- Without a history client, every unknown outcome needs an operator
  decision.
