package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/article"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/source"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/in"
	"github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// DefaultTimedEventGrace is how long after its instant a timed event
	// still runs when none is configured.
	DefaultTimedEventGrace = 15 * time.Minute
	// DefaultTimedEventTick is the tick interval when none is configured.
	DefaultTimedEventTick = time.Minute

	// TimedEventMissedReason is stored in the failed claim of an event that
	// was found outside its grace window.
	TimedEventMissedReason = "missed_window"

	timedArticlePrefix = "timed:"
)

// TimedEventRunner runs the enabled timed event handlers (ADR 0038). On each
// Run it asks every handler what is due, skips what is already claimed,
// records what is past its grace window as missed, and runs the rest. An
// error, panic or timeout in one handler never reaches the pipeline or
// another handler.
type TimedEventRunner struct {
	handlers []registeredTimedHandler
	deps     EventDeps
	articles out.ArticleRepository
	outbox   out.DestinationOutboxRepository
	uow      out.UnitOfWork
	timeout  time.Duration
	grace    time.Duration
}

type registeredTimedHandler struct {
	handler out.TimedEventHandler
	config  map[string]any
}

var _ in.RunTimedEvents = (*TimedEventRunner)(nil)

// NewTimedEventRunner registers handlers in the order given, keeping only the
// ones enabled in settings (a handler with no entry is disabled). It fails on
// a nil handler, a blank name, a duplicate name, a name also in
// articleHandlerNames (they share events.<name> and the claim namespace), or
// when deps.Claims is nil. A zero timeout or grace means the default.
func NewTimedEventRunner(
	handlers []out.TimedEventHandler,
	articleHandlerNames []string,
	settings map[string]EventHandlerSettings,
	deps EventDeps,
	articles out.ArticleRepository,
	outbox out.DestinationOutboxRepository,
	uow out.UnitOfWork,
	timeout, grace time.Duration,
) (*TimedEventRunner, error) {
	if deps.Claims == nil {
		return nil, errors.New("timed event handlers: the claims store is required")
	}
	if timeout <= 0 {
		timeout = DefaultEventHandlerTimeout
	}
	if grace <= 0 {
		grace = DefaultTimedEventGrace
	}
	r := &TimedEventRunner{deps: deps, articles: articles, outbox: outbox, uow: uow, timeout: timeout, grace: grace}
	seen := make(map[string]bool, len(handlers)+len(articleHandlerNames))
	for _, n := range articleHandlerNames {
		seen[strings.TrimSpace(n)] = true
	}
	for i, h := range handlers {
		if h == nil {
			return nil, fmt.Errorf("timed event handlers: handler %d is nil", i)
		}
		name := strings.TrimSpace(h.Name())
		if name == "" {
			return nil, fmt.Errorf("timed event handlers: handler %d has a blank name", i)
		}
		if seen[name] {
			return nil, fmt.Errorf("timed event handlers: duplicate handler name %q", name)
		}
		seen[name] = true
		s := settings[name]
		if !s.Enabled {
			deps.Logger.Info("timed event handler disabled", "handler", name)
			continue
		}
		r.handlers = append(r.handlers, registeredTimedHandler{handler: h, config: s.Config})
		deps.Logger.Info("timed event handler enabled", "handler", name)
	}
	return r, nil
}

// Len reports how many handlers are enabled.
func (r *TimedEventRunner) Len() int {
	if r == nil {
		return 0
	}
	return len(r.handlers)
}

// Run implements in.RunTimedEvents. It returns an error only when ctx is done.
func (r *TimedEventRunner) Run(ctx context.Context) (in.RunTimedEventsResult, error) {
	var res in.RunTimedEventsResult
	if r.Len() == 0 {
		return res, nil
	}
	now := r.deps.Clock.Now()
	for _, rh := range r.handlers {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		r.runHandler(ctx, rh, now, &res)
	}
	return res, nil
}

