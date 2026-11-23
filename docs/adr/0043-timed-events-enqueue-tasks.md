# ADR 0043: Timed events can enqueue background tasks

## Status

Accepted. Extends ADR 0038, ADR 0041 and ADR 0042.

## Context

A timed event may start work that takes minutes, for example gathering
data and composing a post from it. A timed handler has a deadline of its own
(`Handle` runs synchronously under the event handler timeout), so it cannot
do that work inline. Article handlers already hand such work to a background
task with `out.EnqueueTask` (ADR 0041); timed handlers could only publish.

## Decision

A timed event handler may return `out.EnqueueTask`, alone or together with
`out.EnqueuePublication`. The runner, in one transaction:

1. takes the event claim with `TryClaim`;
2. when it wins, saves the placeholder article `timed:<handler>:<key>`;
3. enqueues each task with that article id;
4. enqueues the publications, if any.

Losing the claim race, or any failure, writes nothing. The host wires the
task store with `TimedEventRunner.SetTaskRepository`; the engine does it
when it builds the runner.

Errors, counted and alerted like any other persist failure:

- `EnqueueTask` with `CloseEvent`: "close cannot be combined with other
  actions" (this replaces the former, narrower text);
- no task store wired: "task storage is not wired";
- blank `Kind` or `Key`: "task kind and key are required".

The event claim stays `claimed` when no publication is enqueued, and is never settled by the outbox
reconciliation, which only settles claims whose article has outbox rows.
The claim records that the event ran and keeps it from running again; it
does not track the work. The task owns its lifecycle and, when it has
something to say, publishes with its own article. Settling the claim from
the task would couple two lifecycles that retry independently.

## Consequences

- A timed event that only enqueues tasks leaves a claim in `claimed` for
  good. It is not alerted as stuck, because nothing reconciles it.
- Task idempotency stays per `(Kind, Key)`: a task whose key is reused
  across events is created once.
- Without `EnqueueTask` in a timed handler, or without a task store, the
  behaviour is the same as before.
