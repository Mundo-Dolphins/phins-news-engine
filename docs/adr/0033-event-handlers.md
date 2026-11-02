# ADR 0033: Event handlers

## Status

Accepted

## Context

A host sometimes wants to react to a specific incoming article, for example a
journalist's injury-report image or an official `Inactives: Week 7 Dolphins
at Jets` post, by publishing its own text and images to a destination
([ADR 0031](0031-multi-destination-outbox.md),
[ADR 0032](0032-message-payload.md)). Deduplication throws away repeated
copies of the same post, and the first copy can fail validation, so the
reaction cannot wait until an article is accepted. More kinds of events will
follow, and adding one must not mean changing the pipeline.

## Decision

**Port.** `out.EventHandler` has `Name`, a cheap pure pre-filter
`Interested(article)`, and `Handle(ctx, EventContext, article)` returning an
`EventResult`: no match, or a list of `EventAction`s. The set of action types
is closed and owned by the engine; the first is `EnqueuePublication`
(destination, `MessagePayload`, lane). More can be added without touching
handlers that exist.

**Context.** `EventContext` lends a clock, a logger, the
`MatchStateProvider`, the handler's own `config` block and three small
service ports: `EventClaimStore`, `EventMediaStore` and `EventAlertSink`.
Those three are nil until the engine wires an implementation (claims, media
store and alerts are separate pieces of work), so a handler must check them.
A handler never receives a database handle.

**Where it runs.** In `ProcessCycle`, once per article that gets past the
seen-items claim, right after the article row is saved and before the exact
dedup cascade. Copies that dedup rejects therefore still reach handlers. The
article then continues through the normal pipeline untouched; Telegram
behaviour does not change.

**Persistence.** The dispatcher collects the actions and `ProcessCycle`
applies them inside the article's transaction, so a handler's outbox rows
commit or roll back with the article. An action is validated first (payload
rules of ADR 0032); one that fails is counted and logged, not fatal.

**Isolation.** Each call runs under a deadline (`Options.EventHandlerTimeout`,
default 5 s) and a `recover`. An error, panic or overrun is logged, counted
in `ProcessCycleResult.Events` and discards that handler's result; other
handlers and the article are unaffected. Handlers run synchronously, because
the context may carry the transaction and must not be used by a goroutine
that outlives the call, so a handler that ignores its context can still block
the cycle.

**Registry and settings.** `engine.Options.EventHandlers` registers handlers;
names must be unique and non-blank. `events.<name>.enabled` turns one on (a
registered handler with no entry is off) and `events.<name>.config` is
passed to it. A settings entry naming no registered handler is logged.

## Consequences

With no handlers registered the pipeline is identical to before. Handlers are
not re-run for an item the seen-items claim already finished, so a handler
runs once per ingested item and idempotency across different items (the same
tweet from several accounts) is the claims store's job. Because handlers see
rejected copies, a row can exist for an article whose status is
`rejected_duplicate`; its publisher resolves content from the payload, not
from the article's pipeline outcome.
