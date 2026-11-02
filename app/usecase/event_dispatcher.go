package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

// DefaultEventHandlerTimeout bounds one handler call when none is configured.
const DefaultEventHandlerTimeout = 5 * time.Second

// EventHandlerSettings is one handler's entry of the settings file
// (events.<name>).
type EventHandlerSettings struct {
	Enabled bool
	Config  map[string]any
}

// EventDeps are the services lent to every handler through out.EventContext.
// Clock and Logger are required; the rest may be nil.
type EventDeps struct {
	Clock      out.Clock
	Logger     out.Logger
	MatchState out.MatchStateProvider
	Claims     out.EventClaimStore
	Media      out.EventMediaStore
	Alerts     out.EventAlertSink
}

// EventDispatcher runs the enabled event handlers for one article and
// returns the actions they asked for (ADR 0033). A handler that errors,
// panics or overruns its deadline is logged and counted and contributes no
// actions; it never affects other handlers or the article.
type EventDispatcher struct {
	handlers []registeredHandler
	deps     EventDeps
	timeout  time.Duration
}

type registeredHandler struct {
	handler out.EventHandler
	config  map[string]any
}

// NamedAction is an action together with the handler that asked for it.
type NamedAction struct {
	Handler string
	Action  out.EventAction
}

// NewEventDispatcher registers handlers in the order given, keeping only the
// ones enabled in settings (a handler with no entry is disabled). It fails on
// a nil handler or a blank or duplicate name. A zero timeout means
// DefaultEventHandlerTimeout.
func NewEventDispatcher(handlers []out.EventHandler, settings map[string]EventHandlerSettings, deps EventDeps, timeout time.Duration) (*EventDispatcher, error) {
	if timeout <= 0 {
		timeout = DefaultEventHandlerTimeout
	}
	d := &EventDispatcher{deps: deps, timeout: timeout}
	seen := make(map[string]bool, len(handlers))
	for i, h := range handlers {
		if h == nil {
			return nil, fmt.Errorf("event handlers: handler %d is nil", i)
		}
		name := strings.TrimSpace(h.Name())
		if name == "" {
			return nil, fmt.Errorf("event handlers: handler %d has a blank name", i)
		}
		if seen[name] {
			return nil, fmt.Errorf("event handlers: duplicate handler name %q", name)
		}
		seen[name] = true
		s := settings[name]
		if !s.Enabled {
			deps.Logger.Info("event handler disabled", "handler", name)
			continue
		}
		d.handlers = append(d.handlers, registeredHandler{handler: h, config: s.Config})
		deps.Logger.Info("event handler enabled", "handler", name)
	}
	var unknown []string
	for name := range settings {
		if !seen[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		deps.Logger.Warn("events settings name a handler that is not registered", "handler", name)
	}
	return d, nil
}

// Len reports how many handlers are enabled.
func (d *EventDispatcher) Len() int {
	if d == nil {
		return 0
	}
	return len(d.handlers)
}

// Dispatch runs every enabled handler interested in a, updating metrics, and
// returns the actions in handler order. A nil dispatcher does nothing.
func (d *EventDispatcher) Dispatch(ctx context.Context, a article.Article, metrics *in.EventHandlerMetrics) []NamedAction {
	if d.Len() == 0 {
		return nil
	}
	var actions []NamedAction
	for _, rh := range d.handlers {
		name := rh.handler.Name()
		if !d.interested(rh.handler, a) {
			continue
		}
		metrics.Ran++
		result, outcome, err := d.handle(ctx, rh, a)
		switch outcome {
		case outcomePanic:
			metrics.Panics++
			d.deps.Logger.Error("event handler panicked", err, "handler", name, "article_id", a.ID)
			continue
		case outcomeTimeout:
			metrics.Timeouts++
			d.deps.Logger.Warn("event handler timed out", "handler", name, "article_id", a.ID, "timeout", d.timeout.String())
			continue
		case outcomeError:
			metrics.Errors++
			d.deps.Logger.Error("event handler failed", err, "handler", name, "article_id", a.ID)
			continue
		}
		if !result.Matched() {
			continue
		}
		metrics.Matched++
		for _, act := range result.Actions {
			if act != nil {
				actions = append(actions, NamedAction{Handler: name, Action: act})
			}
		}
	}
	return actions
}

type handleOutcome int

const (
	outcomeOK handleOutcome = iota
	outcomeError
	outcomePanic
	outcomeTimeout
)

// interested runs the pre-filter, treating a panic as "not interested".
func (d *EventDispatcher) interested(h out.EventHandler, a article.Article) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			d.deps.Logger.Error("event handler Interested panicked", fmt.Errorf("%v", r), "handler", h.Name(), "article_id", a.ID)
			ok = false
		}
	}()
	return h.Interested(a)
}

// handle calls Handle under a deadline and a recover. It runs synchronously:
// the context may be a database transaction, which a goroutine left running
// after the deadline must never touch.
func (d *EventDispatcher) handle(ctx context.Context, rh registeredHandler, a article.Article) (result out.EventResult, outcome handleOutcome, err error) {
	hctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			result, outcome, err = out.EventResult{}, outcomePanic, fmt.Errorf("%v", r)
		}
	}()
	rc := out.EventContext{
		Clock: d.deps.Clock, Logger: d.deps.Logger, MatchState: d.deps.MatchState,
		Claims: d.deps.Claims, Media: d.deps.Media, Alerts: d.deps.Alerts, Config: rh.config,
	}
	result, err = rh.handler.Handle(hctx, rc, a)
	if hctx.Err() != nil && errors.Is(hctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return out.EventResult{}, outcomeTimeout, hctx.Err()
	}
	if err != nil {
		return out.EventResult{}, outcomeError, err
	}
	return result, outcomeOK, nil
}
