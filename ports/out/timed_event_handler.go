package out

import (
	"context"
	"time"
)

// TimedEventHandler is a host-supplied plugin that runs by the clock rather
// than in reaction to an article (ADR 0038): for example "post the forecast
// one hour before kickoff". The host owns the schedule; the engine ticks,
// asks each enabled handler what is due, and runs it with the same
// EventContext and EventResult as an EventHandler.
//
// Handle runs outside any database transaction and must honour ctx (it
// carries a deadline). The engine claims (Name, DueEvent.Key) and writes the
// handler's outbox rows in one transaction afterwards, so a handler never
// claims keys itself.
type TimedEventHandler interface {
	// Name is the stable identifier used in settings (events.<name>), in
	// claims and in logs. It must be unique among all registered handlers.
	Name() string
	// Due returns the events whose instant has been reached and that the
	// handler still cares about. It is pure and cheap: the engine calls it on
	// every tick. Events scheduled after now are ignored; events the engine
	// already claimed are skipped, so a handler need not remember what it
	// ran. It should not return events far in the past, which the engine
	// reports as missed.
	Due(now time.Time) []DueEvent
	// Handle runs one due event. It returns NoEventMatch() when there is
	// nothing to post yet (the engine asks again on the next tick while the
	// event is within its grace window), or the actions to persist.
	Handle(ctx context.Context, rc EventContext, ev DueEvent) (EventResult, error)
}

// DueEvent is one scheduled occurrence of a timed handler.
type DueEvent struct {
	// Key identifies the occurrence for the handler, for example
	// "2026-reg-9". It is the claim key, so it must be stable across
	// restarts and unique per handler.
	Key string
	// At is the instant the event was scheduled for (kickoff minus offset).
	At time.Time
}
