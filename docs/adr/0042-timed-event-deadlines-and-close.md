# ADR 0042: Timed event deadlines and closing an event

## Status

Accepted. Extends ADR 0038.

## Context

ADR 0038 runs a timed event from its instant until a fixed grace window
after it. Two cases do not fit:

- Some events have no exact time. A period of a live game ends at an unknown
  time: the earliest instant is known, the latest is much later than the
  default grace.
- Some events stop applying once they are due, for example a post about an
  overtime period when the game does not go to overtime. The handler has
  nothing to publish, but returning no match makes the engine ask again on
  every tick until the grace window ends, and then report the event as
  missed.

## Decision

`DueEvent` gains an optional `Until`. The event runs while `now` is not after
`Until`; a zero `Until` keeps the default grace after `At`. An `Until`
before `At` is a handler bug: the engine logs an error and alerts once per
handler and key, and falls back to the default grace. The missed-event alert
names the deadline that was passed.

A new action, `out.CloseEvent{Reason}`, is valid only from a timed event
handler. The engine claims the key with an empty article id, no placeholder
article and no outbox row, and stores `{"at", "reason": "closed", "detail"}`
in the claim data. Combining it with `EnqueuePublication` is an error: the
event is not claimed and the handler is asked again on the next tick. The
article-handler and task runners keep rejecting it as an unknown action.

No migration is needed. Reconciliation with the outbox only considers
claims in state `claimed` with a non-empty article id, so a closed claim is
ignored by it. It stays `claimed`, which is terminal in practice: the engine
skips any key that already has a claim.

## Consequences

- A handler can describe an open-ended window without the engine alerting
  that it was missed, and can settle an event that no longer applies without
  an alert.
- An operator can release a closed claim with `event claims release`, as for
  a missed one, to make the event runnable again.
- Hosts that never set `Until` or return `CloseEvent` see no change.