func (r *TimedEventRunner) runHandler(ctx context.Context, rh registeredTimedHandler, now time.Time, res *in.RunTimedEventsResult) {
	name := rh.handler.Name()
	due, panicked := r.due(rh.handler, now)
	if panicked {
		res.Panics++
		r.alert(ctx, name, "Due panicked")
		return
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].At.Before(due[j].At) })
	seen := make(map[string]bool, len(due))
	for _, ev := range due {
		if err := ctx.Err(); err != nil {
			return
		}
		if strings.TrimSpace(ev.Key) == "" || ev.At.IsZero() {
			res.Errors++
			r.deps.Logger.Error("timed event handler returned an invalid event", errors.New("blank key or zero instant"), "handler", name)
			r.alert(ctx, name, "Due returned an event with a blank key or a zero instant")
			continue
		}
		if seen[ev.Key] || ev.At.After(now) {
			continue
		}
		seen[ev.Key] = true
		_, found, err := r.deps.Claims.Get(ctx, name, ev.Key)
		if err != nil {
			res.Errors++
			r.deps.Logger.Error("timed event claim lookup failed", err, "handler", name, "key", ev.Key)
			continue
		}
		if found {
			continue
		}
		res.Due++
		if now.Sub(ev.At) > r.grace {
			r.markMissed(ctx, name, ev, now, res)
			continue
		}
		r.runEvent(ctx, rh, ev, now, res)
	}
}

// due calls Due under a recover.
func (r *TimedEventRunner) due(h out.TimedEventHandler, now time.Time) (events []out.DueEvent, panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			r.deps.Logger.Error("timed event handler Due panicked", fmt.Errorf("%v", rec), "handler", h.Name())
			events, panicked = nil, true
		}
	}()
	return h.Due(now), false
}

func (r *TimedEventRunner) runEvent(ctx context.Context, rh registeredTimedHandler, ev out.DueEvent, now time.Time, res *in.RunTimedEventsResult) {
	name := rh.handler.Name()
	res.Ran++
	result, outcome, err := r.handle(ctx, rh, ev)
	switch outcome {
	case outcomePanic:
		res.Panics++
		r.deps.Logger.Error("timed event handler panicked", err, "handler", name, "key", ev.Key)
		r.alert(ctx, name, fmt.Sprintf("event %s panicked: %v", ev.Key, err))
		return
	case outcomeTimeout:
		res.Timeouts++
		r.deps.Logger.Warn("timed event handler timed out", "handler", name, "key", ev.Key, "timeout", r.timeout.String())
		r.alert(ctx, name, fmt.Sprintf("event %s timed out after %s", ev.Key, r.timeout))
		return
	case outcomeError:
		res.Errors++
		r.deps.Logger.Error("timed event handler failed", err, "handler", name, "key", ev.Key)
		r.alert(ctx, name, fmt.Sprintf("event %s failed: %v", ev.Key, err))
		return
	}
	if !result.Matched() {
		r.deps.Logger.Debug("timed event handler had nothing to post", "handler", name, "key", ev.Key)
		return
	}
	res.Matched++
	enqueued, err := r.persist(ctx, name, ev, result, now)
	if err != nil {
		res.Errors++
		r.deps.Logger.Error("timed event persist failed", err, "handler", name, "key", ev.Key)
		r.alert(ctx, name, fmt.Sprintf("event %s could not be saved: %v", ev.Key, err))
		return
	}
	res.Enqueued += enqueued
	if enqueued > 0 {
		r.deps.Logger.Info("timed event handler enqueued publication", "handler", name, "key", ev.Key, "rows", enqueued)
	}
}

// handle calls Handle under a deadline and a recover, synchronously.
func (r *TimedEventRunner) handle(ctx context.Context, rh registeredTimedHandler, ev out.DueEvent) (result out.EventResult, outcome handleOutcome, err error) {
	hctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	defer func() {
		if rec := recover(); rec != nil {
			result, outcome, err = out.EventResult{}, outcomePanic, fmt.Errorf("%v", rec)
		}
	}()
	rc := out.EventContext{
		Clock: r.deps.Clock, Logger: r.deps.Logger, MatchState: r.deps.MatchState,
		Claims: r.deps.Claims, Media: r.deps.Media, Alerts: r.deps.Alerts, Config: rh.config,
	}
	result, err = rh.handler.Handle(hctx, rc, ev)
	if errors.Is(hctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return out.EventResult{}, outcomeTimeout, hctx.Err()
	}
	if err != nil {
		return out.EventResult{}, outcomeError, err
	}
	return result, outcomeOK, nil
}

type timedClaimData struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

