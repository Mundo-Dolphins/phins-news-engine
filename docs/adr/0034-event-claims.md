# ADR 0034: Event claims

## Status

Accepted

## Context

Event handlers (ADR 0033) react to articles, and several articles can describe
the same real-world thing: three journalists post the same injury report within
minutes, and two of those posts can be processed at the same moment. Only one
post may result. A check-then-act in handler code cannot guarantee that; the
guarantee has to come from the database.

## Decision

**Table.** `event_claims` holds one row per `(handler, claim_key)`, enforced by
`UNIQUE (handler, claim_key)` (migration 0047). Besides the key it stores a
`state` (`claimed`, `published` or `failed`), the `article_id` that took the
claim, handler-specific `data` as JSON (opaque to the engine) and timestamps.
`article_id` has no foreign key: claims are kept as a record of what was
posted, even if the article row is later deleted.

**Port.** `out.EventClaimRepository`, lent to handlers as
`EventContext.Claims`. `TryClaim` is a single atomic `INSERT ... ON CONFLICT DO
NOTHING` and reports whether this call took the key; the loser gets `false` and
writes nothing. `MarkPublished`, `MarkFailed`, `Release`, `Get` and `List`
complete it.

**Transaction.** Handlers run inside the article's unit of work, so a claim
written during `Handle` commits or rolls back with the article and the outbox
row the handler's action creates. A handler calls `TryClaim` only after its own
validations pass: a rejected report takes no key, so another account's tweet
can still win it.

**State machine.** `claimed` is the state on insert. `published` and `failed`
are set from the outbox outcome: after each publish run,
`ReconcileWithOutbox` looks at the outbox rows of every `claimed` claim's
article. Once all of them are finished (delivered or dead) the claim becomes
`failed` if any is dead, otherwise `published`. The outbox never learns about
handlers. A claim whose article has no outbox row, or a row still in flight,
stays `claimed`.

**Failure and retry.** A `failed` claim keeps its key, so nothing is re-posted
automatically. `Release` deletes a `claimed` or `failed` claim so an operator
can let the key be claimed again; a `published` claim can never be released,
because that would allow a duplicate. A corrected report on the same day is not
re-published automatically for the same reason.

**Retention.** Claims are small and are never purged.

## Consequences

Idempotency across different articles is enforced by SQLite, not by handler
discipline. If a handler claims a key and the engine then fails to persist its
action (the failure is logged and counted, ADR 0033), the claim stays
`claimed` with no outbox row; it is visible through `List` and can be released.
Settling is per article rather than per destination, so an article with a
delivered Telegram row and a dead Bluesky row settles as `failed`.

## Operator commands

`event claims list|show|retry|release` in the tools binary inspect and repair
claims; see [the runbook](../EVENT-CLAIMS-RUNBOOK.md). `retry` is the only
path that moves a `failed` claim back to `claimed`: it re-enqueues the dead
outbox rows and changes the claim in one transaction, and refuses published
claims and claims with an in-flight outbox row.
