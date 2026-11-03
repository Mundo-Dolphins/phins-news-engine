# ADR 0038: Timed event handlers

## Status

Accepted

## Context

Event handlers (ADR 0033) react to incoming articles. Some posts have no
article behind them: the forecast for a game goes out one hour before
kickoff, at a time computed from a schedule the host owns. The post must fire
by the clock, survive restarts without posting twice, and never go out
silently late, reusing claims (ADR 0034), the multi-destination outbox
(ADR 0031), payloads (ADR 0032) and alerts (ADR 0037).

## Decision

**Port.** `out.TimedEventHandler` has `Name`, `Due(now) []DueEvent` and
`Handle(ctx, EventContext, DueEvent) (EventResult, error)`. `Due` is pure and
cheap and is called on every tick. A `DueEvent` is a stable `Key` (for
example `2026-reg-9`) and the scheduled instant `At`. `Handle` receives the
same `EventContext` and returns the same `EventResult` as an article
handler.

**Ticker.** `scheduler.Scheduler.SetTimedEvents` adds a fourth loop that
calls `usecase.TimedEventRunner.Run` every `scheduler.timed_event_interval_seconds`
(default 60). Without timed handlers the scheduler is unchanged.

**One tick.** For each enabled handler the runner takes the `DueEvent`s whose
`At` has been reached, oldest first, and skips every key that already has a
claim in any state. For the rest:

- *Inside the grace window* (`now - At <= scheduler.timed_event_grace_minutes`,
  default 15) it calls `Handle`.
- *Outside it* the event is not run. The runner takes the claim and marks it
  `failed` with `{"reason":"missed_window"}` and sends an alert. Because the
  claim exists, the miss is reported once, not on every tick. An operator who
  still wants the post can `event claims release` it (ADR 0034) while the
  handler still reports it as due.

**Idempotency.** `Handle` runs outside any transaction, so a slow forecast
fetch does not hold the single SQLite connection. Its result is then applied
in one unit of work: `TryClaim(handler, key)`, a placeholder article, and the
outbox rows. Whoever loses the claim writes nothing, so two ticks or a
restart never publish twice. An error, panic, timeout, invalid payload or
unknown action rolls everything back and leaves no claim, so the next tick
retries while the event is inside its grace window. A handler therefore
never calls `TryClaim` itself. A `Handle` that returns `NoEventMatch()` is
also asked again on the next tick.

**Placeholder article.** The outbox requires an article (foreign key, and
`PublishOutbox` loads it before every send). The runner saves a minimal
article `timed:<handler>:<key>` with status `pending_publication`, and the
claim's `article_id` points at it, so `ReconcileWithOutbox` settles the
claim to `published` or `failed` from the outbox as usual. The content is the
payload; the article is only an anchor.

**Isolation.** Each `Handle` runs under a deadline
(`Options.EventHandlerTimeout`, default 5 s) and a `recover`; `Due` runs under
a `recover`. Failures are logged, counted in `in.RunTimedEventsResult` and
alerted through `EventContext.Alerts`; they never reach the article pipeline
or other handlers. Calls are synchronous, so a handler that ignores its
context can delay the next tick.

**Registry and settings.** `engine.Options.TimedEventHandlers` registers
handlers. They share `events.<name>.enabled` / `config` and the name space of
article handlers: a name used by both kinds, a blank name or a duplicate is an
error at start-up. The clock is `EventDeps.Clock`, injected so tests move
time by hand.

## Consequences

Article handlers, the Telegram path and the pipeline are unchanged. The
schedule is entirely the handler's: `Due` should return only the events it
still cares about (for example the last day), because the runner reports every
unclaimed event older than the grace window as missed. Several events due in
one tick run in order of `At`. The runner needs the claims store, so it
cannot run without one.
