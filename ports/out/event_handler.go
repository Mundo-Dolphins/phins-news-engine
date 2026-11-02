package out

import (
	"context"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
)

// EventHandler is a host-supplied plugin that reacts to specific incoming
// articles by adding side effects (ADR 0033). It runs once per ingested
// article, before deduplication, so a copy that dedup would reject still
// reaches it. A handler only adds side effects: the article keeps going
// through the normal pipeline untouched.
//
// Handle must honour ctx (it carries a deadline) and must not retain the
// article or its Metadata map; both are read-only.
type EventHandler interface {
	// Name is the stable identifier used in settings (events.<name>) and in
	// logs. It must be unique among registered handlers.
	Name() string
	// Interested is a cheap, pure pre-filter so the pipeline does not call
	// Handle for every article.
	Interested(a article.Article) bool
	// Handle decides what to do. It returns NoEventMatch() to do nothing, or
	// the actions the engine persists with the article.
	Handle(ctx context.Context, rc EventContext, a article.Article) (EventResult, error)
}

// EventContext is what the engine lends a handler: services only, never a
// database handle.
type EventContext struct {
	Clock  Clock
	Logger Logger
	// MatchState reports the live game, if any. Nil when no provider is
	// configured.
	MatchState MatchStateProvider
	// Claims, Media and Alerts are nil until the engine wires them; a handler
	// that needs one must check.
	Claims EventClaimRepository
	Media  EventMediaStore
	Alerts EventAlertSink
	// Config is the handler's own settings block (events.<name>.config).
	Config map[string]any
}

// EventMediaStore downloads an image into the media store.
type EventMediaStore interface {
	// Fetch stores the image at rawURL and returns a reference usable in a
	// publishing.MessagePayload.
	Fetch(ctx context.Context, rawURL string) (publishing.ImageRef, error)
}

// EventAlertSink tells the operator something needs attention.
type EventAlertSink interface {
	Alert(ctx context.Context, handler, message string)
}

// EventResult is the outcome of EventHandler.Handle: no match, or a list of
// actions.
type EventResult struct {
	Actions []EventAction
}

// NoEventMatch is the result of a handler that does not react.
func NoEventMatch() EventResult { return EventResult{} }

// Matched reports whether the result carries at least one action.
func (r EventResult) Matched() bool { return len(r.Actions) > 0 }

// EventAction is one side effect a handler asks for. The set of action types
// is closed and owned by the engine; EnqueuePublication is the first one.
type EventAction interface {
	eventAction()
}

// EnqueuePublication asks for an outbox row for the article on Destination
// with a payload of the handler's own text and images (ADR 0032). It is
// idempotent per (article, destination) like any other enqueue.
type EnqueuePublication struct {
	Destination publishing.Destination
	Payload     publishing.MessagePayload
	// Lane is the publication lane; empty means normal.
	Lane publishing.PublicationLane
}

func (EnqueuePublication) eventAction() {}