// persist validates the actions, then in one transaction takes the claim,
// saves the event's placeholder article and enqueues the rows. Losing the
// claim race, or any failure, writes nothing.
func (r *TimedEventRunner) persist(ctx context.Context, name string, ev out.DueEvent, result out.EventResult, now time.Time) (int, error) {
	var enqueues []out.EnqueuePublication
	for _, act := range result.Actions {
		switch a := act.(type) {
		case nil:
		case out.EnqueuePublication:
			if err := a.Payload.Validate(); err != nil {
				return 0, fmt.Errorf("invalid payload for %s: %w", a.Destination.Normalized(), err)
			}
			enqueues = append(enqueues, a)
		default:
			return 0, fmt.Errorf("unknown action %T", act)
		}
	}
	if len(enqueues) == 0 {
		return 0, nil
	}
	data, err := json.Marshal(timedClaimData{At: ev.At.UTC()})
	if err != nil {
		return 0, err
	}
	articleID := timedArticleID(name, ev.Key)
	enqueued := 0
	err = r.uow.Do(ctx, func(txCtx context.Context) error {
		enqueued = 0
		claimed, err := r.deps.Claims.TryClaim(txCtx, name, ev.Key, articleID, data, now)
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}
		if !claimed {
			return nil
		}
		if err := r.articles.Save(txCtx, timedArticle(name, ev, articleID, now)); err != nil {
			return fmt.Errorf("save placeholder article: %w", err)
		}
		for _, a := range enqueues {
			payload, err := a.Payload.Marshal()
			if err != nil {
				return fmt.Errorf("marshal payload: %w", err)
			}
			if err := r.outbox.EnqueueDestination(txCtx, articleID, payload, a.Lane, a.Destination, now); err != nil {
				return fmt.Errorf("enqueue: %w", err)
			}
			enqueued++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return enqueued, nil
}

// markMissed records an event found outside its grace window as a failed
// claim, so it is alerted once and never run late. An operator can release
// the claim to let it run.
func (r *TimedEventRunner) markMissed(ctx context.Context, name string, ev out.DueEvent, now time.Time, res *in.RunTimedEventsResult) {
	data, _ := json.Marshal(timedClaimData{At: ev.At.UTC(), Reason: TimedEventMissedReason})
	err := r.uow.Do(ctx, func(txCtx context.Context) error {
		claimed, err := r.deps.Claims.TryClaim(txCtx, name, ev.Key, "", data, now)
		if err != nil || !claimed {
			return err
		}
		return r.deps.Claims.MarkFailed(txCtx, name, ev.Key, now)
	})
	if err != nil {
		res.Errors++
		r.deps.Logger.Error("timed event: record missed event failed", err, "handler", name, "key", ev.Key)
		return
	}
	res.Missed++
	late := now.Sub(ev.At).Round(time.Second)
	r.deps.Logger.Warn("timed event missed its grace window", "handler", name, "key", ev.Key, "scheduled_at", ev.At.UTC().Format(time.RFC3339), "late_by", late.String(), "grace", r.grace.String())
	r.alert(ctx, name, fmt.Sprintf("event %s was scheduled for %s and not run: found %s late (grace %s)", ev.Key, ev.At.UTC().Format(time.RFC3339), late, r.grace))
}

func (r *TimedEventRunner) alert(ctx context.Context, handler, message string) {
	if r.deps.Alerts != nil {
		r.deps.Alerts.Alert(ctx, handler, message)
	}
}

func timedArticleID(handler, key string) string {
	return timedArticlePrefix + handler + ":" + key
}

// timedArticle is the placeholder article a timed event's outbox rows hang
// from: the outbox requires an article, and the payload carries the content.
func timedArticle(handler string, ev out.DueEvent, id string, now time.Time) article.Article {
	sid, _ := source.NewID(source.TypeWeb, "timed-event")
	title := handler + " " + ev.Key
	return article.Article{
		ID: id, ExternalID: id, OriginalURL: id, CanonicalURL: id,
		Source: source.Source{ID: sid, Name: "Timed event", Type: source.TypeWeb},
		Title:  title, Summary: title,
		PublishedAt: ev.At, FetchedAt: now,
		ContentType: article.ContentTypeUnknown,
		Status:      article.StatusPendingPublication,
	}
}
