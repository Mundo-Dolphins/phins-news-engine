// Package telegramupdates is the driving adapter that long-polls the Telegram
// Bot API for updates from one authorised user and chat, and stores the ones
// the host's router claims as task signals. It opens no port.
package telegramupdates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	pollTimeout = 25 * time.Second
	baseBackoff = 5 * time.Second
	maxBackoff  = 2 * time.Minute
)

// Bot is the part of the Bot API client the poller needs.
type Bot interface {
	GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]appout.TelegramUpdate, error)
	AnswerCallback(ctx context.Context, callbackID, text string) error
}

// TaskSignals is the part of the task repository the poller needs.
type TaskSignals interface {
	AddSignal(ctx context.Context, taskID int64, payload json.RawMessage, now time.Time) error
}

// Config wires a Poller.
type Config struct {
	Bot     Bot
	Offsets appout.TelegramOffsetRepository
	Tasks   TaskSignals
	Router  appout.TelegramUpdateRouter
	// AllowedUserID and AllowedChatID are the only sender and chat whose
	// updates are processed; everything else is dropped silently.
	AllowedUserID, AllowedChatID int64
	// BotKey keys the stored offset: the numeric bot id, never the token.
	BotKey string
	// ClosedText answers a button press on a task that is already closed;
	// empty answers with no text.
	ClosedText string
	// Wake, if set, is called after each signal is saved so the task loop
	// can run without waiting for its tick. It must not block.
	Wake   func()
	Logger appout.Logger
	// Now and Sleep are injectable for tests; zero values use the real clock.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
}

// Poller runs the getUpdates loop.
type Poller struct{ cfg Config }

// New builds a Poller.
func New(cfg Config) *Poller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return &Poller{cfg: cfg}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Backoff is the wait after the n-th consecutive failure (0-based):
// min(5s*2^n, 2min).
func Backoff(n int) time.Duration {
	d := baseBackoff
	for i := 0; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// Run polls until ctx is done. Errors never end the loop: they are logged
// (the client's errors carry no token) and retried with exponential backoff.
func (p *Poller) Run(ctx context.Context) error {
	offset, err := p.cfg.Offsets.Load(ctx, p.cfg.BotKey)
	if err != nil {
		p.cfg.Logger.Error("telegram updates: load offset", err)
		offset = 0
	}
	failures := 0
	for ctx.Err() == nil {
		updates, err := p.cfg.Bot.GetUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			p.cfg.Logger.Error("telegram updates: getUpdates failed", err)
			p.cfg.Sleep(ctx, Backoff(failures))
			failures++
			continue
		}
		failures = 0
		if len(updates) == 0 {
			continue
		}
		next := offset
		for _, u := range updates {
			p.handle(ctx, u)
			if u.UpdateID+1 > next {
				next = u.UpdateID + 1
			}
		}
		offset = next
		if err := p.cfg.Offsets.Store(ctx, p.cfg.BotKey, offset); err != nil {
			p.cfg.Logger.Error("telegram updates: store offset", err)
		}
	}
	return ctx.Err()
}

func (p *Poller) handle(ctx context.Context, u appout.TelegramUpdate) {
	if u.UserID != p.cfg.AllowedUserID || u.ChatID != p.cfg.AllowedChatID {
		p.cfg.Logger.Debug("telegram updates: dropped unauthorised update", "update_id", u.UpdateID)
		return
	}
	taskID, payload, ok, panicked := p.route(u)
	if panicked {
		if u.IsCallback() {
			p.answer(ctx, u, "")
		}
		return
	}
	if !ok {
		if u.IsCallback() {
			p.answer(ctx, u, "")
		}
		return
	}
	err := p.cfg.Tasks.AddSignal(ctx, taskID, payload, p.cfg.Now())
	text := ""
	switch {
	case err == nil:
		if p.cfg.Wake != nil {
			p.cfg.Wake()
		}
	case errors.Is(err, appout.ErrTaskClosed):
		text = p.cfg.ClosedText
	default:
		p.cfg.Logger.Error("telegram updates: add signal", err, "update_id", u.UpdateID, "task_id", strconv.FormatInt(taskID, 10))
	}
	if u.IsCallback() {
		p.answer(ctx, u, text)
	}
}

// route asks the host's router under a recover: a panicking router must not
// crash the process, nor repeat after a restart (the offset still advances).
// Only the update id and the panic value are logged, never the content.
func (p *Poller) route(u appout.TelegramUpdate) (taskID int64, payload json.RawMessage, ok, panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			p.cfg.Logger.Error("telegram updates: router panicked", fmt.Errorf("%v", rec), "update_id", u.UpdateID)
			taskID, payload, ok, panicked = 0, nil, false, true
		}
	}()
	taskID, payload, ok = p.cfg.Router.Route(u)
	return taskID, payload, ok, false
}

func (p *Poller) answer(ctx context.Context, u appout.TelegramUpdate, text string) {
	if err := p.cfg.Bot.AnswerCallback(ctx, u.CallbackID, text); err != nil {
		p.cfg.Logger.Error("telegram updates: answer callback", err, "update_id", u.UpdateID)
	}
}
